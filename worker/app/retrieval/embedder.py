"""embedding 客户端(SiliconFlow / OpenAI 兼容): 分批 + 重试 + 用量统计 + 维度校验。

要点(见 process.md D8 成本控制 / D11 供应商抽象):
- 单次批量条数受接口限制 -> 按配置分批, 默认 16;
- 429/5xx/网络错误指数退避重试, 其余 4xx 直接失败(不浪费配额);
- 每次调用累计 token 用量, 供报告做成本核算;
- 返回向量维度必须等于配置, 防止"换了模型忘了改配置"污染索引;
- trust_env=False: 外部 API 也走显式配置, 不受 shell 代理变量影响(可复现)。
"""
from __future__ import annotations

import time
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any

import httpx


class EmbeddingError(RuntimeError):
    """embedding 调用失败(不可重试的 4xx, 或重试耗尽)。"""


@dataclass
class EmbedUsage:
    calls: int = 0
    texts: int = 0
    tokens: int = 0
    elapsed_ms: int = 0

    def merge(self, other: EmbedUsage) -> None:
        self.calls += other.calls
        self.texts += other.texts
        self.tokens += other.tokens
        self.elapsed_ms += other.elapsed_ms

    def to_json(self) -> dict[str, int]:
        return {
            "calls": self.calls,
            "texts": self.texts,
            "tokens": self.tokens,
            "elapsed_ms": self.elapsed_ms,
        }


class Embedder:
    """把文本批量转成向量。可注入 client, 便于测试用 MockTransport。"""

    def __init__(
            self,
            settings: Any,
            client: httpx.Client | None = None,
            max_retries: int = 3,
            backoff_base: float = 1.0,
    ) -> None:
        self.settings = settings
        self.max_retries = max_retries
        self.backoff_base = backoff_base
        self._client = client or httpx.Client(
            base_url=settings.embedding_base_url,
            timeout=60.0,
            trust_env=False,
            headers={"Authorization": f"Bearer {settings.siliconflow_api_key}"},
        )

    def embed_texts(self, texts: Sequence[str]) -> tuple[list[list[float]], EmbedUsage]:
        """返回 (向量列表, 用量)。空输入不发请求。"""
        usage = EmbedUsage()
        vectors: list[list[float]] = []
        if not texts:
            return vectors, usage
        if not self.settings.siliconflow_api_key:
            raise EmbeddingError("缺少 SILICONFLOW_API_KEY, 请在 .env 中配置")

        batch_size = max(1, int(self.settings.embedding_batch_size))
        for start in range(0, len(texts), batch_size):
            batch = list(texts[start : start + batch_size])
            batch_vectors, batch_usage = self._embed_batch(batch)
            vectors.extend(batch_vectors)
            usage.merge(batch_usage)
        return vectors, usage

    def close(self) -> None:
        self._client.close()

    # ---- 内部 ----

    def _embed_batch(self, batch: Sequence[str]) -> tuple[list[list[float]], EmbedUsage]:
        payload = {"model": self.settings.embedding_model, "input": list(batch)}
        last_err = "未知错误"

        for attempt in range(self.max_retries + 1):
            started = time.perf_counter()
            try:
                resp = self._client.post("/embeddings", json=payload)
            except httpx.TransportError as exc:  # 网络/超时
                last_err = f"网络错误: {exc.__class__.__name__}: {exc}"
            else:
                elapsed_ms = int((time.perf_counter() - started) * 1000)
                if resp.status_code == 200:
                    return self._parse_response(resp.json(), len(batch), elapsed_ms)
                body = resp.text[:200]
                if resp.status_code == 429 or resp.status_code >= 500:
                    last_err = f"HTTP {resp.status_code}: {body}"
                else:
                    raise EmbeddingError(f"HTTP {resp.status_code}: {body}")

            if attempt < self.max_retries:
                time.sleep(self.backoff_base * (2**attempt))

        raise EmbeddingError(f"embedding 重试 {self.max_retries} 次后仍失败: {last_err}")

    def _parse_response(
            self, data: dict[str, Any], batch_len: int, elapsed_ms: int
    ) -> tuple[list[list[float]], EmbedUsage]:
        items = sorted(data.get("data", []), key=lambda d: d.get("index", 0))
        vectors = [item["embedding"] for item in items]
        if len(vectors) != batch_len:
            raise EmbeddingError(f"返回向量数 {len(vectors)} != 请求条数 {batch_len}")
        for vec in vectors:
            if len(vec) != self.settings.embedding_dim:
                raise EmbeddingError(
                    f"向量维度 {len(vec)} != 配置 embedding_dim={self.settings.embedding_dim}"
                )
        tokens = int((data.get("usage") or {}).get("total_tokens", 0))
        usage = EmbedUsage(calls=1, texts=batch_len, tokens=tokens, elapsed_ms=elapsed_ms)
        return vectors, usage