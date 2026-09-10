"""锚点映射单测(离线): 三级匹配优先级 / 失败原因 / 并集去重 / 覆盖率统计。"""
from __future__ import annotations

from app.retrieval.anchor import (
    AnchorSpec,
    GoldCase,
    map_anchor,
    map_case,
    map_dataset_cases,
    normalize,
)
from app.retrieval.chunker import Chunk
from app.retrieval.indexer import point_id


def chunk(index: int, text: str, section: str = "") -> Chunk:
    return Chunk(index=index, text=text, char_start=index * 100, char_end=index * 100 + len(text), section=section)


CHUNKS = [
    chunk(0, "# 线索管理\n\n## 自动回收\n\n超过 7 天无跟进自动回收。", "线索管理 / 自动回收"),
    chunk(1, "## 领取与在跟上限\n\n每人默认 200 条。", "线索管理 / 领取与在跟上限"),
    chunk(2, "## 线索查重与转化\n\n手机号判重。", "线索管理 / 线索查重与转化"),
]


def test_section_match_has_priority():
    m = map_anchor(AnchorSpec(doc="B02", span="自动回收"), CHUNKS)
    assert m.ok
    assert [c.index for c in m.matched] == [0]


def test_text_match_when_section_missing():
    # 小节标题被合并后 section 可能只保留首个标题, 但正文里仍有原小标题
    m = map_anchor(AnchorSpec(doc="B02", span="线索查重与转化"), CHUNKS)
    assert m.ok and [c.index for c in m.matched] == [2]


def test_normalized_match_tolerates_spacing_and_punctuation():
    m = map_anchor(AnchorSpec(doc="B02", span="领 取、与 在跟—上限"), CHUNKS)
    assert m.ok and [c.index for c in m.matched] == [1]
    assert normalize("领取、与 在跟—上限") == normalize("领取与在跟上限")


def test_no_match_records_reason():
    m = map_anchor(AnchorSpec(doc="B02", span="不存在的小节"), CHUNKS)
    assert not m.ok
    assert "不存在的小节" in m.reason


def test_empty_span_maps_whole_document():
    m = map_anchor(AnchorSpec(doc="B02", span=""), CHUNKS)
    assert m.ok and len(m.matched) == len(CHUNKS)


def test_empty_chunks_records_reason():
    m = map_anchor(AnchorSpec(doc="X", span="任意"), [])
    assert not m.ok and "没有可用 chunk" in m.reason


def test_map_case_unions_anchors_and_generates_index_point_ids():
    case = GoldCase(qid="zjc-001", anchors=[AnchorSpec("B02", "自动回收"), AnchorSpec("B02", "领取与在跟上限")])
    mapping = map_case(case, {"B02": CHUNKS}, corpus_id=4, cfg_hash="cfg123")

    expected = {
        point_id(4, "B02", 0, "cfg123"),
        point_id(4, "B02", 1, "cfg123"),
    }
    assert mapping.gold_point_ids == expected
    assert mapping.ok and mapping.failures == []


def test_map_case_deduplicates_overlapping_anchors():
    case = GoldCase(qid="q", anchors=[AnchorSpec("B02", "自动回收"), AnchorSpec("B02", "自动")])
    mapping = map_case(case, {"B02": CHUNKS}, corpus_id=4, cfg_hash="cfg123")
    assert mapping.gold_point_ids == {point_id(4, "B02", 0, "cfg123")}


def test_map_case_missing_doc_is_failure():
    case = GoldCase(qid="q", anchors=[AnchorSpec("NOPE", "任意")])
    mapping = map_case(case, {"B02": CHUNKS}, corpus_id=4, cfg_hash="cfg123")
    assert not mapping.ok
    assert any("文档不存在: NOPE" in f for f in mapping.failures)


def test_dataset_coverage_and_failure_report():
    cases = [
        GoldCase(qid="ok-1", anchors=[AnchorSpec("B02", "自动回收")]),
        GoldCase(qid="ok-2", anchors=[AnchorSpec("B02", "领取与在跟上限")]),
        GoldCase(qid="bad", anchors=[AnchorSpec("B02", "不存在的小节")]),
    ]
    mapping = map_dataset_cases(cases, {"B02": CHUNKS}, corpus_id=4, cfg_hash="cfg123")

    assert mapping.total == 3 and mapping.mapped == 2
    assert abs(mapping.coverage - 2 / 3) < 1e-9
    report = mapping.to_json(dataset_id=3, corpus_id=4)
    assert report["cases"] == 3 and report["mapped"] == 2
    assert report["failures"] == [{"qid": "bad", "reasons": ["B02: span 未匹配任何 chunk: '不存在的小节'"]}]


def test_partial_anchor_failure_still_maps_case():
    # 两个锚点坏一个: 仍算映射成功(有一个 gold chunk 可用), 但失败原因要保留
    case = GoldCase(qid="q", anchors=[AnchorSpec("B02", "自动回收"), AnchorSpec("B02", "查无此节")])
    mapping = map_case(case, {"B02": CHUNKS}, corpus_id=4, cfg_hash="cfg123")
    assert mapping.ok
    assert mapping.failures and "查无此节" in mapping.failures[0]