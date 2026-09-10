"""worker 侧 PostgreSQL 读取(只读)。

约定: 业务 CRUD 全在 Go API; worker 只读取评测所需数据 + 写自己的结果表(M3 起)。
"""
from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

import psycopg


@dataclass(frozen=True)
class DocumentRow:
    id: int
    doc_id: str
    title: str
    raw_text: str


@dataclass(frozen=True)
class CaseRow:
    id: int
    qid: str
    question: str
    gold_anchors: list[dict[str, Any]]
    category: str
    difficulty: str


def list_documents(dsn: str, corpus_id: int) -> list[DocumentRow]:
    """按 doc_id 升序返回语料库下的文档(顺序稳定 -> 索引结果可复现)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT id, doc_id, title, raw_text
            FROM documents
            WHERE corpus_id = %s
            ORDER BY doc_id
            """,
            (corpus_id,),
        )
        return [
            DocumentRow(id=row[0], doc_id=row[1], title=row[2], raw_text=row[3])
            for row in cur.fetchall()
        ]


def list_cases(dsn: str, dataset_id: int) -> list[CaseRow]:
    """按 id 升序返回数据集下的用例(顺序稳定 -> 结果可复现)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT id, qid, question, gold_anchors::text,
                COALESCE(category, ''), COALESCE(difficulty, '')
            FROM cases
            WHERE dataset_id = %s
            ORDER BY id
            """,
            (dataset_id,),
        )
        rows = cur.fetchall()

    result: list[CaseRow] = []
    for row in rows:
        try:
            anchors = json.loads(row[3]) if row[3] else []
        except json.JSONDecodeError:
            anchors = []
        result.append(
            CaseRow(
                id=row[0],
                qid=row[1],
                question=row[2],
                gold_anchors=anchors if isinstance(anchors, list) else [],
                category=row[4],
                difficulty=row[5],
            )
        )
    return result