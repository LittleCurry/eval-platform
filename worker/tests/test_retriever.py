"""检索器单测(离线): 批量向量化 + 批量查询映射 + top_k 传递 + payload 提取。"""
from __future__ import annotations

from types import SimpleNamespace
from typing import Any

from app.config import Settings
from app.retrieval.embedder import EmbedUsage
from app.retrieval.retriever import Hit, Retriever


class FakeEmbedder:
    def __init__(self, dim: int = 4) -> None:
        self.dim = dim
        self.calls: list[list[str]] = []
        self.closed = False

    def embed_texts(self, texts: list[str]) -> tuple[list[list[float]], EmbedUsage]:
        self.calls.append(list(texts))
        return [[float(i)] * self.dim for i, _ in enumerate(texts)], EmbedUsage(
            calls=1, texts=len(texts), tokens=len(texts), elapsed_ms=1
        )

    def close(self) -> None:
        self.closed = True


class FakeQdrant:
    """返回固定点集合, 记录收到的查询以便断言。"""

    def __init__(self, points_per_query: list[list[tuple[str, dict[str, Any], float]]] | None = None) -> None:
        self.queries: list[list[Any]] = []
        self._responses = points_per_query or []

    def query_batch_points(self, collection_name: str, queries: list[Any]) -> list[Any]:
        self.queries.append(queries)
        responses = []
        for i, _ in enumerate(queries):
            pts = self._responses[i] if i < len(self._responses) else []
            responses.append(
                SimpleNamespace(
                    points=[
                        SimpleNamespace(id=pid, score=score, payload=payload) for pid, payload, score in pts
                    ]
                )
            )
        return responses

    def close(self) -> None:
        pass


def make_retriever(qdrant: FakeQdrant, embedder: FakeEmbedder | None = None, top_k: int = 3) -> Retriever:
    settings = Settings(_env_file=None, embedding_dim=4)
    return Retriever(settings, "corpus_test", embedder=embedder or FakeEmbedder(), client=qdrant, top_k=top_k)


def test_search_many_embeds_once_and_maps_hits():
    qdrant = FakeQdrant(
        points_per_query=[
            [("p1", {"doc_id": "A01", "text": "线索回收"}, 0.9), ("p2", {"doc_id": "B02", "text": "上限"}, 0.8)],
            [("p3", {"doc_id": "C05", "text": "回收站"}, 0.7)],
        ]
    )
    embedder = FakeEmbedder()
    retriever = make_retriever(qdrant, embedder, top_k=5)

    results = retriever.search_many(["问题一", "问题二"])

    assert len(embedder.calls) == 1 and embedder.calls[0] == ["问题一", "问题二"]
    assert len(qdrant.queries) == 1 and len(qdrant.queries[0]) == 2
    assert qdrant.queries[0][0].limit == 5

    assert [h.point_id for h in results[0]] == ["p1", "p2"]
    assert results[0][0] == Hit(point_id="p1", score=0.9, payload={"doc_id": "A01", "text": "线索回收"})
    assert results[0][0].doc_id == "A01" and results[0][0].text == "线索回收"
    assert [h.point_id for h in results[1]] == ["p3"]


def test_search_single_returns_first_result_set():
    qdrant = FakeQdrant(points_per_query=[[("p1", {"doc_id": "A01"}, 1.0)]])
    retriever = make_retriever(qdrant)

    hits = retriever.search("问题")

    assert [h.point_id for h in hits] == ["p1"]


def test_search_by_vectors_skips_embedding():
    qdrant = FakeQdrant(points_per_query=[[("p1", {}, 0.5)]])
    embedder = FakeEmbedder()
    retriever = make_retriever(qdrant, embedder)

    results = retriever.search_by_vectors([[0.1, 0.2, 0.3, 0.4]])

    assert embedder.calls == []  # 直接给向量, 不应再调 embedding
    assert [h.point_id for h in results[0]] == ["p1"]


def test_empty_inputs_short_circuit():
    qdrant = FakeQdrant()
    retriever = make_retriever(qdrant)

    assert retriever.search_many([]) == []
    assert retriever.search_by_vectors([]) == []
    assert qdrant.queries == []


def test_top_k_override_per_call():
    qdrant = FakeQdrant(points_per_query=[[("p1", {}, 0.5)]])
    retriever = make_retriever(qdrant, top_k=3)

    retriever.search_many(["q"], top_k=10)
    assert qdrant.queries[0][0].limit == 10


def test_injected_deps_are_not_closed():
    qdrant = FakeQdrant()
    embedder = FakeEmbedder()
    retriever = make_retriever(qdrant, embedder)

    retriever.close()

    assert not embedder.closed  # 注入的依赖由调用方负责关闭


class FakeQdrantWithRequestsKwarg:
    """模拟新版 qdrant-client: 批量查询参数名为 requests(而非 queries)。"""

    def __init__(self) -> None:
        self.seen: list[list[Any]] = []

    def query_batch_points(self, collection_name: str, requests: list[Any]) -> list[Any]:
        self.seen.append(requests)
        return [SimpleNamespace(points=[SimpleNamespace(id="p1", score=0.9, payload={"doc_id": "A01"})])]

    def close(self) -> None:
        pass


def test_batch_query_is_compatible_with_requests_kwarg():
    qdrant = FakeQdrantWithRequestsKwarg()
    retriever = make_retriever(qdrant)  # type: ignore[arg-type]

    results = retriever.search_many(["问题"])

    assert len(qdrant.seen) == 1 and len(qdrant.seen[0]) == 1
    assert [h.point_id for h in results[0]] == ["p1"]