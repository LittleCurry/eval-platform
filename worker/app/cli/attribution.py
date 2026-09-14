"""CLI: 重算历史 run 的归因标签(M4-3, process.md D15/D16)。

**为什么要有这个命令**: 规则或阈值一变, 不该重跑评测 —— run #155 一次判定就烧掉 101k
judge tokens, 而标签只是对"已经落库的检索指标 + 判定结论"的重新解释。本命令只改两个地方:
`case_results.flags` 与 `runs.metrics.attribution`, **绝不碰 config_snapshot / config_hash**。

用法:
    python -m app.cli.attribution --run-id 112                          # dry-run: 打印分布与差异
    python -m app.cli.attribution --run-id 112 --apply                  # 写库
    python -m app.cli.attribution --run-id 155 --quality-line 4 --apply # 换达标线重算并记录
    python -m app.cli.attribution --run-id 155 --json                   # 机器可读输出

dry-run 是默认行为: 打标口径直接影响报告的结论, 顺手改库太危险。
"""
from __future__ import annotations

import argparse
import json
import sys
from dataclasses import dataclass
from typing import Any

from app.config import Settings
from app.eval.attribution import (
    DEFAULT_LOW_RANK_RATIO,
    DEFAULT_QUALITY_LINE,
    FLAG_ZH,
    Thresholds,
    attribute_case,
    attribution_meta,
    summarize_flags,
)
from app.logging_conf import setup_logging
from app.queue import list_case_signals, update_case_flags, update_run_attribution
from app.store import get_run

DEFAULT_LIST_CHANGES = 20


@dataclass(frozen=True)
class CasePlan:
    """单题的重算结果(旧标签 vs 新标签)。"""

    case_id: int
    qid: str
    old_flags: tuple[str, ...]
    new_flags: tuple[str, ...]

    @property
    def changed(self) -> bool:
        """标签集合是否变了(顺序不算变化: 旧数据的顺序没有语义)。"""
        return sorted(self.old_flags) != sorted(self.new_flags)

    def to_json(self) -> dict[str, Any]:
        return {
            "case_id": self.case_id,
            "qid": self.qid,
            "old_flags": list(self.old_flags),
            "new_flags": list(self.new_flags),
            "changed": self.changed,
        }


@dataclass(frozen=True)
class AttributionPlan:
    """一次重算的完整计划(纯数据, 可打印/可写库/可断言)。"""

    run_id: int
    thresholds: Thresholds
    judge_enabled: bool
    cases: tuple[CasePlan, ...]

    @property
    def changes(self) -> tuple[CasePlan, ...]:
        return tuple(case for case in self.cases if case.changed)

    @property
    def flagged_cases(self) -> int:
        return sum(1 for case in self.cases if case.new_flags)

    def new_counts(self) -> dict[str, int]:
        return summarize_flags([case.new_flags for case in self.cases])

    def old_counts(self) -> dict[str, int]:
        return summarize_flags([case.old_flags for case in self.cases])

    def meta(self) -> dict[str, Any]:
        return attribution_meta(thresholds=self.thresholds, judge_enabled=self.judge_enabled)

    def to_json(self) -> dict[str, Any]:
        return {
            "run_id": self.run_id,
            "attribution": self.meta(),
            "cases": len(self.cases),
            "flagged_cases": self.flagged_cases,
            "flag_counts": self.new_counts(),
            "old_flag_counts": self.old_counts(),
            "changed": len(self.changes),
            "changes": [case.to_json() for case in self.changes],
        }


def resolve_top_k(run: dict[str, Any], rows: list[dict[str, Any]]) -> int:
    """取本次 run 的 top-k: 优先快照, 缺失时回退到单题指标里的 retrieved_count。

    为什么必须拿到 k: 排序靠后(low_rank)与部分召回(partial)的判定都依赖 k,
    猜错 k 会让标签数量悄悄变化。
    """
    snapshot = run.get("config_snapshot") or {}
    retrieval = snapshot.get("retrieval") if isinstance(snapshot, dict) else None
    if isinstance(retrieval, dict):
        raw = retrieval.get("top_k")
        try:
            if raw is not None and int(raw) > 0:
                return int(raw)
        except (TypeError, ValueError):
            pass
    counts = [
        int(row["metrics"].get("retrieved_count") or 0)
        for row in rows
        if isinstance(row.get("metrics"), dict)
    ]
    return max(counts) if counts and max(counts) > 0 else 5


def judge_enabled_of(run: dict[str, Any]) -> bool:
    """本次 run 是否启用了判定(决定归因覆盖范围 scope)。"""
    snapshot = run.get("config_snapshot") or {}
    return isinstance(snapshot, dict) and isinstance(snapshot.get("judge"), dict)


def build_plan(
        *,
        run_id: int,
        run: dict[str, Any],
        rows: list[dict[str, Any]],
        thresholds: Thresholds | None = None,
) -> AttributionPlan:
    """把库里的单题信号重算成标签计划(纯函数, 不碰 DB)。"""
    if thresholds is None:
        thresholds = Thresholds(k=resolve_top_k(run, rows))
    cases = tuple(
        CasePlan(
            case_id=int(row["case_id"]),
            qid=str(row.get("qid") or ""),
            old_flags=tuple(str(flag) for flag in (row.get("flags") or [])),
            new_flags=tuple(attribute_case(
                metric=row.get("metrics") or {},
                judge=row.get("judge") or {},
                thresholds=thresholds,
            )),
        )
        for row in rows
    )
    return AttributionPlan(
        run_id=run_id,
        thresholds=thresholds,
        judge_enabled=judge_enabled_of(run),
        cases=cases,
    )


def load_run_signals(dsn: str, run_id: int) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    """读 run 行 + 单题信号; run 不存在时抛 LookupError。"""
    run = get_run(dsn, run_id)
    if run is None:
        raise LookupError(f"run {run_id} 不存在")
    return run, list_case_signals(dsn, run_id)


def apply_plan(dsn: str, plan: AttributionPlan) -> tuple[int, int]:
    """写回标签与归因元信息; 返回 (更新的题数, 写入的元信息条数)。"""
    updated = update_case_flags(
        dsn, plan.run_id, {case.case_id: list(case.new_flags) for case in plan.cases},
    )
    update_run_attribution(dsn, plan.run_id, plan.meta())
    return updated, 1


def _flag_label(flag: str) -> str:
    return FLAG_ZH.get(flag, flag)


def render_text(plan: AttributionPlan, *, applied: tuple[int, int] | None, list_changes: int) -> str:
    """人类可读报告(dry-run 与 --apply 共用同一份渲染)。"""
    meta = plan.meta()
    lines = [
        (
            f"run {plan.run_id} | k={meta['k']} low_rank_limit={meta['low_rank_limit']} "
            f"quality_line={meta['quality_line']} scope={meta['scope']}"
        ),
        f"题数 {len(plan.cases)} | 有标签 {plan.flagged_cases} | 差异 {len(plan.changes)}",
    ]

    counts = plan.new_counts()
    if counts:
        lines.append("新标签分布:")
        for flag, count in counts.items():
            lines.append(f"  {flag:<20} {count:>4}  ({_flag_label(flag)})")
    else:
        lines.append("新标签分布: 空(全部题目无归因标签)")

    old_counts = plan.old_counts()
    if old_counts and old_counts != counts:
        lines.append("库内原有分布:")
        for flag, count in old_counts.items():
            lines.append(f"  {flag:<20} {count:>4}  ({_flag_label(flag)})")

    if plan.changes:
        lines.append(f"差异明细(最多列 {list_changes} 条):" if list_changes else "差异明细:")
        for case in plan.changes[:list_changes] if list_changes else plan.changes:
            before = ", ".join(case.old_flags) or "无"
            after = ", ".join(case.new_flags) or "无"
            lines.append(f"  {case.qid or case.case_id}: {before} -> {after}")
        if list_changes and len(plan.changes) > list_changes:
            lines.append(f"  ... 其余 {len(plan.changes) - list_changes} 条省略")

    if applied is None:
        lines.append("(dry-run: 未写库; 需要落库请加 --apply)")
    else:
        lines.append(f"已写库: 更新 {applied[0]} 条标签, 元信息 {applied[1]} 条(runs.metrics.attribution)")
    return "\n".join(lines)


def _ratio(value: str) -> float:
    """argparse 类型校验: low_rank_ratio 必须落在 (0, 1]。"""
    try:
        parsed = float(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(f"需要 0-1 之间的小数, 收到 {value}") from exc
    if not 0.0 < parsed <= 1.0:
        raise argparse.ArgumentTypeError(f"需要落在 (0, 1], 收到 {value}")
    return parsed


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="重算历史 run 的归因标签(M4-3)")
    parser.add_argument("--run-id", type=int, required=True, help="要重算的 run id")
    parser.add_argument(
        "--apply", action="store_true",
        help="真正写库(默认 dry-run: 只打印分布与差异)",
    )
    parser.add_argument(
        "--quality-line", type=int, default=DEFAULT_QUALITY_LINE, choices=range(1, 6),
        metavar="{1..5}",
        help="helpfulness/relevance <= 该值记为生成质量不达标(1-5, 默认 3)",
    )
    parser.add_argument(
        "--low-rank-ratio", type=_ratio, default=DEFAULT_LOW_RANK_RATIO,
        help="首个命中排位 > ceil(k*ratio) 记为排序靠后(默认 0.5)",
    )
    parser.add_argument(
        "--list-changes", type=int, default=DEFAULT_LIST_CHANGES,
        help="差异明细最多列几条(0=不限, 默认 20)",
    )
    parser.add_argument("--json", action="store_true", help="输出机器可读 JSON")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    settings = Settings()
    setup_logging(settings.log_level)

    try:
        run, rows = load_run_signals(settings.pg_dsn, args.run_id)
    except LookupError as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False))
        return 1

    if not rows:
        print(json.dumps({
            "error": f"run {args.run_id} 没有任何单题结果(未执行完或被清理)",
        }, ensure_ascii=False))
        return 1

    # k 只能来自快照(决定 low_rank/partial 的口径), 其余阈值由命令行覆盖 —— 三者都进 metrics(D15)
    plan = build_plan(
        run_id=args.run_id,
        run=run,
        rows=rows,
        thresholds=Thresholds(
            k=resolve_top_k(run, rows),
            low_rank_ratio=args.low_rank_ratio,
            quality_line=args.quality_line,
        ),
    )

    applied: tuple[int, int] | None = None
    if args.apply:
        applied = apply_plan(settings.pg_dsn, plan)

    if args.json:
        payload = plan.to_json()
        payload["applied"] = None if applied is None else {
            "flags_updated": applied[0],
            "attribution_written": applied[1],
        }
        print(json.dumps(payload, ensure_ascii=False, indent=2))
    else:
        print(render_text(plan, applied=applied, list_changes=args.list_changes))
    return 0


if __name__ == "__main__":
    sys.exit(main())