"""两次 run 的一致性对比(M3-5 S2): 用于"中断续跑结果必须与全量跑一致"的验收。

为什么需要一个独立工具:
- 续跑/接管是否"真的没重算、没算错", 不能只看 run 级 4 个平均数 ——  averaging 会把
  单题差异互相抵消(一题变好 + 一题变坏 = 总分不变);
- 必须逐题比 metrics + 标签, 才能证明 checkpoint 的语义正确;
- 它同时是 M5 "A/B 对比报告"的最小内核: 同一份对比逻辑, 换一组"应该不同"的 run 就是
  A/B 报告, 换一组"应该相同"的 run 就是可靠性验收。

**判定口径 vs 参考口径**(实测依据见 docs/fault-drills.md):
- 判定(影响退出码): run 级 metrics、单题 metrics(recall/precision/mrr/first_hit_rank/gold_count/hits)、
  归因标签、题目集合。
- 参考(不影响退出码, 仅打印): top-k 命中序列与分数。
  原因: query embedding 由外部服务产生, **不是逐位确定性的** —— 实测同一 (qid, point_id) 的分数
  跨 run 中位差 4.4e-4、最大 2.1e-3。近似平局的候选会因此换位(如 zjc-027 的 top1/top2 差 8e-6),
  但 gold 的召回与位次不变, 指标一致。要用序列严格判定时请加 --strict。
- **分数噪声(ScoreNoise)会被计算并打印**: 任何小于该量级的指标差异都不该被解读为"提升/退化"。
"""
from __future__ import annotations

import argparse
import json
import statistics
import sys
from dataclasses import asdict, dataclass, field
from typing import Any

import psycopg

from app.config import Settings
from app.store import get_run

# 不参与判定的 run 级字段(元信息)
RUN_META_KEYS = ("git_sha",)
# 不参与判定的单题字段(抖动项)
CASE_IGNORED_KEYS = ("latency_ms",)

EXIT_CONSISTENT = 0
EXIT_INCONSISTENT = 1
EXIT_NOT_COMPARABLE = 2


# ---- 数据模型(纯数据, 便于离线单测) ----


@dataclass(frozen=True)
class CaseView:
    """单题结果的可比对视图。"""

    qid: str
    case_id: int
    metrics: dict[str, Any]
    retrieved_ids: tuple[str, ...]
    retrieved_scores: dict[str, float]
    flags: tuple[str, ...]


@dataclass(frozen=True)
class RunView:
    """一次 run 的可比对视图。"""

    run_id: int
    dataset_id: int | None
    corpus_id: int | None
    status: str
    config_hash: str
    git_sha: str
    metrics: dict[str, Any]
    cases: dict[str, CaseView]


@dataclass(frozen=True)
class MetricDiff:
    scope: str  # "run" 或 qid
    metric: str
    left: Any
    right: Any


@dataclass(frozen=True)
class CaseDiff:
    qid: str
    reasons: tuple[str, ...]


@dataclass(frozen=True)
class ScoreNoise:
    """同一 (qid, point_id) 跨两次 run 的分数抖动 —— 对比的噪声底。"""

    pairs: int = 0
    max_abs_delta: float = 0.0
    median_abs_delta: float = 0.0


@dataclass
class CompareReport:
    left: RunView
    right: RunView
    tolerance: float
    strict: bool = False
    run_metric_diffs: list[MetricDiff] = field(default_factory=list)
    case_diffs: list[CaseDiff] = field(default_factory=list)          # 判定口径
    sequence_diffs: list[CaseDiff] = field(default_factory=list)      # 参考口径(默认不影响判定)
    only_in_left: list[str] = field(default_factory=list)
    only_in_right: list[str] = field(default_factory=list)
    score_noise: ScoreNoise = field(default_factory=ScoreNoise)
    reasons: list[str] = field(default_factory=list)  # 不可比原因

    @property
    def comparable(self) -> bool:
        return not self.reasons

    @property
    def consistent(self) -> bool:
        if not self.comparable:
            return False
        if self.run_metric_diffs or self.case_diffs or self.only_in_left or self.only_in_right:
            return False
        return not (self.strict and self.sequence_diffs)

    @property
    def exit_code(self) -> int:
        if not self.comparable:
            return EXIT_NOT_COMPARABLE
        return EXIT_CONSISTENT if self.consistent else EXIT_INCONSISTENT

    def to_json(self) -> dict[str, Any]:
        return {
            "left": self.left.run_id,
            "right": self.right.run_id,
            "strict": self.strict,
            "comparable": self.comparable,
            "consistent": self.consistent,
            "exit_code": self.exit_code,
            "reasons": self.reasons,
            "cases": {"left": len(self.left.cases), "right": len(self.right.cases)},
            "run_metric_diffs": [asdict(d) for d in self.run_metric_diffs],
            "case_diffs": [asdict(d) for d in self.case_diffs],
            "sequence_diffs": [asdict(d) for d in self.sequence_diffs],
            "only_in_left": self.only_in_left,
            "only_in_right": self.only_in_right,
            "score_noise": asdict(self.score_noise),
        }

    def render(self, *, max_cases: int = 20) -> str:
        lines: list[str] = []
        left, right = self.left, self.right
        lines.append(f"对比 run {left.run_id} (左) vs run {right.run_id} (右)")
        lines.append(
            f"  数据集 {left.dataset_id} vs {right.dataset_id} | 语料 {left.corpus_id} vs {right.corpus_id}"
        )
        lines.append(f"  配置指纹 {_short(left.config_hash)} vs {_short(right.config_hash)}")
        lines.append(f"  状态 {left.status} vs {right.status} | 题数 {len(left.cases)} vs {len(right.cases)}")
        lines.append(f"  代码版本 {_short(left.git_sha)} vs {_short(right.git_sha)}  [信息, 不参与判定]")
        lines.append(f"  判定模式 {'严格(含命中序列)' if self.strict else '标准(指标+标签+题集)'}")

        if not self.comparable:
            lines.append("")
            lines.append("不可比原因:")
            lines.extend(f"  - {reason}" for reason in self.reasons)
            lines.append("")
            lines.append("结论: 不可比 ⛔ (退出码 2)")
            return "\n".join(lines)

        lines.append("")
        lines.append("run 级指标")
        metric_keys = sorted(set(left.metrics) | set(right.metrics))
        if not metric_keys:
            lines.append("  (无指标)")
        for key in metric_keys:
            lv, rv = left.metrics.get(key), right.metrics.get(key)
            mark = "  " if _values_equal(lv, rv, self.tolerance) else "❌"
            lines.append(f"  {mark} {key:<24} {_fmt(lv):>14} {_fmt(rv):>14} {_fmt_delta(lv, rv)}")

        lines.append("")
        lines.append("单题明细(判定口径)")
        if self.only_in_left:
            lines.append(f"  ❌ 仅左侧有的题({len(self.only_in_left)}): {', '.join(self.only_in_left[:10])}")
        if self.only_in_right:
            lines.append(f"  ❌ 仅右侧有的题({len(self.only_in_right)}): {', '.join(self.only_in_right[:10])}")
        if not self.case_diffs:
            lines.append(f"  ✅ {len(left.cases)} 题逐题一致(指标 + 标签)")
        else:
            for diff in self.case_diffs[:max_cases]:
                lines.append(f"  ❌ {diff.qid}: {'; '.join(diff.reasons)}")
            if len(self.case_diffs) > max_cases:
                lines.append(f"  … 另有 {len(self.case_diffs) - max_cases} 题存在差异")

        lines.append("")
        lines.append("命中序列(参考口径)")
        if not self.sequence_diffs:
            lines.append("  ✅ 全部题目 top-k 顺序一致")
        else:
            verdict = "❌(严格模式计入判定)" if self.strict else "ℹ️ 不影响判定"
            lines.append(f"  {verdict}: {len(self.sequence_diffs)} 题顺序不同")
            for diff in self.sequence_diffs[:5]:
                lines.append(f"    {diff.qid}: {'; '.join(diff.reasons)}")
            if len(self.sequence_diffs) > 5:
                lines.append(f"    … 另有 {len(self.sequence_diffs) - 5} 题")

        noise = self.score_noise
        if noise.pairs:
            lines.append("")
            lines.append("分数噪声(同题同 chunk 跨两次 run)")
            lines.append(
                f"  样本 {noise.pairs} 对 | 中位差 {noise.median_abs_delta:.6f} | 最大差 {noise.max_abs_delta:.6f}"
            )
            lines.append("  提示: 小于该量级的指标差异不应解读为提升/退化(外部 embedding 服务非确定性)")

        lines.append("")
        if self.consistent:
            lines.append("结论: 一致 ✅ (退出码 0)")
        else:
            lines.append(
                f"结论: 不一致 ❌ (退出码 1) — run 指标差异 {len(self.run_metric_diffs)} 项, "
                f"单题差异 {len(self.case_diffs)} 题"
            )
        return "\n".join(lines)


# ---- 纯比较逻辑 ----


def compare_runs(left: RunView, right: RunView, *, tolerance: float = 1e-9, strict: bool = False) -> CompareReport:
    """逐字段比较两次 run; 不做任何 IO, 因此可以完全离线单测。"""
    report = CompareReport(left=left, right=right, tolerance=tolerance, strict=strict)

    if left.config_hash != right.config_hash:
        report.reasons.append(
            f"配置指纹不同({_short(left.config_hash)} vs {_short(right.config_hash)}), "
            "属于 A/B 对比场景, 请用 M5 的报告工具而非一致性校验"
        )
    if (left.dataset_id, left.corpus_id) != (right.dataset_id, right.corpus_id):
        report.reasons.append(
            f"数据集/语料不同({left.dataset_id}/{left.corpus_id} vs {right.dataset_id}/{right.corpus_id})"
        )
    if report.reasons:
        return report

    for status in (left.status, right.status):
        if status != "succeeded":
            report.reasons.append(f"run 状态不是 succeeded({left.status} vs {right.status}), 无法验收一致性")
            return report

    for key in sorted((set(left.metrics) | set(right.metrics)) - set(RUN_META_KEYS)):
        lv, rv = left.metrics.get(key), right.metrics.get(key)
        if not _values_equal(lv, rv, tolerance):
            report.run_metric_diffs.append(MetricDiff(scope="run", metric=key, left=lv, right=rv))

    report.only_in_left = sorted(set(left.cases) - set(right.cases))
    report.only_in_right = sorted(set(right.cases) - set(left.cases))
    report.score_noise = _score_noise(left, right)

    for qid in sorted(set(left.cases) & set(right.cases)):
        lc, rc = left.cases[qid], right.cases[qid]
        verdict = _compare_case_metrics(lc, rc, tolerance)
        if verdict.reasons:
            report.case_diffs.append(verdict)
        sequence = _compare_case_sequence(lc, rc)
        if sequence.reasons:
            report.sequence_diffs.append(sequence)
    return report


def _compare_case_metrics(lc: CaseView, rc: CaseView, tolerance: float) -> CaseDiff:
    """判定口径: 指标 + 标签。"""
    reasons: list[str] = []
    for key in sorted((set(lc.metrics) | set(rc.metrics)) - set(CASE_IGNORED_KEYS)):
        lv, rv = lc.metrics.get(key), rc.metrics.get(key)
        if not _values_equal(lv, rv, tolerance):
            reasons.append(f"{key} {_fmt(lv)} != {_fmt(rv)}")
    if lc.flags != rc.flags:
        reasons.append(f"标签不同({sorted(lc.flags)} != {sorted(rc.flags)})")
    return CaseDiff(qid=lc.qid, reasons=tuple(reasons))


def _compare_case_sequence(lc: CaseView, rc: CaseView) -> CaseDiff:
    """参考口径: top-k 命中顺序(受外部 embedding 抖动影响, 默认不计入判定)。"""
    if lc.retrieved_ids == rc.retrieved_ids:
        return CaseDiff(qid=lc.qid, reasons=())
    return CaseDiff(qid=lc.qid, reasons=(f"命中序列不同({_first_divergence(lc.retrieved_ids, rc.retrieved_ids)})",))


def _score_noise(left: RunView, right: RunView) -> ScoreNoise:
    """同一 (qid, point_id) 在两次 run 中的分数差 —— 用实测噪声底约束"多小的差异不算差异"。"""
    deltas: list[float] = []
    for qid in set(left.cases) & set(right.cases):
        ls = left.cases[qid].retrieved_scores
        rs = right.cases[qid].retrieved_scores
        for pid in set(ls) & set(rs):
            deltas.append(abs(ls[pid] - rs[pid]))
    if not deltas:
        return ScoreNoise()
    return ScoreNoise(
        pairs=len(deltas),
        max_abs_delta=max(deltas),
        median_abs_delta=statistics.median(deltas),
    )


def _first_divergence(left: tuple[str, ...], right: tuple[str, ...]) -> str:
    for index, (a, b) in enumerate(zip(left, right), start=1):
        if a != b:
            return f"第 {index} 位 {a[:8]}… -> {b[:8]}…"
    return f"长度不同 {len(left)} -> {len(right)}"


def _values_equal(lv: Any, rv: Any, tolerance: float) -> bool:
    if isinstance(lv, bool) or isinstance(rv, bool):
        return lv is rv
    if isinstance(lv, (int, float)) and isinstance(rv, (int, float)):
        return abs(float(lv) - float(rv)) <= tolerance
    return lv == rv


def _fmt(value: Any) -> str:
    if value is None:
        return "-"
    if isinstance(value, float):
        return f"{value:.6f}".rstrip("0").rstrip(".") if value else "0"
    return str(value)


def _fmt_delta(lv: Any, rv: Any) -> str:
    if isinstance(lv, (int, float)) and isinstance(rv, (int, float)) and not isinstance(lv, bool):
        return f"Δ{float(rv) - float(lv):+.6f}".rstrip("0").rstrip(".")
    return ""


def _short(value: str | None) -> str:
    return (value or "-")[:8]


# ---- IO ----


def load_run_view(dsn: str, run_id: int) -> RunView:
    """从数据库装载一次 run 的可比对视图。"""
    run = get_run(dsn, run_id)
    if run is None:
        raise LookupError(f"run {run_id} 不存在")
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT c.qid, cr.case_id, cr.metrics::text, cr.retrieved::text, cr.flags::text
            FROM case_results cr
            JOIN cases c ON c.id = cr.case_id
            WHERE cr.run_id = %s
            ORDER BY c.qid
            """,
            (run_id,),
        )
        rows = cur.fetchall()

    cases: dict[str, CaseView] = {}
    for qid, case_id, metrics_raw, retrieved_raw, flags_raw in rows:
        metrics = json.loads(metrics_raw) if metrics_raw else {}
        retrieved = json.loads(retrieved_raw) if retrieved_raw else []
        flags = json.loads(flags_raw) if flags_raw else []
        cases[qid] = CaseView(
            qid=qid,
            case_id=case_id,
            metrics={k: v for k, v in metrics.items() if k not in CASE_IGNORED_KEYS},
            retrieved_ids=tuple(str(item.get("point_id", "")) for item in retrieved),
            retrieved_scores={str(item.get("point_id", "")): float(item.get("score") or 0.0) for item in retrieved},
            flags=tuple(sorted(flags)),
        )

    return RunView(
        run_id=run["id"],
        dataset_id=run["dataset_id"],
        corpus_id=run["corpus_id"],
        status=run["status"],
        config_hash=run["config_hash"] or "",
        git_sha=run["git_sha"] or "",
        metrics={k: v for k, v in (run["metrics"] or {}).items() if k not in RUN_META_KEYS},
        cases=cases,
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="两次 run 的一致性对比(逐题校验)")
    parser.add_argument("--left", type=int, required=True, help="基准 run id(通常是全量跑完的那次)")
    parser.add_argument("--right", type=int, required=True, help="待校验 run id(通常是中断续跑的那次)")
    parser.add_argument("--tolerance", type=float, default=1e-9, help="浮点比较容差")
    parser.add_argument(
        "--strict", action="store_true",
        help="严格模式: top-k 命中序列也必须逐位一致(受外部 embedding 抖动影响, 可能误报)",
    )
    parser.add_argument("--json", action="store_true", help="输出 JSON(供脚本消费)")
    parser.add_argument("--max-cases", type=int, default=20, help="文本输出最多列多少条差异题")
    args = parser.parse_args(argv)

    dsn = Settings().pg_dsn
    try:
        left = load_run_view(dsn, args.left)
        right = load_run_view(dsn, args.right)
    except LookupError as exc:
        print(f"❌ {exc}", file=sys.stderr)
        return EXIT_NOT_COMPARABLE

    report = compare_runs(left, right, tolerance=args.tolerance, strict=args.strict)
    if args.json:
        print(json.dumps(report.to_json(), ensure_ascii=False, indent=2))
    else:
        print(report.render(max_cases=args.max_cases))
    return report.exit_code


if __name__ == "__main__":
    sys.exit(main())
