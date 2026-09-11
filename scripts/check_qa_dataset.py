"""离线校验评测集 jsonl: 按生产同口径(DB documents.raw_text)切分 → 锚点映射 → 覆盖率/粒度报告。

为什么要连 DB 取原文:
- map_anchors.py / run_retrieval_eval.py 用的都是 documents.raw_text(不含 YAML front matter);
- gold chunk 由 "当前切分配置 + 该原文" 决定, 用文件原文(含 front matter)复核会多切出 21 个块,
  point_id 与 Qdrant 中的真实 id 对不上 —— 校验就失去意义。
"""
from __future__ import annotations

import json
import subprocess
import sys
from collections import Counter
from pathlib import Path

from app.retrieval.anchor import AnchorSpec, GoldCase, map_anchor, map_dataset_cases
from app.retrieval.chunker import ChunkingConfig, chunk_document, chunking_hash

REPO = Path(__file__).resolve().parent.parent
QA = Path(sys.argv[1]) if len(sys.argv) > 1 else REPO / "datasets" / "qa" / "zhijian-qa-v2.jsonl"
CORPUS_ID = int(sys.argv[2]) if len(sys.argv) > 2 else 4


def load_db_docs() -> list[dict]:
    sql = (
        "SELECT json_agg(json_build_object('doc_id',doc_id,'raw_text',raw_text) ORDER BY doc_id) "
        f"FROM documents WHERE corpus_id={CORPUS_ID}"
    )
    out = subprocess.run(
        ["docker", "compose", "exec", "-T", "postgres", "psql", "-U", "eval", "-d", "eval_platform", "-tAc", sql],
        cwd=REPO, capture_output=True, text=True,
    )
    if out.returncode != 0:
        raise SystemExit(f"读取 DB 失败: {out.stderr.strip()}")
    return json.loads(out.stdout)


def load_cases() -> list[dict]:
    rows = []
    for lineno, line in enumerate(QA.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError as exc:
            raise SystemExit(f"第 {lineno} 行不是合法 JSON: {exc}") from exc
        for key in ("qid", "question", "gold_anchors", "reference_answer", "category", "difficulty"):
            if not row.get(key):
                raise SystemExit(f"第 {lineno} 行缺少字段 {key}")
        if not isinstance(row["gold_anchors"], list) or not row["gold_anchors"]:
            raise SystemExit(f"{row['qid']} 的 gold_anchors 为空")
        for anchor in row["gold_anchors"]:
            if not anchor.get("doc"):
                raise SystemExit(f"{row['qid']} 锚点缺少 doc")
        rows.append(row)
    return rows


def main() -> int:
    cfg = ChunkingConfig()
    docs = load_db_docs()
    doc_chunks = {d["doc_id"]: chunk_document(d["raw_text"], cfg) for d in docs}
    print(f"语料文档数(DB)    : {len(docs)}")
    print(f"chunking_hash     : {chunking_hash(cfg)[:8]}…  (基线 5f45e034)")
    print(f"chunk 总数        : {sum(len(c) for c in doc_chunks.values())}  (基线 80)")

    rows = load_cases()
    qids = [r["qid"] for r in rows]
    if len(set(qids)) != len(qids):
        raise SystemExit(f"qid 重复: {[q for q, n in Counter(qids).items() if n > 1]}")

    cases = [
        GoldCase(qid=r["qid"], anchors=[AnchorSpec(doc=a["doc"], span=a.get("span", "")) for a in r["gold_anchors"]])
        for r in rows
    ]
    mapping = map_dataset_cases(cases, doc_chunks, corpus_id=CORPUS_ID, cfg_hash=chunking_hash(cfg))
    per_case = {c.qid: len(c.gold_point_ids) for c in mapping.cases}
    print(f"题目总数          : {mapping.total}")
    print(f"成功映射          : {mapping.mapped}  覆盖率 {mapping.coverage:.4f}")
    print(f"平均 gold chunk   : {mapping.to_json()['avg_gold_chunks']}")
    print(f"gold chunk 数分布 : {dict(sorted(Counter(per_case.values()).items()))}")

    # 逐锚点诊断: 命中整篇文档 = span 退化成"整篇都是 gold", Recall 会被虚高
    anchor_rows: list[tuple[str, str, str, int, int]] = []
    for row in rows:
        for a in row["gold_anchors"]:
            chunks = doc_chunks[a["doc"]]
            matched = len(map_anchor(AnchorSpec(doc=a["doc"], span=a.get("span", "")), chunks).matched)
            anchor_rows.append((row["qid"], a["doc"], a["span"], matched, len(chunks)))
    degenerate = [r for r in anchor_rows if r[3] >= r[4]]
    print(f"锚点总数          : {len(anchor_rows)}  命中 chunk 数分布 {dict(sorted(Counter(r[3] for r in anchor_rows).items()))}")
    for qid, doc, span, matched, size in degenerate:
        print(f"⚠ 退化锚点 {qid} {doc}:{span}  {matched}/{size}")

    if mapping.failures:
        print("\n映射失败明细:")
        for item in mapping.failures:
            print(f"  {item['qid']}: {item['reasons']}")
        return 1
    print("\n✅ 锚点映射全部成功, 覆盖率 100%")
    return 0


if __name__ == "__main__":
    sys.exit(main())