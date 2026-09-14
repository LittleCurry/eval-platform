"""归因规则内核单测(离线, 不调 LLM)。

覆盖卡片 §7 的 13 条 + 两条真实数据锚点(run #155 的 zjc-027 / zjc-031),
以及最容易写错的三种"降级": 无判定 / 无 rubric / 空 dict judge。
"""
from __future__ import annotations

from typing import Any

import pytest

from app.eval.attribution import (
    ATTRIBUTION_VERSION,
    FLAG_ANCHOR_INCOMPLETE,
    FLAG_GENERATION_QUALITY,
    FLAG_HALLUCINATION,
    FLAG_NO_CLAIMS,
    FLAG_NO_GOLD,
    FLAG_OFF_TOPIC,
    FLAG_PRIORITY,
    FLAG_RETRIEVAL_LOW_RANK,
    FLAG_RETRIEVAL_MISS,
    FLAG_RETRIEVAL_PARTIAL,
    SCOPE_RETRIEVAL,
    SCOPE_RETRIEVAL_JUDGE,
    Thresholds,
    attribute_case,
    attribution_meta,
    is_grounded,
    scope_for,
    sort_flags,
    summarize_flags,
)


def metric(**over: Any) -> dict[str, Any]:
    """默认: 单 gold 命中且排第一(k=5 下的"干净"检索结果)。"""
    base: dict[str, Any] = {
        "qid": "zjc-001",
        "gold_count": 1,
        "retrieved_count": 5,
        "hits": 1,
        "recall": 1.0,
        "precision": 0.2,
        "reciprocal_rank": 1.0,
        "hit": 1.0,
        "first_hit_rank": 1,
    }
    base.update(over)
    return base


def claim(label: str) -> dict[str, Any]:
    return {"id": 1, "text": "断言", "label": label, "evidence": "证据", "reason": "理由"}


def judge(*labels: str, rubric: tuple[int, int] | None = (5, 5)) -> dict[str, Any]:
    """rubric=(relevance, helpfulness); rubric=None 表示本次没打分。"""
    return {
        "claims": [claim(label) for label in labels],
        "rubric": None if rubric is None else {"relevance": rubric[0], "helpfulness": rubric[1]},
        "meta": {"rubric_enabled": rubric is not None},
    }


def thresholds(k: int = 5, **over: Any) -> Thresholds:
    return Thresholds(k=k, **over)


# ---------- 干净 / 优先级 ----------

def test_clean_case_has_no_flags() -> None:
    assert attribute_case(metric=metric(), judge=judge("supported"),
                          thresholds=thresholds()) == []


def test_flags_are_sorted_upstream_first() -> None:
    # 检索没召回到 + 答案在编: 主因必须是检索
    flags = attribute_case(
        metric=metric(hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
        judge=judge("unsupported"),
        thresholds=thresholds(),
    )
    assert flags == [FLAG_RETRIEVAL_MISS, FLAG_HALLUCINATION]
    assert flags[0] == FLAG_RETRIEVAL_MISS


# 原 test_hallucination_outranks_off_topic_and_quality 改名并改预期:
# 有幻觉时不再叠加质量标签 —— 否则一个坏答案同时被计成"幻觉"和"质量不达标", 分布会双计
def test_hallucination_outranks_off_topic_and_blocks_quality() -> None:
    flags = attribute_case(
        metric=metric(),
        judge=judge("unsupported", "irrelevant", rubric=(2, 2)),
        thresholds=thresholds(),
    )
    assert flags == [FLAG_HALLUCINATION, FLAG_OFF_TOPIC]


def test_miss_never_also_partial() -> None:
    # judge=None: 只验检索侧互斥, 否则 unsupported 会再带出 hallucination 干扰断言
    flags = attribute_case(
        metric=metric(gold_count=3, hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
        judge=None,
        thresholds=thresholds(),
    )
    assert flags == [FLAG_RETRIEVAL_MISS]
    assert FLAG_RETRIEVAL_PARTIAL not in flags


def test_quality_is_not_blamed_when_retrieval_missed() -> None:
    # 检索没召回到 + 打分很低: 责任归检索, 不叠加质量标签
    flags = attribute_case(
        metric=metric(gold_count=1, hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
        judge=judge("unsupported", rubric=(2, 2)),
        thresholds=thresholds(),
    )
    assert FLAG_GENERATION_QUALITY not in flags
    assert flags[0] == FLAG_RETRIEVAL_MISS


def test_anchor_incomplete_case_is_not_called_retrieval_miss() -> None:
    # 新增: 同样是 hit=0, 但答案全有据 -> 主因是锚点漏标(数据问题), 检索只是次因
    flags = attribute_case(
        metric=metric(gold_count=1, hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
        judge=judge("supported", rubric=(2, 2)),
        thresholds=thresholds(),
    )
    assert FLAG_GENERATION_QUALITY not in flags
    assert flags == [FLAG_ANCHOR_INCOMPLETE, FLAG_RETRIEVAL_MISS]


def test_off_topic_alone() -> None:
    assert attribute_case(metric=metric(), judge=judge("supported", "irrelevant"),
                          thresholds=thresholds()) == [FLAG_OFF_TOPIC]


# ---------- 检索侧阈值边界 ----------

@pytest.mark.parametrize(("k", "rank", "expected"), [
    (5, 3, False),
    (5, 4, True),
    (5, 5, True),
    (10, 5, False),
    (10, 6, True),
    (3, 2, False),
    (3, 3, True),
    (1, 1, False),   # k=1 时 low_rank_limit=1, rank>1 不可能
])
def test_low_rank_boundary(k: int, rank: int, expected: bool) -> None:
    flags = attribute_case(metric=metric(first_hit_rank=rank), judge=judge("supported"),
                           thresholds=thresholds(k=k))
    assert (FLAG_RETRIEVAL_LOW_RANK in flags) is expected


@pytest.mark.parametrize(("k", "limit"), [(1, 1), (3, 2), (5, 3), (10, 5)])
def test_low_rank_limit_formula(k: int, limit: int) -> None:
    assert thresholds(k=k).low_rank_limit() == limit


def test_partial_recall_is_integer_exact() -> None:
    # 2 个 gold 命中 1 个 -> 部分漏召回; 且与 miss 互斥
    assert attribute_case(metric=metric(gold_count=2, hits=1, recall=0.5),
                          judge=judge("supported"), thresholds=thresholds()) == [
               FLAG_RETRIEVAL_PARTIAL]


def test_partial_and_low_rank_can_coexist_with_order() -> None:
    flags = attribute_case(metric=metric(gold_count=2, hits=1, recall=0.5, first_hit_rank=5),
                           judge=judge("supported"), thresholds=thresholds())
    assert flags == [FLAG_RETRIEVAL_PARTIAL, FLAG_RETRIEVAL_LOW_RANK]


# ---------- 锚点漏标 ----------

def test_anchor_incomplete_outranks_retrieval_miss() -> None:
    flags = attribute_case(
        metric=metric(gold_count=1, hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
        judge=judge("supported"),   # 全有据 -> 证据其实在检索结果里, 是标注漏了
        thresholds=thresholds(),
    )
    assert flags == [FLAG_ANCHOR_INCOMPLETE, FLAG_RETRIEVAL_MISS]


def test_anchor_incomplete_requires_judge() -> None:
    flags = attribute_case(
        metric=metric(gold_count=1, hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
        judge=None,
        thresholds=thresholds(),
    )
    assert flags == [FLAG_RETRIEVAL_MISS]


def test_is_grounded_requires_claims() -> None:
    assert is_grounded(judge("supported")) is True
    assert is_grounded(judge()) is False            # 没有断言 -> 谈不上"有据"
    assert is_grounded(judge("unsupported")) is False
    assert is_grounded(None) is False
    assert is_grounded({}) is False


# ---------- 数据/评测缺失 ----------

def test_no_gold_is_first_and_suppresses_retrieval_flags() -> None:
    flags = attribute_case(metric={}, judge=judge("unsupported"),
                           thresholds=thresholds())
    assert flags == [FLAG_NO_GOLD, FLAG_HALLUCINATION]
    assert flags[0] == FLAG_NO_GOLD


def test_missing_metric_is_no_gold() -> None:
    assert attribute_case(metric=None, judge=None, thresholds=thresholds()) == [FLAG_NO_GOLD]


def test_judge_absent_never_produces_judge_flags() -> None:
    # 只跑检索的 run: 即使命中排位很靠后, 也只能出检索类标签
    flags = attribute_case(metric=metric(first_hit_rank=5), judge=None,
                           thresholds=thresholds())
    assert flags == [FLAG_RETRIEVAL_LOW_RANK]


def test_empty_judge_dict_is_treated_as_absent() -> None:
    # case_results.judge 在未判定的 run 里是 {} -> 不能被当成"判定通过"
    assert attribute_case(metric=metric(), judge={}, thresholds=thresholds()) == []


def test_empty_claims_with_good_rubric_is_only_a_marker() -> None:
    assert attribute_case(metric=metric(), judge=judge(rubric=(5, 5)),
                          thresholds=thresholds()) == [FLAG_NO_CLAIMS]


def test_empty_claims_with_bad_rubric_is_a_quality_problem() -> None:
    flags = attribute_case(metric=metric(), judge=judge(rubric=(4, 2)),
                           thresholds=thresholds())
    assert flags == [FLAG_GENERATION_QUALITY, FLAG_NO_CLAIMS]


# ---------- 质量标签 ----------

@pytest.mark.parametrize(("rubric", "expected"), [
    ((5, 5), False),
    ((5, 4), False),
    ((5, 3), True),
    ((3, 5), True),
    ((4, 4), False),
])
def test_quality_line_boundary(rubric: tuple[int, int], expected: bool) -> None:
    flags = attribute_case(metric=metric(), judge=judge("supported", rubric=rubric),
                           thresholds=thresholds())
    assert (FLAG_GENERATION_QUALITY in flags) is expected


def test_quality_line_is_overridable() -> None:
    signals = {"metric": metric(), "judge": judge("supported", rubric=(5, 4))}
    assert attribute_case(**signals, thresholds=thresholds()) == []
    assert attribute_case(**signals, thresholds=thresholds(quality_line=4)) == [
        FLAG_GENERATION_QUALITY]


def test_missing_rubric_is_not_a_pass() -> None:
    # 没打分 != 合格: 不出质量标签(否则"未评测"会被当成"没问题")
    assert attribute_case(metric=metric(), judge=judge("supported", rubric=None),
                          thresholds=thresholds()) == []


def test_quality_not_blamed_when_hallucinating() -> None:
    flags = attribute_case(metric=metric(), judge=judge("unsupported", rubric=(2, 2)),
                           thresholds=thresholds())
    assert flags == [FLAG_HALLUCINATION]


# ---------- 真实数据锚点(run #155, k=5) ----------

def test_run155_anchor_zjc_027_is_low_rank_only() -> None:
    # zjc-027: hit=1, recall=1.0, first_hit_rank=5, 全 supported, helpfulness=4
    flags = attribute_case(
        metric=metric(qid="zjc-027", first_hit_rank=5),
        judge=judge("supported", "supported", rubric=(5, 4)),
        thresholds=thresholds(),
    )
    assert flags == [FLAG_RETRIEVAL_LOW_RANK]


def test_run155_anchor_zjc_031_is_partial_only() -> None:
    # zjc-031: gold=2, hits=1, recall=0.5, first_hit_rank=1, 全 supported
    flags = attribute_case(
        metric=metric(qid="zjc-031", gold_count=2, hits=1, recall=0.5, precision=0.2,
                      reciprocal_rank=1.0, first_hit_rank=1),
        judge=judge("supported", "supported", "supported", rubric=(5, 5)),
        thresholds=thresholds(),
    )
    assert flags == [FLAG_RETRIEVAL_PARTIAL]


def test_run112_k1_miss_fixture() -> None:
    # run #112 (k=1, 只跑检索): 13 题 gold 全未召回 -> 只出 retrieval_miss
    flags = attribute_case(
        metric=metric(gold_count=2, hits=0, recall=0.0, hit=0.0, first_hit_rank=None),
        judge=None,
        thresholds=thresholds(k=1),
    )
    assert flags == [FLAG_RETRIEVAL_MISS]


# ---------- 排序 / 元信息 / 汇总 ----------

def test_sort_flags_unknown_last_and_stable() -> None:
    assert sort_flags(["unknown_b", FLAG_NO_CLAIMS, "unknown_a"]) == [
        FLAG_NO_CLAIMS, "unknown_b", "unknown_a"]


def test_flag_priority_covers_all_known_flags() -> None:
    assert set(FLAG_PRIORITY) == {
        FLAG_NO_GOLD, FLAG_ANCHOR_INCOMPLETE, FLAG_RETRIEVAL_MISS, FLAG_RETRIEVAL_PARTIAL,
        FLAG_RETRIEVAL_LOW_RANK, FLAG_HALLUCINATION, FLAG_OFF_TOPIC, FLAG_GENERATION_QUALITY,
        FLAG_NO_CLAIMS,
    }
    assert len(FLAG_PRIORITY) == len(set(FLAG_PRIORITY))


@pytest.mark.parametrize(("judge_enabled", "scope"), [
    (True, SCOPE_RETRIEVAL_JUDGE),
    (False, SCOPE_RETRIEVAL),
])
def test_attribution_meta_shape(judge_enabled: bool, scope: str) -> None:
    meta = attribution_meta(thresholds=thresholds(k=10, quality_line=4),
                            judge_enabled=judge_enabled)
    assert meta == {
        "version": ATTRIBUTION_VERSION,
        "scope": scope,
        "k": 10,
        "low_rank_limit": 5,
        "low_rank_ratio": 0.5,
        "quality_line": 4,
    }
    assert scope_for(judge_enabled=judge_enabled) == scope


def test_thresholds_validation() -> None:
    with pytest.raises(ValueError):
        Thresholds(k=0)
    with pytest.raises(ValueError):
        Thresholds(k=5, low_rank_ratio=0.0)
    with pytest.raises(ValueError):
        Thresholds(k=5, low_rank_ratio=1.5)
    with pytest.raises(ValueError):
        Thresholds(k=5, quality_line=0)
    with pytest.raises(ValueError):
        Thresholds(k=5, quality_line=6)


def test_summarize_flags_counts_and_order() -> None:
    summary = summarize_flags([
        [FLAG_RETRIEVAL_MISS, FLAG_HALLUCINATION],
        [FLAG_RETRIEVAL_MISS],
        [],
    ])
    assert summary == {FLAG_RETRIEVAL_MISS: 2, FLAG_HALLUCINATION: 1}


def test_attribute_case_is_pure_over_inputs() -> None:
    # 同样的输入重复调用结果一致(便于 CLI 幂等重算)
    kwargs = {"metric": metric(gold_count=2, hits=1, recall=0.5), "judge": judge("supported"),
              "thresholds": thresholds()}
    assert attribute_case(**kwargs) == attribute_case(**kwargs)