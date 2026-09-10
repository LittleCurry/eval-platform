"""live 测试: 真调 SiliconFlow embedding + 真 Qdrant 端到端(RUN_LIVE=1 才跑, 会消耗少量额度)。"""

from __future__ import annotations

import os

import pytest

from app.config import Settings
from app.retrieval.chunker import ChunkingConfig
from app.retrieval.collections import delete_collection, make_client
from app.retrieval.embedder import Embedder
from app.retrieval.indexer import index_corpus
from app.store import DocumentRow

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实服务"
)

DOC1 = (
    "# 线索回收\n\n"
    "已分配线索超过 7 天无任何跟进会自动回到未分配池。\n\n"
    "## 在跟上限\n\n每人同时在跟线索上限默认 200 条。\n"
)
DOC2 = "# 回收站\n\n被删除的记录进入回收站统一保留 30 天, 仅系统管理员可恢复。\n"


def test_embed_dimension_matches_config():
    settings = Settings()
    embedder = Embedder(settings)
    try:
        vectors, usage = embedder.embed_texts(["测试向量维度"])
    finally:
        embedder.close()
    assert len(vectors) == 1
    assert len(vectors[0]) == settings.embedding_dim
    assert usage.tokens > 0


def test_index_and_query_end_to_end():
    settings = Settings()
    cfg = ChunkingConfig(strategy="headings", chunk_size=120, overlap=0, min_chars=1)
    docs = [
        DocumentRow(id=1, doc_id="T1", title="线索回收", raw_text=DOC1),
        DocumentRow(id=2, doc_id="T2", title="回收站", raw_text=DOC2),
    ]

    report = index_corpus(settings, 9998, cfg, recreate=True, docs=docs)
    assert report.points == report.chunks > 0
    assert report.embed_tokens > 0

    client = make_client(settings.qdrant_url)
    embedder = Embedder(settings)
    try:
        vectors, _ = embedder.embed_texts(["线索超过多少天会被自动回收？"])
        result = client.query_points(
            collection_name=report.collection, query=vectors[0], limit=1
        )
        top = result.points[0].payload or {}
        assert top.get("doc_id") == "T1", f"top1 应为线索文档, 实际 {top.get('doc_id')}"
        assert "7 天" in str(top.get("text", ""))
    finally:
        embedder.close()
        delete_collection(client, report.collection)
        client.close()