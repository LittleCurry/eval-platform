"""语料索引入库编排: 文档 → chunk → 向量 → Qdrant。

设计要点:
- 点 ID 由 (语料, 文档, chunk 序号, 切分配置指纹) 决定 -> 确定性; 重跑同配置即覆盖写, 不产生重复点;
- 文档顺序按 doc_id 稳定排序, chunk 顺序稳定 -> 索引结果可复现;
- embedder/client/docs 可注入 -> 单测完全离线。
"""
from __future__ import annotations

import time
import uuid
from collections.abc import Iterable
from dataclasses import asdict, dataclass
from typing import Any

from app.retrieval.chunker import Chunk, ChunkingConfig, chunk_document, chunking_hash
from app.retrieval.collections import (
    chunk_payload,
    collection_name,
    ensure_collection,
    make_client,
    upsert_chunks,
)
from app.retrieval.embedder import Embedder
from app.store import DocumentRow, list_documents

UPSERT_BATCH = 128

_NAMESPACE = uuid.UUID("6f1d2c3a-9b4e-4c7a-8f10-2b3c4d5e6f70")


@dataclass
class IndexReport:
    collection: str
    cfg_hash: str
    docs: int
    chunks: int
    points: int
    embed_calls: int
    embed_tokens: int
    elapsed_ms: int

    def to_json(self) -> dict[str, Any]:
        return asdict(self)


def point_id(corpus_id: int, doc_id: str, chunk_index: int, cfg_hash: str) -> str:
    """确定性点 ID: 同输入永远同 ID, 保证重复索引是覆盖而非追加。"""
    return str(uuid.uuid5(_NAMESPACE, f"{corpus_id}:{doc_id}:{chunk_index}:{cfg_hash}"))


def index_corpus(
        settings: Any,
        corpus_id: int,
        cfg: ChunkingConfig,
        *,
        recreate: bool = False,
        embedder: Embedder | None = None,
        client: Any | None = None,
        docs: Iterable[DocumentRow] | None = None,
) -> IndexReport:
    """把语料库索引进 Qdrant, 返回索引报告。"""
    started = time.perf_counter()
    cfg_hash = chunking_hash(cfg)
    name = collection_name(corpus_id, cfg_hash)

    owns_client = client is None
    client = client if client is not None else make_client(settings.qdrant_url)
    owns_embedder = embedder is None
    embedder = embedder if embedder is not None else Embedder(settings)

    try:
        ensure_collection(client, name, dim=settings.embedding_dim, recreate=recreate)
        rows = list(docs) if docs is not None else list_documents(settings.pg_dsn, corpus_id)

        entries: list[tuple[DocumentRow, Chunk]] = []
        for row in rows:
            for chunk in chunk_document(row.raw_text, cfg):
                entries.append((row, chunk))

        if not entries:
            return IndexReport(
                collection=name,
                cfg_hash=cfg_hash,
                docs=len(rows),
                chunks=0,
                points=0,
                embed_calls=0,
                embed_tokens=0,
                elapsed_ms=int((time.perf_counter() - started) * 1000),
            )

        vectors, usage = embedder.embed_texts([chunk.text for _, chunk in entries])

        points: list[tuple[str, list[float], dict[str, Any]]] = []
        for (row, chunk), vector in zip(entries, vectors, strict=True):
            payload = chunk_payload(
                doc_id=row.doc_id,
                corpus_id=corpus_id,
                chunk_index=chunk.index,
                char_start=chunk.char_start,
                char_end=chunk.char_end,
                section=chunk.section,
                text=chunk.text,
            )
            payload["doc_title"] = row.title
            points.append((point_id(corpus_id, row.doc_id, chunk.index, cfg_hash), vector, payload))

        written = 0
        for start in range(0, len(points), UPSERT_BATCH):
            written += upsert_chunks(client, name, points[start : start + UPSERT_BATCH])

        return IndexReport(
            collection=name,
            cfg_hash=cfg_hash,
            docs=len(rows),
            chunks=len(entries),
            points=written,
            embed_calls=usage.calls,
            embed_tokens=usage.tokens,
            elapsed_ms=int((time.perf_counter() - started) * 1000),
        )
    finally:
        if owns_embedder:
            embedder.close()
        if owns_client:
            client.close()