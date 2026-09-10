"""CLI: 校验评测集 gold 锚点在当前切分配置下的映射覆盖率。

用法:
    python -m app.cli.map_anchors --dataset-id 3 --corpus-id 4
    python -m app.cli.map_anchors --dataset-id 3 --corpus-id 4 --chunk-size 300 --min-coverage 0.95

退出码: 覆盖率低于 --min-coverage 时返回 1(可用于 CI/回归门禁)。
"""
from __future__ import annotations

import argparse
import json
import sys

from app.config import Settings
from app.logging_conf import setup_logging
from app.retrieval.anchor import AnchorSpec, GoldCase, map_dataset_cases
from app.retrieval.chunker import STRATEGIES, ChunkingConfig, chunk_document, chunking_hash
from app.store import list_cases, list_documents


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="gold 锚点映射覆盖率检查")
    parser.add_argument("--dataset-id", type=int, required=True, help="评测集 id")
    parser.add_argument("--corpus-id", type=int, required=True, help="语料库 id")
    parser.add_argument("--strategy", choices=STRATEGIES, default="headings", help="分块策略")
    parser.add_argument("--chunk-size", type=int, default=500, help="块字符数上限")
    parser.add_argument("--overlap", type=int, default=50, help="定长滑窗重叠字符数")
    parser.add_argument("--min-chars", type=int, default=80, help="过短块合并阈值")
    parser.add_argument("--min-coverage", type=float, default=0.0, help="覆盖率低于此值时退出码为 1")
    parser.add_argument("--show-all-failures", action="store_true", help="打印全部失败明细")
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

    docs = list_documents(settings.pg_dsn, args.corpus_id)
    if not docs:
        print(json.dumps({"error": f"语料库 {args.corpus_id} 没有文档"}, ensure_ascii=False))
        return 1

    doc_chunks = {row.doc_id: chunk_document(row.raw_text, cfg) for row in docs}

    case_rows = list_cases(settings.pg_dsn, args.dataset_id)
    if not case_rows:
        print(json.dumps({"error": f"数据集 {args.dataset_id} 没有用例"}, ensure_ascii=False))
        return 1

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
    report = mapping.to_json(dataset_id=args.dataset_id, corpus_id=args.corpus_id)
    report["chunking"] = {
        "strategy": cfg.strategy,
        "chunk_size": cfg.chunk_size,
        "overlap": cfg.overlap,
        "min_chars": cfg.min_chars,
    }
    report["docs"] = len(docs)
    report["chunks"] = sum(len(v) for v in doc_chunks.values())
    if not args.show_all_failures:
        report["failures"] = report["failures"][:10]  # type: ignore[index]

    print(json.dumps(report, ensure_ascii=False, indent=2))

    coverage = float(report["coverage"])  # type: ignore[arg-type]
    if coverage < args.min_coverage:
        print(
            f"覆盖率 {coverage:.2%} 低于阈值 {args.min_coverage:.2%}",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())