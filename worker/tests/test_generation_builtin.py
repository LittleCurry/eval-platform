"""builtin 生成链路离线单测: 只测纯逻辑 + prompt 渲染 + 元信息组装(不联网)。

保护两条最贵的不变式:
1. **答案与元信息必须来自同一次调用**, 且元信息足以复盘("哪个模型、哪个 prompt、看了哪些 chunk、花了多少 token");
2. **空答案不产生结果对象** —— 空串一旦落库, 报告会显示"有答案但内容为空", 指标静默变假。
"""
from __future__ import annotations

from dataclasses import dataclass

import pytest

from app.generation.builtin import EmptyAnswerError, generate_answer
from app.generation.prompts import load_prompt
from app.llm.client import ChatResult, LLMTransientError


@dataclass
class FakeHit:
    text: str
    doc_id: str = "B02"


class FakeClient:
    """记录调用参数的假客户端(与 ChatClient.complete 同形)。"""

    def __init__(self, text: str = "资料中未提及", *, error: Exception | None = None) -> None:
        self.text = text
        self.error = error
        self.calls: list[dict] = []

    def complete(self, messages, *, temperature: float = 0.0, max_tokens: int = 512) -> ChatResult:
        self.calls.append({"messages": messages, "temperature": temperature, "max_tokens": max_tokens})
        if self.error is not None:
            raise self.error
        return ChatResult(
            text=self.text, prompt_tokens=311, completion_tokens=27, latency_ms=2345,
            raw_model="deepseek-ai/DeepSeek-V3.2",
        )


def generate(client: FakeClient, *, hits=None, question="线索超期回收后会通知谁?", **overrides):
    params = {
        "provider": "siliconflow",
        "base_url": "https://api.siliconflow.cn/v1",
        "max_context_chars": 3000,
        "temperature": 0.0,
        "max_tokens": 512,
    }
    params.update(overrides)
    return generate_answer(
        question=question,
        hits=hits if hits is not None else [FakeHit("线索超过 7 天无跟进会自动回到未分配池, 并站内通知原负责人。")],
        client=client,
        prompt=load_prompt("qa_zh_v1"),
        **params,
    )


def test_answer_and_meta_are_produced_together() -> None:
    client = FakeClient("线索会回到未分配池, 并站内通知原负责人。")
    result = generate(client)

    assert result.answer == "线索会回到未分配池, 并站内通知原负责人。"
    meta = result.meta
    assert meta.provider == "siliconflow"
    assert meta.model == "deepseek-ai/DeepSeek-V3.2"
    assert meta.prompt_id == "qa_zh_v1"
    assert len(meta.prompt_hash) == 16
    assert meta.temperature == 0.0 and meta.max_tokens == 512
    assert (meta.prompt_tokens, meta.completion_tokens) == (311, 27)
    assert meta.latency_ms == 2345
    assert meta.context_chunks == 1 and meta.context_chars > 0


def test_prompt_receives_question_and_contexts() -> None:
    client = FakeClient()
    generate(client, question="默认回收天数是多少?")

    messages = client.calls[0]["messages"]
    assert [m["role"] for m in messages] == ["system", "user"]
    assert "默认回收天数是多少?" in messages[1]["content"]
    assert "线索超过 7 天无跟进会自动回到未分配池" in messages[1]["content"]
    assert "{contexts}" not in messages[1]["content"]


def test_generation_params_are_forwarded() -> None:
    client = FakeClient()
    generate(client, temperature=0.3, max_tokens=128)

    assert client.calls[0]["temperature"] == 0.3
    assert client.calls[0]["max_tokens"] == 128


def test_meta_records_context_budget_usage() -> None:
    hits = [FakeHit("x" * 200), FakeHit("y" * 200), FakeHit("z" * 200)]
    result = generate(FakeClient(), hits=hits, max_context_chars=500)

    assert result.meta.context_chunks == 2
    assert result.meta.dropped_contexts == 1
    assert result.meta.context_chars <= 500


def test_empty_answer_raises_instead_of_returning() -> None:
    """空答案必须变成异常: 让调用方按可重试失败处理, 而不是写一条空记录进库。"""
    with pytest.raises(EmptyAnswerError) as excinfo:
        generate(FakeClient("   \n  "))

    assert "空答案" in str(excinfo.value)
    assert "qa_zh_v1" in str(excinfo.value)


def test_transient_error_propagates_untouched() -> None:
    client = FakeClient(error=LLMTransientError("HTTP 429"))
    with pytest.raises(LLMTransientError):
        generate(client)


def test_answer_is_stripped_but_inner_text_preserved() -> None:
    result = generate(FakeClient("  第一行\n第二行  "))
    assert result.answer == "第一行\n第二行"


def test_meta_to_json_is_flat_and_serializable() -> None:
    import json

    payload = json.loads(json.dumps(generate(FakeClient()).meta.to_json()))

    assert payload["prompt_id"] == "qa_zh_v1"
    assert set(payload) >= {
        "provider", "base_url", "model", "prompt_id", "prompt_hash", "temperature", "max_tokens",
        "context_chunks", "context_chars", "dropped_contexts", "prompt_tokens", "completion_tokens",
        "latency_ms",
    }
