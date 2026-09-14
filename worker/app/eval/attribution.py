"""单题归因规则内核(M4-3, process.md D15/D16)。

设计约束(每条都由踩过的坑换来, 改规则前先读):

1. **纯函数**: 只吃已经算好的事实(单题检索指标 + judge 结论), 不碰网络/DB/LLM。
   所以同一套规则既能被 queue_runner 在落库那一刻调用, 也能被 CLI 对历史 run 重算
   —— 规则升级或调阈值不需要重跑 LLM(run #155 一次判定 101k tokens, 重跑很贵)。
2. **上游优先**: 返回的 flags 已按严重度/上游性排序, 报告侧"主因" = flags[0], 不新增列。
   检索没把证据送到位时, 再谈"答案有据没据"意义有限 -> 检索类标签压过判定类标签。
3. **缺失 != 合格**: 没有判定结果时不产出任何判定类标签; 没有 rubric 时不产出质量标签。
   否则"未评测"会被当成"没问题"(M4-2 rubric v1 负控的教训: 全编造的答案拿到 5/5)。
4. **阈值必须随 run 落库(D15)**: k 变了 retrieval_partial 的数量本来就会变
   (实测 run #112 k=1 有 12 条, run #155 k=5 只剩 5 条), 不记阈值则跨 run 的标签数不可比。
5. **整数口径**: recall 用 hits < gold_count 判断, 不走浮点比较, 避免 0.9999 之类的假部分召回。

标签语义与降级矩阵见 docs/attribution.md。
"""
from __future__ import annotations

import math
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass
from typing import Any

# 规则版本: 谓词或优先级发生变化时必须 +1(与落库的 rules_version 一起用于解释历史标签)
ATTRIBUTION_VERSION = 1

# ---- 标签常量(前两个是 M3/M4-1 起就已在库的既有标签, 名字不能改, 报告筛选用得到) ----
FLAG_NO_GOLD = "no_gold"                        # 数据集缺 gold: 检索侧不可评测
FLAG_ANCHOR_INCOMPLETE = "anchor_incomplete"    # 没召回 gold 但答案全有据 -> 锚点漏标
FLAG_RETRIEVAL_MISS = "retrieval_miss"          # gold 全未召回
FLAG_RETRIEVAL_PARTIAL = "retrieval_partial"    # 多 gold 题部分未召回
FLAG_RETRIEVAL_LOW_RANK = "retrieval_low_rank"  # 命中但排位靠后
FLAG_HALLUCINATION = "hallucination"            # 有断言无证据
FLAG_OFF_TOPIC = "off_topic"                    # 有据但答非所问
FLAG_GENERATION_QUALITY = "generation_quality"  # 检索到位/无幻觉, 质量仍不达标
FLAG_NO_CLAIMS = "no_claims"                    # 无可核查断言(信息性标签)

# 优先级 = 数组顺序。报告侧取 flags[0] 作为主因, 所以顺序即口径。
FLAG_PRIORITY: tuple[str, ...] = (
    FLAG_NO_GOLD,
    FLAG_ANCHOR_INCOMPLETE,
    FLAG_RETRIEVAL_MISS,
    FLAG_RETRIEVAL_PARTIAL,
    FLAG_RETRIEVAL_LOW_RANK,
    FLAG_HALLUCINATION,
    FLAG_OFF_TOPIC,
    FLAG_GENERATION_QUALITY,
    FLAG_NO_CLAIMS,
)

FLAG_ZH: dict[str, str] = {
    FLAG_NO_GOLD: "数据集缺 gold(不可评测)",
    FLAG_ANCHOR_INCOMPLETE: "锚点漏标(答案其实有据)",
    FLAG_RETRIEVAL_MISS: "检索漏召回(全未命中)",
    FLAG_RETRIEVAL_PARTIAL: "检索部分漏召回",
    FLAG_RETRIEVAL_LOW_RANK: "命中但排序靠后",
    FLAG_HALLUCINATION: "幻觉(有断言无据)",
    FLAG_OFF_TOPIC: "答非所问(断言与问题无关)",
    FLAG_GENERATION_QUALITY: "生成质量不达标",
    FLAG_NO_CLAIMS: "无可核查断言",
}

# 归因覆盖范围: 只跑检索的 run 出不了判定类标签, 报告必须能说清"本次覆盖到哪一环"
SCOPE_RETRIEVAL = "retrieval"
SCOPE_RETRIEVAL_JUDGE = "retrieval+judge"

DEFAULT_LOW_RANK_RATIO = 0.5   # first_hit_rank > ceil(k * ratio) 记为排序靠后
DEFAULT_QUALITY_LINE = 3       # helpfulness/relevance <= 该线记为质量不达标(1-5 分制)
_UNKNOWN_RANK = len(FLAG_PRIORITY)


@dataclass(frozen=True)
class Thresholds:
    """归因阈值; 会随 run 一起落进 runs.metrics.attribution(D15)。"""

    k: int
    low_rank_ratio: float = DEFAULT_LOW_RANK_RATIO
    quality_line: int = DEFAULT_QUALITY_LINE

    def __post_init__(self) -> None:
        if self.k < 1:
            raise ValueError(f"k 必须 >= 1, 收到 {self.k}")
        if not 0.0 < self.low_rank_ratio <= 1.0:
            raise ValueError(f"low_rank_ratio 必须落在 (0, 1], 收到 {self.low_rank_ratio}")
        if not 1 <= self.quality_line <= 5:
            raise ValueError(f"quality_line 必须落在 [1, 5], 收到 {self.quality_line}")

    def low_rank_limit(self) -> int:
        """排位超过该值记 retrieval_low_rank。k=5 -> 3; k=1 -> 1(即永不触发)。"""
        return max(1, math.ceil(self.k * self.low_rank_ratio))

    def to_json(self) -> dict[str, Any]:
        return {
            "k": self.k,
            "low_rank_limit": self.low_rank_limit(),
            "low_rank_ratio": self.low_rank_ratio,
            "quality_line": self.quality_line,
        }


def _to_int(value: Any, default: int | None = None) -> int | None:
    if value is None or isinstance(value, bool):
        return default
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _to_float(value: Any, default: float = 0.0) -> float:
    if value is None:
        return default
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def _gold_count(metric: Mapping[str, Any]) -> int:
    return max(0, _to_int(metric.get("gold_count"), 0) or 0)


def _hit_and_hits(metric: Mapping[str, Any], gold_count: int) -> tuple[bool, int]:
    """返回 (是否命中, 命中条数)。hits 是权威口径, 缺失时才回退到 recall/hit。"""
    hits = _to_int(metric.get("hits"))
    if hits is None:
        hits = round(_to_float(metric.get("recall"), 0.0) * gold_count)   # round() 已返回 int, 不要套 int()
    hits = max(0, hits)
    if hits == 0 and _to_float(metric.get("hit"), 0.0) > 0:
        hits = 1  # 兼容只写了 hit 的历史行
    return hits > 0, hits


def _first_hit_rank(metric: Mapping[str, Any]) -> int | None:
    rank = _to_int(metric.get("first_hit_rank"))
    return rank if rank and rank > 0 else None


def _claim_counts(judge: Mapping[str, Any]) -> dict[str, int]:
    counts = {"total": 0, "supported": 0, "unsupported": 0, "irrelevant": 0}
    raw = judge.get("claims")
    if not isinstance(raw, Sequence):
        return counts
    for claim in raw:
        if not isinstance(claim, Mapping):
            continue
        counts["total"] += 1
        label = str(claim.get("label", ""))
        if label in counts:
            counts[label] += 1
    return counts


def _rubric_scores(judge: Mapping[str, Any]) -> tuple[int | None, int | None]:
    rubric = judge.get("rubric")
    if not isinstance(rubric, Mapping):
        return None, None
    return _to_int(rubric.get("relevance")), _to_int(rubric.get("helpfulness"))


def is_grounded(judge: Mapping[str, Any] | None) -> bool:
    """答案是否"完全有据"(拆出了断言, 且没有 unsupported/irrelevant)。"""
    if not judge:
        return False
    counts = _claim_counts(judge)
    return counts["total"] > 0 and counts["unsupported"] == 0 and counts["irrelevant"] == 0


def sort_flags(flags: Iterable[str]) -> list[str]:
    """按 FLAG_PRIORITY 排序; 未知标签排末尾且保持原有相对顺序(稳定)。"""
    indexed = list(enumerate(flags))
    indexed.sort(key=lambda pair: (FLAG_PRIORITY.index(pair[1]) if pair[1] in FLAG_PRIORITY
                                   else _UNKNOWN_RANK, pair[0]))
    return [flag for _, flag in indexed]


def attribute_case(
        *,
        metric: Mapping[str, Any] | None,
        judge: Mapping[str, Any] | None,
        thresholds: Thresholds,
) -> list[str]:
    """给单题打归因标签。

    metric: case_results.metrics 的内容(CaseMetric.to_json()); 空/无 gold -> no_gold
    judge:  case_results.judge 的内容(JudgeResult.to_json()); 空 dict 视为"本次未判定"
    """
    flags: list[str] = []
    judged = judge if judge else None          # {} 与 None 一律视为"没有判定信号"
    counts = _claim_counts(judged) if judged is not None else None
    grounded = is_grounded(judged)

    gold_count = _gold_count(metric) if metric else 0
    has_gold = gold_count > 0
    hit, hits = _hit_and_hits(metric, gold_count) if has_gold else (False, 0)

    # ---- 检索侧 ----
    if not has_gold:
        # 不可评测: 数据集问题, 但判定侧标签(幻觉等)依然成立, 所以不提前返回
        flags.append(FLAG_NO_GOLD)
    elif not hit:
        # 没召回 gold 却拆出了全有据的断言 -> 证据其实在检索结果里, 是锚点漏标
        if grounded:
            flags.append(FLAG_ANCHOR_INCOMPLETE)
        flags.append(FLAG_RETRIEVAL_MISS)
    else:
        if hits < gold_count:
            flags.append(FLAG_RETRIEVAL_PARTIAL)
        rank = _first_hit_rank(metric)
        if rank is not None and rank > thresholds.low_rank_limit():
            flags.append(FLAG_RETRIEVAL_LOW_RANK)

    # ---- 判定侧(缺失时一律不出标签) ----
    if counts is not None:
        if counts["total"] == 0:
            flags.append(FLAG_NO_CLAIMS)
        if counts["unsupported"] > 0:
            flags.append(FLAG_HALLUCINATION)
        if counts["irrelevant"] > 0:
            flags.append(FLAG_OFF_TOPIC)

        # 质量标签的前提: 证据到位 + 无幻觉 + 确实打了分
        if has_gold and hit and counts["unsupported"] == 0 and counts["irrelevant"] == 0:
            relevance, helpfulness = _rubric_scores(judged)
            if relevance is not None and helpfulness is not None and \
                    min(relevance, helpfulness) <= thresholds.quality_line:
                flags.append(FLAG_GENERATION_QUALITY)

    return sort_flags(flags)


def scope_for(*, judge_enabled: bool) -> str:
    return SCOPE_RETRIEVAL_JUDGE if judge_enabled else SCOPE_RETRIEVAL


def attribution_meta(*, thresholds: Thresholds, judge_enabled: bool) -> dict[str, Any]:
    """写进 runs.metrics.attribution 的元信息(D15): 规则版本 + 覆盖范围 + 全部阈值。"""
    return {
        "version": ATTRIBUTION_VERSION,
        "scope": scope_for(judge_enabled=judge_enabled),
        **thresholds.to_json(),
    }


def summarize_flags(flag_lists: Iterable[Sequence[str]]) -> dict[str, int]:
    """统计标签分布(按次数降序, 同次数按优先级顺序), 供 CLI/报告展示。"""
    counts: dict[str, int] = {}
    for flags in flag_lists:
        for flag in flags:
            counts[flag] = counts.get(flag, 0) + 1
    return dict(sorted(counts.items(), key=lambda kv: (-kv[1], sort_flags([kv[0]]))))