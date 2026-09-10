"""检索执行: 问题 → 向量 → Qdrant top-k。

要点:
- 问题向量与索引向量必须来自同一 embedding 模型(否则指标无意义);
- 用 query_batch_points 批量检索, 一次网络往返拿多题结果(大批量评测时显著省时);
- 返回 Hit 保留 point_id 与 payload(取 chunk 文本/来源, 供 M4 生成侧与 Bad Case 展示)。
"""
from __future__ import annotations

import inspect
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any

from qdrant_client.http import models as qmodels

from app.retrieval.collections import make_client
from app.retrieval.embedder import Embedder


@dataclass(frozen=True)
class Hit:
    point_id: str
    score: float
    payload: dict[str, Any]

    @property
    def doc_id(self) -> str:
        return str(self.payload.get("doc_id", ""))

    @property
    def text(self) -> str:
        return str(self.payload.get("text", ""))


class Retriever:
    """针对某个 collection 的检索器。可注入 embedder/client, 便于测试。"""

    def __init__(
            self,
            settings: Any,
            collection: str,
            *,
            embedder: Embedder | None = None,
            client: Any | None = None,
            top_k: int = 5,
    ) -> None:
        self.settings = settings
        self.collection = collection
        self.top_k = top_k
        self._owns_embedder = embedder is None
        self.embedder = embedder if embedder is not None else Embedder(settings)
        self._owns_client = client is None
        self.client = client if client is not None else make_client(settings.qdrant_url)

    def search(self, question: str) -> list[Hit]:
        return self.search_many([question])[0]

    def search_many(self, questions: Sequence[str], top_k: int | None = None) -> list[list[Hit]]:
        """批量检索: 先批量向量化, 再一次批量查询 Qdrant。"""
        if not questions:
            return []
        k = top_k or self.top_k
        vectors, _ = self.embedder.embed_texts(list(questions))
        return self.search_by_vectors(vectors, top_k=k)

    def search_by_vectors(self, vectors: Sequence[Sequence[float]], top_k: int | None = None) -> list[list[Hit]]:
        """已有问题向量时直接检索(便于复用一次 embedding 结果)。"""
        if not vectors:
            return []
        k = top_k or self.top_k
        queries = [
            qmodels.QueryRequest(query=list(vec), limit=k, with_payload=True, with_vector=False)
            for vec in vectors
        ]
        responses = self._query_batch(queries)
        return [
            [
                Hit(point_id=str(point.id), score=float(point.score or 0.0), payload=dict(point.payload or {}))
                for point in response.points
            ]
            for response in responses
        ]

    def _query_batch(self, queries: list[qmodels.QueryRequest]) -> list[Any]:
        """批量查询, 兼容不同 qdrant-client 版本的参数名(requests / queries)。"""
        params = inspect.signature(self.client.query_batch_points).parameters
        key = "requests" if "requests" in params else "queries"
        return self.client.query_batch_points(collection_name=self.collection, **{key: queries})

    def close(self) -> None:
        if self._owns_embedder:
            self.embedder.close()
        if self._owns_client:
            self.client.close()