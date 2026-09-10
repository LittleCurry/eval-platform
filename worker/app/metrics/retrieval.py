"""评测指标计算(自研核心, process.md D10 口径)。

检索侧口径(每题计算后取均值):
- Recall@k    = |G ∩ Rk| / |G|        (G = 该题 gold chunk 集合, Rk = 检索 top-k)
- Precision@k = |G ∩ Rk| / k          (k 为请求的 top-k, 与业界定义一致)
- MRR@k       = 1 / rank(首个命中)    (无命中记 0)
- Hit@k       = 1 若至少命中 1 个, 否则 0

G 为空的题目无法计算召回 -> 计入 skipped 而不是当作 0 分(否则会虚假拉低均值)。
"""
from __future__ import annotations

from collections.abc import Sequence
from dataclasses import asdict, dataclass


@dataclass(frozen=True)
class CaseMetric:
    qid: str
    gold_count: int
    retrieved_count: int
    hits: int
    recall: float
    precision: float
    reciprocal_rank: float
    hit: float
    first_hit_rank: int | None

    def to_json(self) -> dict[str, object]:
        return asdict(self)


def evaluate_case(
        qid: str, gold: set[str], retrieved: Sequence[str], k: int
) -> CaseMetric | None:
    """计算单题指标; gold 为空返回 None(该题不可评测)。"""
    if not gold:
        return None

    topk = list(retrieved[:k])
    hit_positions = [i for i, pid in enumerate(topk, start=1) if pid in gold]
    hits = len(hit_positions)
    first_rank = hit_positions[0] if hit_positions else None

    return CaseMetric(
        qid=qid,
        gold_count=len(gold),
        retrieved_count=len(topk),
        hits=hits,
        recall=hits / len(gold),
        precision=hits / k if k > 0 else 0.0,
        reciprocal_rank=(1.0 / first_rank) if first_rank else 0.0,
        hit=1.0 if hits else 0.0,
        first_hit_rank=first_rank,
    )


def aggregate(case_metrics: Sequence[CaseMetric], k: int, skipped: int = 0) -> dict[str, object]:
    """把单题指标聚合成 run 级指标(算术平均)。"""
    n = len(case_metrics)
    if n == 0:
        return {
            "k": k,
            "cases_total": skipped,
            "cases_evaluated": 0,
            "cases_skipped_no_gold": skipped,
            "recall_at_k": 0.0,
            "precision_at_k": 0.0,
            "mrr_at_k": 0.0,
            "hit_at_k": 0.0,
        }
    return {
        "k": k,
        "cases_total": n + skipped,
        "cases_evaluated": n,
        "cases_skipped_no_gold": skipped,
        "recall_at_k": round(sum(m.recall for m in case_metrics) / n, 6),
        "precision_at_k": round(sum(m.precision for m in case_metrics) / n, 6),
        "mrr_at_k": round(sum(m.reciprocal_rank for m in case_metrics) / n, 6),
        "hit_at_k": round(sum(m.hit for m in case_metrics) / n, 6),
        "avg_gold_chunks": round(sum(m.gold_count for m in case_metrics) / n, 4),
        "avg_hits": round(sum(m.hits for m in case_metrics) / n, 4),
    }