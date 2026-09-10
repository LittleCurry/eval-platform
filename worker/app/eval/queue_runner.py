"""队列消费者: 领任务 → 逐条执行评测 → checkpoint 落库 → 结束任务。

设计要点:
- 每条 case 由 queue.complete_item 原子提交(结果 + 微任务状态 + 进度); 中断只丢当前一条;
- 心跳按周期刷新, M3-3 起用于识别并接管僵尸任务;
- 支持 max_items 提前停止(测试/灰度): 未跑的微任务放回 pending, 任务也放回队列,
  下一次 run_once 从断点继续 —— 这就是"中断续跑"。
"""
from __future__ import annotations

import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

import structlog

from app.config import Settings
from app.metrics.retrieval import CaseMetric, aggregate, evaluate_case
from app.queue import (
    ClaimedItem,
    ClaimedJob,
    claim_job,
    claim_job_items,
    complete_item,
    fail_item,
    finish_job,
    get_run_context,
    heartbeat,
    job_progress,
    list_case_metric_rows,
    release_items,
    release_job,
    update_run_metrics,
)
from app.retrieval.anchor import AnchorSpec, GoldCase, map_dataset_cases
from app.retrieval.chunker import ChunkingConfig, chunk_document, chunking_hash
from app.retrieval.collections import collection_exists, collection_name
from app.retrieval.retriever import Retriever
from app.store import list_cases, list_documents

_CASE_METRIC_FIELDS = (
    "qid",
    "gold_count",
    "retrieved_count",
    "hits",
    "recall",
    "precision",
    "reciprocal_rank",
    "hit",
    "first_hit_rank",
)


@dataclass
class RunnerOptions:
    batch_size: int = 8
    max_retries: int = 3
    heartbeat_seconds: float = 10.0
    poll_interval: float = 1.0
    max_items: int | None = None


@dataclass
class JobSummary:
    job_id: int
    run_id: int
    status: str  # succeeded | failed | paused
    succeeded: int
    failed: int
    processed: int
    elapsed_ms: int

    def to_json(self) -> dict[str, Any]:
        return {
            "job_id": self.job_id,
            "run_id": self.run_id,
            "status": self.status,
            "succeeded": self.succeeded,
            "failed": self.failed,
            "processed": self.processed,
            "elapsed_ms": self.elapsed_ms,
        }


class QueueRunner:
    """评测任务的队列消费者。依赖均可注入, 便于离线单测。"""

    def __init__(
            self,
            settings: Settings,
            options: RunnerOptions | None = None,
            *,
            dsn: str | None = None,
            retriever_factory: Callable[..., Retriever] | None = None,
    ) -> None:
        self.settings = settings
        self.options = options or RunnerOptions()
        self.dsn = dsn or settings.pg_dsn
        self._retriever_factory = retriever_factory or (
            lambda s, collection, top_k: Retriever(s, collection, top_k=top_k)
        )
        self.log = structlog.get_logger("worker.runner")
        self._stop_requested = False

    def request_stop(self) -> None:
        """请求停止(信号处理或测试调用); 当前批次处理完后退出。"""
        self._stop_requested = True

    def run_once(self, job_id: int | None = None) -> JobSummary | None:
        """领取并执行一个任务; 没有可领任务时返回 None。指定 job_id 时只跑该任务。"""
        job = claim_job(self.dsn, job_id)
        if job is None:
            return None
        return self.execute_job(job)

    def run_forever(self) -> None:
        """常驻模式: 轮询领任务, 空闲时按 poll_interval 休眠。"""
        while not self._stop_requested:
            summary = self.run_once()
            if summary is None:
                time.sleep(self.options.poll_interval)

    # ---- 内部 ----

    def execute_job(self, job: ClaimedJob) -> JobSummary:
        started = time.perf_counter()
        ctx = get_run_context(self.dsn, job.run_id)
        snapshot = ctx.config_snapshot or {}
        chunking_cfg = ChunkingConfig(**snapshot.get("chunking", {}))
        top_k = int((snapshot.get("retrieval") or {}).get("top_k", 5) or 5)
        chunk_hash = chunking_hash(chunking_cfg)
        collection = collection_name(ctx.corpus_id, chunk_hash)

        doc_chunks = {
            row.doc_id: chunk_document(row.raw_text, chunking_cfg)
            for row in list_documents(self.dsn, ctx.corpus_id)
        }
        case_rows = list_cases(self.dsn, ctx.dataset_id)
        gold_cases = [
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
        mapping = map_dataset_cases(gold_cases, doc_chunks, ctx.corpus_id, chunk_hash)
        gold_by_qid = {c.qid: c.gold_point_ids for c in mapping.cases}
        qid_by_case_id = {row.id: row.qid for row in case_rows}
        question_by_case_id = {row.id: row.question for row in case_rows}

        retriever = self._retriever_factory(self.settings, collection, top_k)
        succeeded = 0
        dead = 0
        processed = 0
        last_heartbeat = time.monotonic()

        try:
            if not collection_exists(retriever.client, collection):
                message = f"索引不存在: {collection}, 请先构建索引(index_corpus)"
                finish_job(self.dsn, job.id, "failed", message)
                return JobSummary(job.id, job.run_id, "failed", 0, 0, 0, _ms(started))

            while True:
                if self._stopped_or_paused(processed):
                    break
                remaining = self._remaining_batch(processed)
                items = claim_job_items(self.dsn, job.id, remaining)
                if not items:
                    break
                if time.monotonic() - last_heartbeat >= self.options.heartbeat_seconds:
                    heartbeat(self.dsn, job.id)
                    last_heartbeat = time.monotonic()

                todo = items
                if self.options.max_items is not None:
                    allowance = self.options.max_items - processed
                    if allowance < len(items):
                        todo, leftover = items[:allowance], items[allowance:]
                        release_items(self.dsn, [item.id for item in leftover])
                success_count, dead_count = self._process_items(
                    job, todo, gold_by_qid, qid_by_case_id, question_by_case_id, retriever, top_k
                )
                succeeded += success_count
                dead += dead_count
                processed += len(todo)

            if self._stopped_or_paused(processed):
                release_job(self.dsn, job.id)
                progress = job_progress(self.dsn, job.id)
                self.log.info("job_paused", job_id=job.id, run_id=job.run_id, progress=progress)
                return JobSummary(
                    job.id, job.run_id, "paused", succeeded, dead, processed, _ms(started)
                )

            progress = job_progress(self.dsn, job.id)
            status = "failed" if progress["failed"] else "succeeded"
            error = f"{progress['failed']} 条用例失败(已达重试上限)" if progress["failed"] else ""
            finish_job(self.dsn, job.id, status, error)
            update_run_metrics(self.dsn, job.run_id, self._aggregate_run_metrics(job.run_id, top_k))
            self.log.info(
                "job_finished", job_id=job.id, run_id=job.run_id, status=status, progress=progress
            )
            return JobSummary(
                job.id, job.run_id, status, progress["succeeded"], progress["failed"], processed, _ms(started)
            )
        finally:
            retriever.close()

    def _process_items(
            self,
            job: ClaimedJob,
            items: list[ClaimedItem],
            gold_by_qid: dict[str, set[str]],
            qid_by_case_id: dict[int, str],
            question_by_case_id: dict[int, str],
            retriever: Retriever,
            top_k: int,
    ) -> tuple[int, int]:
        """执行一批微任务: 批量向量化 + 批量检索 + 逐条 checkpoint。"""
        succeeded = 0
        dead = 0
        questions: list[str] = []
        valid_items: list[ClaimedItem] = []
        for item in items:
            qid = qid_by_case_id.get(item.case_id)
            if qid is None:
                # 用例已被删除: 直接判失败(不重试)
                fail_item(self.dsn, job_id=job.id, item_id=item.id, error="用例不存在", max_retries=1)
                dead += 1
                continue
            questions.append(question_by_case_id.get(item.case_id, ""))
            valid_items.append(item)

        if not valid_items:
            return succeeded, dead

        try:
            responses = retriever.search_many(questions, top_k=top_k)
        except Exception as exc:  # noqa: BLE001 - 检索/embedding 失败原因多样, 统一转成可重试错误
            for item in valid_items:
                if fail_item(
                        self.dsn, job_id=job.id, item_id=item.id, error=f"检索失败: {exc}",
                        max_retries=self.options.max_retries,
                ):
                    pass
                else:
                    dead += 1
            return succeeded, dead

        for item, hits in zip(valid_items, responses, strict=True):
            qid = qid_by_case_id[item.case_id]
            gold = gold_by_qid.get(qid, set())
            retrieved = [
                {"point_id": h.point_id, "doc_id": h.doc_id, "score": round(h.score, 6)} for h in hits
            ]
            metric = evaluate_case(qid, gold, [h.point_id for h in hits], top_k)
            if metric is None:
                # gold 为空: 该题不可评测, 记为 skipped 而不是失败
                complete_item(
                    self.dsn, job_id=job.id, item_id=item.id, run_id=job.run_id, case_id=item.case_id,
                    retrieved=retrieved, metrics={}, flags=["no_gold"],
                )
                succeeded += 1
                continue
            try:
                complete_item(
                    self.dsn, job_id=job.id, item_id=item.id, run_id=job.run_id, case_id=item.case_id,
                    retrieved=retrieved, metrics=metric.to_json(), flags=[],
                )
            except Exception as exc:  # noqa: BLE001 - 落库失败需记录并决定是否重试
                if not fail_item(
                        self.dsn, job_id=job.id, item_id=item.id, error=f"落库失败: {exc}",
                        max_retries=self.options.max_retries,
                ):
                    dead += 1
                continue
            succeeded += 1
        return succeeded, dead

    def _aggregate_run_metrics(self, run_id: int, top_k: int) -> dict[str, Any]:
        """从 DB 读全部单题指标聚合(续跑场景下也能算全量, 不依赖内存态)。"""
        rows = list_case_metric_rows(self.dsn, run_id)
        metrics = [_to_case_metric(row) for row in rows if row]
        evaluated = [m for m in metrics if m is not None]
        skipped = len([row for row in rows if not row])
        return aggregate(evaluated, top_k, skipped=skipped)

    def _stopped_or_paused(self, processed: int) -> bool:
        if self._stop_requested:
            return True
        return self.options.max_items is not None and processed >= self.options.max_items

    def _remaining_batch(self, processed: int) -> int:
        if self.options.max_items is None:
            return self.options.batch_size
        return max(1, min(self.options.batch_size, self.options.max_items - processed))


def _to_case_metric(row: dict[str, Any]) -> CaseMetric | None:
    if not row:
        return None
    values = {field: row.get(field) for field in _CASE_METRIC_FIELDS}
    if values.get("qid") is None:
        return None
    return CaseMetric(
        qid=str(values["qid"]),
        gold_count=int(values["gold_count"] or 0),
        retrieved_count=int(values["retrieved_count"] or 0),
        hits=int(values["hits"] or 0),
        recall=float(values["recall"] or 0.0),
        precision=float(values["precision"] or 0.0),
        reciprocal_rank=float(values["reciprocal_rank"] or 0.0),
        hit=float(values["hit"] or 0.0),
        first_hit_rank=int(values["first_hit_rank"]) if values["first_hit_rank"] else None,
    )


def _ms(started: float) -> int:
    return int((time.perf_counter() - started) * 1000)