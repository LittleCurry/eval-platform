"""worker 侧 PostgreSQL 读取(只读)。

约定: 业务 CRUD 全在 Go API; worker 只读取评测所需数据 + 写自己的结果表(M3 起)。
"""
from __future__ import annotations

from dataclasses import dataclass

import psycopg


@dataclass(frozen=True)
class DocumentRow:
    id: int
    doc_id: str
    title: str
    raw_text: str


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