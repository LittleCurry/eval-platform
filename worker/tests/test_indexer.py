"""indexer 单测: fake embedder + fake qdrant, 校验 chunk→point 映射、确定性 ID、幂等重跑(离线)。"""
from __future__ import annotations

from types import SimpleNamespace
from typing import Any

from app.config import Settings
from app.retrieval.chunker import ChunkingConfig
from app.retrieval.embedder import EmbedUsage
from app.retrieval.indexer import index_corpus, point_id
from app.store import DocumentRow

DOC1 = "# 线索回收\n\n线索超过 7 天无跟进会自动回到未分配池。\n\n## 上限\n\n每人同时在跟线索默认 200 条。\n"
DOC2 = "# 回收站\n\n删除的记录进入回收站保留 30 天。\n"
DOCS = [
    DocumentRow(id=1, doc_id="A01", title="线索回收", raw_text=DOC1),
    DocumentRow(id=2, doc_id="B02", title="回收站", raw_text=DOC2),
]


class FakeEmbedder:
    def __init__(self, dim: int = 4) -> None:
        self.dim = dim
        self.calls: list[list[str]] = []
        self.closed = False

    def embed_texts(self, texts: list[str]) -> tuple[list[list[float]], EmbedUsage]:
        self.calls.append(list(texts))
        vectors = [[0.5] * self.dim for _ in texts]
        return vectors, EmbedUsage(calls=1, texts=len(texts), tokens=2 * len(texts), elapsed_ms=1)

    def close(self) -> None:
        self.closed = True


class FakeQdrant:
    def __init__(self) -> None:
        self.dims: dict[str, int] = {}
        self.points: dict[str, list[Any]] = {}
        self.recreated: list[str] = []

    def collection_exists(self, name: str) -> bool:
        return name in self.dims

    def create_collection(self, collection_name: str, vectors_config: Any) -> None:
        self.dims[collection_name] = vectors_config.size
        self.points.setdefault(collection_name, [])

    def get_collection(self, name: str) -> Any:
        return SimpleNamespace(
            config=SimpleNamespace(
                params=SimpleNamespace(vectors=SimpleNamespace(size=self.dims[name]))
            ),
            points_count=len(self.points.get(name, [])),
        )

    def delete_collection(self, name: str) -> None:
        self.recreated.append(name)
        self.dims.pop(name, None)
        self.points.pop(name, None)

    def upsert(self, collection_name: str, points: list[Any], wait: bool = True) -> None:
        self.points.setdefault(collection_name, []).extend(points)

    def close(self) -> None:
        pass


def test_index_maps_chunks_to_points_with_payload():
    settings = Settings(_env_file=None, embedding_dim=4)
    cfg = ChunkingConfig(strategy="headings", chunk_size=120, overlap=0, min_chars=1)
    client, embedder = FakeQdrant(), FakeEmbedder()

    report = index_corpus(settings, 7, cfg, embedder=embedder, client=client, docs=DOCS)

    assert report.docs == 2
    assert report.chunks == report.points > 0
    assert report.embed_tokens == 2 * report.chunks
    points = client.points[report.collection]
    assert len(points) == report.chunks
    assert all(p.vector == [0.5] * 4 for p in points)
    payload = points[0].payload
    for key in ("doc_id", "corpus_id", "chunk_index", "char_start", "char_end", "section", "text", "doc_title"):
        assert key in payload
    assert payload["corpus_id"] == 7
    # embedder 收到的文本与 chunk 数一致
    assert sum(len(batch) for batch in embedder.calls) == report.chunks
    # 注入的依赖由调用方持有, index_corpus 不应擅自关闭
    assert not embedder.closed


def test_rerun_is_idempotent_same_point_ids():
    settings = Settings(_env_file=None, embedding_dim=4)
    cfg = ChunkingConfig(strategy="headings", chunk_size=120, overlap=0, min_chars=1)
    client, embedder = FakeQdrant(), FakeEmbedder()

    first = index_corpus(settings, 7, cfg, embedder=embedder, client=client, docs=DOCS)
    ids_first = {p.id for p in client.points[first.collection]}

    second = index_corpus(settings, 7, cfg, embedder=embedder, client=client, docs=DOCS)
    ids_second = {p.id for p in client.points[second.collection]}

    assert ids_first == ids_second  # 确定性 ID -> 重跑覆盖, 不会翻倍


def test_point_id_is_deterministic_and_sensitive():
    a = point_id(7, "A01", 0, "hash-a")
    assert a == point_id(7, "A01", 0, "hash-a")
    assert a != point_id(7, "A01", 1, "hash-a")
    assert a != point_id(7, "A01", 0, "hash-b")


def test_recreate_flag_rebuilds_collection():
    settings = Settings(_env_file=None, embedding_dim=4)
    cfg = ChunkingConfig(strategy="headings", chunk_size=120, overlap=0, min_chars=1)
    client, embedder = FakeQdrant(), FakeEmbedder()

    first = index_corpus(settings, 7, cfg, embedder=embedder, client=client, docs=DOCS)
    assert client.recreated == []  # 首次创建无需删除

    second = index_corpus(
        settings, 7, cfg, recreate=True, embedder=embedder, client=client, docs=DOCS
    )
    assert second.collection == first.collection
    assert client.recreated == [first.collection]


def test_empty_docs_skips_embedding():
    settings = Settings(_env_file=None, embedding_dim=4)
    cfg = ChunkingConfig(strategy="headings")
    client, embedder = FakeQdrant(), FakeEmbedder()

    report = index_corpus(settings, 7, cfg, embedder=embedder, client=client, docs=[])
    assert report.docs == 0 and report.chunks == 0 and report.points == 0
    assert embedder.calls == []