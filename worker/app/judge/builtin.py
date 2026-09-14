"""builtin judge 执行器(M4-2): 每个案例 = claims 阶段 + rubric 阶段, 带缓存与重试。

分工与顺序:
1. **claims 阶段**(必做): 答案 → 原子断言 + 逐条 supported/unsupported/irrelevant 判定
   —— 这是幻觉率与"哪句在编"的唯一来源;
2. **rubric 阶段**(可关): 独立评 relevance/helpfulness —— 两次调用分开是**有意的**,
   合在一起会让"有幻觉"压低质量分, 而这两个问题的修法完全不同(一个改检索/提示, 一个改答案组织)。

错误语义(与队列的重试/死信机制配合, 已与项目 owner 确认):
- `JudgeProtocolError`(模型没按协议输出) → 在**本函数内**重试 `max_retries` 次; 用尽则抛 `JudgeFailed`,
  由队列按"可重试失败"处理 —— 绝不把猜的判定写库;
- `LLMTransientError`(限流/5xx/超时) → **不在本函数重试**: ChatClient 内部已做有界重试,
  再叠一层会让一次限流放大成 9 次调用; 直接抛给队列的 item 级重试(它有退避与死信);
- `LLMFatalError`(401/402/参数错) → 直接抛出, 由队列**中止整个任务**(不能刷 60 条死信)。

缓存(D5/D8):
- 键含 (协议版本, stage, prompt_hash, model, payload), 其中 payload **包含 contexts**;
- 命中即复用, 本次 token 记 0, 同时累计 `cache_saved_*` 用于成本核算;
- 命中后仍会**重新校验**缓存内容: 协议版本演进时旧条目不该被静默复用(校验失败按 miss 处理)。

rubric 失败的取舍(已确认): rubric 是附加信息, 协议重试用尽时**不拖垮整条 case** ——
记录 `rubric_skipped_reason` 并保留 claims 结果(claims 才是幻觉率的来源)。

rubric v2(负控实验暴露 v1 缺陷后的修订): 断言核查结果以 `claims_summary` 摘要传入 rubric,
模板声明了该占位符才传(所以 v1 的缓存键与历史判定不受影响)。v1 把"不要因事实错误扣分"
误用到 helpfulness 上, 导致通篇编造的答案也能拿满分; v2 把 helpfulness 语义收紧为
"能否放心照着做", 并写死 "unsupported > 0 → 不得高于 2"。
"""
from __future__ import annotations

import json
import time
from collections.abc import Sequence
from dataclasses import asdict, dataclass
from typing import Any, Protocol

from app.generation.prompts import PromptTemplate, build_contexts
from app.judge.cache import STAGE_CLAIMS, STAGE_RUBRIC, JudgeCache, judge_cache_key
from app.judge.protocol import (
    ClaimsResponse,
    JudgeProtocolError,
    RubricResponse,
    parse_claims,
    parse_rubric,
)
from app.llm.client import ChatResult

# 协议版本: 改动协议形状时必须 +1 —— 它进缓存键, 保证旧条目不会被新代码误用
JUDGE_PROTOCOL_VERSION = "v1"
# 两类 prompt 各自需要的占位符(与 prompts/judge_*.md 一致)
CLAIMS_REQUIRED_PLACEHOLDERS = ("contexts", "question", "answer")
RUBRIC_REQUIRED_PLACEHOLDERS = ("question", "answer")


class SupportsComplete(Protocol):
    """judge 所需的最小客户端接口(便于注入 fake)。"""

    def complete(
            self,
            messages: list[dict[str, Any]],
            *,
            temperature: float = ...,
            max_tokens: int = ...,
    ) -> ChatResult: ...


class JudgeFailed(RuntimeError):
    """judge 未能产出可信结果(协议重试用尽/答案为空) —— 属**可重试失败**, 不写库。"""


@dataclass(frozen=True)
class JudgeMeta:
    """一次判定的完整条件与用量(落进 `case_results.judge.meta`)。"""

    provider: str
    base_url: str
    judge_model: str
    claims_prompt_id: str
    claims_prompt_hash: str
    rubric_prompt_id: str
    rubric_prompt_hash: str
    temperature: float
    max_tokens: int
    max_context_chars: int
    max_claims: int
    protocol_version: str
    context_chunks: int
    context_chars: int
    dropped_contexts: int
    claims_calls: int
    claims_cache_hits: int
    rubric_calls: int
    rubric_cache_hits: int
    protocol_retries: int
    truncated_claims: int
    prompt_tokens: int
    completion_tokens: int
    cache_saved_prompt_tokens: int
    cache_saved_completion_tokens: int
    latency_ms: int
    rubric_enabled: bool
    rubric_skipped_reason: str

    def to_json(self) -> dict[str, Any]:
        return asdict(self)


@dataclass(frozen=True)
class JudgeResult:
    """一次判定结果; to_json() 的形状就是 `case_results.judge` 的内容。"""

    claims: list[dict[str, Any]]
    rubric: dict[str, Any] | None
    meta: JudgeMeta

    def to_json(self) -> dict[str, Any]:
        return {"claims": self.claims, "rubric": self.rubric, "meta": self.meta.to_json()}

    def verdict_counts(self) -> dict[str, int]:
        """按标签计数(含 total) —— 三个率的分母都是 total。"""
        counts = {"total": len(self.claims), "supported": 0, "unsupported": 0, "irrelevant": 0}
        for claim in self.claims:
            label = str(claim.get("label", ""))
            if label in counts:
                counts[label] += 1
        return counts


def judge_case(
        *,
        question: str,
        answer: str,
        hits: Sequence[Any],
        client: SupportsComplete,
        claims_prompt: PromptTemplate,
        cache: JudgeCache | None,
        model: str,
        provider: str = "",
        base_url: str = "",
        rubric_prompt: PromptTemplate | None = None,
        reference_answer: str = "",
        temperature: float = 0.0,
        max_tokens: int = 1024,
        max_claims: int = 12,
        max_context_chars: int = 3000,
        max_retries: int = 2,
) -> JudgeResult:
    """对一个案例做 claim 级核查(可选 rubric 打分)。"""
    if not answer.strip():
        raise JudgeFailed("答案为空, 无法判定(生成侧不应写入空答案)")

    started = time.perf_counter()
    bundle = build_contexts(hits, max_context_chars)
    counters = _Counters()

    claims, truncated = _claims_stage(
        question=question,
        answer=answer,
        contexts=bundle.text,
        client=client,
        prompt=claims_prompt,
        cache=cache,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        max_claims=max_claims,
        max_retries=max_retries,
        counters=counters,
    )

    rubric: dict[str, Any] | None = None
    rubric_skipped = ""
    if rubric_prompt is None:
        rubric_skipped = "未启用 rubric(快照未提供 rubric_prompt_id)"
    else:
        rubric, rubric_skipped = _rubric_stage(
            question=question,
            answer=answer,
            reference_answer=reference_answer,
            claims=claims,
            client=client,
            prompt=rubric_prompt,
            cache=cache,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
            max_retries=max_retries,
            counters=counters,
        )

    meta = JudgeMeta(
        provider=provider,
        base_url=base_url,
        judge_model=model,
        claims_prompt_id=claims_prompt.prompt_id,
        claims_prompt_hash=claims_prompt.prompt_hash,
        rubric_prompt_id=rubric_prompt.prompt_id if rubric_prompt else "",
        rubric_prompt_hash=rubric_prompt.prompt_hash if rubric_prompt else "",
        temperature=temperature,
        max_tokens=max_tokens,
        max_context_chars=max_context_chars,
        max_claims=max_claims,
        protocol_version=JUDGE_PROTOCOL_VERSION,
        context_chunks=bundle.chunks,
        context_chars=bundle.chars,
        dropped_contexts=bundle.dropped,
        claims_calls=counters.claims_calls,
        claims_cache_hits=counters.claims_cache_hits,
        rubric_calls=counters.rubric_calls,
        rubric_cache_hits=counters.rubric_cache_hits,
        protocol_retries=counters.protocol_retries,
        truncated_claims=truncated,
        prompt_tokens=counters.prompt_tokens,
        completion_tokens=counters.completion_tokens,
        cache_saved_prompt_tokens=counters.saved_prompt_tokens,
        cache_saved_completion_tokens=counters.saved_completion_tokens,
        latency_ms=int((time.perf_counter() - started) * 1000),
        rubric_enabled=rubric is not None,
        rubric_skipped_reason=rubric_skipped,
    )
    return JudgeResult(claims=claims, rubric=rubric, meta=meta)


# ---- 阶段实现 ----


def _claims_stage(
        *,
        question: str,
        answer: str,
        contexts: str,
        client: SupportsComplete,
        prompt: PromptTemplate,
        cache: JudgeCache | None,
        model: str,
        temperature: float,
        max_tokens: int,
        max_claims: int,
        max_retries: int,
        counters: _Counters,
) -> tuple[list[dict[str, Any]], int]:
    payload = {
        "_protocol": JUDGE_PROTOCOL_VERSION,
        "question": question,
        "answer": answer,
        "contexts": contexts,
    }
    key = judge_cache_key(stage=STAGE_CLAIMS, prompt_hash=prompt.prompt_hash, model=model, payload=payload)

    cached, cached_prompt_tokens, cached_completion_tokens = _cache_hit(cache, key)
    if cached is not None:
        try:
            cached_claims = ClaimsResponse.model_validate(cached).claims
        except Exception:  # noqa: BLE001 - 旧协议/损坏条目一律按 miss, 脏数据不进判定
            cached = None
        else:
            counters.claims_cache_hits += 1
            counters.saved_prompt_tokens += cached_prompt_tokens
            counters.saved_completion_tokens += cached_completion_tokens
            return [c.model_dump() for c in cached_claims], 0

    messages = prompt.render(question=question, contexts=contexts, answer=answer)
    for attempt in range(max_retries + 1):
        result = client.complete(messages, temperature=temperature, max_tokens=max_tokens)
        counters.claims_calls += 1
        counters.prompt_tokens += result.prompt_tokens
        counters.completion_tokens += result.completion_tokens
        try:
            claims, truncated = parse_claims(result.text, max_claims=max_claims)
        except JudgeProtocolError as exc:
            if attempt < max_retries:
                counters.protocol_retries += 1
                continue
            raise JudgeFailed(f"claims 协议失败(已重试 {max_retries} 次): {exc}") from exc

        stored = {"claims": [c.model_dump() for c in claims]}
        _cache_put(cache, key=key, stage=STAGE_CLAIMS, model=model, prompt_hash=prompt.prompt_hash,
                   response=stored, result=result)
        return stored["claims"], truncated

    raise JudgeFailed("claims 阶段异常")  # pragma: no cover - 循环内必返回或抛错


def _rubric_stage(
        *,
        question: str,
        answer: str,
        reference_answer: str,
        claims: list[dict[str, Any]],
        client: SupportsComplete,
        prompt: PromptTemplate,
        cache: JudgeCache | None,
        model: str,
        temperature: float,
        max_tokens: int,
        max_retries: int,
        counters: _Counters,
) -> tuple[dict[str, Any] | None, str]:
    # claims_summary 只在模板声明该占位符时才进 payload —— 于是:
    #   * v2 的 rubric 能看到断言核查结果(硬规则需要它);
    #   * v1 的缓存键与历史判定记录**逐字节不变**(run #155 仍可复现、旧缓存仍可命中)。
    values: dict[str, str] = {
        "question": question,
        "answer": answer,
        "reference_answer": reference_answer or "（未提供）",
    }
    payload: dict[str, Any] = {
        "_protocol": JUDGE_PROTOCOL_VERSION,
        "question": question,
        "answer": answer,
        "reference_answer": reference_answer,
    }
    if "claims_summary" in prompt.placeholders:
        summary = claims_summary_text(claims)
        values["claims_summary"] = summary
        payload["claims_summary"] = summary
    key = judge_cache_key(stage=STAGE_RUBRIC, prompt_hash=prompt.prompt_hash, model=model, payload=payload)

    cached, cached_prompt_tokens, cached_completion_tokens = _cache_hit(cache, key)
    if cached is not None:
        try:
            cached_rubric = RubricResponse.model_validate(cached)
        except Exception:  # noqa: BLE001 - 同上: 旧协议条目按 miss
            cached = None
        else:
            counters.rubric_cache_hits += 1
            counters.saved_prompt_tokens += cached_prompt_tokens
            counters.saved_completion_tokens += cached_completion_tokens
            return cached_rubric.model_dump(), ""

    messages = prompt.render(**values)
    for attempt in range(max_retries + 1):
        result = client.complete(messages, temperature=temperature, max_tokens=max_tokens)
        counters.rubric_calls += 1
        counters.prompt_tokens += result.prompt_tokens
        counters.completion_tokens += result.completion_tokens
        try:
            rubric = parse_rubric(result.text)
        except JudgeProtocolError as exc:
            if attempt < max_retries:
                counters.protocol_retries += 1
                continue
            # 附加信息: 失败不拖垮整条 case
            return None, f"rubric 协议失败(已重试 {max_retries} 次): {exc}"

        _cache_put(cache, key=key, stage=STAGE_RUBRIC, model=model, prompt_hash=prompt.prompt_hash,
                   response=rubric.model_dump(), result=result)
        return rubric.model_dump(), ""

    return None, "rubric 阶段异常"  # pragma: no cover


def claims_summary_text(claims: list[dict[str, Any]]) -> str:
    """把断言核查结果压成一行摘要, 供 rubric v2 使用。

    为什么传"摘要"而不是明细: rubric 的职责是打分, 不是重判事实 —— 给它结论即可,
    明细留在 case_results.judge.claims 里供人工复核。这样也避免 rubric 被 claim 明细带偏。
    """
    if not claims:
        return "共 0 条断言（答案没有可核查的断言）"
    counts = {"supported": 0, "unsupported": 0, "irrelevant": 0}
    for claim in claims:
        label = str(claim.get("label", ""))
        if label in counts:
            counts[label] += 1
    return (
        f"共 {len(claims)} 条断言：supported {counts['supported']} / "
        f"unsupported {counts['unsupported']} / irrelevant {counts['irrelevant']}"
    )


def _cache_hit(cache: JudgeCache | None, key: str) -> tuple[dict[str, Any] | None, int, int]:
    """读缓存并**只读一次**(每次 get 都会累加命中计数, 读两次会让 hits 虚高)。

    返回 (response, 首次调用的 prompt_tokens, 首次调用的 completion_tokens)。
    """
    if cache is None:
        return None, 0, 0
    entry = cache.get(key)
    if entry is None:
        return None, 0, 0
    return entry.response, int(entry.prompt_tokens or 0), int(entry.completion_tokens or 0)


def _cache_put(
        cache: JudgeCache | None,
        *,
        key: str,
        stage: str,
        model: str,
        prompt_hash: str,
        response: dict[str, Any],
        result: ChatResult,
) -> None:
    if cache is None:
        return
    cache.put(
        key=key,
        stage=stage,
        model=model,
        prompt_hash=prompt_hash,
        response=json.loads(json.dumps(response, ensure_ascii=False)),
        prompt_tokens=result.prompt_tokens,
        completion_tokens=result.completion_tokens,
    )


@dataclass
class _Counters:
    claims_calls: int = 0
    claims_cache_hits: int = 0
    rubric_calls: int = 0
    rubric_cache_hits: int = 0
    protocol_retries: int = 0
    prompt_tokens: int = 0
    completion_tokens: int = 0
    saved_prompt_tokens: int = 0
    saved_completion_tokens: int = 0