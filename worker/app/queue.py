"""PG 任务队列的 worker 侧操作(与 Go 侧 store/queue.go 语义一致)。

队列 = jobs + job_items 两张普通表:
- 领取用 FOR UPDATE SKIP LOCKED -> 多 worker 并发安全, 不会领到同一条;
- checkpoint = 每条 case 一个事务(写 case_results + 推进 job_items + 刷新进度一起提交),
  所以 worker 崩在任意时刻, 已完成的 case 不会重算;
- 心跳用于识别僵尸任务(M3-3 的接管逻辑依据): 心跳超时的 running 任务会被接管回 pending。
"""
from __future__ import annotations

import json
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any

import psycopg

PROGRESS_SQL = """
               UPDATE jobs
               SET progress = (
                   SELECT jsonb_build_object(
                                  'pending',   count(*) FILTER (WHERE status = 'pending'),
                                  'running',   count(*) FILTER (WHERE status = 'running'),
                                  'succeeded', count(*) FILTER (WHERE status = 'succeeded'),
                                  'failed',    count(*) FILTER (WHERE status = 'failed'),
                                  'total',     count(*))
                   FROM job_items WHERE job_id = %s),
                   heartbeat_at = now(),
                   updated_at   = now()
               WHERE id = %s \
               """


@dataclass(frozen=True)
class ClaimedJob:
    id: int
    run_id: int
    status: str
    progress: dict[str, Any]


@dataclass(frozen=True)
class ClaimedItem:
    id: int
    job_id: int
    case_id: int
    retry_count: int


@dataclass(frozen=True)
class ReclaimResult:
    """一次"僵尸任务接管"的结果。"""

    jobs: int
    items: int


@dataclass(frozen=True)
class RunContext:
    run_id: int
    dataset_id: int
    corpus_id: int
    config_snapshot: dict[str, Any]


def claim_job(dsn: str, job_id: int | None = None) -> ClaimedJob | None:
    """领取一个待执行任务; 没有可领任务时返回 None。

    job_id 非空时只领该任务(用于重跑指定任务/调试), 仍然走 SKIP LOCKED 的并发安全路径。
    """
    condition = "AND id = %s" if job_id else ""
    params: tuple[Any, ...] = (job_id,) if job_id else ()
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            f"""
            SELECT id FROM jobs
            WHERE status = 'pending'
            {condition}
            ORDER BY id
            FOR UPDATE SKIP LOCKED
            LIMIT 1
            """,
            params,
        )
        row = cur.fetchone()
        if row is None:
            return None
        cur.execute(
            """
            UPDATE jobs
            SET status = 'running', heartbeat_at = now(), updated_at = now()
            WHERE id = %s
                RETURNING id, run_id, status, progress::text
            """,
            (row[0],),
        )
        job_id, run_id, status, progress = cur.fetchone()
        # 任务被领取 = 实验开始执行: run 同步置为 running 并记录开始时间
        cur.execute(
            """
            UPDATE runs
            SET status = 'running',
                started_at = COALESCE(started_at, now()),
                updated_at = now()
            WHERE id = %s AND status IN ('pending', 'running')
            """,
            (run_id,),
        )
    return ClaimedJob(id=job_id, run_id=run_id, status=status, progress=_loads(progress, {}))


def claim_job_items(dsn: str, job_id: int, limit: int = 8) -> list[ClaimedItem]:
    """领取该任务下最多 limit 条待执行微任务, 置为 running。"""
    if limit <= 0:
        limit = 8
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            UPDATE job_items
            SET status = 'running', updated_at = now()
            WHERE id IN (
                SELECT id FROM job_items
                WHERE job_id = %s AND status = 'pending'
                ORDER BY id
                FOR UPDATE SKIP LOCKED
                        LIMIT %s
                        )
                        RETURNING id, job_id, case_id, retry_count
            """,
            (job_id, limit),
        )
        rows = cur.fetchall()
    return [ClaimedItem(id=r[0], job_id=r[1], case_id=r[2], retry_count=r[3]) for r in rows]


def heartbeat(dsn: str, job_id: int) -> None:
    """刷新任务心跳(长任务期间定期调用)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("UPDATE jobs SET heartbeat_at = now(), updated_at = now() WHERE id = %s", (job_id,))


def complete_item(
        dsn: str,
        *,
        job_id: int,
        item_id: int,
        run_id: int,
        case_id: int,
        retrieved: list[dict[str, Any]],
        metrics: dict[str, Any],
        flags: list[str],
        latency_ms: int | None = None,
        answer: str | None = None,
        generation: dict[str, Any] | None = None,
        judge: dict[str, Any] | None = None,
) -> None:
    """单条 case 完成(原子 checkpoint): 结果 + 状态 + 进度一起提交。

    M4 起 answer/generation(M4-1) 与 judge(M4-2) 也走这**同一个事务** ——
    不能"另起一个事务再补一笔", 否则崩溃时会留下"有答案没指标"或"有判定没答案"的
    半成品状态, 报告与归因都会失真。
    """
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            INSERT INTO case_results
            (run_id, case_id, retrieved, metrics, flags, latency_ms, answer, generation, judge)
            VALUES (%s, %s, %s::jsonb, %s::jsonb, %s::jsonb, %s, %s, %s::jsonb, %s::jsonb)
                ON CONFLICT (run_id, case_id) DO UPDATE
                                                     SET retrieved  = EXCLUDED.retrieved,
                                                     metrics    = EXCLUDED.metrics,
                                                     flags      = EXCLUDED.flags,
                                                     latency_ms = EXCLUDED.latency_ms,
                                                     answer     = EXCLUDED.answer,
                                                     generation = EXCLUDED.generation,
                                                     judge      = EXCLUDED.judge,
                                                     updated_at = now()
            """,
            (
                run_id,
                case_id,
                _dumps(retrieved),
                _dumps(metrics),
                _dumps(flags),
                latency_ms,
                answer,
                _dumps(generation or {}),
                _dumps(judge or {}),
            ),
        )
        cur.execute(
            """
            UPDATE job_items
            SET status = 'succeeded', last_error = NULL, updated_at = now()
            WHERE id = %s
            """,
            (item_id,),
        )
        cur.execute(PROGRESS_SQL, (job_id, job_id))


def fail_item(dsn: str, *, job_id: int, item_id: int, error: str, max_retries: int = 3) -> bool:
    """标记微任务失败; 未超重试上限则回到 pending(返回 True), 否则置 failed(死信, 返回 False)。"""
    if max_retries <= 0:
        max_retries = 3
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            UPDATE job_items
            SET retry_count = retry_count + 1,
                last_error  = %s,
                status      = CASE WHEN retry_count + 1 >= %s THEN 'failed' ELSE 'pending' END,
                updated_at  = now()
            WHERE id = %s
                RETURNING status
            """,
            (error[:500], max_retries, item_id),
        )
        row = cur.fetchone()
        cur.execute(PROGRESS_SQL, (job_id, job_id))
    return bool(row and row[0] == "pending")


def finish_job(dsn: str, job_id: int, status: str, error: str = "") -> None:
    """结束任务: 同时把所属 run 置为同样的终态。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            "UPDATE jobs SET status = %s, error = NULLIF(%s, ''), updated_at = now() WHERE id = %s RETURNING run_id",
            (status, error, job_id),
        )
        row = cur.fetchone()
        if row is None:
            return
        cur.execute(
            """
            UPDATE runs
            SET status = %s, error = NULLIF(%s, ''), finished_at = now(), updated_at = now()
            WHERE id = %s
            """,
            (status, error, row[0]),
        )


def release_items(dsn: str, item_ids: list[int]) -> int:
    """把已领但未执行的微任务放回 pending(worker 优雅退出时使用)。"""
    if not item_ids:
        return 0
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            "UPDATE job_items SET status = 'pending', updated_at = now() WHERE id = ANY(%s) AND status = 'running'",
            (item_ids,),
        )
        return cur.rowcount


def release_running_items(dsn: str, job_id: int) -> int:
    """把该任务下仍处于 running 的微任务**全部**放回 pending, 并刷新进度。

    为什么需要它: 任务被"致命错误"中止时, 本批已领取的条目还没写结果。
    若直接把它们留在 running:
    - 进度条会永远显示"运行中 N 条"(进度一致性问题);
    - 接管逻辑只扫描 `jobs.status='running'` 的任务, 而这个 job 已是 failed, **永远不会被接管**,
      这些条目就成了孤儿。
    因此中止路径必须先把 running 条目归位, 再结束任务。
    """
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            "UPDATE job_items SET status = 'pending', updated_at = now() WHERE job_id = %s AND status = 'running'",
            (job_id,),
        )
        released = cur.rowcount
        cur.execute(PROGRESS_SQL, (job_id, job_id))
    return released


def release_job(dsn: str, job_id: int) -> bool:
    """把任务放回 pending(仅当没有 running 的微任务时成功), 供 worker 主动退出/暂停使用。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            UPDATE jobs SET status = 'pending', updated_at = now()
            WHERE id = %s
              AND status = 'running'
              AND NOT EXISTS (SELECT 1 FROM job_items WHERE job_id = %s AND status = 'running')
                RETURNING id
            """,
            (job_id, job_id),
        )
        return cur.fetchone() is not None


def job_progress(dsn: str, job_id: int) -> dict[str, int]:
    """任务进度统计。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT count(*) FILTER (WHERE status = 'pending'),
                count(*) FILTER (WHERE status = 'running'),
                count(*) FILTER (WHERE status = 'succeeded'),
                count(*) FILTER (WHERE status = 'failed')
            FROM job_items WHERE job_id = %s
            """,
            (job_id,),
        )
        pending, running, succeeded, failed = cur.fetchone()
    return {
        "pending": pending,
        "running": running,
        "succeeded": succeeded,
        "failed": failed,
        "total": pending + running + succeeded + failed,
    }


def reclaim_stale_jobs(dsn: str, timeout_seconds: float = 60.0) -> ReclaimResult:
    """接管心跳超时的任务(worker 被强杀的场景)。

    把 running 且心跳早于 timeout_seconds 的任务: running 微任务放回 pending, 任务回到 pending,
    从而被任意 worker 重新领取并从断点继续。仍用 SKIP LOCKED, 活跃任务不会被误接管。
    """
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT id FROM jobs
            WHERE status = 'running'
              AND (heartbeat_at IS NULL
                OR heartbeat_at < now() - make_interval(secs => %s::double precision))
            ORDER BY id
                FOR UPDATE SKIP LOCKED
            """,
            (timeout_seconds,),
        )
        job_ids = [row[0] for row in cur.fetchall()]
        if not job_ids:
            return ReclaimResult(jobs=0, items=0)

        cur.execute(
            """
            UPDATE job_items
            SET status = 'pending', updated_at = now()
            WHERE job_id = ANY(%s) AND status = 'running'
            """,
            (job_ids,),
        )
        released = cur.rowcount
        cur.execute(
            """
            UPDATE jobs
            SET status = 'pending',
                error = '心跳超时被接管(worker 可能被强杀)',
                updated_at = now()
            WHERE id = ANY(%s)
            """,
            (job_ids,),
        )
        for job_id in job_ids:
            cur.execute(PROGRESS_SQL, (job_id, job_id))
    return ReclaimResult(jobs=len(job_ids), items=released)


def get_run_context(dsn: str, run_id: int) -> RunContext:
    """读取 run 的执行上下文(数据集/语料/配置快照)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT dataset_id, COALESCE(corpus_id, 0), config_snapshot::text FROM runs WHERE id = %s",
            (run_id,),
        )
        row = cur.fetchone()
    if row is None:
        raise LookupError(f"run {run_id} 不存在")
    return RunContext(
        run_id=run_id,
        dataset_id=row[0],
        corpus_id=row[1],
        config_snapshot=_loads(row[2], {}),
    )


def update_run_metrics(dsn: str, run_id: int, metrics: dict[str, Any]) -> None:
    """把 run 级指标写回 runs.metrics。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            "UPDATE runs SET metrics = %s::jsonb, updated_at = now() WHERE id = %s",
            (_dumps(metrics), run_id),
        )


def list_case_metric_rows(dsn: str, run_id: int) -> list[dict[str, Any]]:
    """读取该 run 全部单题指标(用于聚合 run 级指标; 续跑后也能算全量)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT metrics::text FROM case_results WHERE run_id = %s ORDER BY case_id",
            (run_id,),
        )
        rows = cur.fetchall()
    return [_loads(r[0], {}) for r in rows]


def list_case_signals(dsn: str, run_id: int) -> list[dict[str, Any]]:
    """读取归因重算所需的单题信号: 指标 + 判定 + 现有标签。

    与 list_case_metric_rows 分开: 那个只服务 run 级指标聚合, 这里要连 judge 一起取 ——
    判定结论(claims/rubric)是幻觉类与质量类标签的唯一来源。
    """
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT cr.case_id, COALESCE(c.qid, ''), cr.metrics::text, cr.flags::text,
                COALESCE(cr.judge::text, '{}')
            FROM case_results cr
                     LEFT JOIN cases c ON c.id = cr.case_id
            WHERE cr.run_id = %s
            ORDER BY cr.case_id
            """,
            (run_id,),
        )
        rows = cur.fetchall()
    return [
        {
            "case_id": row[0],
            "qid": row[1],
            "metrics": _loads(row[2], {}),
            "flags": _loads(row[3], []),
            "judge": _loads(row[4], {}),
        }
        for row in rows
    ]


def update_case_flags(dsn: str, run_id: int, flags_by_case_id: Mapping[int, Sequence[str]]) -> int:
    """重算单题归因标签(M4-3 重算 CLI 用), 返回实际更新的行数。

    只改 flags 一个字段: 检索指标、答案、判定都是"既有事实", 重算标签只是重新解释它们。
    所以这里**绝不能**碰 config_snapshot / config_hash —— 那会让历史 run 的复现指纹失真。
    """
    if not flags_by_case_id:
        return 0
    payload = _dumps([
        {"case_id": int(case_id), "flags": [str(flag) for flag in flags]}
        for case_id, flags in flags_by_case_id.items()
    ])
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        # 一条语句完成全部更新(单次往返), rowcount 即真实命中行数
        cur.execute(
            """
            UPDATE case_results AS cr
            SET flags = v.flags, updated_at = now()
                FROM jsonb_to_recordset(%s::jsonb) AS v(case_id bigint, flags jsonb)
            WHERE cr.run_id = %s AND cr.case_id = v.case_id
            """,
            (payload, run_id),
        )
        return cur.rowcount


def update_run_attribution(dsn: str, run_id: int, meta: Mapping[str, Any]) -> None:
    """把归因元信息(规则版本/覆盖范围/阈值)浅合并进 runs.metrics.attribution(D15)。

    用 `||` 合并而不是整块替换 metrics: 重算标签不该动 token 用量/检索指标等其它键,
    否则"重算标签"会顺手改掉成本记录。
    """
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            UPDATE runs
            SET metrics = COALESCE(metrics, '{}'::jsonb)
                || jsonb_build_object('attribution', %s::jsonb),
                updated_at = now()
            WHERE id = %s
            """,
            (_dumps(dict(meta)), run_id),
        )


def _dumps(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False)


def _loads(raw: str | None, default: Any) -> Any:
    if not raw:
        return default
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return default