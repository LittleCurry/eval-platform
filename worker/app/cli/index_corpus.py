"""CLI: 把某个语料库索引进 Qdrant。

用法示例:
    python -m app.cli.index_corpus --corpus-id 4
    python -m app.cli.index_corpus --corpus-id 4 --strategy fixed --chunk-size 300 --overlap 30 --recreate
"""
from __future__ import annotations

import argparse
import json
import sys

from app.config import Settings
from app.logging_conf import setup_logging
from app.retrieval.chunker import STRATEGIES, ChunkingConfig
from app.retrieval.indexer import index_corpus


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="语料索引入库(Qdrant)")
    parser.add_argument("--corpus-id", type=int, required=True, help="语料库 id")
    parser.add_argument("--strategy", choices=STRATEGIES, default="headings", help="分块策略")
    parser.add_argument("--chunk-size", type=int, default=500, help="块字符数上限")
    parser.add_argument("--overlap", type=int, default=50, help="定长滑窗重叠字符数")
    parser.add_argument("--min-chars", type=int, default=80, help="过短块合并阈值")
    parser.add_argument("--recreate", action="store_true", help="先删除同名 collection 再建")
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
    report = index_corpus(settings, args.corpus_id, cfg, recreate=args.recreate)
    print(json.dumps(report.to_json(), ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())