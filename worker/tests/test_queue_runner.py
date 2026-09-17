"""QueueRunner 离线单测: 用 fake 队列 + fake 检索器验证编排逻辑(不碰 DB/网络)。

覆盖: 正常跑完 / 单条失败与死信 / max_items 暂停并放回队列 / 索引缺失 / 空队列,
M4-1 生成链路(写库带上 answer 与元信息 / 空答案绝不落库 / 致命错误立刻中止任务),
以及 M4-2 judge 链路(快照驱动 / 判定失败可重试 / rubric 缺失降级 / 缓存复用 / 三率聚合)。
"""
from __future__ import annotations

import json
from typing import Any

import pytest

from app.config import Settings
from app.eval import queue_runner as qr
from app.eval.attribution import ATTRIBUTION_VERSION
from app.eval.queue_runner import QueueRunner, RunnerOptions
from app.generation.prompts import load_prompt
from app.judge.builtin import CLAIMS_REQUIRED_PLACEHOLDERS, RUBRIC_REQUIRED_PLACEHOLDERS
from app.judge.cache import InMemoryJudgeCache
from app.llm.client import ChatResult, LLMFatalError, LLMTransientError
from app.metrics.retrieval import CaseMetric, aggregate
from app.queue import ClaimedItem, ClaimedJob, RunContext
from app.store import CaseRow, DocumentRow

DOC = "# 线索回收\n\n超过 7 天无跟进会自动回收。\n\n## 上限\n\n每人默认 200 条。\n"

# run 级 judge 用量的"空"形态(默认打桩值: 没有 judge 时聚合出来的就是这一组 0)
JUDGE_USAGE_ZERO: dict[str, float] = {
    "claims_total": 0, "claims_supported": 0, "claims_unsupported": 0, "claims_irrelevant": 0,
    "cases_judged": 0, "rubric_cases": 0, "judge_claims_calls": 0, "judge_rubric_calls": 0,
    "judge_cache_hits": 0, "judge_prompt_tokens": 0, "judge_completion_tokens": 0,
    "judge_cache_saved_prompt_tokens": 0, "judge_cache_saved_completion_tokens": 0,
    "relevance_avg": 0, "helpfulness_avg": 0,
}


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
            judge: dict[str, Any] | None = None,
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
        monkeypatch.setattr(
            qr, "get_judge_usage", lambda dsn, run_id: JUDGE_USAGE_ZERO,
        )
        monkeypatch.setattr(qr, "collection_exists", lambda client, name: collection_exists_flag)
        monkeypatch.setattr(
            qr, "get_run_context",
            lambda dsn, run_id: RunContext(
                run_id=run_id, dataset_id=3, corpus_id=4,
                config_snapshot={
                    "chunking": {"strategy": "headings", "chunk_size": 500, "overlap": 50, "min_chars": 80},
                    "retrieval": {"top_k": 5},
                    # 只有带 generation / judge 段的 run 才会跑对应阶段(D14)
                    **({"generation": generation} if generation is not None else {}),
                    **({"judge": judge} if judge is not None else {}),
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


# ---- M4-2: judge 链路接入 ----

OK_CLAIMS = json.dumps(
    {"claims": [
        {"id": 1, "text": "线索超过 7 天无跟进会被自动回收", "label": "supported",
         "evidence": "超过 7 天无跟进会自动回收", "reason": "资料原文一致"},
    ]},
    ensure_ascii=False,
)
MIXED_CLAIMS = json.dumps(
    {"claims": [
        {"id": 1, "text": "线索超过 7 天无跟进会被自动回收", "label": "supported"},
        {"id": 2, "text": "会通过企业微信通知原负责人", "label": "unsupported"},
    ]},
    ensure_ascii=False,
)
EMPTY_CLAIMS = '{"claims":[]}'
OK_RUBRIC = json.dumps({"relevance": 4, "helpfulness": 3, "reason": "要点齐全"}, ensure_ascii=False)

JUDGE_SECTION: dict[str, Any] = {
    "provider": "siliconflow",
    "base_url": "https://api.siliconflow.cn/v1",
    "model": "fake-judge",
    "claims_prompt_id": "judge_claims_zh_v1",
    "rubric_prompt_id": "judge_rubric_zh_v1",
    "temperature": 0.0,
    "max_tokens": 1024,
    "max_context_chars": 3000,
    "enable_rubric": True,
    "max_claims": 12,
}


class QueuedLLM:
    """按顺序返回预置文本/异常的假客户端(judge 一问一答会调两次, 需要排队)。"""

    def __init__(self, *responses: object, default: str = OK_CLAIMS) -> None:
        self.queue = list(responses)
        self.default = default
        self.calls: list[dict[str, Any]] = []

    def complete(self, messages: list[dict[str, Any]], *, temperature: float = 0.0,
                 max_tokens: int = 512) -> ChatResult:
        self.calls.append({"messages": messages, "temperature": temperature, "max_tokens": max_tokens})
        item = self.queue.pop(0) if self.queue else self.default
        if isinstance(item, Exception):
            raise item
        return ChatResult(text=str(item), prompt_tokens=900, completion_tokens=60, latency_ms=1500,
                          raw_model="fake-judge-raw")


def make_judging_runner(
        retriever: FakeRetriever,
        gen_llm: FakeLLM,
        judge_llm: QueuedLLM | None = None,
        *,
        judge_cache: InMemoryJudgeCache | None = None,
        **options: Any,
) -> QueueRunner:
    """注入了 fake 生成 + fake judge 的 runner; prompt 用真实资产(快照只决定参数与开关)。"""
    return QueueRunner(
        Settings(_env_file=None),
        RunnerOptions(**options),
        retriever_factory=lambda settings, collection, top_k: retriever,
        llm_client=gen_llm,
        prompt=load_prompt("qa_zh_v1"),
        judge_client=judge_llm if judge_llm is not None else QueuedLLM(),
        judge_claims_prompt=load_prompt("judge_claims_zh_v1", required=CLAIMS_REQUIRED_PLACEHOLDERS),
        judge_rubric_prompt=load_prompt("judge_rubric_zh_v1", required=RUBRIC_REQUIRED_PLACEHOLDERS),
        judge_cache=judge_cache if judge_cache is not None else InMemoryJudgeCache(),
    )


def test_judge_is_skipped_when_snapshot_has_no_judge_section(patched):
    """只启用生成时不应跑判定: judge 结果为空(None), 也不该多花钱。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"}, generation=GENERATION_SECTION
    )
    judge_llm = QueuedLLM()

    run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    assert judge_llm.calls == []
    assert fake_queue.completed[0]["judge"] is None
    assert fake_queue.completed[0]["answer"]


def test_judge_writes_claims_rubric_and_meta(patched):
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    judge_llm = QueuedLLM(MIXED_CLAIMS, OK_RUBRIC)

    summary = run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    assert summary.status == "succeeded"
    payload = fake_queue.completed[0]["judge"]
    assert [c["label"] for c in payload["claims"]] == ["supported", "unsupported"]
    assert payload["rubric"] == {"relevance": 4, "helpfulness": 3, "reason": "要点齐全"}
    meta = payload["meta"]
    assert meta["judge_model"] == "fake-judge", "judge 模型来自快照"
    assert meta["claims_prompt_id"] == "judge_claims_zh_v1"
    assert meta["rubric_prompt_id"] == "judge_rubric_zh_v1"
    assert meta["protocol_version"] == "v1"
    assert meta["claims_calls"] == 1 and meta["rubric_calls"] == 1
    assert len(judge_llm.calls) == 2, "每题两次调用: claims + rubric"
    # 判定阶段必须同时看到答案与检索上下文(否则无法核对"是否被资料支持")
    first_user_content = judge_llm.calls[0]["messages"][1]["content"]
    assert "线索超过 7 天会被回收。" in first_user_content, "答案要进 prompt"
    assert "线索超过 7 天无跟进会自动回收" in first_user_content, "检索上下文要进 prompt"


def test_judge_section_without_generation_fails_fast(patched):
    """没有答案就没有可核查对象: 这种快照是提交错误, 必须立刻说明而不是空跑一轮。"""
    fake_queue, retriever = patched(
        items=make_items(2), hits=["p1"], gold_ids={"p1"}, judge=JUDGE_SECTION
    )

    summary = run_job(make_judging_runner(retriever, FakeLLM()))

    assert summary.status == "failed"
    assert "没有 generation" in fake_queue.finished[0][1]
    assert fake_queue.completed == [] and fake_queue.failed == []


def test_judge_fatal_error_aborts_job_and_releases_items(patched):
    """judge 的 402/401 与生成一样: 立刻中止任务并归位孤儿条目, 不刷死信。"""
    fake_queue, retriever = patched(
        items=make_items(4), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    judge_llm = QueuedLLM(LLMFatalError("HTTP 402: account balance is insufficient"))

    summary = run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    assert summary.status == "failed"
    assert fake_queue.completed == []
    assert len(fake_queue.failed) == 1, "只记当前这一条"
    assert "402" in fake_queue.finished[0][1]
    assert fake_queue.released_running == [7]


def test_judge_protocol_failure_is_retryable_not_persisted(patched):
    """判定不出来就让该条失败(可重试); 绝不写一份空判定进库。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    judge_llm = QueuedLLM("不是 JSON", "还不是 JSON", "仍然不是 JSON")

    run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    assert fake_queue.completed == [], "判定失败不得落库"
    assert "判定失败" in fake_queue.failed[0]["error"]
    assert fake_queue.failed[0]["max_retries"] == 3, "协议失败属可重试(由队列退避)"
    assert len(judge_llm.calls) == 3, "judge 内部按 judge_max_retries 重试满"


def test_judge_transient_error_keeps_item_retryable(patched):
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    judge_llm = QueuedLLM(LLMTransientError("HTTP 429: rate limited"))

    run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    assert fake_queue.completed == []
    assert "429" in fake_queue.failed[0]["error"]
    assert fake_queue.failed[0]["max_retries"] == 3


def test_judge_uses_snapshot_parameters_not_worker_env(patched):
    """D14 红线: judge 的模型/温度/claims 上限都来自快照。"""
    section = dict(JUDGE_SECTION, model="snapshot-judge", temperature=0.7, max_claims=3)
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=section,
    )
    judge_llm = QueuedLLM(MIXED_CLAIMS, OK_RUBRIC)

    run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    meta = fake_queue.completed[0]["judge"]["meta"]
    assert meta["judge_model"] == "snapshot-judge"
    assert meta["temperature"] == 0.7
    assert meta["max_claims"] == 3
    assert judge_llm.calls[0]["temperature"] == 0.7, "温度必须真的传给模型"


def test_judge_rubric_can_be_disabled_from_snapshot(patched):
    """rubric 由快照开关控制: 关掉时只算幻觉率/支持率, 不产生无谓调用。"""
    section = dict(JUDGE_SECTION, enable_rubric=False)
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=section,
    )
    judge_llm = QueuedLLM(OK_CLAIMS)

    run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    payload = fake_queue.completed[0]["judge"]
    assert payload["rubric"] is None
    assert payload["meta"]["rubric_enabled"] is False
    assert "未启用 rubric" in payload["meta"]["rubric_skipped_reason"]
    assert len(judge_llm.calls) == 1


def test_no_claims_answer_is_flagged(patched):
    """答案只说"资料中未提及"时没有可核查断言: 合法结果, 但要打标便于报告筛选。

    这里打分必须**达标**(5/4): 打分低于达标线时会额外带上 generation_quality
    (见 test_no_claims_with_bad_rubric_is_flagged_as_quality), 本用例只验"无断言"这一件事。
    """
    good_rubric = json.dumps(
        {"relevance": 5, "helpfulness": 4, "reason": "资料里确实没有相关信息, 拒答是对的"},
        ensure_ascii=False,
    )
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    judge_llm = QueuedLLM(EMPTY_CLAIMS, good_rubric)

    summary = run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    assert summary.status == "succeeded"
    assert fake_queue.completed[0]["judge"]["claims"] == []
    assert fake_queue.completed[0]["flags"] == ["no_claims"]


def test_judge_metrics_rates_are_aggregated(patched, monkeypatch: pytest.MonkeyPatch):
    """三个率的分母都是 claims_total(三率之和 = 1), 另加均值与 token 用量。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    monkeypatch.setattr(qr, "get_judge_usage", lambda dsn, run_id: {
        **JUDGE_USAGE_ZERO,
        "claims_total": 10, "claims_supported": 6, "claims_unsupported": 3, "claims_irrelevant": 1,
        "cases_judged": 4, "judge_claims_calls": 4, "judge_rubric_calls": 4,
        "judge_cache_hits": 2, "judge_prompt_tokens": 3600, "judge_completion_tokens": 240,
    })

    run_job(make_judging_runner(retriever, FakeLLM()))

    metrics = fake_queue.metrics_updates[0]
    assert metrics["claim_support_rate"] == 0.6
    assert metrics["hallucination_rate"] == 0.3
    assert metrics["irrelevant_rate"] == 0.1
    assert abs(
        metrics["claim_support_rate"] + metrics["hallucination_rate"] + metrics["irrelevant_rate"] - 1.0
    ) < 1e-9, "三率之和必须为 1(口径自洽的硬校验)"
    assert metrics["avg_claims_per_answer"] == 2.5
    assert metrics["judge_cache_hits"] == 2
    assert metrics["recall_at_k"] == 0.5, "judge 不影响检索侧指标"


def test_no_claims_omits_rate_metrics(patched, monkeypatch: pytest.MonkeyPatch):
    """判过但答案里没有可核查断言时: **不写三率**(分母为 0 的率值没有意义)。

    以前这里写 0.0 —— 那会让"分母是 0"与"真的一条幻觉都没有"在库里长得一样。
    但"判过"这件事仍要看得出来(否则与"根本没跑判定"混淆), 所以用量键照写。
    """
    fake_queue, retriever = patched(items=make_items(1), hits=["p1"], gold_ids={"p1"},
                                   generation=GENERATION_SECTION, judge=JUDGE_SECTION)
    monkeypatch.setattr(qr, "get_judge_usage", lambda dsn, run_id: {
        **JUDGE_USAGE_ZERO, "judge_claims_calls": 1, "judge_rubric_calls": 1,
    })

    run_job(make_judging_runner(retriever, FakeLLM(), QueuedLLM(EMPTY_CLAIMS, OK_RUBRIC)))

    metrics = fake_queue.metrics_updates[0]
    for key in ("claim_support_rate", "hallucination_rate", "irrelevant_rate", "avg_claims_per_answer"):
        assert key not in metrics, f"没有可核查断言时不该写 {key}"
    assert metrics["cases_judged"] == 0
    assert metrics["judge_claims_calls"] == 1, "判过这件事要看得出来(否则与没跑判定混淆)"
    assert metrics["recall_at_k"] == 0.5, "检索侧指标照常"


def test_retrieval_only_run_has_no_llm_metrics(patched):
    """**纯检索的 run 不该出现任何 LLM 侧键**(实测踩过).

    真机上遇过: 一个只跑检索的 run 在 runs.metrics 里带着 hallucination_rate=0.0 ——
    报告页有 hasLlmMetrics 兜着不会误显示, 但直接查库的人会读成"这次零幻觉"。
    数据本身不该说谎。
    """
    fake_queue, retriever = patched(items=make_items(1), hits=["p1"], gold_ids={"p1"})

    # 纯检索: 用 make_runner(不带生成/judge 客户端与 prompt), 快照里也没有那两段
    run_job(make_runner(retriever))

    metrics = fake_queue.metrics_updates[0]
    for key in ("cases_judged", "claims_total", "hallucination_rate", "claim_support_rate",
                "irrelevant_rate", "avg_claims_per_answer", "rubric_cases"):
        assert key not in metrics, f"纯检索的 run 不该有 {key}"
    assert metrics["recall_at_k"] == 0.5
    assert metrics["attribution"]["scope"] == "retrieval"


def test_judge_cache_is_reused_across_runs(patched):
    """同一批数据重跑: 判定应命中缓存(零调用), 这正是 judge 结果可复现的手段。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    cache = InMemoryJudgeCache()
    judge_llm = QueuedLLM(MIXED_CLAIMS, OK_RUBRIC)
    runner = make_judging_runner(retriever, FakeLLM(), judge_llm, judge_cache=cache)

    run_job(runner)                     # 第一次: 两次真实调用
    assert len(judge_llm.calls) == 2
    fake_queue.pending = make_items(1)  # 把条目放回队列, 模拟"同一批数据重跑"
    run_job(runner)                     # 第二次: 判定应全部命中缓存

    assert len(judge_llm.calls) == 2, "命中缓存时不该再调模型"
    second = fake_queue.completed[1]["judge"]
    assert second["meta"]["claims_cache_hits"] == 1
    assert second["meta"]["rubric_cache_hits"] == 1
    assert second["meta"]["prompt_tokens"] == 0
    assert second["meta"]["cache_saved_prompt_tokens"] == 900 * 2

# ---- M4-3: 归因标签接入 ----

def test_no_claims_with_bad_rubric_is_flagged_as_quality(patched):
    """检索到位、答案没拆出断言、打分又低于达标线 -> 主因是生成质量(而不是"无断言")。

    这是"拒答"场景的正确归因: 证据就在上下文里, 模型却什么都没答, 属于生成侧问题。
    """
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )
    judge_llm = QueuedLLM(EMPTY_CLAIMS, OK_RUBRIC)  # helpfulness=3, 恰好在默认达标线上

    run_job(make_judging_runner(retriever, FakeLLM(), judge_llm))

    assert fake_queue.completed[0]["flags"] == ["generation_quality", "no_claims"]


def test_retrieval_only_run_never_gets_judge_flags(patched):
    """只跑检索的 run: 归因只覆盖检索环节, 绝不产出幻觉/质量标签(D16 降级矩阵)。"""
    fake_queue, retriever = patched(items=make_items(1), hits=["p9"], gold_ids={"p1"})

    run_job(make_runner(retriever))

    assert fake_queue.completed[0]["judge"] is None
    assert fake_queue.completed[0]["flags"] == ["retrieval_miss"]
    assert fake_queue.metrics_updates[0]["attribution"]["scope"] == "retrieval"


def test_low_rank_hit_is_flagged(patched):
    """gold 命中了但排在第 4 位(k=5 -> 阈值 3): 记"排序靠后", 这是 reranker 的用武之地。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p9", "p8", "p7", "p1"], gold_ids={"p1"}
    )

    run_job(make_runner(retriever))

    flags = fake_queue.completed[0]["flags"]
    assert flags == ["retrieval_low_rank"]
    assert fake_queue.completed[0]["metrics"]["first_hit_rank"] == 4


def test_attribution_meta_is_recorded_in_run_metrics(patched):
    """D15: 规则版本 + 覆盖范围 + 全部阈值必须随 run 落库, 否则跨 run 的标签数不可比。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )

    run_job(make_judging_runner(retriever, FakeLLM(), QueuedLLM(OK_CLAIMS, OK_RUBRIC)))

    assert fake_queue.metrics_updates[0]["attribution"] == {
        "version": ATTRIBUTION_VERSION,
        "scope": "retrieval+judge",
        "k": 5,
        "low_rank_limit": 3,
        "low_rank_ratio": 0.5,
        "quality_line": 3,
    }


def test_quality_line_option_changes_flags_and_meta(patched):
    """达标线可由 RunnerOptions 覆盖(CLI --quality-line), 且覆盖后的值同样落进 metrics。"""
    fake_queue, retriever = patched(
        items=make_items(1), hits=["p1"], gold_ids={"p1"},
        generation=GENERATION_SECTION, judge=JUDGE_SECTION,
    )

    run_job(make_judging_runner(
        retriever, FakeLLM(), QueuedLLM(OK_CLAIMS, OK_RUBRIC), quality_line=2,
    ))

    assert fake_queue.completed[0]["flags"] == [], "helpfulness=3 > 达标线 2 -> 不算质量不达标"
    assert fake_queue.metrics_updates[0]["attribution"]["quality_line"] == 2