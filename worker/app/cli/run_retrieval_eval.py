"""CLI: 离线对照跑一次检索评测, 可选落库(M2 最简 runner)。

**定位(定稿, 见 docs/reliability.md §6)**: 这是**调试/对照工具**, 不是生产路径。
生产路径是异步队列: `POST /api/v1/runs` 入队 + `python -m app.cli.worker` 消费
(有 checkpoint / 心跳 / 接管 / 重试 / 进度)。本 CLI 不经队列、不建 job, 因此:
- 中途失败即整次作废(没有断点续跑);
- `GET /runs/:id/progress` 对它返回 `job: null`, 前端显示"无队列任务" —— **这是有意行为**, 不是 bug。
适用场景: 快速验证指标口径改动、试不同切分/top_k、用已存结果复算 k 敏感性。

用法:
    # 只跑不落库(输出 JSON 报告)
    python -m app.cli.run_retrieval_eval --dataset-id 3 --corpus-id 4 --top-k 5

    # 落库: 创建一次 run, 逐题写 case_results, 结束时写 run 指标
    python -m app.cli.run_retrieval_eval --dataset-id 3 --corpus-id 4 --top-k 5 --persist

退出码: recall@k 低于 --min-recall 或索引缺失时返回 1。
"""
from __future__ import annotations

import argparse
import json
import sys
from typing import Any

from app.config import Settings
from app.eval.snapshot import build_config_snapshot, git_sha, snapshot_hash
from app.logging_conf import setup_logging
from app.metrics.retrieval import CaseMetric, aggregate, evaluate_case
from app.retrieval.anchor import AnchorSpec, GoldCase, map_dataset_cases
from app.retrieval.chunker import STRATEGIES, ChunkingConfig, chunk_document, chunking_hash
from app.retrieval.collections import collection_exists, collection_name
from app.retrieval.retriever import Retriever
from app.store import (
    CaseResultRow,
    create_run,
    get_dataset_project,
    list_cases,
    list_documents,
    save_case_results,
    set_run_status,
)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="检索评测(同步 runner, 可落库)")
    parser.add_argument("--dataset-id", type=int, required=True)
    parser.add_argument("--corpus-id", type=int, required=True)
    parser.add_argument("--top-k", type=int, default=5)
    parser.add_argument("--strategy", choices=STRATEGIES, default="headings")
    parser.add_argument("--chunk-size", type=int, default=500)
    parser.add_argument("--overlap", type=int, default=50)
    parser.add_argument("--min-chars", type=int, default=80)
    parser.add_argument("--worst", type=int, default=10, help="报告里展示的最差 N 题")
    parser.add_argument("--min-recall", type=float, default=0.0, help="recall@k 低于此值时退出码 1")
    parser.add_argument("--persist", action="store_true", help="把结果落库(runs + case_results)")
    parser.add_argument("--project-id", type=int, default=0, help="落库用项目 id(默认从数据集推断)")
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
    chunk_hash = chunking_hash(cfg)
    collection = collection_name(args.corpus_id, chunk_hash)

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
    mapping = map_dataset_cases(cases, doc_chunks, args.corpus_id, chunk_hash)
    gold_by_qid = {c.qid: c.gold_point_ids for c in mapping.cases}

    # ---- 可选: 创建 run(先落库再跑, 中途失败也能在库里留下 failed 记录) ----
    run_id: int | None = None
    config_hash = ""
    sha = git_sha()
    if args.persist:
        project_id = args.project_id or get_dataset_project(settings.pg_dsn, args.dataset_id)
        if not project_id:
            print(json.dumps({"error": f"数据集 {args.dataset_id} 不存在, 无法落库"}, ensure_ascii=False))
            return 1
        snapshot = build_config_snapshot(
            settings=settings,
            cfg=cfg,
            corpus_id=args.corpus_id,
            dataset_id=args.dataset_id,
            top_k=args.top_k,
        )
        config_hash = snapshot_hash(snapshot)
        run_id = create_run(
            settings.pg_dsn,
            project_id=project_id,
            dataset_id=args.dataset_id,
            corpus_id=args.corpus_id,
            config_snapshot=snapshot,
            config_hash=config_hash,
            git_sha=sha,
        )

    retriever = Retriever(settings, collection, top_k=args.top_k)
    case_metrics: list[CaseMetric] = []
    evaluated: list[dict[str, Any]] = []
    try:
        if not collection_exists(retriever.client, collection):
            message = f"索引不存在: {collection}, 请先跑 index_corpus"
            if run_id is not None:
                set_run_status(settings.pg_dsn, run_id, "failed", error=message, finished=True)
            print(json.dumps({"error": message}, ensure_ascii=False))
            return 1

        evaluable = [row for row in case_rows if gold_by_qid.get(row.qid)]
        responses = retriever.search_many([row.question for row in evaluable])

        for row, hits in zip(evaluable, responses, strict=True):
            gold = gold_by_qid[row.qid]
            metric = evaluate_case(row.qid, gold, [h.point_id for h in hits], args.top_k)
            if metric is None:
                continue
            case_metrics.append(metric)
            evaluated.append(
                {
                    "case_id": row.id,
                    "qid": row.qid,
                    "question": row.question,
                    "difficulty": row.difficulty,
                    "category": row.category,
                    "recall": metric.recall,
                    "first_hit_rank": metric.first_hit_rank,
                    "gold_docs": sorted({h.doc_id for h in hits if h.point_id in gold}),
                    "top_docs": [h.doc_id for h in hits],
                    "retrieved": [
                        {"point_id": h.point_id, "doc_id": h.doc_id, "score": round(h.score, 6)}
                        for h in hits
                    ],
                    "metrics": metric.to_json(),
                    "flags": [],  # M4 起写入 retrieval_miss / hallucination 等归因标签
                }
            )
    except Exception as exc:  # 落库场景下必须把失败写进 run, 不能只抛栈
        if run_id is not None:
            set_run_status(settings.pg_dsn, run_id, "failed", error=str(exc), finished=True)
        raise
    finally:
        retriever.close()

    skipped = len(case_rows) - len(case_metrics)
    metrics = aggregate(case_metrics, args.top_k, skipped=skipped)

    if run_id is not None:
        rows = [
            CaseResultRow(
                case_id=item["case_id"],
                retrieved=item["retrieved"],
                metrics=item["metrics"],
                flags=item["flags"],
                latency_ms=None,  # 逐题耗时要等 M3 的单题执行路径; 本轮只记录 run 级耗时
            )
            for item in evaluated
        ]
        saved = save_case_results(settings.pg_dsn, run_id, rows)
        set_run_status(settings.pg_dsn, run_id, "succeeded", metrics=metrics, finished=True)
        print(json.dumps({"persisted": saved, "run_id": run_id}, ensure_ascii=False), file=sys.stderr)

    details = [
        {key: value for key, value in item.items() if key not in ("retrieved", "metrics", "flags")}
        for item in evaluated
    ]
    worst = sorted(details, key=lambda d: (d["recall"], -float(d["first_hit_rank"] or 0)))[: args.worst]

    report = {
        "dataset_id": args.dataset_id,
        "corpus_id": args.corpus_id,
        "collection": collection,
        "chunking_hash": chunk_hash,
        "run_id": run_id,
        "config_hash": config_hash or None,
        "git_sha": sha,
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