"""ChatClient 离线单测: 用 httpx.MockTransport 造 200/429/401 三种响应, 不联网、不花钱。

为什么必须离线测而不是"连真网跑一次看看":
- **错误分类是重试语义的开关**。把 401 当瞬时错误, 生产上就是 60 道题各重试 3 次 + 60 条死信,
  而真正的原因(key 没配好)被埋在日志里; 把 429 当致命错误, 一次限流判死一题, 指标静默变差。
  这两种错法都不会让程序崩, 只会让结果变得不可信 —— 所以要用断言把它们钉住。
- 顺带钉住"请求次数": 重试次数写错时, 调用次数是最直接的证据。
"""
from __future__ import annotations

import json

import httpx
import pytest

from app.llm import client as client_module
from app.llm.client import ChatClient, LLMFatalError, LLMTransientError

BASE_URL = "https://api.example.com/v1"
MODEL = "deepseek-ai/DeepSeek-V3.2"


@pytest.fixture(autouse=True)
def _no_sleep(monkeypatch: pytest.MonkeyPatch) -> None:
    """避免真等退避时间(否则 429 用例要睡 2 秒)。"""
    monkeypatch.setattr(client_module.time, "sleep", lambda _seconds: None)


class Recorder:
    """记录每次请求, 供断言"到底调了几次 / 发了什么 payload"。"""

    def __init__(self, responses: list[httpx.Response | Exception]) -> None:
        self._responses = responses
        self.requests: list[httpx.Request] = []

    def handler(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        index = min(len(self.requests) - 1, len(self._responses) - 1)
        item = self._responses[index]
        if isinstance(item, Exception):
            raise item
        return item

    @property
    def calls(self) -> int:
        return len(self.requests)

    def payload(self, index: int = 0) -> dict:
        return json.loads(self.requests[index].content.decode("utf-8"))


def make_client(recorder: Recorder, *, max_retries: int = 2, api_key: str = "sk-test") -> ChatClient:
    return ChatClient(
        base_url=BASE_URL,
        api_key=api_key,
        model=MODEL,
        max_retries=max_retries,
        client=httpx.Client(transport=httpx.MockTransport(recorder.handler), trust_env=False),
    )


def ok_response(
    content: str = "资料中未提及",
    *,
    usage: dict | None = None,
    model: str = MODEL,
) -> httpx.Response:
    body = {
        "id": "chatcmpl-1",
        "model": model,
        "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
        "usage": usage if usage is not None else {"prompt_tokens": 123, "completion_tokens": 45},
    }
    return httpx.Response(200, json=body)


MESSAGES = [{"role": "user", "content": "线索多久无跟进会被回收?"}]


# ---- 正常路径 ----


def test_parses_text_usage_and_model() -> None:
    recorder = Recorder([ok_response("默认 7 天。")])
    result = make_client(recorder).complete(MESSAGES, max_tokens=64)

    assert result.text == "默认 7 天。"
    assert result.prompt_tokens == 123
    assert result.completion_tokens == 45
    assert result.total_tokens == 168
    assert result.raw_model == MODEL
    assert result.latency_ms >= 0
    assert recorder.calls == 1


def test_request_goes_to_chat_completions_with_bearer_auth() -> None:
    recorder = Recorder([ok_response()])
    make_client(recorder).complete(MESSAGES)

    request = recorder.requests[0]
    assert str(request.url) == f"{BASE_URL}/chat/completions"
    assert request.headers["Authorization"] == "Bearer sk-test"


def test_base_url_with_trailing_slash_is_normalized() -> None:
    recorder = Recorder([ok_response()])
    client = ChatClient(
        base_url=BASE_URL + "/",
        api_key="sk-test",
        model=MODEL,
        client=httpx.Client(transport=httpx.MockTransport(recorder.handler)),
    )
    client.complete(MESSAGES)

    assert str(recorder.requests[0].url) == f"{BASE_URL}/chat/completions"


def test_temperature_and_response_format_are_forwarded() -> None:
    recorder = Recorder([ok_response("{}")])
    make_client(recorder).complete(MESSAGES, temperature=0.0, max_tokens=32, response_format={"type": "json_object"})

    payload = recorder.payload()
    assert payload["model"] == MODEL
    assert payload["temperature"] == 0.0
    assert payload["max_tokens"] == 32
    assert payload["response_format"] == {"type": "json_object"}


def test_empty_content_is_not_an_error() -> None:
    # "模型说不知道"与"接口抽风"要能区分: 客户端只如实返回空文本, 由调用方决定语义
    recorder = Recorder([ok_response("")])
    assert make_client(recorder).complete(MESSAGES).text == ""


def test_missing_usage_defaults_to_zero() -> None:
    body = {"model": MODEL, "choices": [{"message": {"content": "hi"}}]}
    recorder = Recorder([httpx.Response(200, json=body)])
    result = make_client(recorder).complete(MESSAGES)

    assert (result.prompt_tokens, result.completion_tokens) == (0, 0)


# ---- 瞬时错误: 可重试 ----


def test_429_then_success_is_retried_and_succeeds() -> None:
    recorder = Recorder([httpx.Response(429, text="rate limited"), ok_response("重试后的答案")])
    result = make_client(recorder).complete(MESSAGES)

    assert result.text == "重试后的答案"
    assert recorder.calls == 2, "429 应该重试一次后成功"


def test_persistent_429_raises_transient_after_bounded_retries() -> None:
    recorder = Recorder([httpx.Response(429, text="rate limited")])
    with pytest.raises(LLMTransientError) as excinfo:
        make_client(recorder, max_retries=2).complete(MESSAGES)

    assert recorder.calls == 3, "max_retries=2 时总尝试次数应为 3(首次 + 2 次重试)"
    assert "429" in str(excinfo.value)


def test_5xx_is_transient() -> None:
    recorder = Recorder([httpx.Response(503, text="upstream down")])
    with pytest.raises(LLMTransientError):
        make_client(recorder, max_retries=0).complete(MESSAGES)
    assert recorder.calls == 1


def test_timeout_is_transient_and_retried() -> None:
    recorder = Recorder([httpx.TimeoutException("read timeout")])
    with pytest.raises(LLMTransientError) as excinfo:
        make_client(recorder, max_retries=1).complete(MESSAGES)

    assert recorder.calls == 2
    assert "超时" in str(excinfo.value)


def test_network_error_is_transient() -> None:
    recorder = Recorder([httpx.ConnectError("connection refused")])
    with pytest.raises(LLMTransientError) as excinfo:
        make_client(recorder, max_retries=0).complete(MESSAGES)
    assert "网络错误" in str(excinfo.value)


# ---- 致命错误: 绝不重试(本文件最重要的一组断言) ----


def test_401_is_fatal_and_never_retried() -> None:
    recorder = Recorder([httpx.Response(401, text='{"error":{"message":"Authentication Fails"}}')])
    with pytest.raises(LLMFatalError) as excinfo:
        make_client(recorder, max_retries=3).complete(MESSAGES)

    assert recorder.calls == 1, "401 是配置问题, 重试只会把 60 道题变成 60 条死信"
    assert "401" in str(excinfo.value)


def test_400_is_fatal() -> None:
    recorder = Recorder([httpx.Response(400, text="bad request")])
    with pytest.raises(LLMFatalError):
        make_client(recorder, max_retries=2).complete(MESSAGES)
    assert recorder.calls == 1


def test_404_is_fatal() -> None:
    recorder = Recorder([httpx.Response(404, text="model not found")])
    with pytest.raises(LLMFatalError):
        make_client(recorder, max_retries=2).complete(MESSAGES)
    assert recorder.calls == 1


def test_402_insufficient_balance_is_fatal() -> None:
    """实测场景: SiliconFlow 余额不足返回 402 + code 30001 —— 欠费重试一万次也一样。"""
    body = '{"code":30001,"message":"Sorry, your account balance is insufficient","data":null}'
    recorder = Recorder([httpx.Response(402, text=body)])
    with pytest.raises(LLMFatalError) as excinfo:
        make_client(recorder, max_retries=3).complete(MESSAGES)

    assert recorder.calls == 1, "欠费是不可重试的配置/账务问题, 必须立刻暴露"
    assert "402" in str(excinfo.value)


def test_non_json_body_on_200_is_fatal() -> None:
    recorder = Recorder([httpx.Response(200, text="<html>gateway</html>")])
    with pytest.raises(LLMFatalError) as excinfo:
        make_client(recorder).complete(MESSAGES)

    assert recorder.calls == 1
    assert "JSON" in str(excinfo.value)


def test_missing_choices_is_fatal() -> None:
    recorder = Recorder([httpx.Response(200, json={"model": MODEL})])
    with pytest.raises(LLMFatalError) as excinfo:
        make_client(recorder).complete(MESSAGES)

    assert "choices" in str(excinfo.value)


def test_missing_api_key_fails_fast() -> None:
    with pytest.raises(LLMFatalError) as excinfo:
        ChatClient(base_url=BASE_URL, api_key="", model=MODEL)
    assert "api_key" in str(excinfo.value)


def test_error_message_is_truncated_and_flattened() -> None:
    """错误信息要能塞进日志/死信原因, 不能把整个响应体灌进去。"""
    long_body = "x" * 5000
    recorder = Recorder([httpx.Response(500, text=long_body)])
    with pytest.raises(LLMTransientError) as excinfo:
        make_client(recorder, max_retries=0).complete(MESSAGES)

    assert len(str(excinfo.value)) < 400
