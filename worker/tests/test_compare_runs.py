"""compare_runs 离线单测: 只测纯比较逻辑(不碰 DB/网络)。

为什么这些用例重要:
- 这个工具是"续跑结果必须与全量跑一致"的**判定依据**, 它自己必须被判定过;
- 最容易出错的不是"发现差异", 而是"把不该算差异的算成差异"(抖动字段 -> 假警报)
  与"把该算差异的漏掉"(指标变了却放行 -> 假通过);
- 另一个易错点是**口径分层**: 外部 embedding 的分数抖动会让近分候选换位, 这属于"参考口径",
  不能让它把可靠性验收判成失败(实测依据见 docs/fault-drills.md)。
"""
from __future__ import annotations

from typing import Any

import pytest

from app.cli.compare_runs import (
    EXIT_CONSISTENT,
    EXIT_INCONSISTENT,
    EXIT_NOT_COMPARABLE,
    CaseView,
    RunView,
    compare_runs,
)


def case(
    qid: str,
    *,
    recall: float = 1.0,
    mrr: float = 1.0,
    gold: int = 1,
    hits: int = 1,
    latency: float = 120.0,
    retrieved: tuple[str, ...] = ("p1", "p2", "p3"),
    scores: dict[str, float] | None = None,
    flags: tuple[str, ...] = (),
) -> CaseView:
    if scores is None:
        scores = {pid: 0.9 - 0.1 * index for index, pid in enumerate(retrieved)}
    return CaseView(
        qid=qid,
        case_id=abs(hash(qid)) % 10_000,
        metrics={
            "recall": recall,
            "reciprocal_rank": mrr,
            "gold_count": gold,
            "hits": hits,
            "latency_ms": latency,
        },
        retrieved_ids=retrieved,
        retrieved_scores=scores,
        flags=flags,
    )


def run(
    run_id: int,
    cases: list[CaseView],
    *,
    config_hash: str = "cfg-a",
    dataset_id: int = 5,
    corpus_id: int = 4,
    status: str = "succeeded",
    git_sha: str = "1c52f74",
    metrics: dict[str, Any] | None = None,
) -> RunView:
    if metrics is None:
        metrics = {"cases_total": len(cases), "cases_evaluated": len(cases), "recall_at_k": 0.948611}
    return RunView(
        run_id=run_id,
        dataset_id=dataset_id,
        corpus_id=corpus_id,
        status=status,
        config_hash=config_hash,
        git_sha=git_sha,
        metrics=metrics,
        cases={c.qid: c for c in cases},
    )


def test_identical_runs_are_consistent() -> None:
    cases = [case("zjc-001"), case("zjc-002", recall=0.5, mrr=1.0, gold=2, hits=1)]
    report = compare_runs(run(100, cases), run(101, cases))

    assert report.consistent
    assert report.exit_code == EXIT_CONSISTENT
    assert report.case_diffs == []
    assert report.sequence_diffs == []


def test_latency_and_git_sha_differences_are_ignored() -> None:
    # 抖动项(耗时)与元信息(代码版本)都不应造成"不一致"
    left = run(100, [case("zjc-001", latency=80.0)], git_sha="aaaaaaa")
    right = run(101, [case("zjc-001", latency=999.0)], git_sha="bbbbbbb")

    report = compare_runs(left, right)

    assert report.consistent, report.render()
    assert report.exit_code == EXIT_CONSISTENT


def test_metric_difference_is_reported_with_qid() -> None:
    left = run(100, [case("zjc-001"), case("zjc-031", recall=1.0, gold=2, hits=2)])
    right = run(101, [case("zjc-001"), case("zjc-031", recall=0.5, gold=2, hits=1)])

    report = compare_runs(left, right)

    assert not report.consistent
    assert report.exit_code == EXIT_INCONSISTENT
    assert [d.qid for d in report.case_diffs] == ["zjc-031"]
    assert any("recall" in reason for reason in report.case_diffs[0].reasons)


def test_run_level_metric_difference_is_reported() -> None:
    cases = [case("zjc-001")]
    left = run(100, cases, metrics={"recall_at_k": 0.948611, "hit_at_k": 1.0})
    right = run(101, cases, metrics={"recall_at_k": 0.913889, "hit_at_k": 1.0})

    report = compare_runs(left, right)

    assert not report.consistent
    assert [d.metric for d in report.run_metric_diffs] == ["recall_at_k"]


def test_sequence_only_difference_passes_by_default_but_fails_in_strict_mode() -> None:
    # 实测场景: zjc-027 的 top1/top2 只差 8e-6, 会因外部 embedding 抖动换位, 但 gold 位次不变
    left = run(100, [case("zjc-027", retrieved=("pa", "pb", "pc"))])
    right = run(101, [case("zjc-027", retrieved=("pb", "pa", "pc"))])

    standard = compare_runs(left, right)
    strict = compare_runs(left, right, strict=True)

    assert standard.consistent, standard.render()
    assert standard.exit_code == EXIT_CONSISTENT
    assert [d.qid for d in standard.sequence_diffs] == ["zjc-027"]
    assert not strict.consistent
    assert strict.exit_code == EXIT_INCONSISTENT


def test_retrieved_shorter_list_is_detected_in_sequence_section() -> None:
    left = run(100, [case("zjc-001", retrieved=("pa", "pb", "pc", "pd", "pe"))])
    right = run(101, [case("zjc-001", retrieved=("pa", "pb", "pc"))])

    report = compare_runs(left, right)

    assert [d.qid for d in report.sequence_diffs] == ["zjc-001"]
    assert "长度不同" in report.sequence_diffs[0].reasons[0]
    assert report.consistent  # 指标未变, 标准口径下仍算一致


def test_flag_difference_is_detected() -> None:
    left = run(100, [case("zjc-031", flags=("retrieval_miss",))])
    right = run(101, [case("zjc-031", flags=())])

    report = compare_runs(left, right)

    assert not report.consistent
    assert "标签不同" in report.case_diffs[0].reasons[0]


def test_missing_case_on_one_side_is_detected() -> None:
    left = run(100, [case("zjc-001"), case("zjc-002")])
    right = run(101, [case("zjc-001")])

    report = compare_runs(left, right)

    assert not report.consistent
    assert report.only_in_left == ["zjc-002"]
    assert report.only_in_right == []
    assert report.exit_code == EXIT_INCONSISTENT


def test_float_tolerance_avoids_false_alarm() -> None:
    left = run(100, [case("zjc-001", recall=0.9138889999)])
    right = run(101, [case("zjc-001", recall=0.913889)])

    assert compare_runs(left, right).consistent
    assert compare_runs(left, right, tolerance=1e-12).exit_code == EXIT_INCONSISTENT


def test_score_noise_is_measured() -> None:
    # 同一 chunk 的分数跨 run 有 1e-3 量级抖动(实测), 工具要把它量出来
    left = run(100, [case("zjc-027", retrieved=("pa", "pb"), scores={"pa": 0.590321, "pb": 0.590188})])
    right = run(101, [case("zjc-027", retrieved=("pa", "pb"), scores={"pa": 0.590495, "pb": 0.590241})])

    report = compare_runs(left, right)

    assert report.score_noise.pairs == 2
    assert report.score_noise.max_abs_delta == pytest.approx(0.000174)
    assert report.consistent
    assert "分数噪声" in report.render()


def test_score_noise_ignores_chunks_missing_on_one_side() -> None:
    left = run(100, [case("zjc-001", retrieved=("pa", "pb"), scores={"pa": 0.5, "pb": 0.4})])
    right = run(101, [case("zjc-001", retrieved=("pb", "pc"), scores={"pb": 0.4, "pc": 0.3})])

    assert compare_runs(left, right).score_noise.pairs == 1


def test_different_config_hash_is_not_comparable() -> None:
    cases = [case("zjc-001")]
    report = compare_runs(run(100, cases, config_hash="cfg-a"), run(101, cases, config_hash="cfg-b"))

    assert not report.comparable
    assert report.exit_code == EXIT_NOT_COMPARABLE
    assert "配置指纹不同" in report.reasons[0]


def test_different_dataset_is_not_comparable() -> None:
    cases = [case("zjc-001")]
    report = compare_runs(run(100, cases, dataset_id=3), run(101, cases, dataset_id=5))

    assert report.exit_code == EXIT_NOT_COMPARABLE
    assert "数据集/语料不同" in report.reasons[0]


def test_unfinished_run_is_not_comparable() -> None:
    cases = [case("zjc-001")]
    report = compare_runs(run(100, cases), run(101, cases, status="running"))

    assert report.exit_code == EXIT_NOT_COMPARABLE
    assert "succeeded" in report.reasons[0]


def test_render_mentions_counts_and_conclusion() -> None:
    cases = [case("zjc-001")]
    text = compare_runs(run(100, cases), run(101, cases)).render()

    assert "run 100" in text and "run 101" in text
    assert "1 题逐题一致" in text
    assert "退出码 0" in text


def test_to_json_is_serializable() -> None:
    import json

    cases = [case("zjc-001"), case("zjc-002")]
    payload = json.loads(json.dumps(compare_runs(run(100, cases), run(101, cases)).to_json()))

    assert payload["consistent"] is True
    assert payload["exit_code"] == EXIT_CONSISTENT
    assert payload["cases"] == {"left": 2, "right": 2}
    assert payload["score_noise"]["pairs"] == 6
