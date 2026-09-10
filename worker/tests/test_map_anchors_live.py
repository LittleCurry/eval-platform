"""live 测试: 用真实 PG(评测集/语料) + 真实 Qdrant 校验锚点映射(RUN_LIVE=1)。

覆盖两个关键不变量:
1. 覆盖率 >= 阈值(评测集质量门禁);
2. **映射出的 gold point id 必须真实存在于 Qdrant collection 中** —— 保证映射与索引一致。
"""

from __future__ import annotations

import os

import pytest

from app.config import Settings
from app.retrieval.anchor import AnchorSpec, GoldCase, map_dataset_cases
from app.retrieval.chunker import ChunkingConfig, chunk_document, chunking_hash
from app.retrieval.collections import collection_exists, collection_name, make_client
from app.store import list_cases, list_documents

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实服务"
)

CORPUS_ID = int(os.getenv("LIVE_CORPUS_ID", "4"))
DATASET_ID = int(os.getenv("LIVE_DATASET_ID", "3"))


def build_mapping(settings: Settings):
    cfg = ChunkingConfig()
    cfg_hash = chunking_hash(cfg)
    docs = list_documents(settings.pg_dsn, CORPUS_ID)
    doc_chunks = {row.doc_id: chunk_document(row.raw_text, cfg) for row in docs}
    case_rows = list_cases(settings.pg_dsn, DATASET_ID)
    cases = [
        GoldCase(
            qid=row.qid,
            anchors=[AnchorSpec(doc=str(a.get("doc", "")), span=str(a.get("span", ""))) for a in row.gold_anchors],
        )
        for row in case_rows
    ]
    return cfg_hash, map_dataset_cases(cases, doc_chunks, CORPUS_ID, cfg_hash), doc_chunks


def test_live_anchor_mapping_coverage():
    settings = Settings()
    _, mapping, doc_chunks = build_mapping(settings)

    assert mapping.total == 30, f"评测集应有 30 题, 实际 {mapping.total}"
    assert mapping.coverage >= 0.9, f"覆盖率过低: {mapping.coverage:.2%}, 失败: {mapping.failures}"
    # 每个成功映射的用例都应有非空 G
    assert all(c.gold_point_ids for c in mapping.cases if c.ok)
    # 语料被切成多块且不是碎片
    assert sum(len(v) for v in doc_chunks.values()) > len(doc_chunks)


def test_live_gold_point_ids_exist_in_qdrant():
    settings = Settings()
    cfg_hash, mapping, _ = build_mapping(settings)
    name = collection_name(CORPUS_ID, cfg_hash)

    client = make_client(settings.qdrant_url)
    try:
        assert collection_exists(client, name), f"索引不存在: {name}, 请先跑 index_corpus"
        indexed: set[str] = set()
        offset = None
        while True:
            points, offset = client.scroll(
                collection_name=name, limit=256, offset=offset, with_payload=False, with_vectors=False
            )
            indexed.update(str(p.id) for p in points)
            if offset is None:
                break
    finally:
        client.close()

    gold_ids = {pid for c in mapping.cases for pid in c.gold_point_ids}
    missing = gold_ids - indexed
    assert not missing, f"{len(missing)} 个 gold point id 不在索引里: {list(missing)[:3]}"