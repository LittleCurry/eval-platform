"""builtin 生成链路(M4-1 S4): top-k 上下文 + prompt → answer + 生成元信息。

为什么生成元信息必须落库(而不是只留日志):
- 生成侧结论建立在"用了哪个模型、哪个 prompt、看了哪些 chunk、花了多少 token"之上;
  报告页要能回答"这句答案是模型在什么条件下说出来的"(D7 可复现 / D8 成本);
- M4-2 的 judge 要按 `(prompt_hash, model, claim)` 做缓存(D5), 键的前半截就来自这里;
- `context_chunks / context_chars / dropped_contexts` 记录了"模型实际看到的资料",
  否则事后无法判断一次错误是"检索没给到" 还是"给了但模型没用"。

一个硬规则: **空答案绝不写库**。空串入库会让报告显示"有答案但内容为空", 指标静默变假;
这里统一抛 `EmptyAnswerError`, 由调用方按可重试失败处理。
"""
from __future__ import annotations

from collections.abc import Sequence
from dataclasses import asdict, dataclass
from typing import Any, Protocol

from app.generation.prompts import PromptTemplate, build_contexts
from app.llm.client import ChatResult


class SupportsComplete(Protocol):
    """生成所需的最小客户端接口(便于注入 fake)。"""

    def complete(
        self,
        messages: list[dict[str, Any]],
        *,
        temperature: float = ...,
        max_tokens: int = ...,
    ) -> ChatResult: ...


class EmptyAnswerError(RuntimeError):
    """模型返回空文本 —— 视为**可重试失败**, 不能落库。"""


@dataclass(frozen=True)
class GenerationMeta:
    """一次生成的完整条件(落库到 `case_results.generation`)。"""

    provider: str
    base_url: str
    model: str
    prompt_id: str
    prompt_hash: str
    temperature: float
    max_tokens: int
    context_chunks: int
    context_chars: int
    dropped_contexts: int
    prompt_tokens: int
    completion_tokens: int
    latency_ms: int

    def to_json(self) -> dict[str, Any]:
        return asdict(self)


@dataclass(frozen=True)
class GenerationResult:
    answer: str
    meta: GenerationMeta


def generate_answer(
    *,
    question: str,
    hits: Sequence[Any],
    client: SupportsComplete,
    prompt: PromptTemplate,
    provider: str,
    base_url: str,
    max_context_chars: int,
    temperature: float,
    max_tokens: int,
) -> GenerationResult:
    """跑一次生成: 拼上下文 → 渲染 prompt → 调模型 → 组装答案与元信息。"""
    bundle = build_contexts(hits, max_context_chars)
    messages = prompt.render(question=question, contexts=bundle.text)
    result = client.complete(messages, temperature=temperature, max_tokens=max_tokens)

    answer = (result.text or "").strip()
    if not answer:
        raise EmptyAnswerError(
            f"模型返回空答案(model={result.raw_model}, prompt={prompt.prompt_id}/{prompt.prompt_hash}, "
            f"contexts={bundle.chunks} 块)"
        )

    meta = GenerationMeta(
        provider=provider,
        base_url=base_url,
        model=result.raw_model,
        prompt_id=prompt.prompt_id,
        prompt_hash=prompt.prompt_hash,
        temperature=temperature,
        max_tokens=max_tokens,
        context_chunks=bundle.chunks,
        context_chars=bundle.chars,
        dropped_contexts=bundle.dropped,
        prompt_tokens=result.prompt_tokens,
        completion_tokens=result.completion_tokens,
        latency_ms=result.latency_ms,
    )
    return GenerationResult(answer=answer, meta=meta)
