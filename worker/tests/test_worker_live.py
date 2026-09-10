"""live 测试: worker 队列消费者的端到端行为(RUN_LIVE=1)。

覆盖:
1. 端到端: 通过 API 提交任务 → worker 拉取并跑完 → run/job/结果全部落库且指标正确;
2. 中断续跑: 只跑 5 条后暂停(任务放回队列) → 再跑一次把剩余 25 条补完,
   并通过"累计向量化文本数 = 30"证明已完成的 5 条**没有被重算**。
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any

import httpx
import psycopg
import pytest

from app.config import Settings
from app.eval.queue_runner import QueueRunner, RunnerOptions
from app.queue import job_progress
from app.retrieval.embedder import Embedder
from app.retrieval.retriever import Retriever
from app.store import delete_run, get_run, list_case_results

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实服务"
)

API_BASE = os.getenv("LIVE_API_BASE", "http://localhost:8080")
CORPUS_ID = int(os.getenv("LIVE_CORPUS_ID", "4"))
DATASET_ID = int(os.getenv("LIVE_DATASET_ID", "3"))
EXPECTED_CASES = 30
EXPECTED_RECALL = 0.913889


@dataclass
class CountingEmbedder:
    """包一层 Embedder, 统计被向量化的文本数(用于证明"不重算")."""

    inner: Embedder
    texts: int = 0

    def embed_texts(self, texts: list[str]) -> Any:
        self.texts += len(texts)
        return self.inner.embed_texts(texts)

    def close(self) -> None:
        self.inner.close()


def api_available() -> bool:
    try:
        response = httpx.get(f"{API_BASE}/healthz", timeout=2.0, trust_env=False)
    except httpx.HTTPError:
        return False
    return response.status_code == 200


def pending_jobs(dsn: str) -> int:
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("SELECT count(*) FROM jobs WHERE status = 'pending'")
        return int(cur.fetchone()[0])


def submit_run(settings: Settings) -> tuple[int, int]:
    """通过 API 提交一次评测任务, 返回 (run_id, job_id)。"""
    if not api_available():
        pytest.skip(f"API 未启动: {API_BASE}, 请先 make api")
    response = httpx.post(
        f"{API_BASE}/api/v1/runs",
        json={"dataset_id": DATASET_ID, "corpus_id": CORPUS_ID, "top_k": 5},
        timeout=10.0,
        trust_env=False,
    )
    response.raise_for_status()
    payload = response.json()
    run_id = int(payload["run_id"])
    with psycopg.connect(settings.pg_dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("SELECT id FROM jobs WHERE run_id = %s ORDER BY id LIMIT 1", (run_id,))
        row = cur.fetchone()
    assert row is not None, "任务未创建"
    return run_id, int(row[0])


def counting_factory(counter: CountingEmbedder):
    def factory(settings: Settings, collection: str, top_k: int) -> Retriever:
        return Retriever(settings, collection, embedder=counter, top_k=top_k)

    return factory


def test_worker_processes_submitted_job_end_to_end():
    settings = Settings()
    run_id, job_id = submit_run(settings)
    try:
        runner = QueueRunner(settings, RunnerOptions(batch_size=10))
        summary = runner.run_once(job_id)

        assert summary is not None, "应有待执行任务"
        assert summary.run_id == run_id, f"领到了别的任务(run {summary.run_id}), 队列可能不干净"
        assert summary.status == "succeeded"
        assert summary.succeeded == EXPECTED_CASES

        run = get_run(settings.pg_dsn, run_id)
        assert run is not None
        assert run["status"] == "succeeded" and run["finished_at"] is not None
        assert abs(float(run["metrics"]["recall_at_k"]) - EXPECTED_RECALL) < 1e-6
        assert int(run["metrics"]["cases_evaluated"]) == EXPECTED_CASES

        results = list_case_results(settings.pg_dsn, run_id)
        assert len(results) == EXPECTED_CASES
        assert all(r["retrieved"] for r in results), "每条结果都应记录检索到的 chunk"

        progress = job_progress(settings.pg_dsn, job_id)
        assert progress["succeeded"] == EXPECTED_CASES
        assert progress["pending"] == 0 and progress["running"] == 0 and progress["failed"] == 0
    finally:
        delete_run(settings.pg_dsn, run_id)


def test_worker_resume_after_partial_run_without_recompute():
    settings = Settings()
    run_id, job_id = submit_run(settings)
    counter = CountingEmbedder(Embedder(settings))
    try:
        first = QueueRunner(
            settings,
            RunnerOptions(max_items=5, batch_size=10),
            retriever_factory=counting_factory(counter),
        ).run_once(job_id)
        assert first is not None and first.run_id == run_id
        assert first.status == "paused" and first.processed == 5 and first.succeeded == 5

        progress = job_progress(settings.pg_dsn, job_id)
        assert progress["succeeded"] == 5
        assert progress["pending"] == EXPECTED_CASES - 5

        # 任务已放回队列, 可以被再次领取
        assert pending_jobs(settings.pg_dsn) >= 1
        run = get_run(settings.pg_dsn, run_id)
        assert run is not None and run["status"] == "running"  # 未写终态

        second = QueueRunner(
            settings, RunnerOptions(batch_size=10), retriever_factory=counting_factory(counter)
        ).run_once(job_id)
        assert second is not None and second.run_id == run_id
        assert second.status == "succeeded" and second.processed == EXPECTED_CASES - 5

        # 关键断言: 两次共向量化 30 条问题 —— 已完成的 5 条没有重算
        assert counter.texts == EXPECTED_CASES, f"发生重复计算: 共向量化 {counter.texts} 条"

        results = list_case_results(settings.pg_dsn, run_id)
        assert len(results) == EXPECTED_CASES
        finished = get_run(settings.pg_dsn, run_id)
        assert finished is not None
        assert finished["status"] == "succeeded"
        assert abs(float(finished["metrics"]["recall_at_k"]) - EXPECTED_RECALL) < 1e-6

        progress = job_progress(settings.pg_dsn, job_id)
        assert progress["succeeded"] == EXPECTED_CASES and progress["failed"] == 0
    finally:
        counter.close()
        delete_run(settings.pg_dsn, run_id)