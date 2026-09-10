"""检索指标单测(手算核对): D10 口径的四个指标 + 聚合 + 空 gold 跳过。"""
from __future__ import annotations

from app.metrics.retrieval import CaseMetric, aggregate, evaluate_case


def test_recall_precision_mrr_hit_hand_computed():
    # 手算: G={a,b}, Rk=[x,a,y,b,z], k=5
    #   命中 2 个 -> recall = 2/2 = 1.0; precision = 2/5 = 0.4
    #   首个命中在第 2 位 -> RR = 1/2 = 0.5; hit = 1
    m = evaluate_case("q1", {"a", "b"}, ["x", "a", "y", "b", "z"], k=5)
    assert m is not None
    assert m.hits == 2
    assert m.recall == 1.0
    assert m.precision == 0.4
    assert m.reciprocal_rank == 0.5
    assert m.first_hit_rank == 2
    assert m.hit == 1.0


def test_no_hit_gives_zero_metrics():
    m = evaluate_case("q2", {"a"}, ["b", "c"], k=2)
    assert m is not None
    assert (m.hits, m.recall, m.precision, m.reciprocal_rank, m.hit) == (0, 0.0, 0.0, 0.0, 0.0)
    assert m.first_hit_rank is None


def test_partial_recall():
    # G={a,b,c}, Rk=[a,x,x,x,x], k=5 -> recall 1/3, precision 1/5, RR 1.0
    m = evaluate_case("q3", {"a", "b", "c"}, ["a", "x", "x", "x", "x"], k=5)
    assert m is not None
    assert abs(m.recall - 1 / 3) < 1e-9
    assert m.precision == 0.2
    assert m.reciprocal_rank == 1.0


def test_only_top_k_considered():
    # gold 在第 6 位, k=5 -> 未命中
    m = evaluate_case("q4", {"a"}, ["1", "2", "3", "4", "5", "a"], k=5)
    assert m is not None
    assert m.hits == 0 and m.hit == 0.0


def test_empty_gold_is_skipped():
    assert evaluate_case("q5", set(), ["a"], k=5) is None


def test_aggregate_averages_and_counts():
    m1 = evaluate_case("q1", {"a"}, ["a"], k=1)  # recall 1.0, rr 1.0
    m2 = evaluate_case("q2", {"b"}, ["x"], k=1)  # recall 0.0, rr 0.0
    assert m1 and m2
    agg = aggregate([m1, m2], k=1, skipped=1)

    assert agg["cases_total"] == 3
    assert agg["cases_evaluated"] == 2
    assert agg["cases_skipped_no_gold"] == 1
    assert agg["recall_at_k"] == 0.5
    assert agg["hit_at_k"] == 0.5
    assert agg["mrr_at_k"] == 0.5
    assert agg["precision_at_k"] == 0.5


def test_aggregate_with_no_evaluable_cases():
    agg = aggregate([], k=5, skipped=3)
    assert agg["cases_evaluated"] == 0
    assert agg["cases_total"] == 3
    assert agg["recall_at_k"] == 0.0


def test_case_metric_json_roundtrip():
    m = evaluate_case("q1", {"a"}, ["a"], k=3)
    assert m is not None
    data = m.to_json()
    assert data["qid"] == "q1" and data["gold_count"] == 1 and data["retrieved_count"] == 1


def _dummy() -> CaseMetric:  # 类型使用示例, 保证 dataclass 可比较
    m = evaluate_case("q", {"a"}, ["a"], k=1)
    assert m is not None
    return m


def test_dummy_metric_equality():
    assert _dummy() == _dummy()