"""Qdrant collection 管理封装。

约定(见 process.md D1/D7):
- collection 名 = corpus{corpus_id}_{chunking_hash[:8]} —— 不同切分配置索引隔离, 互不污染;
- payload 结构固定: {doc_id, corpus_id, chunk_index, char_start, char_end, section, text};
- client 显式 trust_env=False: 本地基础设施流量不走 shell 代理(踩过 httpx/socks 的坑)。
"""
from __future__ import annotations

from collections.abc import Iterable, Sequence
from typing import Any

from qdrant_client import QdrantClient
from qdrant_client.http import models as qmodels

DISTANCE = qmodels.Distance.COSINE

def make_client(url: str, timeout: float = 10.0) -> QdrantClient:
    """创建 Qdrant 客户端(不信任环境代理)。

    check_compatibility=False: 版本由 compose 固定(服务端 v1.19.1)+客户端大版本锁定,
    跳过每次建连的兼容性探测, 避免偶发超时产生无意义警告。
    """
    return QdrantClient(url=url, timeout=timeout, trust_env=False, check_compatibility=False)


def collection_name(corpus_id: int, cfg_hash: str) -> str:
    """按 (语料, 切分配置指纹) 命名, 保证实验隔离与可复现。"""
    return f"corpus{corpus_id}_{cfg_hash[:8]}"


def collection_exists(client: QdrantClient, name: str) -> bool:
    return bool(client.collection_exists(name))


def ensure_collection(
        client: QdrantClient, name: str, dim: int, recreate: bool = False
) -> None:
    """确保 collection 存在且维度正确。

    - 不存在: 创建
    - 已存在且维度一致: 幂等返回
    - 已存在但维度不符: 报错提示(除非 recreate=True 先删后建)
    """
    exists = collection_exists(client, name)
    if exists and recreate:
        client.delete_collection(name)
        exists = False

    if exists:
        current = _vector_size(client, name)
        if current != dim:
            raise ValueError(
                f"collection {name} 维度为 {current}, 期望 {dim}; "
                f"请显式 recreate=True 或换切分配置"
            )
        return

    client.create_collection(
        collection_name=name,
        vectors_config=qmodels.VectorParams(size=dim, distance=DISTANCE),
    )


def collection_info(client: QdrantClient, name: str) -> dict[str, Any]:
    """返回 collection 概况, 供 CLI/测试查看。"""
    info = client.get_collection(name)
    return {
        "name": name,
        "dim": _vector_size(client, name),
        "points_count": info.points_count,
        "status": str(info.status),
    }


def delete_collection(client: QdrantClient, name: str) -> None:
    client.delete_collection(name)


def upsert_chunks(
        client: QdrantClient,
        name: str,
        items: Sequence[tuple[str | int, list[float], dict[str, Any]]],
) -> int:
    """写入 (id, vector, payload) 三元组列表, 返回写入条数。"""
    if not items:
        return 0
    points = [
        qmodels.PointStruct(id=point_id, vector=vector, payload=payload)
        for point_id, vector, payload in items
    ]
    client.upsert(collection_name=name, points=points, wait=True)
    return len(points)


def chunk_payload(
        *,
        doc_id: str,
        corpus_id: int,
        chunk_index: int,
        char_start: int,
        char_end: int,
        section: str,
        text: str,
) -> dict[str, Any]:
    """构造统一的 chunk payload(写入与读取两侧共用同一约定)。"""
    return {
        "doc_id": doc_id,
        "corpus_id": corpus_id,
        "chunk_index": chunk_index,
        "char_start": char_start,
        "char_end": char_end,
        "section": section,
        "text": text,
    }


def iter_payloads(points: Iterable[Any]) -> list[dict[str, Any]]:
    """从检索结果点里取出 payload 列表(便于测试与指标计算)。"""
    return [dict(p.payload or {}) for p in points]


def _vector_size(client: QdrantClient, name: str) -> int | None:
    """读取 collection 的向量维度(仅支持匿名单向量配置)。"""
    vectors = client.get_collection(name).config.params.vectors
    size = getattr(vectors, "size", None)
    if size is None and isinstance(vectors, dict):  # 命名向量场景取第一个
        first = next(iter(vectors.values()), None)
        size = getattr(first, "size", None)
    return size