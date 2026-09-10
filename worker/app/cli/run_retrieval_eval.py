"""CLI: 同步跑一次检索评测(M2 最简 runner, M3 迁入异步 worker + 落库)。

用法:
    python -m app.cli.run_retrieval_eval --dataset-id 3 --corpus-id 4 --top-k 5
    python -m app.cli.run_retrieval_eval --dataset-id 3 --corpus-id 4 --top-k 5 --min-recall 0.9

输出 JSON 报告: run 级指标 + 最差 N 题明细(便于直接看 bad case)。
退出码: recall@k 低于 --min-recall 时返回 1。
"""
from __future__ import annotations

import argparse
import json
import sys

from app.config import Settings
from app.logging_conf import setup_logging
from app.metrics.retrieval import CaseMetric, aggregate, evaluate_case
from app.retrieval.anchor import AnchorSpec, GoldCase, map_dataset_cases
from app.retrieval.chunker import STRATEGIES, ChunkingConfig, chunk_document, chunking_hash
from app.retrieval.collections import collection_exists, collection_name
from app.retrieval.retriever import Retriever
from app.store import list_cases, list_documents


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="检索评测(同步 runner)")
    parser.add_argument("--dataset-id", type=int, required=True)
    parser.add_argument("--corpus-id", type=int, required=True)
    parser.add_argument("--top-k", type=int, default=5)
    parser.add_argument("--strategy", choices=STRATEGIES, default="headings")
    parser.add_argument("--chunk-size", type=int, default=500)
    parser.add_argument("--overlap", type=int, default=50)
    parser.add_argument("--min-chars", type=int, default=80)
    parser.add_argument("--worst", type=int, default=10, help="报告里展示的最差 N 题")
    parser.add_argument("--min-recall", type=float, default=0.0, help="recall@k 低于此值时退出码 1")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    settings = Settings()
    setup_logging(settings.log_level)

    cfg = ChunkingConfig(
        strategy=args.strategy,
        chunk_size=args.chunk_size,
        overlap=args.overlap,
        min_chars=args.min_chars,
    )
    cfg_hash = chunking_hash(cfg)
    collection = collection_name(args.corpus_id, cfg_hash)

    docs = list_documents(settings.pg_dsn, args.corpus_id)
    case_rows = list_cases(settings.pg_dsn, args.dataset_id)
    if not docs or not case_rows:
        print(json.dumps({"error": "语料或评测集为空"}, ensure_ascii=False))
        return 1

    doc_chunks = {row.doc_id: chunk_document(row.raw_text, cfg) for row in docs}
    cases = [
        GoldCase(
            qid=row.qid,
            anchors=[
                AnchorSpec(doc=str(a.get("doc", "")), span=str(a.get("span", "")))
                for a in row.gold_anchors
                if isinstance(a, dict)
            ],
        )
        for row in case_rows
    ]
    mapping = map_dataset_cases(cases, doc_chunks, args.corpus_id, cfg_hash)
    gold_by_qid = {c.qid: c.gold_point_ids for c in mapping.cases}

    retriever = Retriever(settings, collection, top_k=args.top_k)
    try:
        if not collection_exists(retriever.client, collection):
            print(json.dumps({"error": f"索引不存在: {collection}, 请先跑 index_corpus"}, ensure_ascii=False))
            return 1

        evaluable = [row for row in case_rows if gold_by_qid.get(row.qid)]
        responses = retriever.search_many([row.question for row in evaluable])

        case_metrics: list[CaseMetric] = []
        details: list[dict[str, object]] = []
        for row, hits in zip(evaluable, responses, strict=True):
            gold = gold_by_qid[row.qid]
            retrieved_ids = [h.point_id for h in hits]
            metric = evaluate_case(row.qid, gold, retrieved_ids, args.top_k)
            if metric is None:
                continue
            case_metrics.append(metric)
            details.append(
                {
                    "qid": row.qid,
                    "question": row.question,
                    "difficulty": row.difficulty,
                    "category": row.category,
                    "recall": metric.recall,
                    "first_hit_rank": metric.first_hit_rank,
                    "gold_docs": sorted({h.doc_id for h in hits if h.point_id in gold}),
                    "top_docs": [h.doc_id for h in hits],
                }
            )
    finally:
        retriever.close()

    skipped = len(case_rows) - len(case_metrics)
    metrics = aggregate(case_metrics, args.top_k, skipped=skipped)
    worst = sorted(details, key=lambda d: (d["recall"], -float(d["first_hit_rank"] or 0)))[: args.worst]

    report = {
        "dataset_id": args.dataset_id,
        "corpus_id": args.corpus_id,
        "collection": collection,
        "cfg_hash": cfg_hash,
        "chunking": {
            "strategy": cfg.strategy,
            "chunk_size": cfg.chunk_size,
            "overlap": cfg.overlap,
            "min_chars": cfg.min_chars,
        },
        "mapping": {
            "cases": mapping.total,
            "mapped": mapping.mapped,
            "coverage": round(mapping.coverage, 4),
            "failures": mapping.failures[:10],
        },
        "metrics": metrics,
        "worst_cases": worst,
    }
    print(json.dumps(report, ensure_ascii=False, indent=2))

    if float(metrics["recall_at_k"]) < args.min_recall:  # type: ignore[arg-type]
        print(f"recall@{args.top_k} 低于阈值 {args.min_recall}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())