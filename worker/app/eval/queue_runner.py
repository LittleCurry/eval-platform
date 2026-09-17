"""队列消费者: 领任务 → 逐条执行评测 → checkpoint 落库 → 结束任务。

设计要点:
- 每条 case 由 queue.complete_item 原子提交(检索结果 + 生成答案 + judge 判定 + 微任务状态 + 进度);
  中断只丢当前一条;
- 心跳按周期刷新; 心跳超时的任务由 reclaim_stale_jobs 接管(常驻模式周期自动执行);
- 支持 max_items 提前停止(测试/灰度): 未跑的微任务放回 pending, 任务也放回队列,
  下一次 run_once 从断点继续 —— 这就是"中断续跑"。

M4 起多出两个阶段, 二者都由 **run 的配置快照**决定是否执行(D14):
1. **生成**(generation 段): 用检索到的 top-k 上下文产出 answer;
2. **判定**(judge 段): 对 answer 做 claim 级事实核查(±rubric 打分)。
worker 不看 CLI/环境变量决定"跑不跑", 只看快照 —— 这样"跑过的实验"与"记录的配置"永远一致。
"""
from __future__ import annotations

import time
from collections.abc import Callable, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Any

import structlog

from app.config import Settings
from app.eval.attribution import (
    DEFAULT_LOW_RANK_RATIO,
    DEFAULT_QUALITY_LINE,
    Thresholds,
    attribute_case,
    attribution_meta,
)
from app.generation.builtin import EmptyAnswerError, GenerationResult, SupportsComplete, generate_answer
from app.generation.prompts import PromptError, PromptTemplate, load_prompt
from app.judge.builtin import (
    CLAIMS_REQUIRED_PLACEHOLDERS,
    RUBRIC_REQUIRED_PLACEHOLDERS,
    JudgeFailed,
    JudgeResult,
    judge_case,
)
from app.judge.cache import JudgeCache, NullJudgeCache, PostgresJudgeCache
from app.llm.client import ChatClient, LLMFatalError, LLMTransientError
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
    reclaim_stale_jobs,
    release_items,
    release_job,
    release_running_items,
    update_run_metrics,
)
from app.retrieval.anchor import AnchorSpec, GoldCase, map_dataset_cases
from app.retrieval.chunker import ChunkingConfig, chunk_document, chunking_hash
from app.retrieval.collections import collection_exists, collection_name
from app.retrieval.retriever import Retriever
from app.store import get_generation_usage, get_judge_usage, list_cases, list_documents

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
    stale_timeout_seconds: float = 60.0
    reclaim_every_polls: int = 10
    batch_pause_ms: float = 0.0  # 批次间暂停(故障演练/模拟慢 LLM 用)
    # ---- M4: 生成 / judge 链路的**执行资源**(实验参数在快照里, 见下) ----
    # "跑不跑"由快照的 generation / judge 段决定(D14), 这里只决定"跑多快"。
    generation_concurrency: int = 4   # 同批次内并行生成数(实测单题稳态 ~2.4s, 4 并发足够且不易触发限流)
    judge_concurrency: int = 4        # 同批次内并行判定数(M4-2; judge 每案例 2 次调用)
    # ---- M4-3: 归因阈值(打标口径)。它们**会随标签一起落进 runs.metrics.attribution**(D15) ----
    # k=5 时 low_rank_limit = ceil(5*0.5) = 3(排位 > 3 记"排序靠后"); k=1 时 = 1(永不触发)。
    low_rank_ratio: float = DEFAULT_LOW_RANK_RATIO
    quality_line: int = DEFAULT_QUALITY_LINE   # helpfulness/relevance <= 3 记为质量不达标


class GenerationSectionError(RuntimeError):
    """配置快照里的 generation 段缺失/类型不对 —— 属于提交侧的问题, 直接失败并说明原因。"""


class JudgeSectionError(RuntimeError):
    """配置快照里的 judge 段不合法(缺 model / 缺 prompt id) —— 同样是提交侧的问题。"""


class LlmAborted(RuntimeError):
    """LLM 侧**致命**错误(鉴权/欠费/参数错/配置非法): 结束整个任务而不是刷 N 条死信。

    为什么必须区分: 402 欠费、401 key 错这类问题重试一万次也一样, 但如果在逐条循环里
    各自"失败并继续", 结果是 N 次无意义请求 + N 条死信, 真正的原因被埋在日志里。
    (M4-1 时叫 GenerationAborted, M4-2 起同时服务 judge, 故改名。)
    """


# 兼容旧名(文档与历史提交里出现过)
GenerationAborted = LlmAborted


@dataclass
class _GenerationContext:
    """一次任务内共享的生成依赖(客户端与 prompt 都是线程安全的只读对象)。"""

    client: SupportsComplete
    prompt: PromptTemplate
    provider: str
    base_url: str
    max_context_chars: int
    temperature: float
    max_tokens: int
    concurrency: int


@dataclass
class _JudgeContext:
    """一次任务内共享的判定依赖。"""

    client: SupportsComplete
    cache: JudgeCache
    claims_prompt: PromptTemplate
    rubric_prompt: PromptTemplate | None
    provider: str
    base_url: str
    model: str
    temperature: float
    max_tokens: int
    max_claims: int
    max_context_chars: int
    max_retries: int
    concurrency: int


# 单条 LLM 阶段结果标记:
#   ("ok", GenerationResult/JudgeResult) | ("transient", Exception) | ("fatal", Exception)
#   | ("disabled", None) 未启用该阶段 | ("skip", None) 前置阶段失败, 本阶段不执行
StageOutcome = tuple[str, Any]


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
            llm_client: SupportsComplete | None = None,
            prompt: PromptTemplate | None = None,
            judge_client: SupportsComplete | None = None,
            judge_claims_prompt: PromptTemplate | None = None,
            judge_rubric_prompt: PromptTemplate | None = None,
            judge_cache: JudgeCache | None = None,
    ) -> None:
        self.settings = settings
        self.options = options or RunnerOptions()
        self.dsn = dsn or settings.pg_dsn
        self._retriever_factory = retriever_factory or (
            lambda s, collection, top_k: Retriever(s, collection, top_k=top_k)
        )
        # 生成/判定的依赖都可注入: 离线单测传 fake, 不联网
        self._injected_llm_client = llm_client
        self._injected_prompt = prompt
        self._injected_judge_client = judge_client
        self._injected_claims_prompt = judge_claims_prompt
        self._injected_rubric_prompt = judge_rubric_prompt
        self._injected_judge_cache = judge_cache
        self.log = structlog.get_logger("worker.runner")
        self._stop_requested = False

    def request_stop(self) -> None:
        """请求停止(信号处理或测试调用); 当前批次处理完后退出。"""
        self._stop_requested = True

    def _thresholds(self, top_k: int) -> Thresholds:
        """本次任务的归因阈值(M4-3)。

        它们是**执行侧旋钮**(可通过 CLI 覆盖), 但会被原样写进 runs.metrics.attribution:
        k 从 1 变到 5 时 retrieval_partial 的数量本来就会变(实测 12 条 -> 5 条),
        不记阈值则跨 run 的标签数根本不可比(D15)。
        """
        return Thresholds(
            k=top_k,
            low_rank_ratio=self.options.low_rank_ratio,
            quality_line=self.options.quality_line,
        )

    def run_once(self, job_id: int | None = None) -> JobSummary | None:
        """领取并执行一个任务; 没有可领任务时返回 None。指定 job_id 时只跑该任务。"""
        job = claim_job(self.dsn, job_id)
        if job is None:
            return None
        return self.execute_job(job)

    def reclaim_stale(self) -> Any:
        """接管心跳超时的僵尸任务(被强杀的 worker 留下的 running 任务)。"""
        result = reclaim_stale_jobs(self.dsn, self.options.stale_timeout_seconds)
        if result.jobs:
            self.log.warning("stale_jobs_reclaimed", jobs=result.jobs, items=result.items)
        return result

    def run_forever(self) -> None:
        """常驻模式: 轮询领任务 + 周期性接管僵尸任务。"""
        idle_polls = 0
        while not self._stop_requested:
            summary = self.run_once()
            if summary is None:
                idle_polls += 1
                if idle_polls % max(1, self.options.reclaim_every_polls) == 0:
                    self.reclaim_stale()
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

        # 生成/判定依赖在**任务开始前**就准备好: 快照不合法、prompt 缺失、key 缺失都属于配置错误,
        # 应当立刻以 failed 结束任务, 而不是跑到第 37 道题才炸出 N 条死信。
        owned_clients: list[ChatClient] = []
        generation: _GenerationContext | None = None
        judging: _JudgeContext | None = None
        try:
            generation, gen_client = self._generation_from_snapshot(snapshot)
            if gen_client is not None:
                owned_clients.append(gen_client)
            judging, judge_client = self._judge_from_snapshot(snapshot)
            if judge_client is not None:
                owned_clients.append(judge_client)
            if judging is not None and generation is None:
                # 没有答案就没有可核查的对象: 这种快照是提交错误, 立刻说明而不是空跑一轮
                raise JudgeSectionError(
                    "快照含 judge 段但没有 generation 段: 判定无从下手(请在提交时同时启用 generation)"
                )
        except (PromptError, LLMFatalError, GenerationSectionError, JudgeSectionError) as exc:
            # 文案区分阶段: 排障时一眼看出是生成侧还是判定侧的配置问题
            stage = "判定" if isinstance(exc, JudgeSectionError) else "生成"
            message = f"{stage}配置错误: {exc}"
            for client in owned_clients:
                client.close()
            finish_job(self.dsn, job.id, "failed", message)
            self.log.error("llm_config_invalid", job_id=job.id, error=str(exc))
            return JobSummary(job.id, job.run_id, "failed", 0, 0, 0, _ms(started))

        mode = "retrieval" + ("+generation" if generation else "") + ("+judge" if judging else "")
        self.log.info("job_mode", job_id=job.id, run_id=job.run_id, mode=mode)

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

            try:
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
                        job, todo, gold_by_qid, qid_by_case_id, question_by_case_id,
                        retriever, top_k, generation, judging,
                    )
                    succeeded += success_count
                    dead += dead_count
                    processed += len(todo)
                    if self.options.batch_pause_ms > 0:
                        time.sleep(self.options.batch_pause_ms / 1000.0)
            except LlmAborted as exc:
                message = f"LLM 致命错误, 已中止任务: {exc}"
                # 先把本批已领未处理的条目归位: 否则它们会永远停在 running(接管只看 running 的 job,
                # 而本任务已是 failed), 进度条会一直显示"运行中 N 条"。
                released = release_running_items(self.dsn, job.id)
                finish_job(self.dsn, job.id, "failed", message)
                update_run_metrics(
                    self.dsn, job.run_id,
                    self._aggregate_run_metrics(job.run_id, top_k, judge_enabled=judging is not None),
                )
                progress = job_progress(self.dsn, job.id)
                self.log.error(
                    "llm_aborted", job_id=job.id, run_id=job.run_id,
                    error=str(exc), released_items=released,
                )
                return JobSummary(
                    job.id, job.run_id, "failed", progress["succeeded"], progress["failed"],
                    processed, _ms(started),
                )

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
            update_run_metrics(
                self.dsn, job.run_id,
                self._aggregate_run_metrics(job.run_id, top_k, judge_enabled=judging is not None),
            )
            self.log.info(
                "job_finished", job_id=job.id, run_id=job.run_id, status=status, progress=progress
            )
            return JobSummary(
                job.id, job.run_id, status, progress["succeeded"], progress["failed"], processed, _ms(started)
            )
        finally:
            retriever.close()
            for client in owned_clients:
                client.close()

    def _generation_from_snapshot(
            self, snapshot: dict[str, Any],
    ) -> tuple[_GenerationContext | None, ChatClient | None]:
        """按配置快照决定本次任务是否做生成(D14)。

        快照里没有 generation 段 -> 只跑检索(历史 M2/M3 run 的语义);
        有 generation 段 -> 一律按**快照里记录的参数**执行, 不看 worker 的环境变量:
        否则报告里写的模型/prompt 与实际调用可能不一致, "可复现"就成了空话。

        返回值第二个元素是"本任务自己创建的客户端"(需要关闭); 注入的客户端不归本任务管。
        """
        section = snapshot.get("generation")
        if section is None:
            return None, None
        if not isinstance(section, dict):
            raise GenerationSectionError(f"快照 generation 段类型不符: {type(section).__name__}")

        prompt_id = str(section.get("prompt_id") or "").strip()
        model = str(section.get("model") or "").strip()
        if not prompt_id or not model:
            raise GenerationSectionError(f"快照 generation 段缺少 prompt_id/model: {section}")

        prompt = self._injected_prompt or load_prompt(prompt_id)
        client = self._injected_llm_client
        owned: ChatClient | None = None
        if client is None:
            owned = ChatClient(
                base_url=str(section.get("base_url") or self.settings.generation_base_url),
                api_key=self.settings.resolved_generation_api_key,
                model=model,
                timeout=self.settings.generation_timeout_seconds,
                max_retries=self.settings.generation_max_retries,
            )
            client = owned

        context = _GenerationContext(
            client=client,
            prompt=prompt,
            provider=str(section.get("provider") or ""),
            base_url=str(section.get("base_url") or ""),
            max_context_chars=int(section.get("max_context_chars") or 3000),
            temperature=float(section.get("temperature") or 0.0),
            max_tokens=int(section.get("max_tokens") or 512),
            concurrency=max(1, self.options.generation_concurrency),
        )
        return context, owned

    def _judge_from_snapshot(
            self, snapshot: dict[str, Any],
    ) -> tuple[_JudgeContext | None, ChatClient | None]:
        """按配置快照决定本次任务是否做 judge; 参数同样以快照为准(D14)。

        两类 prompt 的必需占位符不同(claims 需要 contexts, rubric 不需要), 因此分别加载。
        rubric 可通过快照的 enable_rubric/rubric_prompt_id 关闭 —— 关掉时只算幻觉率与支持率。
        """
        section = snapshot.get("judge")
        if section is None:
            return None, None
        if not isinstance(section, dict):
            raise JudgeSectionError(f"快照 judge 段类型不符: {type(section).__name__}")

        model = str(section.get("model") or "").strip()
        claims_prompt_id = str(section.get("claims_prompt_id") or "").strip()
        if not model or not claims_prompt_id:
            raise JudgeSectionError(f"快照 judge 段缺少 model/claims_prompt_id: {section}")

        claims_prompt = self._injected_claims_prompt or load_prompt(
            claims_prompt_id, required=CLAIMS_REQUIRED_PLACEHOLDERS
        )
        rubric_prompt: PromptTemplate | None = None
        enable_rubric = bool(section.get("enable_rubric", True))
        rubric_prompt_id = str(section.get("rubric_prompt_id") or "").strip()
        if enable_rubric and rubric_prompt_id:
            rubric_prompt = self._injected_rubric_prompt or load_prompt(
                rubric_prompt_id, required=RUBRIC_REQUIRED_PLACEHOLDERS
            )

        client = self._injected_judge_client
        owned: ChatClient | None = None
        if client is None:
            owned = ChatClient(
                base_url=str(section.get("base_url") or self.settings.judge_base_url),
                api_key=self.settings.resolved_judge_api_key,
                model=model,
                timeout=self.settings.judge_timeout_seconds,
                max_retries=self.settings.generation_max_retries,
            )
            client = owned

        cache: JudgeCache
        if self._injected_judge_cache is not None:
            cache = self._injected_judge_cache
        elif self.settings.judge_use_cache:
            cache = PostgresJudgeCache(self.dsn)
        else:
            cache = NullJudgeCache()

        context = _JudgeContext(
            client=client,
            cache=cache,
            claims_prompt=claims_prompt,
            rubric_prompt=rubric_prompt,
            provider=str(section.get("provider") or ""),
            base_url=str(section.get("base_url") or ""),
            model=model,
            temperature=float(section.get("temperature") or 0.0),
            max_tokens=int(section.get("max_tokens") or 1024),
            max_claims=int(section.get("max_claims") or 12),
            max_context_chars=int(section.get("max_context_chars") or 3000),
            max_retries=self.settings.judge_max_retries,
            concurrency=max(1, self.options.judge_concurrency),
        )
        return context, owned

    def _process_items(
            self,
            job: ClaimedJob,
            items: list[ClaimedItem],
            gold_by_qid: dict[str, set[str]],
            qid_by_case_id: dict[int, str],
            question_by_case_id: dict[int, str],
            retriever: Retriever,
            top_k: int,
            generation: _GenerationContext | None = None,
            judging: _JudgeContext | None = None,
    ) -> tuple[int, int]:
        """执行一批微任务: 批量向量化 + 批量检索 (+ 并行生成) (+ 并行判定) + 逐条 checkpoint。

        LLM 是这一环里唯一的慢操作, 所以两个 LLM 阶段各自整批并行、写库仍逐条串行
        (避免并发事务争抢同一 job 的进度行)。生成失败/判定失败的条目**不写库**,
        交给 fail_item 走队列的重试与死信 —— 绝不写"半条结果"。
        """
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

        generated: list[StageOutcome] = (
            self._generate_batch(generation, questions, responses)
            if generation is not None
            else [("disabled", None)] * len(valid_items)
        )
        answers = [
            payload.answer if tag == "ok" and isinstance(payload, GenerationResult) else None
            for tag, payload in generated
        ]
        judged: list[StageOutcome] = (
            self._judge_batch(judging, valid_items, questions, responses, answers)
            if judging is not None
            else [("disabled", None)] * len(valid_items)
        )

        thresholds = self._thresholds(top_k)

        for index, (item, hits) in enumerate(zip(valid_items, responses, strict=True)):
            qid = qid_by_case_id[item.case_id]
            gold = gold_by_qid.get(qid, set())
            retrieved = [
                {"point_id": h.point_id, "doc_id": h.doc_id, "score": round(h.score, 6)} for h in hits
            ]

            # ---- 生成阶段 ----
            generation_result: GenerationResult | None = None
            gen_tag, gen_payload = generated[index]
            if gen_tag == "transient":
                if not fail_item(
                        self.dsn, job_id=job.id, item_id=item.id, error=f"生成失败: {gen_payload}",
                        max_retries=self.options.max_retries,
                ):
                    dead += 1
                continue
            if gen_tag == "fatal":
                fail_item(
                    self.dsn, job_id=job.id, item_id=item.id, error=f"生成致命错误: {gen_payload}",
                    max_retries=1,
                )
                raise LlmAborted(str(gen_payload))
            if gen_tag == "ok":
                assert isinstance(gen_payload, GenerationResult)
                generation_result = gen_payload

            # ---- 判定阶段 ----
            judge_result: JudgeResult | None = None
            judge_tag, judge_payload = judged[index]
            if judge_tag == "transient":
                if not fail_item(
                        self.dsn, job_id=job.id, item_id=item.id, error=f"判定失败: {judge_payload}",
                        max_retries=self.options.max_retries,
                ):
                    dead += 1
                continue
            if judge_tag == "fatal":
                fail_item(
                    self.dsn, job_id=job.id, item_id=item.id, error=f"判定致命错误: {judge_payload}",
                    max_retries=1,
                )
                raise LlmAborted(str(judge_payload))
            if judge_tag == "ok":
                assert isinstance(judge_payload, JudgeResult)
                judge_result = judge_payload

            metric = evaluate_case(qid, gold, [h.point_id for h in hits], top_k)
            # gold 为空: 检索侧不可评测(metrics 记 {}), 但生成/判定侧仍然有效
            # (judge 只看答案与上下文), 所以结果照常落库, 由规则内核打上 no_gold。
            metric_json = metric.to_json() if metric is not None else {}

            # ---- 归因(M4-3, D16) ----
            # flags 是"环节事实标签集", 已按优先级排好序 -> flags[0] 就是主因(不新增列)。
            # 规则内核自己处理两种缺失: metric 为空 -> no_gold, judge 为空 -> 不出判定类标签,
            # 所以这里不再手工拼 "no_gold" / "no_claims"(口径只在 attribution.py 一处)。
            flags = attribute_case(
                metric=metric_json,
                judge=judge_result.to_json() if judge_result else None,
                thresholds=thresholds,
            )
            try:
                complete_item(
                    self.dsn, job_id=job.id, item_id=item.id, run_id=job.run_id, case_id=item.case_id,
                    retrieved=retrieved, metrics=metric_json, flags=flags,
                    answer=generation_result.answer if generation_result else None,
                    generation=generation_result.meta.to_json() if generation_result else None,
                    judge=judge_result.to_json() if judge_result else None,
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

    def _generate_batch(
            self,
            generation: _GenerationContext,
            questions: list[str],
            responses: list[list[Any]],
    ) -> list[StageOutcome]:
        """整批并行生成; 错误按类型转成标记, 交给写库阶段决定"重试还是中止"。

        这里不做任何 DB 写 —— 生成阶段是纯计算+网络, 失败只记录不落库, 保证
        "写库 == 成功的生成 + 检索结果"这一不变式。
        """

        def run_one(question: str, hits: list[Any]) -> StageOutcome:
            try:
                result = generate_answer(
                    question=question,
                    hits=hits,
                    client=generation.client,
                    prompt=generation.prompt,
                    provider=generation.provider,
                    base_url=generation.base_url,
                    max_context_chars=generation.max_context_chars,
                    temperature=generation.temperature,
                    max_tokens=generation.max_tokens,
                )
            except (EmptyAnswerError, LLMTransientError) as exc:
                return "transient", exc
            except (LLMFatalError, PromptError) as exc:
                return "fatal", exc
            return "ok", result

        return _run_parallel(generation.concurrency, run_one, questions, responses)

    def _judge_batch(
            self,
            judging: _JudgeContext,
            items: Sequence[ClaimedItem],
            questions: list[str],
            responses: list[list[Any]],
            answers: list[str | None],
    ) -> list[StageOutcome]:
        """整批并行判定; 没有答案的条目标成 skip(生成失败时不该再白调一次模型)。"""

        def run_one(index: int) -> StageOutcome:
            answer = answers[index]
            if not answer:
                return "skip", None
            try:
                result = judge_case(
                    question=questions[index],
                    answer=answer,
                    hits=responses[index],
                    client=judging.client,
                    claims_prompt=judging.claims_prompt,
                    rubric_prompt=judging.rubric_prompt,
                    cache=judging.cache,
                    model=judging.model,
                    provider=judging.provider,
                    base_url=judging.base_url,
                    temperature=judging.temperature,
                    max_tokens=judging.max_tokens,
                    max_claims=judging.max_claims,
                    max_context_chars=judging.max_context_chars,
                    max_retries=judging.max_retries,
                )
            except (JudgeFailed, LLMTransientError) as exc:
                return "transient", exc
            except (LLMFatalError, PromptError) as exc:
                return "fatal", exc
            return "ok", result

        workers = max(1, min(judging.concurrency, len(items)))
        if workers == 1:
            return [run_one(index) for index in range(len(items))]
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = [pool.submit(run_one, index) for index in range(len(items))]
            return [future.result() for future in futures]

    def _aggregate_run_metrics(
            self, run_id: int, top_k: int, *, judge_enabled: bool = False,
    ) -> dict[str, Any]:
        """从 DB 读全部单题指标聚合(续跑场景下也能算全量, 不依赖内存态)。"""
        rows = list_case_metric_rows(self.dsn, run_id)
        metrics = [_to_case_metric(row) for row in rows if row]
        evaluated = [m for m in metrics if m is not None]
        skipped = len([row for row in rows if not row])
        result = aggregate(evaluated, top_k, skipped=skipped)
        # 生成侧用量(D8 成本)
        result.update(get_generation_usage(self.dsn, run_id))
        # judge 侧结果与用量(M4-2): 三个率的分母都是 claims_total, 三率之和 = 1。
        #
        # **没有判定的 run 不写这些键**(实测踩过): 以前无论有没有判定都写
        # `hallucination_rate: 0.0`, 于是一个纯检索的 run 在库里长得像"零幻觉" ——
        # 直接查库或看 JSON 的人会被这个 0 骗到(报告页有 hasLlmMetrics 兜着, 但数据本身不该说谎)。
        # 口径与 M4-2 一致: **「没有判定」不等于「零幻觉」**。
        usage = get_judge_usage(self.dsn, run_id)
        total = float(usage.get("claims_total") or 0)
        judged_cases = float(usage.get("cases_judged") or 0)
        judge_calls = float(usage.get("judge_claims_calls") or 0)
        # "这次判过没有"的判据是"有没有调用过判定"(判过但答案没有可核查断言时
        # cases_judged 会是 0, 那时候"判过"这件事仍然要看得出来, 否则与"根本没跑判定"混淆)。
        if judged_cases > 0 or judge_calls > 0:
            result.update(usage)
            if total > 0:
                result["claim_support_rate"] = round(float(usage.get("claims_supported") or 0) / total, 6)
                result["hallucination_rate"] = round(float(usage.get("claims_unsupported") or 0) / total, 6)
                result["irrelevant_rate"] = round(float(usage.get("claims_irrelevant") or 0) / total, 6)
                result["avg_claims_per_answer"] = round(total / judged_cases, 4)
            # total == 0: 分母为 0, 三个率与均值都不写 —— 它们在这里没有意义
        # 归因元信息(D15): 规则版本 + 覆盖范围 + 全部阈值。
        # 放进 metrics 而不是快照: 快照参与 config_hash, 加段会让历史 run 的指纹全部失配,
        # 而 metrics 本来就是"这次 run 实际发生了什么"(已有 k / token 用量)。
        result["attribution"] = attribution_meta(
            thresholds=self._thresholds(top_k), judge_enabled=judge_enabled,
        )
        return result

    def _stopped_or_paused(self, processed: int) -> bool:
        if self._stop_requested:
            return True
        return self.options.max_items is not None and processed >= self.options.max_items

    def _remaining_batch(self, processed: int) -> int:
        if self.options.max_items is None:
            return self.options.batch_size
        return max(1, min(self.options.batch_size, self.options.max_items - processed))


def _run_parallel(
        concurrency: int,
        run_one: Callable[..., StageOutcome],
        questions: list[str],
        responses: list[list[Any]],
) -> list[StageOutcome]:
    """整批并行执行 run_one(question, hits); 并发数为 1 时退化为串行(便于调试)。"""
    workers = max(1, min(concurrency, len(questions)))
    if workers == 1:
        return [run_one(q, h) for q, h in zip(questions, responses, strict=True)]
    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = [pool.submit(run_one, q, h) for q, h in zip(questions, responses, strict=True)]
        return [future.result() for future in futures]


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