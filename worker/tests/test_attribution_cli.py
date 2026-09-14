"""归因重算 CLI 单测(离线, 不连 DB)。

覆盖四条最容易出事的地方:
1. **计划构建**: 标签、分布、差异明细是否与实际信号一致(含 judge 信号参与);
2. **阈值落地**: k 取快照、达标线取命令行, 两者都要进 runs.metrics.attribution(D15);
3. **dry-run 默认不写库**: 打标口径直接影响报告结论, 顺手改库是最危险的默认值;
4. **幂等**: 同一批数据重算两次, 第二次差异必须为 0(否则 CLI 不可重复执行)。
"""
from __future__ import annotations

import json
from typing import Any

import pytest

from app.cli import attribution as attr_cli
from app.eval.attribution import (
    ATTRIBUTION_VERSION,
    FLAG_GENERATION_QUALITY,
    FLAG_HALLUCINATION,
    FLAG_RETRIEVAL_LOW_RANK,
    FLAG_RETRIEVAL_MISS,
    FLAG_RETRIEVAL_PARTIAL,
    Thresholds,
)

SNAPSHOT: dict[str, Any] = {
    "chunking": {"strategy": "headings", "chunk_size": 500, "overlap": 50, "min_chars": 80},
    "retrieval": {"top_k": 5},
}


def run_row(**over: Any) -> dict[str, Any]:
    base: dict[str, Any] = {
        "id": 5,
        "config_snapshot": dict(SNAPSHOT),
        "config_hash": "locked-hash",
    }
    base.update(over)
    return base


def metric(**over: Any) -> dict[str, Any]:
    base: dict[str, Any] = {
        "qid": "zjc-001", "gold_count": 1, "retrieved_count": 5, "hits": 1, "recall": 1.0,
        "precision": 0.2, "reciprocal_rank": 1.0, "hit": 1.0, "first_hit_rank": 1,
    }
    base.update(over)
    return base


def claim(label: str) -> dict[str, Any]:
    return {"id": 1, "text": "断言", "label": label, "evidence": "证据", "reason": "理由"}


def judge(*labels: str, rubric: tuple[int, int] | None = (5, 5)) -> dict[str, Any]:
    return {
        "claims": [claim(label) for label in labels],
        "rubric": None if rubric is None else {"relevance": rubric[0], "helpfulness": rubric[1]},
        "meta": {"rubric_enabled": rubric is not None},
    }


def case_row(
        case_id: int,
        qid: str,
        *,
        metrics: dict[str, Any] | None = None,
        judge_payload: dict[str, Any] | None = None,
        flags: tuple[str, ...] = (),
) -> dict[str, Any]:
    return {
        "case_id": case_id,
        "qid": qid,
        "metrics": metric() if metrics is None else metrics,
        "judge": {} if judge_payload is None else judge_payload,
        "flags": list(flags),
    }


@pytest.fixture()
def cli(monkeypatch: pytest.MonkeyPatch):
    """屏蔽 Settings/.env 与日志初始化, 让 main() 可以在离线环境跑。"""

    class StubSettings:
        pg_dsn = "postgresql://unused/unused"
        log_level = "INFO"

    monkeypatch.setattr(attr_cli, "Settings", lambda: StubSettings())
    monkeypatch.setattr(attr_cli, "setup_logging", lambda level: None)
    return attr_cli


# ---- 计划构建 ----


def test_plan_computes_flags_and_changes() -> None:
    rows = [
        case_row(1, "q-1"),                                              # 干净
        case_row(2, "q-2", metrics=metric(hits=0, recall=0.0, hit=0.0, first_hit_rank=None)),
        case_row(3, "q-3", metrics=metric(gold_count=2, hits=1, recall=0.5)),
        case_row(4, "q-4", metrics=metric(first_hit_rank=5)),
        case_row(5, "q-5", judge_payload=judge("unsupported")),
    ]

    plan = attr_cli.build_plan(run_id=5, run=run_row(), rows=rows)

    by_qid = {case.qid: case for case in plan.cases}
    assert by_qid["q-1"].new_flags == ()
    assert by_qid["q-2"].new_flags == (FLAG_RETRIEVAL_MISS,)
    assert by_qid["q-3"].new_flags == (FLAG_RETRIEVAL_PARTIAL,)
    assert by_qid["q-4"].new_flags == (FLAG_RETRIEVAL_LOW_RANK,)
    assert by_qid["q-5"].new_flags == (FLAG_HALLUCINATION,)
    assert plan.flagged_cases == 4
    assert plan.new_counts()[FLAG_RETRIEVAL_MISS] == 1
    assert [case.qid for case in plan.changes] == ["q-2", "q-3", "q-4", "q-5"]


def test_plan_is_idempotent_when_flags_already_match() -> None:
    rows = [
        case_row(1, "q-1", metrics=metric(hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
                 flags=(FLAG_RETRIEVAL_MISS,)),
        case_row(2, "q-2"),
    ]

    plan = attr_cli.build_plan(run_id=5, run=run_row(), rows=rows)

    assert plan.changes == ()
    assert plan.new_counts() == {FLAG_RETRIEVAL_MISS: 1}


def test_plan_treats_reordered_flags_as_unchanged() -> None:
    """旧数据的标签顺序没有语义: 只排序不同不算差异(但仍会被规范化写回)。"""
    rows = [case_row(1, "q-1", metrics=metric(first_hit_rank=5, gold_count=2, hits=1, recall=0.5),
                     flags=(FLAG_RETRIEVAL_LOW_RANK, FLAG_RETRIEVAL_PARTIAL))]

    plan = attr_cli.build_plan(run_id=5, run=run_row(), rows=rows)

    assert plan.changes == ()
    assert plan.cases[0].new_flags == (FLAG_RETRIEVAL_PARTIAL, FLAG_RETRIEVAL_LOW_RANK)


def test_plan_scope_depends_on_snapshot_judge_section() -> None:
    rows = [case_row(1, "q-1")]

    without = attr_cli.build_plan(run_id=5, run=run_row(), rows=rows)
    with_judge = attr_cli.build_plan(
        run_id=5,
        run=run_row(config_snapshot={**SNAPSHOT, "judge": {"model": "m", "claims_prompt_id": "p"}}),
        rows=rows,
    )

    assert without.meta()["scope"] == "retrieval"
    assert with_judge.meta()["scope"] == "retrieval+judge"


def test_top_k_comes_from_snapshot_else_from_retrieved_count() -> None:
    rows = [case_row(1, "q-1", metrics=metric(retrieved_count=10))]

    snapshot_k = attr_cli.build_plan(
        run_id=5, run=run_row(config_snapshot={**SNAPSHOT, "retrieval": {"top_k": 3}}), rows=rows,
    )
    fallback_k = attr_cli.build_plan(
        run_id=5, run=run_row(config_snapshot={"chunking": {}}), rows=rows,
    )

    assert snapshot_k.thresholds.k == 3
    assert fallback_k.thresholds.k == 10, "快照缺 top_k 时回退到单题 retrieved_count"


def test_quality_line_override_reaches_thresholds_and_meta() -> None:
    rows = [case_row(1, "q-1", judge_payload=judge("supported", rubric=(5, 4)))]

    run_with_judge = run_row(
        config_snapshot={**SNAPSHOT, "judge": {"model": "m", "claims_prompt_id": "p"}},
    )
    default_plan = attr_cli.build_plan(run_id=5, run=run_with_judge, rows=rows)
    strict_plan = attr_cli.build_plan(
        run_id=5, run=run_with_judge, rows=rows, thresholds=Thresholds(k=5, quality_line=4),
    )

    assert default_plan.cases[0].new_flags == ()
    assert strict_plan.cases[0].new_flags == (FLAG_GENERATION_QUALITY,)
    assert strict_plan.meta()["quality_line"] == 4
    assert strict_plan.meta() == {
        "version": ATTRIBUTION_VERSION,
        "scope": "retrieval+judge",
        "k": 5,
        "low_rank_limit": 3,
        "low_rank_ratio": 0.5,
        "quality_line": 4,
    }


# ---- main(): dry-run / --apply ----


def test_dry_run_does_not_write(cli, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]):
    rows = [case_row(1, "q-1", metrics=metric(hits=0, recall=0.0, hit=0.0, first_hit_rank=None))]
    monkeypatch.setattr(cli, "load_run_signals", lambda dsn, run_id: (run_row(), rows))
    writes: list[Any] = []
    monkeypatch.setattr(cli, "apply_plan", lambda dsn, plan: writes.append(plan))

    code = cli.main(["--run-id", "5"])

    out = capsys.readouterr().out
    assert code == 0
    assert writes == [], "默认必须只读: 没有 --apply 就不准写库"
    assert "dry-run" in out


def test_apply_writes_flags_and_reports_counts(cli, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]):
    rows = [
        case_row(1, "q-1", metrics=metric(hits=0, recall=0.0, hit=0.0, first_hit_rank=None)),
        case_row(2, "q-2"),
    ]
    monkeypatch.setattr(cli, "load_run_signals", lambda dsn, run_id: (run_row(), rows))
    captured: list[attr_cli.AttributionPlan] = []

    def fake_apply(dsn: str, plan: attr_cli.AttributionPlan) -> tuple[int, int]:
        captured.append(plan)
        return len(plan.cases), 1

    monkeypatch.setattr(cli, "apply_plan", fake_apply)

    code = cli.main(["--run-id", "5", "--apply"])

    out = capsys.readouterr().out
    assert code == 0
    assert len(captured) == 1
    assert captured[0].run_id == 5
    assert "已写库" in out and "更新 2 条标签" in out


def test_json_output_is_machine_readable(cli, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]):
    rows = [case_row(1, "q-1", metrics=metric(hits=0, recall=0.0, hit=0.0, first_hit_rank=None))]

    class Recorder:
        calls = 0

        @staticmethod
        def apply(dsn: str, plan: attr_cli.AttributionPlan) -> tuple[int, int]:
            Recorder.calls += 1
            return 1, 1

    monkeypatch.setattr(cli, "load_run_signals", lambda dsn, run_id: (run_row(), rows))
    monkeypatch.setattr(cli, "apply_plan", Recorder.apply)

    code = cli.main(["--run-id", "5", "--apply", "--json"])

    payload = json.loads(capsys.readouterr().out)
    assert code == 0
    assert payload["run_id"] == 5
    assert payload["flag_counts"] == {FLAG_RETRIEVAL_MISS: 1}
    assert payload["attribution"]["version"] == ATTRIBUTION_VERSION
    assert payload["attribution"]["k"] == 5
    assert payload["changed"] == 1
    assert payload["applied"] == {"flags_updated": 1, "attribution_written": 1}
    assert Recorder.calls == 1


def test_json_dry_run_reports_applied_null(cli, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]):
    monkeypatch.setattr(cli, "load_run_signals", lambda dsn, run_id: (run_row(), [case_row(1, "q-1")]))
    monkeypatch.setattr(cli, "apply_plan", lambda dsn, plan: (0, 0))

    cli.main(["--run-id", "5", "--json"])

    assert json.loads(capsys.readouterr().out)["applied"] is None


def test_missing_run_fails_with_message(cli, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]):
    def boom(dsn: str, run_id: int) -> Any:
        raise LookupError(f"run {run_id} 不存在")

    monkeypatch.setattr(cli, "load_run_signals", boom)

    code = cli.main(["--run-id", "999"])

    assert code == 1
    assert "不存在" in capsys.readouterr().out


def test_run_without_case_results_fails(cli, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]):
    monkeypatch.setattr(cli, "load_run_signals", lambda dsn, run_id: (run_row(), []))

    code = cli.main(["--run-id", "5"])

    assert code == 1
    assert "没有任何单题结果" in capsys.readouterr().out


def test_text_report_lists_changes_with_chinese_labels(cli) -> None:
    rows = [case_row(1, "zjc-027", metrics=metric(first_hit_rank=5), flags=())]
    plan = attr_cli.build_plan(run_id=5, run=run_row(), rows=rows)

    text = attr_cli.render_text(plan, applied=None, list_changes=20)

    assert "run 5" in text and "k=5" in text and "quality_line=3" in text
    assert FLAG_RETRIEVAL_LOW_RANK in text
    assert "命中但排序靠后" in text, "报告要给中文解释, 不只是一串英文标签"
    assert "zjc-027: 无 -> retrieval_low_rank" in text


def test_cli_requires_run_id() -> None:
    with pytest.raises(SystemExit):
        attr_cli.build_parser().parse_args([])


def test_invalid_quality_line_is_rejected() -> None:
    with pytest.raises(SystemExit):
        attr_cli.build_parser().parse_args(["--run-id", "5", "--quality-line", "9"])