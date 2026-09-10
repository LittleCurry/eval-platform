"""live 测试: 僵尸任务接管与崩溃恢复(RUN_LIVE=1)。

场景: worker 被 kill -9 后, 它领走的微任务会永久停在 running、任务停在 running。
接管逻辑(reclaim_stale_jobs)按心跳超时把它们放回 pending, 任务回到队列, 由此实现"崩溃续跑"。
"""

from __future__ import annotations

import os

import psycopg
import pytest

from app.config import Settings
from app.eval.queue_runner import QueueRunner, RunnerOptions
from app.queue import (
    ClaimedJob,
    claim_job,
    claim_job_items,
    complete_item,
    heartbeat,
    job_progress,
    reclaim_stale_jobs,
)
from app.store import delete_run, get_run, list_case_results

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实服务"
)

DATASET_ID = int(os.getenv("LIVE_DATASET_ID", "3"))
CORPUS_ID = int(os.getenv("LIVE_CORPUS_ID", "4"))
EXPECTED_CASES = 30


def submit_run_via_db(settings: Settings) -> tuple[int, int]:
    """直接构造 run + job + job_items(不依赖 API, 便于独立验证接管逻辑)。"""
    with psycopg.connect(settings.pg_dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("SELECT project_id FROM datasets WHERE id = %s", (DATASET_ID,))
        project_id = cur.fetchone()[0]
        cur.execute(
            """
            INSERT INTO runs (project_id, dataset_id, corpus_id, status, config_snapshot, config_hash, git_sha)
            VALUES (%s, %s, %s, 'pending', %s::jsonb, 'reclaim-test', 'test-sha')
                RETURNING id
            """,
            (
                project_id,
                DATASET_ID,
                CORPUS_ID,
                (
                    '{"chunking": {"strategy": "headings", "chunk_size": 500, "overlap": 50, "min_chars": 80},'
                    ' "retrieval": {"top_k": 5}}'
                ),
            ),
        )
        run_id = cur.fetchone()[0]
        cur.execute(
            "INSERT INTO jobs (run_id, status, progress) VALUES (%s, 'pending', '{}'::jsonb) RETURNING id",
            (run_id,),
        )
        job_id = cur.fetchone()[0]
        cur.execute(
            "INSERT INTO job_items (job_id, case_id, status) SELECT %s, id, 'pending' FROM cases WHERE dataset_id = %s",
            (job_id, DATASET_ID),
        )
    return run_id, job_id


def age_heartbeat(dsn: str, job_id: int, minutes: int = 10) -> None:
    """模拟 worker 被强杀: 把心跳时间推到过去(而不是真的等超时)。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            "UPDATE jobs SET heartbeat_at = now() - make_interval(mins => %s) WHERE id = %s",
            (minutes, job_id),
        )


def test_reclaim_threshold_respected():
    settings = Settings()
    run_id, job_id = submit_run_via_db(settings)
    try:
        job = claim_job(settings.pg_dsn, job_id)
        assert isinstance(job, ClaimedJob)
        items = claim_job_items(settings.pg_dsn, job_id, 3)
        assert len(items) == 3
        age_heartbeat(settings.pg_dsn, job_id, minutes=10)

        # 阈值大于心跳年龄 -> 视为活跃, 不接管
        result = reclaim_stale_jobs(settings.pg_dsn, timeout_seconds=3600)
        assert result.jobs == 0

        # 阈值放开 -> 被接管
        result = reclaim_stale_jobs(settings.pg_dsn, timeout_seconds=60)
        assert result.jobs >= 1
        assert result.items >= 3

        progress = job_progress(settings.pg_dsn, job_id)
        assert progress["running"] == 0
        assert progress["pending"] == EXPECTED_CASES
    finally:
        delete_run(settings.pg_dsn, run_id)


def test_reclaim_keeps_active_job_and_completed_work():
    settings = Settings()
    run_id, job_id = submit_run_via_db(settings)
    try:
        assert claim_job(settings.pg_dsn, job_id) is not None
        items = claim_job_items(settings.pg_dsn, job_id, 4)
        # 完成 2 条, 另外 2 条保持 running(模拟"崩在中间")
        for item in items[:2]:
            complete_item(
                settings.pg_dsn,
                job_id=job_id,
                item_id=item.id,
                run_id=run_id,
                case_id=item.case_id,
                retrieved=[],
                metrics={"recall": 1.0},
                flags=[],
            )
        age_heartbeat(settings.pg_dsn, job_id)

        reclaim_stale_jobs(settings.pg_dsn, timeout_seconds=0)

        progress = job_progress(settings.pg_dsn, job_id)
        assert progress["succeeded"] == 2, "已完成的 case 不能因为接管被重置"
        assert progress["running"] == 0
        assert progress["pending"] == EXPECTED_CASES - 2
    finally:
        delete_run(settings.pg_dsn, run_id)


def test_worker_resumes_after_hard_kill():
    """完整闭环: worker 领走 5 条后被 kill -9 -> 接管 -> 续跑到底, 结果与正常跑完一致。"""
    settings = Settings()
    run_id, job_id = submit_run_via_db(settings)
    try:
        # 1) 模拟被强杀的 worker: 领走任务和 5 条微任务, 但不写任何结果也不归还
        assert claim_job(settings.pg_dsn, job_id) is not None
        claimed = claim_job_items(settings.pg_dsn, job_id, 5)
        assert len(claimed) == 5
        crashed = job_progress(settings.pg_dsn, job_id)
        assert crashed["running"] == 5 and crashed["succeeded"] == 0

        # 2) 心跳停住 -> 接管: 5 条 running 回到 pending, 任务回到队列
        age_heartbeat(settings.pg_dsn, job_id)
        result = reclaim_stale_jobs(settings.pg_dsn, timeout_seconds=60)
        assert result.jobs >= 1
        assert result.items >= 5
        reclaimed = job_progress(settings.pg_dsn, job_id)
        assert reclaimed["running"] == 0
        assert reclaimed["pending"] == EXPECTED_CASES

        # 3) 新 worker 领取并跑完 -> 指标与"从未崩溃"完全一致
        second = QueueRunner(settings, RunnerOptions(batch_size=10)).run_once(job_id)
        assert second is not None and second.status == "succeeded"
        assert second.succeeded == EXPECTED_CASES

        run = get_run(settings.pg_dsn, run_id)
        assert run is not None and run["status"] == "succeeded"
        assert abs(float(run["metrics"]["recall_at_k"]) - 0.913889) < 1e-6
        assert len(list_case_results(settings.pg_dsn, run_id)) == EXPECTED_CASES
    finally:
        delete_run(settings.pg_dsn, run_id)


def test_heartbeat_prevents_reclaim():
    settings = Settings()
    run_id, job_id = submit_run_via_db(settings)
    try:
        assert claim_job(settings.pg_dsn, job_id) is not None
        heartbeat(settings.pg_dsn, job_id)
        result = reclaim_stale_jobs(settings.pg_dsn, timeout_seconds=60)
        assert result.jobs == 0, "刚心跳过的任务不应被接管"
    finally:
        delete_run(settings.pg_dsn, run_id)