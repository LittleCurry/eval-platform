"""OpenAI 兼容 chat 客户端(M4-1): 生成链路的最小依赖。

三个设计要点, 都是踩过才知道的:

1. **错误分类决定重试语义**(写反的代价很具体):
   - `429 / 5xx / 408 / 超时 / 网络错误` = 瞬时 → `LLMTransientError`(可重试);
   - `400 / 401 / 403 / 404 / 402 / 响应体不合法` = 致命 → `LLMFatalError`(**绝不重试**)。
   把 401 当瞬时: 60 道题各重试 3 次, 白等数分钟并产出 60 条死信, 而真正的原因(key 没配好)被埋在日志里;
   把 429 当致命: 一次限流就把这一题判死, 60 题里零星死信, 指标静默变差。
   `402` 也归致命: 它是欠费(如 SiliconFlow 返回 `{"code":30001,"message":"account balance is insufficient"}`),
   重试一万次也一样, 必须立刻暴露给人看。

2. **重试只做传输层, 且次数很少**(默认 2 次, 退避 0.5s / 1.5s):
   队列级 item 重试是外层兜底, 它会重跑整个 case(重新 embed query + 检索), 代价高,
   不该被限流这种抖动频繁触发。

3. **`httpx.Client` 由外部注入**: 测试用 `httpx.MockTransport` 离线构造 200/429/401,
   不联网、不花钱、不依赖配额; 这是"客户端可测"和"只能连真网测"的分水岭。
   `trust_env=False` 与 Qdrant 客户端同策略, 避免本机代理(Clash)干扰外部 API 调用。

用法:
    client = ChatClient(base_url=..., api_key=..., model="deepseek-ai/DeepSeek-V3.2")
    result = client.complete([{"role": "user", "content": "你好"}], temperature=0, max_tokens=64)
    print(result.text, result.prompt_tokens, result.latency_ms)
"""
from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Any, Self

import httpx

# 可重试的 HTTP 状态: 限流 / 服务端抖动 / 网关
RETRYABLE_STATUS = frozenset({408, 409, 425, 429, 500, 502, 503, 504})
# 退避序列(秒); 超出长度时取最后一个
RETRY_BACKOFF_SECONDS = (0.5, 1.5)
DEFAULT_MAX_RETRIES = 2
_PREVIEW_CHARS = 200


class LLMError(RuntimeError):
    """LLM 调用相关错误的基类。"""


class LLMTransientError(LLMError):
    """瞬时错误(可重试): 限流、服务端 5xx、超时、网络抖动。"""


class LLMFatalError(LLMError):
    """致命错误(不可重试): 鉴权/参数/响应体结构问题 —— 重试一万次也一样。"""


@dataclass(frozen=True)
class ChatResult:
    """一次 chat 调用的结果与用量。"""

    text: str
    prompt_tokens: int
    completion_tokens: int
    latency_ms: int
    raw_model: str

    @property
    def total_tokens(self) -> int:
        return self.prompt_tokens + self.completion_tokens


class ChatClient:
    """OpenAI 兼容 `/chat/completions` 客户端(DeepSeek / SiliconFlow 等通用)。"""

    def __init__(
        self,
        *,
        base_url: str,
        api_key: str,
        model: str,
        timeout: float = 60.0,
        max_retries: int = DEFAULT_MAX_RETRIES,
        client: httpx.Client | None = None,
    ) -> None:
        if not api_key:
            raise LLMFatalError(f"缺少 api_key(provider base_url={base_url}); 请检查 .env 里的生成侧配置")
        if not base_url:
            raise LLMFatalError("缺少 base_url")
        if not model:
            raise LLMFatalError("缺少 model")

        self.base_url = base_url.rstrip("/")
        self.model = model
        self.timeout = timeout
        self.max_retries = max(0, max_retries)
        self._api_key = api_key
        self._owns_client = client is None
        self._client = client or httpx.Client(timeout=timeout, trust_env=False)

    # ---- 生命周期 ----

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> Self:
        return self

    def __exit__(self, *exc_info: object) -> None:
        self.close()

    # ---- 主入口 ----

    def complete(
        self,
        messages: list[dict[str, Any]],
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int = 512,
        response_format: dict[str, Any] | None = None,
    ) -> ChatResult:
        """调用 chat completions。

        response_format 用于 M4-2 的 judge(严格 JSON 输出); 生成链路不需要。
        空文本(`text == ""`)**不算异常** —— 由调用方决定语义(通常应视为一次失败并重试,
        因为"资料中未提及"这类合规回答不会是空串)。
        """
        payload: dict[str, Any] = {
            "model": model or self.model,
            "messages": messages,
            "temperature": temperature,
            "max_tokens": max_tokens,
        }
        if response_format is not None:
            payload["response_format"] = response_format

        url = f"{self.base_url}/chat/completions"
        attempts = self.max_retries + 1
        last_error: LLMTransientError | None = None

        for attempt in range(1, attempts + 1):
            started = time.perf_counter()
            try:
                response = self._client.post(url, json=payload, headers=self._headers())
            except httpx.TimeoutException as exc:
                last_error = LLMTransientError(f"请求超时({self.timeout}s, 第 {attempt}/{attempts} 次): {exc}")
            except httpx.HTTPError as exc:
                last_error = LLMTransientError(f"网络错误(第 {attempt}/{attempts} 次): {exc}")
            else:
                latency_ms = int((time.perf_counter() - started) * 1000)
                if response.status_code == 200:
                    return self._parse(response, latency_ms)
                if response.status_code in RETRYABLE_STATUS:
                    last_error = LLMTransientError(
                        f"HTTP {response.status_code}(第 {attempt}/{attempts} 次): {_preview(response.text)}"
                    )
                else:
                    # 致命错误立刻抛出, 不做任何重试
                    raise LLMFatalError(f"HTTP {response.status_code}: {_preview(response.text)}")

            if attempt < attempts:
                index = min(attempt - 1, len(RETRY_BACKOFF_SECONDS) - 1)
                time.sleep(RETRY_BACKOFF_SECONDS[index])

        assert last_error is not None  # 循环至少执行一次, 走到这里必然有错误
        raise last_error

    # ---- 内部 ----

    def _headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self._api_key}", "Content-Type": "application/json"}

    def _parse(self, response: httpx.Response, latency_ms: int) -> ChatResult:
        try:
            data = response.json()
        except ValueError as exc:  # 200 但响应体不是 JSON: 网关插页/供应商异常
            raise LLMFatalError(f"响应不是合法 JSON: {_preview(response.text)}") from exc

        choices = data.get("choices") or []
        if not choices:
            raise LLMFatalError(f"响应缺少 choices: {_preview(response.text)}")

        message = choices[0].get("message") or {}
        usage = data.get("usage") or {}
        return ChatResult(
            text=str(message.get("content") or "").strip(),
            prompt_tokens=_as_int(usage.get("prompt_tokens")),
            completion_tokens=_as_int(usage.get("completion_tokens")),
            latency_ms=latency_ms,
            raw_model=str(data.get("model") or self.model),
        )


def _as_int(value: Any) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def _preview(text: str, limit: int = _PREVIEW_CHARS) -> str:
    flat = " ".join((text or "").split())
    return flat[:limit] + ("…" if len(flat) > limit else "")
