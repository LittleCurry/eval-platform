"""judge 执行器离线单测(不联网): 阶段编排 / 缓存 / 重试语义 / 降级与记账。

这一层是 M4 最容易被"看起来在工作"骗过去的地方, 所以每条断言都对应一个具体事故:
- 缓存命中还去调模型 -> 白烧钱且重复判定;
- 缓存键漏了 contexts -> A 上下文的判定被用到 B 上, 幻觉率失真;
- 协议失败被"兜住"写成空判定 -> 幻觉率虚低且没人发现;
- 限流在 judge 内再重试一层 -> 一次 429 放大成 9 次调用, 打爆配额;
- rubric 失败拖垮整条 case -> 幻觉率直接算不出来。

prompt 相关断言(占位符机制)也放在这里: judge prompt 比生成 prompt 多一个 {answer},
rubric prompt 反而**没有** {contexts} —— 这正是 M4-2 要扩展 load_prompt 的原因。
rubric v2 起还要把断言核查结果(claims_summary)传进去, 否则那条
"unsupported > 0 → helpfulness 不得高于 2"的硬规则无从执行。
"""
from __future__ import annotations

import json
from dataclasses import dataclass

import pytest

from app.generation.prompts import PromptError, load_prompt
from app.judge.builtin import (
    CLAIMS_REQUIRED_PLACEHOLDERS,
    JUDGE_PROTOCOL_VERSION,
    RUBRIC_REQUIRED_PLACEHOLDERS,
    JudgeFailed,
    claims_summary_text,
    judge_case,
)
from app.judge.cache import STAGE_RUBRIC, InMemoryJudgeCache, judge_cache_key
from app.llm.client import ChatResult, LLMFatalError, LLMTransientError

MODEL = "deepseek-ai/DeepSeek-V3.2"
QUESTION = "线索超过多少天无跟进会被自动回收？"


@dataclass
class FakeHit:
    text: str
    doc_id: str = "B02"


HITS = [FakeHit("已分配线索超过 7 天(默认, 可配 3–30 天)无任何跟进, 自动回到未分配池, 并站内通知原负责人。")]
ANSWER = "线索超过 7 天无跟进会被自动回收。"


def claims_json(*labels_and_texts: tuple[str, str]) -> str:
    items = [
        {"id": index, "text": text, "label": label, "evidence": "资料原文", "reason": "对比资料"}
        for index, (label, text) in enumerate(labels_and_texts, start=1)
    ]
    return json.dumps({"claims": items}, ensure_ascii=False)


OK_CLAIMS = claims_json(("supported", "线索超过 7 天无跟进会被自动回收"))
MIXED_CLAIMS = claims_json(
    ("supported", "线索超过 7 天无跟进会被自动回收"),
    ("unsupported", "会通过企业微信通知原负责人"),
    ("irrelevant", "套餐价格是每席 199 元"),
)
OK_RUBRIC = json.dumps({"relevance": 4, "helpfulness": 3, "reason": "要点齐全"}, ensure_ascii=False)


class FakeClient:
    """按队列返回响应: 字符串=正常输出, 异常实例=抛出。"""

    def __init__(self, *responses: object) -> None:
        self.queue = list(responses)
        self.calls: list[list[dict]] = []

    def complete(self, messages: list[dict], *, temperature: float = 0.0,
                 max_tokens: int = 512) -> ChatResult:
        self.calls.append(messages)
        item = self.queue.pop(0) if self.queue else OK_CLAIMS
        if isinstance(item, Exception):
            raise item
        return ChatResult(text=str(item), prompt_tokens=311, completion_tokens=27,
                          latency_ms=1234, raw_model="judge-model")

    @property
    def call_count(self) -> int:
        return len(self.calls)


def _load_prompts(rubric_id: str):
    return (
        load_prompt("judge_claims_zh_v1", required=CLAIMS_REQUIRED_PLACEHOLDERS),
        load_prompt(rubric_id, required=RUBRIC_REQUIRED_PLACEHOLDERS),
    )


@pytest.fixture()
def prompts():
    """rubric v1(历史版本, run #155 用的就是它)。"""
    return _load_prompts("judge_rubric_zh_v1")


@pytest.fixture()
def prompts_v2():
    """rubric v2: 带 {claims_summary}, 用硬规则约束 helpfulness。"""
    return _load_prompts("judge_rubric_zh_v2")


def run(client: FakeClient, prompts, cache=None, **overrides):
    claims_prompt, rubric_prompt = prompts
    params: dict = {
        "question": QUESTION,
        "answer": ANSWER,
        "hits": HITS,
        "client": client,
        "claims_prompt": claims_prompt,
        "rubric_prompt": rubric_prompt,
        "cache": cache if cache is not None else InMemoryJudgeCache(),
        "model": MODEL,
        "provider": "siliconflow",
        "base_url": "https://api.siliconflow.cn/v1",
        "temperature": 0.0,
        "max_tokens": 1024,
        "max_claims": 12,
        "max_context_chars": 3000,
        "max_retries": 2,
        "reference_answer": "",
    }
    params.update(overrides)
    return judge_case(**params)


# ---- prompt 资产与占位符机制 ----


def test_judge_prompts_load_with_their_own_required_placeholders(prompts):
    claims_prompt, rubric_prompt = prompts
    assert claims_prompt.placeholders == ("contexts", "question", "answer")
    assert rubric_prompt.placeholders == ("question", "answer", "reference_answer")
    assert claims_prompt.prompt_hash and rubric_prompt.prompt_hash
    assert claims_prompt.prompt_hash != rubric_prompt.prompt_hash


def test_rubric_prompt_needs_its_own_required_set():
    """rubric prompt 没有 {contexts}: 用默认必需项加载它必须失败(否则会渲染出空上下文硬伤)。"""
    with pytest.raises(PromptError, match="缺少必需占位符"):
        load_prompt("judge_rubric_zh_v1")


def test_rendering_rejects_missing_and_unknown_values(prompts):
    claims_prompt, _ = prompts
    with pytest.raises(PromptError, match="缺少必需占位符"):
        claims_prompt.render(question="q", contexts="c")           # 少了 answer
    with pytest.raises(PromptError, match="未使用的占位符"):
        claims_prompt.render(question="q", contexts="c", answer="a", bogus="x")


def test_generation_prompt_hash_is_unchanged_by_the_placeholder_refactor():
    """红线: prompt_hash 只覆盖两段正文, 占位符机制改造不该动它(run 141 的记录必须仍然对得上)。"""
    assert load_prompt("qa_zh_v1").prompt_hash == "22d446d0398cdaeb"


def test_claims_prompt_receives_question_contexts_and_answer(prompts):
    claims_prompt, _ = prompts
    messages = claims_prompt.render(question=QUESTION, contexts="[1] (B02)\n资料正文", answer=ANSWER)
    user_content = messages[1]["content"]
    assert QUESTION in user_content and ANSWER in user_content and "资料正文" in user_content
    assert "{answer}" not in user_content and "{contexts}" not in user_content


# ---- 正常路径 ----


def test_happy_path_calls_claims_then_rubric(prompts):
    client = FakeClient(OK_CLAIMS, OK_RUBRIC)
    result = run(client, prompts)

    assert len(result.claims) == 1
    assert result.claims[0]["label"] == "supported"
    assert result.claims[0]["id"] == 1
    assert result.rubric == {"relevance": 4, "helpfulness": 3, "reason": "要点齐全"}
    assert client.call_count == 2
    assert result.meta.claims_calls == 1 and result.meta.rubric_calls == 1
    assert result.meta.rubric_enabled is True
    assert result.meta.rubric_skipped_reason == ""


def test_meta_records_full_judging_conditions(prompts):
    result = run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, temperature=0.0, max_claims=8)
    meta = result.meta

    assert meta.judge_model == MODEL
    assert meta.claims_prompt_id == "judge_claims_zh_v1"
    assert meta.rubric_prompt_id == "judge_rubric_zh_v1"
    assert meta.protocol_version == JUDGE_PROTOCOL_VERSION
    assert meta.temperature == 0.0 and meta.max_claims == 8
    assert meta.context_chunks == 1 and meta.context_chars > 0
    assert meta.prompt_tokens == 311 * 2 and meta.completion_tokens == 27 * 2


def test_verdict_counts_and_json_shape(prompts):
    result = run(FakeClient(MIXED_CLAIMS, OK_RUBRIC), prompts)

    assert result.verdict_counts() == {"total": 3, "supported": 1, "unsupported": 1, "irrelevant": 1}
    payload = json.loads(json.dumps(result.to_json(), ensure_ascii=False))
    assert set(payload) == {"claims", "rubric", "meta"}
    assert payload["claims"][1]["label"] == "unsupported"


def test_empty_claims_is_valid_and_rubric_still_runs(prompts):
    """答案只说"资料中未提及"时没有可核查断言 —— 合法结果, 不是错误。"""
    client = FakeClient('{"claims":[]}', OK_RUBRIC)
    result = run(client, prompts)

    assert result.claims == []
    assert result.verdict_counts()["total"] == 0
    assert result.rubric is not None
    assert client.call_count == 2


def test_rubric_can_be_disabled(prompts):
    claims_prompt, _ = prompts
    client = FakeClient(OK_CLAIMS)
    result = run(client, prompts, rubric_prompt=None)

    assert result.rubric is None
    assert result.meta.rubric_enabled is False
    assert "未启用 rubric" in result.meta.rubric_skipped_reason
    assert client.call_count == 1
    assert claims_prompt.prompt_id == "judge_claims_zh_v1"


def test_claims_over_limit_are_truncated_and_recorded(prompts):
    many = claims_json(*[("supported", f"断言 {i}") for i in range(5)])
    result = run(FakeClient(many, OK_RUBRIC), prompts, max_claims=3)

    assert len(result.claims) == 3
    assert result.meta.truncated_claims == 2


# ---- 缓存 ----


def test_cache_hit_makes_zero_model_calls(prompts):
    cache = InMemoryJudgeCache()
    first = run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)
    second = run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)

    assert first.meta.claims_cache_hits == 0
    assert second.meta.claims_cache_hits == 1 and second.meta.rubric_cache_hits == 1
    assert second.claims == first.claims and second.rubric == first.rubric
    assert cache.hit_count == 2, "一次命中只累加一次(读两次会让 hits 虚高)"


def test_cache_hit_counts_saved_tokens_but_not_new_usage(prompts):
    cache = InMemoryJudgeCache()
    run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)
    hit = run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)

    assert hit.meta.prompt_tokens == 0 and hit.meta.completion_tokens == 0
    assert hit.meta.cache_saved_prompt_tokens == 311 * 2
    assert hit.meta.cache_saved_completion_tokens == 27 * 2


def test_different_contexts_must_miss_claims_cache(prompts):
    """红线: claims 的键里没有 contexts 就会把 A 上下文的判定用到 B 上。

    注意 rubric 阶段的**有意不对称**: 它只看问题+答案(评分与检索无关),
    所以换上下文时 rubric 命中是正确行为, claims 必须 miss。
    """
    cache = InMemoryJudgeCache()
    run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)

    other_hits = [FakeHit("完全不同的资料: 商机阶段可回退。", "B03")]
    second = run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache, hits=other_hits)

    assert second.meta.claims_cache_hits == 0, "上下文变了, claims 判定必须重算"
    assert second.meta.rubric_cache_hits == 1, "rubric 与检索无关, 命中缓存是对的"
    assert second.meta.claims_calls == 1 and second.meta.rubric_calls == 0


def test_same_answer_different_contexts_rejudges_claims_but_not_rubric(prompts):
    """把上面那条不对称性再钉一次(它就是"串味"事故的对照组)。"""
    cache = InMemoryJudgeCache()
    run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)
    run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache, hits=[FakeHit("另一段资料", "B05")])

    assert cache.hit_count == 1, "两次运行共命中 1 次(只有 rubric 那条)"


def test_different_model_must_miss_cache(prompts):
    cache = InMemoryJudgeCache()
    run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)
    second = run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache, model="Qwen/Qwen3-8B")

    assert second.meta.claims_cache_hits == 0


def test_cache_entry_from_other_protocol_version_is_ignored(prompts):
    """协议演进后旧条目不能静默复用(键里带协议版本)。"""
    cache = InMemoryJudgeCache()
    claims_prompt, _ = prompts
    stale_payload = {"_protocol": "v0", "question": QUESTION, "answer": ANSWER,
                     "contexts": "[1] (B02)\n已分配线索超过 7 天(默认, 可配 3–30 天)无任何跟进, 自动回到未分配池, 并站内通知原负责人。"}
    cache.put(
        key=judge_cache_key(stage="claims", prompt_hash=claims_prompt.prompt_hash, model=MODEL,
                            payload=stale_payload),
        stage="claims", model=MODEL, prompt_hash=claims_prompt.prompt_hash,
        response={"claims": [{"id": 1, "text": "旧判定", "label": "supported"}]},
    )

    result = run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)

    assert result.meta.claims_cache_hits == 0
    assert result.claims[0]["text"] != "旧判定"


def test_corrupted_cache_entry_is_treated_as_miss(prompts):
    """缓存里的脏数据(枚举非法)不能被直接采信。"""
    cache = InMemoryJudgeCache()
    claims_prompt, _ = prompts
    payload = {"_protocol": JUDGE_PROTOCOL_VERSION, "question": QUESTION, "answer": ANSWER,
               "contexts": "[1] (B02)\n已分配线索超过 7 天(默认, 可配 3–30 天)无任何跟进, 自动回到未分配池, 并站内通知原负责人。"}
    cache.put(
        key=judge_cache_key(stage="claims", prompt_hash=claims_prompt.prompt_hash, model=MODEL,
                            payload=payload),
        stage="claims", model=MODEL, prompt_hash=claims_prompt.prompt_hash,
        response={"claims": [{"id": 1, "text": "x", "label": "totally-wrong"}]},
    )

    client = FakeClient(OK_CLAIMS, OK_RUBRIC)
    result = run(client, prompts, cache=cache)

    assert result.meta.claims_cache_hits == 0
    assert client.call_count == 2, "脏条目按 miss 处理, 必须重新调用"


def test_successful_stages_are_written_to_cache_once_each(prompts):
    cache = InMemoryJudgeCache()
    run(FakeClient(OK_CLAIMS, OK_RUBRIC), prompts, cache=cache)
    assert cache.put_calls == 2


# ---- 重试与错误语义 ----


def test_protocol_failure_is_retried_then_succeeds(prompts):
    client = FakeClient("这不是 JSON", OK_CLAIMS, OK_RUBRIC)
    result = run(client, prompts, max_retries=2)

    assert len(result.claims) == 1
    assert result.meta.protocol_retries == 1
    assert result.meta.claims_calls == 2
    assert client.call_count == 3


def test_protocol_failure_exhausted_raises_and_writes_nothing(prompts):
    """红线: 判定不出来就失败, 绝不写空判定。"""
    cache = InMemoryJudgeCache()
    client = FakeClient("垃圾输出 1", "垃圾输出 2", "垃圾输出 3")

    with pytest.raises(JudgeFailed, match="claims 协议失败"):
        run(client, prompts, cache=cache, max_retries=2)

    assert client.call_count == 3
    assert cache.put_calls == 0, "失败不得写缓存(否则下次会复用一份空判定)"
    assert cache.entries == {}


def test_transient_error_propagates_without_inner_retry(prompts):
    """限流交给队列的 item 级重试: judge 内再重试会把一次 429 放大成 9 次调用。"""
    client = FakeClient(LLMTransientError("HTTP 429: rate limited"))

    with pytest.raises(LLMTransientError):
        run(client, prompts, max_retries=2)

    assert client.call_count == 1


def test_fatal_error_propagates_so_task_can_abort(prompts):
    client = FakeClient(LLMFatalError("HTTP 402: insufficient balance"))

    with pytest.raises(LLMFatalError):
        run(client, prompts)

    assert client.call_count == 1


def test_empty_answer_is_rejected_without_any_call(prompts):
    client = FakeClient(OK_CLAIMS)

    with pytest.raises(JudgeFailed, match="答案为空"):
        run(client, prompts, answer="   ")

    assert client.call_count == 0


def test_rubric_protocol_failure_degrades_without_failing_the_case(prompts):
    """rubric 是附加信息: 失败要降级记录, claims 结果照常返回(幻觉率不能因此算不出来)。"""
    client = FakeClient(OK_CLAIMS, "不是 JSON", "还不是 JSON", "仍然不是 JSON")
    result = run(client, prompts, max_retries=2)

    assert len(result.claims) == 1
    assert result.rubric is None
    assert result.meta.rubric_enabled is False
    assert "rubric 协议失败" in result.meta.rubric_skipped_reason
    assert result.meta.rubric_calls == 3, "rubric 自身应在内部重试满 max_retries 次"


def test_rubric_retry_then_success(prompts):
    client = FakeClient(OK_CLAIMS, "坏 JSON", OK_RUBRIC)
    result = run(client, prompts, max_retries=2)

    assert result.rubric is not None and result.rubric["relevance"] == 4
    assert result.meta.protocol_retries == 1


# ---- rubric v2: 断言核查结果驱动 helpfulness(负控实验后的修订) ----


def test_claims_summary_text_formats_counts():
    claims = [
        {"label": "supported"}, {"label": "supported"},
        {"label": "unsupported"}, {"label": "irrelevant"},
    ]
    summary = claims_summary_text(claims)
    assert summary == "共 4 条断言：supported 2 / unsupported 1 / irrelevant 1"


def test_claims_summary_text_handles_empty_and_unknown_labels():
    assert claims_summary_text([]) == "共 0 条断言（答案没有可核查的断言）"
    # 未知 label 不应让统计错位(只统计三类已知标签, 总数按列表长度)
    assert claims_summary_text([{"label": "weird"}]) == "共 1 条断言：supported 0 / unsupported 0 / irrelevant 0"


def test_rubric_v2_receives_claims_summary(prompts_v2):
    """v2 必须看到断言核查结论, 否则那条"unsupported>0 → helpfulness≤2"的硬规则无从执行。"""
    judge_llm = FakeClient(MIXED_CLAIMS, OK_RUBRIC)

    result = run(judge_llm, prompts_v2)

    assert len(result.claims) == 3
    rubric_messages = judge_llm.calls[1][1]["content"]
    assert "共 3 条断言：supported 1 / unsupported 1 / irrelevant 1" in rubric_messages
    assert "{claims_summary}" not in rubric_messages


def test_rubric_v1_does_not_receive_claims_summary(prompts):
    """v1 保持原样: 不传摘要、渲染里不出现占位符(否则历史判定与缓存键会漂)。"""
    judge_llm = FakeClient(OK_CLAIMS, OK_RUBRIC)

    run(judge_llm, prompts)

    rubric_messages = judge_llm.calls[1][1]["content"]
    assert "条断言" not in rubric_messages


def test_rubric_cache_key_includes_claims_summary_only_for_v2(prompts_v2):
    """摘要参与 v2 的缓存键: 核查结论不同 -> 评分不该复用旧结果。"""
    cache = InMemoryJudgeCache()
    first_judge = FakeClient(OK_CLAIMS, OK_RUBRIC)          # 1 supported / 0 unsupported
    run(first_judge, prompts_v2, cache=cache)
    entries_after_first = len(cache.entries)

    # 同一问题与答案, 但答案是"被换掉的" -> 核查结论不同 -> rubric 必须重判
    second_judge = FakeClient(MIXED_CLAIMS, OK_RUBRIC)      # 1 supported / 1 unsupported
    second = run(second_judge, prompts_v2, cache=cache, answer="另一个版本的答案")

    assert len(cache.entries) > entries_after_first
    assert second.meta.rubric_cache_hits == 0, "答案/核查结论变了, rubric 不该命中"


def test_rubric_v2_prompt_states_the_hard_rule():
    """把红线写进 prompt 文件本身: 改 prompt 时不能顺手删掉这条规则。"""
    rubric = load_prompt("judge_rubric_zh_v2", required=RUBRIC_REQUIRED_PLACEHOLDERS)
    assert "unsupported > 0" in rubric.user
    assert "不得高于 2" in rubric.user
    assert "只有 unsupported = 0 且 irrelevant = 0 时，helpfulness 才可能给 5" in rubric.user
    # 两轴独立这条也要留着(否则 relevance 会被事实错误污染, 归因就分不清"跑题"与"编造")
    assert "不因事实对错扣分" in rubric.user


def test_rubric_v1_and_v2_have_different_hashes_and_cache_keys(prompts, prompts_v2):
    v1_rubric = prompts[1]
    v2_rubric = prompts_v2[1]

    assert v1_rubric.prompt_id != v2_rubric.prompt_id
    assert v1_rubric.prompt_hash != v2_rubric.prompt_hash
    key_v1 = judge_cache_key(stage=STAGE_RUBRIC, prompt_hash=v1_rubric.prompt_hash,
                             model=MODEL, payload={"question": "q", "answer": "a"})
    key_v2 = judge_cache_key(stage=STAGE_RUBRIC, prompt_hash=v2_rubric.prompt_hash,
                             model=MODEL, payload={"question": "q", "answer": "a"})
    assert key_v1 != key_v2, "换了 rubric 版本必须换缓存键"