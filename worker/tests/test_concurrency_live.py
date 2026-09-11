"""live 测试: 并发粒度(D12) —— 一个 job 只能被一个 worker 持有, 多 worker 靠多 job 并行。

为什么必须把它固定成测试:
- "多 worker 同时消费一个 job" 是最容易被面试官追问的点。当前实现的答案是**不允许**:
  `jobs.status` 单值守护 + `FOR UPDATE SKIP LOCKED`, 领取即置 running, 之后任何路径都领不到;
- 一旦有人把 `WHERE status='pending'` 的条件改掉(例如为了"提高并发"去掉它), 同一条用例会被
  两个 worker 各算一遍 —— `job_items` 行数翻倍、指标被重复累加, 而且**不会报错**;
- 因此这里断言的是"抢占失败"这一**安全行为**, 而不是"抢到了"。

item 级共享(job_items 租约)是 D12 的 backlog: 触发条件 = 单 job 题量 > 500 或单 job 时长 > 10 min。
"""

from __future__ import annotations

import os
import threading
from concurrent.futures import ThreadPoolExecutor

import psycopg
import pytest

from app.config import Settings
from app.queue import ClaimedJob, claim_job, claim_job_items
from app.store import delete_run

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实服务"
)

DATASET_ID = int(os.getenv("LIVE_DATASET_ID", "3"))
CORPUS_ID = int(os.getenv("LIVE_CORPUS_ID", "4"))
EXPECTED_CASES = 30


def submit_run_via_db(settings: Settings, config_hash: str = "concurrency-test") -> tuple[int, int]:
    """直接构造 run + job + job_items(不依赖 API, 便于独立验证领取语义)。"""
    with psycopg.connect(settings.pg_dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("SELECT project_id FROM datasets WHERE id = %s", (DATASET_ID,))
        project_id = cur.fetchone()[0]
        cur.execute(
            """
            INSERT INTO runs (project_id, dataset_id, corpus_id, status, config_snapshot, config_hash, git_sha)
            VALUES (%s, %s, %s, 'pending', '{}'::jsonb, %s, 'test-sha')
            RETURNING id
            """,
            (project_id, DATASET_ID, CORPUS_ID, config_hash),
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


def job_status(dsn: str, job_id: int) -> str:
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("SELECT status FROM jobs WHERE id = %s", (job_id,))
        return cur.fetchone()[0]


def test_running_job_cannot_be_claimed_again():
    """已被持有的 job: 再次按 id 领取必须拿不到(None), 而不是被重复执行。"""
    settings = Settings()
    run_id, job_id = submit_run_via_db(settings)
    try:
        first = claim_job(settings.pg_dsn, job_id)
        assert isinstance(first, ClaimedJob)
        assert first.id == job_id
        assert first.status == "running"
        assert job_status(settings.pg_dsn, job_id) == "running"

        assert claim_job(settings.pg_dsn, job_id) is None, "运行中的任务被重复领取了"
    finally:
        delete_run(settings.pg_dsn, run_id)


def test_parallel_claim_of_same_job_has_exactly_one_winner():
    """两个 worker 同时抢同一个 pending job: 有且只有一个成功(SKIP LOCKED + 状态守护)。"""
    settings = Settings()
    run_id, job_id = submit_run_via_db(settings)
    try:
        # 用 barrier 让两个线程尽量同时发起领取(否则 ThreadPool 可能串行执行, 测不到竞态)
        barrier = threading.Barrier(2)

        def claim_in_race() -> ClaimedJob | None:
            barrier.wait(timeout=10)
            return claim_job(settings.pg_dsn, job_id)

        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: claim_in_race(), range(2)))

        winners = [r for r in results if r is not None]
        assert len(winners) == 1, f"应恰好一个 worker 领到任务, 实际 {len(winners)}: {winners}"
        assert winners[0].id == job_id
    finally:
        delete_run(settings.pg_dsn, run_id)


def test_two_workers_hold_two_jobs_in_parallel():
    """两个 job 可以同时被两个 worker 持有 —— 这就是允许的并行方式(job 级)。"""
    settings = Settings()
    run_a, job_a = submit_run_via_db(settings, config_hash="concurrency-a")
    run_b, job_b = submit_run_via_db(settings, config_hash="concurrency-b")
    try:
        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [
                pool.submit(claim_job, settings.pg_dsn, job_a),
                pool.submit(claim_job, settings.pg_dsn, job_b),
            ]
            claimed = [f.result() for f in futures]

        assert all(isinstance(job, ClaimedJob) for job in claimed), claimed
        assert {job.id for job in claimed} == {job_a, job_b}, "两个 worker 应各领一个不同的 job"
        assert job_status(settings.pg_dsn, job_a) == "running"
        assert job_status(settings.pg_dsn, job_b) == "running"

        # 两个 job 都在运行时, 第三方领取者什么也拿不到
        assert claim_job(settings.pg_dsn, job_a) is None
        assert claim_job(settings.pg_dsn, job_b) is None
    finally:
        delete_run(settings.pg_dsn, run_a)
        delete_run(settings.pg_dsn, run_b)


def test_claimed_items_never_overlap():
    """同一 job 的微任务批次之间不重叠(即使未来放开 item 级并发, 也不能重复执行)。"""
    settings = Settings()
    run_id, job_id = submit_run_via_db(settings)
    try:
        assert claim_job(settings.pg_dsn, job_id) is not None
        first = claim_job_items(settings.pg_dsn, job_id, 5)
        second = claim_job_items(settings.pg_dsn, job_id, 5)

        assert len(first) == 5 and len(second) == 5
        ids_first = {item.id for item in first}
        ids_second = {item.id for item in second}
        assert ids_first.isdisjoint(ids_second), "两次领取出现重复微任务"
        assert len(ids_first | ids_second) == 10
    finally:
        delete_run(settings.pg_dsn, run_id)
