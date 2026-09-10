"""live 测试: 真实数据上跑一次检索评测(RUN_LIVE=1)。

校验不变量:
1. 30 题全部可评测(映射覆盖率应为 100%);
2. 每题都拿到 top-k 条结果;
3. recall@k 达到基线阈值(评测集与切分方案匹配时应接近 1.0);
4. 指标口径自洽: 0 <= recall/precision/mrr/hit <= 1, 且 hit_at_k == 命中题占比。
"""

from __future__ import annotations

import os

import pytest

from app.config import Settings
from app.metrics.retrieval import aggregate, evaluate_case
from app.retrieval.anchor import AnchorSpec, GoldCase, map_dataset_cases
from app.retrieval.chunker import ChunkingConfig, chunk_document, chunking_hash
from app.retrieval.collections import collection_name
from app.retrieval.retriever import Retriever
from app.store import list_cases, list_documents

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实服务"
)

CORPUS_ID = int(os.getenv("LIVE_CORPUS_ID", "4"))
DATASET_ID = int(os.getenv("LIVE_DATASET_ID", "3"))
TOP_K = int(os.getenv("LIVE_TOP_K", "5"))


def test_live_retrieval_metrics():
    settings = Settings()
    cfg = ChunkingConfig()
    cfg_hash = chunking_hash(cfg)
    collection = collection_name(CORPUS_ID, cfg_hash)

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
    mapping = map_dataset_cases(cases, doc_chunks, CORPUS_ID, cfg_hash)
    gold_by_qid = {c.qid: c.gold_point_ids for c in mapping.cases}

    evaluable = [row for row in case_rows if gold_by_qid.get(row.qid)]
    assert len(evaluable) == len(case_rows) == 30, "30 题应全部可评测"

    retriever = Retriever(settings, collection, top_k=TOP_K)
    try:
        responses = retriever.search_many([row.question for row in evaluable])
    finally:
        retriever.close()

    assert len(responses) == len(evaluable)
    for hits in responses:
        assert len(hits) == TOP_K, "每题都应拿到 top-k 条结果"
        assert hits == sorted(hits, key=lambda h: -h.score), "结果应按相似度降序"

    metrics = [
        evaluate_case(row.qid, gold_by_qid[row.qid], [h.point_id for h in hits], TOP_K)
        for row, hits in zip(evaluable, responses, strict=True)
    ]
    assert all(m is not None for m in metrics)
    case_metrics = [m for m in metrics if m is not None]

    agg = aggregate(case_metrics, TOP_K)
    assert 0.0 <= float(agg["recall_at_k"]) <= 1.0  # type: ignore[arg-type]
    assert 0.0 <= float(agg["mrr_at_k"]) <= 1.0  # type: ignore[arg-type]
    assert float(agg["recall_at_k"]) >= 0.8, f"recall@{TOP_K} 过低: {agg}"
    assert float(agg["hit_at_k"]) == pytest.approx(
        sum(1 for m in case_metrics if m.hit) / len(case_metrics), abs=1e-6
    )