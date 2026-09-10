"""分块器单测: 偏移正确性 / 策略行为 / 配置指纹稳定性。

原则: 不依赖外部服务, 用"手写小样例 + 反查偏移"验证, 而不是硬编码大段期望文本。
"""
from __future__ import annotations

import itertools

import pytest

from app.retrieval.chunker import ChunkingConfig, chunk_document, chunking_hash

SAMPLE_MD = """# 产品简介

知简 CRM 是面向 B2B 销售团队的客户关系管理系统。

## 核心对象

- 客户：企业级档案
- 联系人：客户下的具体人员

## 对象关系

一个客户可拥有多个联系人。
"""


def test_offsets_match_text_for_every_strategy():
    for strategy in ("headings", "paragraph", "fixed"):
        cfg = ChunkingConfig(strategy=strategy, chunk_size=60, overlap=10, min_chars=10)
        chunks = chunk_document(SAMPLE_MD, cfg)
        assert chunks, f"{strategy} 未产出任何 chunk"
        for i, c in enumerate(chunks):
            assert c.index == i
            # 偏移必须能原样切回文本 —— M2-3 锚点映射的基础
            assert SAMPLE_MD[c.char_start : c.char_end] == c.text
        for prev, cur in itertools.pairwise(chunks):
            assert cur.char_start >= prev.char_start


def test_headings_sections_and_content():
    cfg = ChunkingConfig(strategy="headings", chunk_size=500, overlap=0, min_chars=1)
    chunks = chunk_document(SAMPLE_MD, cfg)
    sections = [c.section for c in chunks]
    assert sections == ["产品简介", "产品简介 / 核心对象", "产品简介 / 对象关系"]
    assert "客户：企业级档案" in chunks[1].text
    assert chunks[2].text.startswith("## 对象关系")


def test_fixed_overlap_region_is_duplicated():
    text = "".join(f"{i:03d}" for i in range(40))  # 120 字符
    cfg = ChunkingConfig(strategy="fixed", chunk_size=50, overlap=20, min_chars=1)
    chunks = chunk_document(text, cfg)
    assert chunks[-1].char_end == len(text)
    for prev, cur in itertools.pairwise(chunks):
        assert prev.char_end - cur.char_start == 20  # 重叠区长度
        assert text[prev.char_end - 20 : prev.char_end] == text[cur.char_start : cur.char_start + 20]


def test_long_section_is_split_within_chunk_size():
    long_para = "段落内容。" * 60  # 300 字符
    md = f"# 标题\n\n{long_para}\n"
    cfg = ChunkingConfig(strategy="headings", chunk_size=100, overlap=20, min_chars=1)
    chunks = chunk_document(md, cfg)
    assert len(chunks) >= 3
    assert all(len(c.text) <= cfg.chunk_size for c in chunks)
    # 覆盖度: 纯文本拼接后不应丢失正文内容
    assert sum(len(c.text) for c in chunks) >= len(long_para)


def test_short_block_is_merged():
    md = "# 短标题\n\n一句话。\n\n# 长标题\n\n" + "内容。" * 60
    cfg = ChunkingConfig(strategy="headings", chunk_size=200, overlap=0, min_chars=80)
    chunks = chunk_document(md, cfg)
    assert len(chunks) == 1  # 短块被并入后一块
    assert "一句话。" in chunks[0].text
    assert "内容。" in chunks[0].text


def test_chunking_hash_stable_and_sensitive():
    a = ChunkingConfig(strategy="headings", chunk_size=500, overlap=50, min_chars=80)
    b = ChunkingConfig(strategy="headings", chunk_size=500, overlap=50, min_chars=80)
    c = ChunkingConfig(strategy="headings", chunk_size=501, overlap=50, min_chars=80)
    assert chunking_hash(a) == chunking_hash(b)
    assert chunking_hash(a) != chunking_hash(c)
    assert len(chunking_hash(a)) == 64


def test_invalid_config_rejected():
    with pytest.raises(ValueError):
        ChunkingConfig(strategy="unknown")
    with pytest.raises(ValueError):
        ChunkingConfig(chunk_size=0)
    with pytest.raises(ValueError):
        ChunkingConfig(chunk_size=100, overlap=100)