"""embedding 客户端单测: 分批 / 重试 / 维度校验 / 用量统计(用 MockTransport, 不碰网络)。"""
from __future__ import annotations

import json
from collections.abc import Callable

import httpx
import pytest

from app.config import Settings
from app.retrieval.embedder import Embedder, EmbeddingError


def make_settings(**over: object) -> Settings:
    base: dict[str, object] = {
        "_env_file": None,
        "siliconflow_api_key": "sk-test",
        "embedding_base_url": "https://fake.local/v1",
        "embedding_model": "BAAI/bge-m3",
        "embedding_dim": 4,
        "embedding_batch_size": 2,
    }
    base.update(over)
    return Settings(**base)  # type: ignore[arg-type]


def make_embedder(
        handler: Callable[[httpx.Request], httpx.Response],
        settings: Settings | None = None,
        **kw: object,
) -> Embedder:
    settings = settings or make_settings()
    client = httpx.Client(
        transport=httpx.MockTransport(handler),
        base_url=settings.embedding_base_url,
        headers={"Authorization": f"Bearer {settings.siliconflow_api_key}"},
    )
    return Embedder(settings, client=client, backoff_base=0.0, **kw)  # type: ignore[arg-type]


def success_handler(calls: list[list[str]], dim: int = 4) -> Callable[[httpx.Request], httpx.Response]:
    def handler(request: httpx.Request) -> httpx.Response:
        batch = json.loads(request.content)["input"]
        calls.append(batch)
        data = [{"index": i, "embedding": [0.1] * dim} for i in range(len(batch))]
        return httpx.Response(200, json={"data": data, "usage": {"total_tokens": 3 * len(batch)}})

    return handler


def test_batching_splits_by_batch_size_and_accumulates_usage():
    calls: list[list[str]] = []
    emb = make_embedder(success_handler(calls))
    vectors, usage = emb.embed_texts(["a", "b", "c", "d", "e"])

    assert len(vectors) == 5
    assert [len(b) for b in calls] == [2, 2, 1]
    assert (usage.calls, usage.texts, usage.tokens) == (3, 5, 15)
    emb.close()


def test_retry_on_429_then_success():
    attempts: list[int] = []

    def handler(request: httpx.Request) -> httpx.Response:
        attempts.append(1)
        if len(attempts) == 1:
            return httpx.Response(429, json={"message": "rate limited"})
        return httpx.Response(
            200,
            json={"data": [{"index": 0, "embedding": [0.0] * 4}], "usage": {"total_tokens": 2}},
        )

    emb = make_embedder(handler, max_retries=2)
    vectors, usage = emb.embed_texts(["x"])

    assert len(attempts) == 2  # 第一次 429, 第二次成功
    assert len(vectors) == 1 and usage.calls == 1 and usage.tokens == 2
    emb.close()


def test_client_error_is_not_retried():
    attempts: list[int] = []

    def handler(request: httpx.Request) -> httpx.Response:
        attempts.append(1)
        return httpx.Response(400, json={"message": "bad request"})

    emb = make_embedder(handler, max_retries=3)
    with pytest.raises(EmbeddingError, match="HTTP 400"):
        emb.embed_texts(["x"])
    assert len(attempts) == 1  # 4xx 不重试, 不浪费配额
    emb.close()


def test_dimension_mismatch_raises():
    calls: list[list[str]] = []
    emb = make_embedder(success_handler(calls, dim=3))  # 配置要求 4 维
    with pytest.raises(EmbeddingError, match="向量维度"):
        emb.embed_texts(["x"])
    emb.close()


def test_empty_input_makes_no_request():
    def handler(request: httpx.Request) -> httpx.Response:  # pragma: no cover
        raise AssertionError("空输入不应发起请求")

    emb = make_embedder(handler)
    vectors, usage = emb.embed_texts([])
    assert vectors == [] and usage.calls == 0
    emb.close()


def test_missing_api_key_raises():
    settings = make_settings(siliconflow_api_key="")
    emb = make_embedder(success_handler([]), settings=settings)
    with pytest.raises(EmbeddingError, match="SILICONFLOW_API_KEY"):
        emb.embed_texts(["x"])
    emb.close()