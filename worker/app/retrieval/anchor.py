"""gold 锚点映射(process.md D1): case.gold_anchors[{doc, span}] → 当前切分下的 gold chunk 集合 G。

为什么需要:
- 标注时锚点指向**稳定的原文位置**(doc + 小节/段落), 而 chunk 随切分配置变化;
- 只有按"当前 run 的切分配置"把锚点映射成 chunk 集合 G, Recall/Precision 才能跨切分配置公平比较;
- 映射失败的行(span 对不上、文档不存在)必须显式报告, 不能静默丢题 —— 它是评测集质量信号。

匹配优先级(逐级降级, 命中即止):
1. chunk.section 包含 span(章节级锚点最常见的写法)
2. chunk.text 包含 span(原文子串)
3. 归一化(去空白/标点, 统一大小写)后包含(容忍排版差异)

G 用 **point id** 表达: 由 indexer.point_id(corpus_id, doc_id, chunk_index, cfg_hash) 生成,
与写入 Qdrant 的 id 完全一致, 因此可以直接和检索结果做集合运算。
"""
from __future__ import annotations

import re
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field

from app.retrieval.chunker import Chunk
from app.retrieval.indexer import point_id

_NORM_RE = re.compile(r"[\s#*`>:\-—、,，。.；;!！?？()（）\[\]【】\"'“”‘’]+")


def normalize(text: str) -> str:
    """归一化: 去掉空白与常见标点, 统一小写 —— 用于容忍排版差异的模糊匹配。"""
    return _NORM_RE.sub("", text).lower()


@dataclass(frozen=True)
class AnchorSpec:
    """一条 gold 锚点: 指向某文档的某个小节/段落。"""

    doc: str
    span: str = ""


@dataclass(frozen=True)
class GoldCase:
    """待映射的评测用例(只取映射所需字段)。"""

    qid: str
    anchors: list[AnchorSpec]


@dataclass
class AnchorMapping:
    anchor: AnchorSpec
    matched: list[Chunk] = field(default_factory=list)
    reason: str = ""  # 失败原因; 成功时为空串

    @property
    def ok(self) -> bool:
        return bool(self.matched)


@dataclass
class CaseMapping:
    qid: str
    gold_point_ids: set[str] = field(default_factory=set)
    failures: list[str] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return bool(self.gold_point_ids)


@dataclass
class DatasetMapping:
    cfg_hash: str
    cases: list[CaseMapping] = field(default_factory=list)

    @property
    def total(self) -> int:
        return len(self.cases)

    @property
    def mapped(self) -> int:
        return sum(1 for c in self.cases if c.ok)

    @property
    def coverage(self) -> float:
        return self.mapped / self.total if self.total else 0.0

    @property
    def failures(self) -> list[dict[str, object]]:
        return [{"qid": c.qid, "reasons": c.failures} for c in self.cases if not c.ok]

    def to_json(self, *, dataset_id: int | None = None, corpus_id: int | None = None) -> dict[str, object]:
        gold_counts = [len(c.gold_point_ids) for c in self.cases if c.ok]
        return {
            "dataset_id": dataset_id,
            "corpus_id": corpus_id,
            "cfg_hash": self.cfg_hash,
            "cases": self.total,
            "mapped": self.mapped,
            "coverage": round(self.coverage, 4),
            "avg_gold_chunks": round(sum(gold_counts) / len(gold_counts), 2) if gold_counts else 0.0,
            "failures": self.failures,
        }


def map_anchor(anchor: AnchorSpec, chunks: list[Chunk]) -> AnchorMapping:
    """把单条锚点映射到 chunk 列表。"""
    span = anchor.span.strip()
    if not chunks:
        return AnchorMapping(anchor=anchor, reason="该文档没有可用 chunk(文档为空?)")
    if not span:
        # 无 span = 整篇文档都是 gold: 粒度过粗会拉高召回、降低区分度, 但语义正确
        return AnchorMapping(anchor=anchor, matched=list(chunks))

    matched = [c for c in chunks if span in c.section]
    if not matched:
        matched = [c for c in chunks if span in c.text]
    if not matched:
        norm_span = normalize(span)
        if norm_span:
            matched = [
                c for c in chunks if norm_span in normalize(c.text) or norm_span in normalize(c.section)
            ]
    if not matched:
        return AnchorMapping(anchor=anchor, reason=f"span 未匹配任何 chunk: {span!r}")
    return AnchorMapping(anchor=anchor, matched=matched)


def map_case(
        case: GoldCase, doc_chunks: Mapping[str, list[Chunk]], corpus_id: int, cfg_hash: str
) -> CaseMapping:
    """映射单条用例: 多个锚点命中的 chunk 取并集。"""
    result = CaseMapping(qid=case.qid)
    for anchor in case.anchors:
        chunks = doc_chunks.get(anchor.doc)
        if chunks is None:
            result.failures.append(f"文档不存在: {anchor.doc}")
            continue
        mapping = map_anchor(anchor, chunks)
        if not mapping.ok:
            result.failures.append(f"{anchor.doc}: {mapping.reason}")
            continue
        for chunk in mapping.matched:
            result.gold_point_ids.add(point_id(corpus_id, anchor.doc, chunk.index, cfg_hash))
    return result


def map_dataset_cases(
        cases: Iterable[GoldCase],
        doc_chunks: Mapping[str, list[Chunk]],
        corpus_id: int,
        cfg_hash: str,
) -> DatasetMapping:
    """映射整个评测集, 返回含覆盖率与失败明细的报告。"""
    mapping = DatasetMapping(cfg_hash=cfg_hash)
    for case in cases:
        mapping.cases.append(map_case(case, doc_chunks, corpus_id, cfg_hash))
    return mapping