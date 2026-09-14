"""QueueRunner 离线单测: 用 fake 队列 + fake 检索器验证编排逻辑(不碰 DB/网络)。

覆盖: 正常跑完 / 单条失败与死信 / max_items 暂停并放回队列 / 索引缺失 / 空队列,
以及 M4 生成链路(写库带上 answer 与元信息 / 空答案绝不落库 / 致命错误立刻中止任务)。
"""
from __future__ import annotations

from typing import Any

import pytest

from app.config import Settings
from app.eval import queue_runner as qr
from app.eval.queue_runner import QueueRunner, RunnerOptions
from app.generation.prompts import load_prompt
from app.llm.client import ChatResult, LLMFatalError, LLMTransientError
from app.metrics.retrieval import CaseMetric, aggregate
from app.queue import ClaimedItem, ClaimedJob, RunContext
from app.store import CaseRow, DocumentRow

DOC = "# 线索回收\n\n超过 7 天无跟进会自动回收。\n\n## 上限\n\n每人默认 200 条。\n"


class FakeQueue:
    """记录所有队列副作用, 供断言。"""

    def __init__(self, items: list[ClaimedItem]) -> None:
        self.pending = list(items)
        self.completed: list[dict[str, Any]] = []
        self.failed: list[dict[str, Any]] = []
        self.released: list[list[int]] = []
        self.finished: list[tuple[str, str]] = []
        self.released_jobs: list[int] = []
        self.released_running: list[int] = []
        self.metrics_updates: list[dict[str, Any]] = []
        self.heartbeats = 0

    def claim_items(self, dsn: str, job_id: int, limit: int) -> list[ClaimedItem]:
        batch, self.pending = self.pending[:limit], self.pending[limit:]
        return batch

    def complete(self, dsn: str, **kwargs: Any) -> None:
        self.completed.append(kwargs)

    def fail(self, dsn: str, *, job_id: int, item_id: int, error: str, max_retries: int) -> bool:
        self.failed.append({"item_id": item_id, "error": error, "max_retries": max_retries})
        return max_retries > 1  # 简化: 只有上限为 1 时视为死信

    def release(self, dsn: str, ids: list[int]) -> int:
        self.released.append(ids)
        return len(ids)

    def release_job(self, dsn: str, job_id: int) -> bool:
        self.released_jobs.append(job_id)
        return True

    def release_running(self, dsn: str, job_id: int) -> int:
        self.released_running.append(job_id)
        self.released_running_count = len(self.pending)
        return self.released_running_count

    def progress(self, dsn: str, job_id: int) -> dict[str, int]:
        succeeded = len(self.completed)
        failed = len([f for f in self.failed if f["max_retries"] <= 1])
        pending = len(self.pending)
        return {
            "pending": pending, "running": 0, "succeeded": succeeded, "failed": failed,
            "total": succeeded + failed + pending,
        }

    def finish(self, dsn: str, job_id: int, status: str, error: str = "") -> None:
        self.finished.append((status, error))

    def update_metrics(self, dsn: str, run_id: int, metrics: dict[str, Any]) -> None:
        self.metrics_updates.append(metrics)


class FakeRetriever:
    def __init__(self, hit_ids: list[str], *, raise_on_search: bool = False) -> None:
        self.hit_ids = hit_ids
        self.raise_on_search = raise_on_search
        self.client = object()
        self.closed = False

    def search_many(self, questions: list[str], top_k: int | None = None) -> list[list[Any]]:
        if self.raise_on_search:
            raise RuntimeError("模拟检索失败")
        return [
            [
                type("Hit", (), {
                    "point_id": pid, "doc_id": "A01", "score": 0.9,
                    "text": "线索超过 7 天无跟进会自动回收, 并站内通知原负责人。",
                })()
                for pid in self.hit_ids
            ]
            for _ in questions
        ]

    def close(self) -> None:
        self.closed = True


@pytest.fixture()
def patched(monkeypatch: pytest.MonkeyPatch):
    """把 runner 依赖的 DB/检索函数替换为 fake。"""

    def install(
            *,
            items: list[ClaimedItem],
            hits: list[str],
            gold_ids: set[str],
            collection_exists_flag: bool = True,
            raise_on_search: bool = False,
            generation: dict[str, Any] | None = None,
    ) -> tuple[FakeQueue, FakeRetriever]:
        fake_queue = FakeQueue(items)
        fake_retriever = FakeRetriever(hits, raise_on_search=raise_on_search)

        monkeypatch.setattr(qr, "claim_job_items", fake_queue.claim_items)
        monkeypatch.setattr(qr, "complete_item", fake_queue.complete)
        monkeypatch.setattr(qr, "fail_item", fake_queue.fail)
        monkeypatch.setattr(qr, "release_items", fake_queue.release)
        monkeypatch.setattr(qr, "release_job", fake_queue.release_job)
        monkeypatch.setattr(qr, "job_progress", fake_queue.progress)
        monkeypatch.setattr(qr, "finish_job", fake_queue.finish)
        monkeypatch.setattr(qr, "update_run_metrics", fake_queue.update_metrics)
        monkeypatch.setattr(qr, "heartbeat", lambda dsn, job_id: None)
        monkeypatch.setattr(qr, "release_running_items", fake_queue.release_running)
        monkeypatch.setattr(
            qr, "get_generation_usage",
            lambda dsn, run_id: {"answers_generated": 0, "prompt_tokens": 0, "completion_tokens": 0},
        )
        monkeypatch.setattr(qr, "collection_exists", lambda client, name: collection_exists_flag)
        monkeypatch.setattr(
            qr, "get_run_context",
            lambda dsn, run_id: RunContext(
                run_id=run_id, dataset_id=3, corpus_id=4,
                config_snapshot={
                    "chunking": {"strategy": "headings", "chunk_size": 500, "overlap": 50, "min_chars": 80},
                    "retrieval": {"top_k": 5},
                    # 只有带 generation 段的 run 才会跑生成(D14)
                    **({"generation": generation} if generation is not None else {}),
                },
            ),
        )
        monkeypatch.setattr(
            qr, "list_documents",
            lambda dsn, corpus_id: [DocumentRow(id=1, doc_id="A01", title="线索", raw_text=DOC)],
        )
        monkeypatch.setattr(
            qr, "list_cases",
            lambda dsn, dataset_id: [
                CaseRow(id=101, qid="q-1", question="回收多少天?", gold_anchors=[{"doc": "A01", "span": "线索回收"}],
                        category="线索", difficulty="易"),
                CaseRow(id=102, qid="q-2", question="上限多少?", gold_anchors=[{"doc": "A01", "span": "上限"}],
                        category="线索", difficulty="易"),
            ],
        )
        monkeypatch.setattr(
            qr, "map_dataset_cases",
            lambda cases, doc_chunks, corpus_id, cfg_hash: type(
                "M", (), {"cases": [type("C", (), {"qid": c.qid, "gold_point_ids": set(gold_ids)})() for c in cases]}
            )(),
        )
        monkeypatch.setattr(
            qr, "list_case_metric_rows",
            lambda dsn, run_id: [
                {"qid": "q-1", "gold_count": 1, "retrieved_count": 5, "hits": 1, "recall": 1.0,
                 "precision": 0.2, "reciprocal_rank": 1.0, "hit": 1.0, "first_hit_rank": 1},
                {"qid": "q-2", "gold_count": 1, "retrieved_count": 5, "hits": 0, "recall": 0.0,
                 "precision": 0.0, "reciprocal_rank": 0.0, "hit": 0.0, "first_hit_rank": None},
            ],
        )
        return fake_queue, fake_retriever

    return install


def make_runner(retriever: FakeRetriever, **options: Any) -> QueueRunner:
    """构造注入了 fake 检索器的 runner(默认工厂会去连真 Qdrant)。"""
    opts = RunnerOptions(**options) if options else None
    return QueueRunner(
        Settings(_env_file=None),
        opts,
        retriever_factory=lambda settings, collection, top_k: retriever,
    )


def make_items(count: int) -> list[ClaimedItem]:
    case_ids = [101, 102]
    return [
        ClaimedItem(id=1000 + i, job_id=7, case_id=case_ids[i % 2], retry_count=0)
        for i in range(count)
    ]


def run_job(runner: QueueRunner) -> qr.JobSummary:
    return runner.execute_job(ClaimedJob(id=7, run_id=5, status="running", progress={}))


def test_run_once_returns_none_when_queue_empty(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setattr(qr, "claim_job", lambda dsn, job_id=None: None)
    runner = QueueRunner(Settings(_env_file=None))
    assert runner.run_once() is None


def test_execute_job_success_writes_metrics(patched):
    install = patched
    fake_queue, retriever = install(items=make_items(2), hits=["p1"], gold_ids={"p1"})
    runner = make_runner(retriever)

    summary = run_job(runner)

    assert summary.status == "succeeded"
    assert (summary.succeeded, summary.failed, summary.processed) == (2, 0, 2)
    assert len(fake_queue.completed) == 2
    assert fake_queue.finished == [("succeeded", "")]
    # 每条完成记录都应带指标与检索结果
    first = fake_queue.completed[0]
    assert first["metrics"]["recall"] == 1.0
    assert first["retrieved"][0]["point_id"] == "p1"
    # run 级指标来自 DB 聚合(续跑安全)
    assert fake_queue.metrics_updates[0]["recall_at_k"] == 0.5
    assert fake_queue.metrics_updates[0]["cases_evaluated"] == 2


def test_execute_job_item_failure_goes_dead_letter(patched):
    install = patched
    fake_queue, retriever = install(items=make_items(1), hits=["p1"], gold_ids={"p1"}, raise_on_search=True)
    runner = make_runner(retriever, max_retries=1)

    summary = run_job(runner)

    assert summary.status == "failed"
    assert summary.failed == 1
    assert fake_queue.finished[0][0] == "failed"
    assert "失败" in fake_queue.finished[0][1]
    assert len(fake_queue.failed) == 1


def test_max_items_pauses_and_puts_job_back(patched):
    install = patched
    fake_queue, retriever = install(items=make_items(3), hits=["p1"], gold_ids={"p1"})
    runner = make_runner(retriever, max_items=1, batch_size=8)

    summary = run_job(runner)

    assert summary.status == "paused"
    assert summary.processed == 1
    assert len(fake_queue.completed) == 1
    # 领取额度已按 max_items 收敛: 不会出现"领了却不跑"的项, 剩余仍为 pending
    assert fake_queue.released == []
    assert [item.id for item in fake_queue.pending] == [1001, 1002]
    # 任务放回队列(下一次 run_once 从断点继续), 且未写终态
    assert fake_queue.released_jobs == [7]
    assert fake_queue.finished == []


def test_missing_index_fails_fast(patched):
    install = patched
    fake_queue, retriever = install(
        items=make_items(1), hits=["p1"], gold_ids={"p1"}, collection_exists_flag=False
    )
    runner = make_runner(retriever)

    summary = run_job(runner)

    assert summary.status == "failed"
    assert fake_queue.completed == []
    assert fake_queue.finished[0][0] == "failed"
    assert "索引不存在" in fake_queue.finished[0][1]


def test_case_without_gold_is_marked_no_gold(patched):
    install = patched
    fake_queue, retriever = install(items=make_items(1), hits=["p1"], gold_ids=set())
    runner = make_runner(retriever)

    summary = run_job(runner)

    assert summary.status == "succeeded"
    assert fake_queue.completed[0]["metrics"] == {}
    assert fake_queue.completed[0]["flags"] == ["no_gold"]


def test_aggregate_helper_matches_metrics_module():
    """聚合口径必须复用 metrics.aggregate, 避免两处实现漂移。"""
    metric = CaseMetric(
        qid="q", gold_count=1, retrieved_count=5, hits=1, recall=1.0, precision=0.2,
        reciprocal_rank=0.5, hit=1.0, first_hit_rank=2,
    )
    assert aggregate([metric], 5, skipped=0)["recall_at_k"] == 1.0

# ---- M4: 生成链路接入 ----


class FakeLLM:
    """假的生成客户端: 记录调用, 可注入固定答案或异常。"""

    def __init__(self, text: str = "线索超过 7 天会被回收。", *, error: Exception | None = None) -> None:
        self.text = text
        self.error = error
        self.calls: list[dict[str, Any]] = []

    def complete(self, messages: list[dict[str, Any]], *, temperature: float = 0.0,
                 max_tokens: int = 512) -> ChatResult:
        self.calls.append({"messages": messages})
        if self.error is not None:
            raise self.error
        return ChatResult(text=self.text, prompt_tokens=280, completion_tokens=22, latency_ms=2400,
                          raw_model="fake-deepseek")


GENERATION_SECTION: dict[str, Any] = {
    "provider": "siliconflow",
    "base_url": "https://api.siliconflow.cn/v1",
    "model": "fake-deepseek",
    "prompt_id": "qa_zh_v1",
    "temperature": 0.0,
    "max_tokens": 512,
    "max_context_chars": 3000,
}


def make_generating_runner(
        retriever: FakeRetriever, llm: FakeLLM, *, prompt_id: str = "qa_zh_v1", **options: Any
) -> QueueRunner:
    """注入了 fake 生成客户端的 runner; prompt 从快照的 prompt_id 加载(真实资产)。"""
    return QueueRunner(
        Settings(_env_file=None),
        RunnerOptions(**options),
        retriever_factory=lambda settings, collection, top_k: retriever,
        llm_client=llm,
        prompt=load_prompt(prompt_id),
    )


def test_retrieval_only_when_snapshot_has_no_generation(patched):
    """快照没有 generation 段 = 只跑检索: 行为与 M3 完全一致(没有 answer/元信息)。"""
    fake_queue, retriever = patched(items=make_items(1), hits=["p1"], gold_ids={"p1"})
    summary = run_job(make_runner(retriever))

    assert summary.status == "succeeded"
    assert fake_queue.completed[0]["answer"] is None
    assert fake_queue.completed[0]["generation"] is None


def test_generate_writes_answer_and_metadata(patched):
    fake_queue, retriever = patched(
        items=make_items(2), hits=["p1"], gold_ids={"p1"}, generation=GENERATION_SECTION
    )
    llm = FakeLLM(text="线索超过 7 天无跟进会被自动回收。")

    summary = run_job(make_generating_runner(retriever, llm))

    assert summary.status == "succeeded"
    assert len(llm.calls) == 2, "每题各生成一次"
    first = fake_queue.completed[0]
    assert first["answer"] == "线索超过 7 天无跟进会被自动回收。"
    meta = first["generation"]
    assert meta["prompt_id"] == "qa_zh_v1"
    assert len(meta["prompt_hash"]) == 16
    assert meta["model"] == "fake-deepseek"
    assert (meta["prompt_tokens"], meta["completion_tokens"]) == (280, 22)
    assert meta["context_chunks"] == 1, "检索到的 chunk 应作为资料传给模型"
    assert meta["temperature"] == 0.0


def test_empty_answer_is_never_persisted(patched):
    """红线: 空答案必须走重试路径, 绝不能写进 case_results。"""
    fake_queue, retriever = patched(
        items=make_items(2), hits=["p1"], gold_ids={"p1"}, generation=GENERATION_SECTION
    )
    llm = FakeLLM(text="   ")

    summary = run_job(make_generating_runner(retriever, llm))

    assert fake_queue.completed == [], "空答案不得落库"
    assert len(fake_queue.failed) == 2
    assert all("生成失败" in f["error"] for f in fake_queue.failed)
    assert all(f["max_retries"] == 3 for f in fake_queue.failed), "空答案属可重试失败"
    assert summary.processed == 2


def test_transient_generation_error_keeps_item_retryable(patched):
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"}, generation=GENERATION_SECTION
    )
    llm = FakeLLM(error=LLMTransientError("HTTP 429: rate limited"))

    run_job(make_generating_runner(retriever, llm))

    assert fake_queue.completed == []
    assert "429" in fake_queue.failed[0]["error"]
    assert fake_queue.failed[0]["max_retries"] == 3


def test_fatal_generation_error_aborts_job_instead_of_flooding_dead_letters(patched):
    """红线: 欠费/鉴权这类致命错误要立刻中止任务, 不能刷 N 条死信把原因埋掉。"""
    fake_queue, retriever = patched(
        items=make_items(4), hits=["p1"], gold_ids={"p1"}, generation=GENERATION_SECTION
    )
    llm = FakeLLM(error=LLMFatalError("HTTP 402: account balance is insufficient"))

    summary = run_job(make_generating_runner(retriever, llm))

    assert summary.status == "failed"
    assert len(fake_queue.failed) == 1, "只记当前这一条, 其余不再尝试"
    assert fake_queue.completed == []
    status, error = fake_queue.finished[0]
    assert status == "failed" and "402" in error
    # 已领取但未处理的条目要归位, 否则会永远停在 running(进度条卡住 + 成为孤儿)
    assert fake_queue.released_running == [7]


def test_invalid_prompt_fails_job_before_touching_retrieval(patched):
    """快照里引用的 prompt 资产不存在属于配置错误: 应在任何检索/索引 IO 之前就让任务失败。"""
    section = dict(GENERATION_SECTION, prompt_id="不存在的prompt")
    fake_queue, retriever = patched(
        items=make_items(2), hits=["p1"], gold_ids={"p1"}, generation=section
    )
    factory_calls: list[str] = []

    def factory(settings: Settings, collection: str, top_k: int) -> FakeRetriever:
        factory_calls.append(collection)
        return retriever

    runner = QueueRunner(
        Settings(_env_file=None),
        RunnerOptions(),
        retriever_factory=factory,
        llm_client=FakeLLM(),   # 注入 client, 但 prompt 仍从快照的 prompt_id 加载
    )

    summary = run_job(runner)

    assert summary.status == "failed"
    assert "生成配置错误" in fake_queue.finished[0][1]
    assert fake_queue.completed == [] and fake_queue.failed == []
    assert factory_calls == [], "配置错误应在创建检索器之前就暴露(不做任何无谓 IO)"


def test_generation_usage_is_merged_into_run_metrics(patched, monkeypatch: pytest.MonkeyPatch):
    """成本必须能从 run.metrics 读到(D8): 否则 M4 之后的账单无从核算。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"}, generation=GENERATION_SECTION
    )
    monkeypatch.setattr(
        qr, "get_generation_usage",
        lambda dsn, run_id: {"answers_generated": 1, "prompt_tokens": 280, "completion_tokens": 22},
    )

    run_job(make_generating_runner(retriever, FakeLLM()))

    metrics = fake_queue.metrics_updates[0]
    assert metrics["answers_generated"] == 1
    assert metrics["prompt_tokens"] == 280 and metrics["completion_tokens"] == 22
    assert metrics["recall_at_k"] == 0.5, "检索侧指标不受生成影响"


def test_generation_uses_snapshot_parameters_not_worker_env(patched):
    """红线(D14): 生成参数以**快照**为准, 不看 worker 环境变量 —— 否则报告写的与实际调的不一致。"""
    section = dict(GENERATION_SECTION, model="snapshot-model", provider="snapshot-provider",
                   temperature=0.3, max_tokens=128, max_context_chars=500)
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"}, generation=section
    )
    llm = FakeLLM()

    run_job(make_generating_runner(retriever, llm))

    meta = fake_queue.completed[0]["generation"]
    assert meta["model"] == "fake-deepseek", "model 用客户端实际返回的模型名"
    assert meta["provider"] == "snapshot-provider"
    assert meta["temperature"] == 0.3, "温度必须来自快照"
    assert meta["max_tokens"] == 128
    assert meta["context_chars"] <= 500, "上下文预算来自快照(而不是 worker 的 GENERATION_MAX_CONTEXT_CHARS)"


def test_blank_generation_prompt_in_snapshot_fails_fast(patched):
    section = dict(GENERATION_SECTION, prompt_id="")
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"}, generation=section
    )

    summary = run_job(make_generating_runner(retriever, FakeLLM()))

    assert summary.status == "failed"
    assert "生成配置错误" in fake_queue.finished[0][1]
    assert fake_queue.completed == []
