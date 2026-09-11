"""worker 侧 PostgreSQL 读写。

约定: 业务 CRUD 全在 Go API; worker 读取评测所需数据, 并写入自己的实验结果表
(runs / case_results; jobs / job_items 由 M3 的异步 worker 使用)。
"""
from __future__ import annotations

import json
from collections.abc import Iterable
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


@dataclass(frozen=True)
class CaseResultRow:
    case_id: int
    retrieved: list[dict[str, Any]]
    metrics: dict[str, Any]
    flags: list[str]
    latency_ms: int | None = None
    answer: str | None = None
    generation: dict[str, Any] | None = None


# ---- 读 ----


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


def get_dataset_project(dsn: str, dataset_id: int) -> int | None:
    """取数据集所属项目 id(创建 run 需要 project_id)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("SELECT project_id FROM datasets WHERE id = %s", (dataset_id,))
        row = cur.fetchone()
        return int(row[0]) if row else None


def get_run(dsn: str, run_id: int) -> dict[str, Any] | None:
    """读取一次 run(用于 CLI/测试校验落库结果)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT id, project_id, dataset_id, corpus_id, status,
                   config_snapshot::text, config_hash, git_sha, metrics::text,
                error, started_at, finished_at
            FROM runs WHERE id = %s
            """,
            (run_id,),
        )
        row = cur.fetchone()
    if row is None:
        return None
    return {
        "id": row[0],
        "project_id": row[1],
        "dataset_id": row[2],
        "corpus_id": row[3],
        "status": row[4],
        "config_snapshot": _loads(row[5], {}),
        "config_hash": row[6],
        "git_sha": row[7],
        "metrics": _loads(row[8], {}),
        "error": row[9],
        "started_at": row[10],
        "finished_at": row[11],
    }


def list_case_results(dsn: str, run_id: int) -> list[dict[str, Any]]:
    """按 case_id 升序读取某次 run 的单题结果。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT case_id, retrieved::text, metrics::text, flags::text, latency_ms
            FROM case_results WHERE run_id = %s ORDER BY case_id
            """,
            (run_id,),
        )
        rows = cur.fetchall()
    return [
        {
            "case_id": row[0],
            "retrieved": _loads(row[1], []),
            "metrics": _loads(row[2], {}),
            "flags": _loads(row[3], []),
            "latency_ms": row[4],
        }
        for row in rows
    ]


# ---- 写 ----


def create_run(
        dsn: str,
        *,
        project_id: int,
        dataset_id: int,
        corpus_id: int | None,
        config_snapshot: dict[str, Any],
        config_hash: str,
        git_sha: str,
) -> int:
    """创建一次 run, 初始状态 running(set started_at)。返回 run_id。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            INSERT INTO runs (project_id, dataset_id, corpus_id, status,
                              config_snapshot, config_hash, git_sha, started_at)
            VALUES (%s, %s, %s, 'running', %s::jsonb, %s, %s, now())
                RETURNING id
            """,
            (
                project_id,
                dataset_id,
                corpus_id,
                json.dumps(config_snapshot, ensure_ascii=False),
                config_hash,
                git_sha,
            ),
        )
        return int(cur.fetchone()[0])


def set_run_status(
        dsn: str,
        run_id: int,
        status: str,
        *,
        metrics: dict[str, Any] | None = None,
        error: str | None = None,
        finished: bool = False,
) -> None:
    """更新 run 状态; finished=True 时写 finished_at。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            UPDATE runs
            SET status      = %s,
                metrics     = COALESCE(%s::jsonb, metrics),
                error       = COALESCE(%s, error),
                finished_at = CASE WHEN %s THEN now() ELSE finished_at END,
                updated_at  = now()
            WHERE id = %s
            """,
            (
                status,
                json.dumps(metrics, ensure_ascii=False) if metrics is not None else None,
                error,
                finished,
                run_id,
            ),
        )


def save_case_results(dsn: str, run_id: int, rows: Iterable[CaseResultRow]) -> int:
    """写入/更新单题结果(UNIQUE(run_id, case_id) -> 重跑同一 run 是覆盖而非追加)。"""
    payload = list(rows)
    if not payload:
        return 0
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        for item in payload:
            cur.execute(
                """
                INSERT INTO case_results (run_id, case_id, retrieved, metrics, flags, latency_ms, answer, generation)
                VALUES (%s, %s, %s::jsonb, %s::jsonb, %s::jsonb, %s, %s, %s::jsonb)
                    ON CONFLICT (run_id, case_id) DO UPDATE
                                                         SET retrieved  = EXCLUDED.retrieved,
                                                         metrics    = EXCLUDED.metrics,
                                                         flags      = EXCLUDED.flags,
                                                         latency_ms = EXCLUDED.latency_ms,
                                                         answer     = EXCLUDED.answer,
                                                         generation = EXCLUDED.generation,
                                                         updated_at = now()
                """,
                (
                    run_id,
                    item.case_id,
                    json.dumps(item.retrieved, ensure_ascii=False),
                    json.dumps(item.metrics, ensure_ascii=False),
                    json.dumps(item.flags, ensure_ascii=False),
                    item.latency_ms,
                    item.answer,
                    json.dumps(item.generation or {}, ensure_ascii=False),
                ),
            )
    return len(payload)


def get_generation_usage(dsn: str, run_id: int) -> dict[str, int]:
    """汇总该 run 的生成侧用量(答案条数 + token 消耗), 用于写进 run.metrics(D8 成本)。

    续跑场景下也要能算全量, 所以依然从 DB 聚合而不是用内存计数。
    """
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT count(*) FILTER (WHERE answer IS NOT NULL AND answer <> '') AS answers,
                   COALESCE(sum(CASE WHEN generation ? 'prompt_tokens'
                                     THEN (generation ->> 'prompt_tokens')::int END), 0) AS prompt_tokens,
                   COALESCE(sum(CASE WHEN generation ? 'completion_tokens'
                                     THEN (generation ->> 'completion_tokens')::int END), 0) AS completion_tokens
            FROM case_results
            WHERE run_id = %s
            """,
            (run_id,),
        )
        row = cur.fetchone()
    answers, prompt_tokens, completion_tokens = row if row else (0, 0, 0)
    return {
        "answers_generated": int(answers or 0),
        "prompt_tokens": int(prompt_tokens or 0),
        "completion_tokens": int(completion_tokens or 0),
    }


def delete_run(dsn: str, run_id: int) -> None:
    """删除 run(级联删除其 case_results / jobs)。测试清理与人工重跑时使用。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("DELETE FROM runs WHERE id = %s", (run_id,))


def _loads(raw: str | None, default: Any) -> Any:
    if not raw:
        return default
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return default