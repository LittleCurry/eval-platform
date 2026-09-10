"""自研分块器: 把原始文档切成可检索 chunk, 保留字符偏移与标题路径。

设计目标(对应 process.md D1/D7):
- Chunk 带 char_start/char_end: M2-3 用它把 gold 锚点(章节/段落)映射到 chunk 集合;
- ChunkingConfig 可序列化, chunking_hash 只依赖配置 -> 同配置跨进程同指纹, 可复现;
- 三种策略: headings(默认) / paragraph / fixed, 供实验对比。

约定: chunk_size 按**字符数**计(中文友好), 不按 token。
"""
from __future__ import annotations

import hashlib
import json
import re
from dataclasses import asdict, dataclass

_HEADING_RE = re.compile(r"^(#{1,6})\s+(.*)$")

STRATEGIES = ("headings", "paragraph", "fixed")


@dataclass(frozen=True)
class ChunkingConfig:
    """分块配置(实验变量之一, 见 D7 配置快照)。"""

    strategy: str = "headings"
    chunk_size: int = 500
    overlap: int = 50
    min_chars: int = 80

    def __post_init__(self) -> None:
        if self.strategy not in STRATEGIES:
            raise ValueError(f"未知 strategy: {self.strategy}, 可选 {STRATEGIES}")
        if self.chunk_size <= 0:
            raise ValueError("chunk_size 必须为正整数")
        if self.overlap < 0:
            raise ValueError("overlap 不能为负")
        if self.overlap >= self.chunk_size:
            raise ValueError("overlap 必须小于 chunk_size")


@dataclass(frozen=True)
class Chunk:
    index: int
    text: str
    char_start: int
    char_end: int
    section: str  # 标题路径, 如 "核心对象 / 对象关系与流转规则"; 无标题时为空串


def chunking_hash(cfg: ChunkingConfig) -> str:
    """配置指纹: 同配置必同值, 任一字段变化必不同值。"""
    payload = json.dumps(asdict(cfg), sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def chunk_document(raw_text: str, cfg: ChunkingConfig) -> list[Chunk]:
    """按配置切分文档, 返回按顺序编号的 Chunk 列表。"""
    if cfg.strategy == "fixed":
        blocks: list[tuple[str, int, int]] = [("", 0, len(raw_text))]
    elif cfg.strategy == "paragraph":
        blocks = [("", s, e) for s, e in _paragraph_spans(raw_text)]
        blocks = _merge_short(blocks, raw_text, cfg)
    else:
        blocks = _merge_short(_heading_blocks(raw_text), raw_text, cfg)

    spans: list[tuple[str, int, int]] = []
    for section, start, end in blocks:
        segment = raw_text[start:end]
        if len(segment) <= cfg.chunk_size:
            spans.append((section, start, end))
            continue
        for rel_start, rel_end in _pack_paragraphs(segment, cfg):
            spans.append((section, start + rel_start, start + rel_end))

    chunks: list[Chunk] = []
    for section, start, end in spans:
        text = raw_text[start:end]
        if not text.strip():
            continue
        chunks.append(
            Chunk(index=len(chunks), text=text, char_start=start, char_end=end, section=section)
        )
    return chunks


# ---- 内部实现 ----


def _heading_blocks(raw_text: str) -> list[tuple[str, int, int]]:
    """按 markdown 标题(任意层级)切块, section 为标题路径。"""
    blocks: list[tuple[str, int, int]] = []
    stack: list[tuple[int, str]] = []  # (level, title)
    cur_path = ""
    cur_start = 0
    offset = 0

    for line in raw_text.splitlines(keepends=True):
        m = _HEADING_RE.match(line.rstrip("\n"))
        if m:
            if offset > 0 or blocks:
                blocks.append((cur_path, cur_start, offset))
            level = len(m.group(1))
            while stack and stack[-1][0] >= level:
                stack.pop()
            stack.append((level, m.group(2).strip()))
            cur_path = " / ".join(title for _, title in stack)
            cur_start = offset
        offset += len(line)

    blocks.append((cur_path, cur_start, offset))
    return [(p, s, e) for p, s, e in blocks if raw_text[s:e].strip()]


def _paragraph_spans(text: str) -> list[tuple[int, int]]:
    """按空行切分, 返回相对 text 的段落区间(不含前后空行)。"""
    spans: list[tuple[int, int]] = []
    start: int | None = None
    pos = 0
    for line in text.splitlines(keepends=True):
        if line.strip() == "":
            if start is not None:
                spans.append((start, pos))
                start = None
        elif start is None:
            start = pos
        pos += len(line)
    if start is not None:
        spans.append((start, pos))
    return spans


def _fixed_spans(text: str, size: int, overlap: int) -> list[tuple[int, int]]:
    """定长滑窗(带 overlap), 返回相对 text 的区间。"""
    spans: list[tuple[int, int]] = []
    step = size - overlap
    start = 0
    while start < len(text):
        end = min(start + size, len(text))
        spans.append((start, end))
        if end >= len(text):
            break
        start += step
    return spans


def _pack_paragraphs(segment: str, cfg: ChunkingConfig) -> list[tuple[int, int]]:
    """把超长块按段落打包到 chunk_size 以内; 单个超长段落再走定长滑窗。

    返回相对 segment 的区间。
    """
    out: list[tuple[int, int]] = []
    cur_start: int | None = None
    cur_end = 0

    for p_start, p_end in _paragraph_spans(segment):
        if p_end - p_start > cfg.chunk_size:
            # 单段就超长: 先收掉累积的, 再对该段做定长滑窗
            if cur_start is not None:
                out.append((cur_start, cur_end))
                cur_start = None
            for rel_start, rel_end in _fixed_spans(
                    segment[p_start:p_end], cfg.chunk_size, cfg.overlap
            ):
                out.append((p_start + rel_start, p_start + rel_end))
            continue

        if cur_start is None:
            cur_start, cur_end = p_start, p_end
        elif p_end - cur_start <= cfg.chunk_size:
            cur_end = p_end
        else:
            out.append((cur_start, cur_end))
            cur_start, cur_end = p_start, p_end

    if cur_start is not None:
        out.append((cur_start, cur_end))
    return out


def _merge_short(
        blocks: list[tuple[str, int, int]], raw_text: str, cfg: ChunkingConfig
) -> list[tuple[str, int, int]]:
    """过短的块与后一块合并(min_chars), 避免产生碎片 chunk。"""
    merged: list[tuple[str, int, int]] = []
    for section, start, end in blocks:
        if merged and len(raw_text[merged[-1][1] : merged[-1][2]].strip()) < cfg.min_chars:
            prev_section, prev_start, _ = merged[-1]
            merged[-1] = (prev_section, prev_start, end)
            continue
        merged.append((section, start, end))

    # 末尾剩余块过短则并入前一块
    if len(merged) >= 2 and len(raw_text[merged[-1][1] : merged[-1][2]].strip()) < cfg.min_chars:
        _, _, last_end = merged.pop()
        prev_section, prev_start, _ = merged[-1]
        merged[-1] = (prev_section, prev_start, last_end)
    return merged