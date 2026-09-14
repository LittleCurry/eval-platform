"""prompt 资产与上下文拼装的离线单测(M4-1 S2)。

这组测试保护的是"实验可复现"的前半截:
- `prompt_hash` 必须**只随行为文本变化**(注释不算), 否则历史 run 的 prompt_hash 会对不上,
  M4-2 的 judge 缓存键也跟着错;
- `[user]` 段里的花括号必须在**加载时**就被拒绝, 而不是等到第 37 道题上抛 KeyError 混进死信;
- 上下文拼装必须严格按检索排名 + 预算截断, 且把实际用量报出来(要落库复盘"模型看到了什么")。
"""
from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import pytest

from app.generation.prompts import (
    DEFAULT_REQUIRED_PLACEHOLDERS,
    EMPTY_CONTEXT_TEXT,
    PromptError,
    PromptTemplate,
    build_contexts,
    load_prompt,
    prompt_hash,
)

REAL_PROMPT_ID = "qa_zh_v1"


@dataclass
class FakeHit:
    """替身: 只依赖 .text / .doc_id 两个属性(与 retrieval.Hit 同形)。"""

    text: str
    doc_id: str = "B02"


def write_prompt(directory: Path, name: str, body: str) -> Path:
    path = directory / f"{name}.md"
    path.write_text(body, encoding="utf-8")
    return path


# ---- 真实资产 ----


def test_real_prompt_loads_and_has_required_parts() -> None:
    prompt = load_prompt(REAL_PROMPT_ID)

    assert prompt.prompt_id == REAL_PROMPT_ID
    assert prompt.system and prompt.user
    for name in DEFAULT_REQUIRED_PLACEHOLDERS:
        assert "{" + name + "}" in prompt.user
    assert len(prompt.prompt_hash) == 16
    assert prompt.path.name == f"{REAL_PROMPT_ID}.md"


def test_real_prompt_keeps_the_dont_know_escape_hatch() -> None:
    """规则 2 是 M4-3 幻觉归因的前提: 没有"不知道"出口, 就无法区分"编造"与"诚实回答"。"""
    prompt = load_prompt(REAL_PROMPT_ID)

    assert "资料中未提及" in prompt.user
    assert "不要编造" in prompt.user


def test_real_prompt_renders_messages_in_order() -> None:
    prompt = load_prompt(REAL_PROMPT_ID)
    messages = prompt.render(question="线索多久无跟进会被回收?", contexts="[1] (B02)\n超过 7 天。")

    assert [m["role"] for m in messages] == ["system", "user"]
    assert messages[0]["content"] == prompt.system
    assert "线索多久无跟进会被回收?" in messages[1]["content"]
    assert "超过 7 天。" in messages[1]["content"]
    assert "{contexts}" not in messages[1]["content"]
    assert "{question}" not in messages[1]["content"]


def test_load_prompt_reports_available_ids_when_missing(tmp_path: Path) -> None:
    write_prompt(tmp_path, "qa_zh_v2", "[system]\ns\n\n[user]\n{contexts}{question}\n")

    with pytest.raises(PromptError) as excinfo:
        load_prompt("not_exist", prompt_dir=tmp_path)

    assert "qa_zh_v2" in str(excinfo.value)


# ---- 指纹语义 ----


def test_hash_normalizes_whitespace_but_tracks_body() -> None:
    """首尾空白不算差异, 正文差异必须算(注释不影响 hash 由下面的 loader 级用例覆盖)。"""
    assert prompt_hash("你是助手", "{contexts}\n{question}") == prompt_hash(" 你是助手 ", "{contexts}\n{question} ")
    assert prompt_hash("你是助手", "{contexts}\n{question}") != prompt_hash(
        "你是助手", "{contexts}\n{question}\n请简洁作答"
    )


def test_hash_is_stable_and_content_sensitive(tmp_path: Path) -> None:
    body = "[system]\n系统A\n\n[user]\n{contexts}\n{question}\n"
    write_prompt(tmp_path, "p", body)
    first = load_prompt("p", prompt_dir=tmp_path)

    assert load_prompt("p", prompt_dir=tmp_path).prompt_hash == first.prompt_hash

    write_prompt(tmp_path, "p", body.replace("系统A", "系统B"))
    assert load_prompt("p", prompt_dir=tmp_path).prompt_hash != first.prompt_hash


def test_comment_only_change_keeps_hash(tmp_path: Path) -> None:
    """补一句注释不该算新实验 —— 否则每次写文档都会让历史 prompt_hash 变成孤儿。"""
    body = "[system]\n系统A\n\n[user]\n{contexts}\n{question}\n"
    write_prompt(tmp_path, "p", body)
    before = load_prompt("p", prompt_dir=tmp_path).prompt_hash

    write_prompt(tmp_path, "p", "# 新增说明: 这条注释不改变行为\n\n" + body)
    assert load_prompt("p", prompt_dir=tmp_path).prompt_hash == before


# ---- 格式校验(把错误挡在评测开始前) ----


def test_missing_user_section_is_rejected(tmp_path: Path) -> None:
    write_prompt(tmp_path, "bad", "[system]\n只有系统段\n")

    with pytest.raises(PromptError) as excinfo:
        load_prompt("bad", prompt_dir=tmp_path)
    assert "[user]" in str(excinfo.value)


def test_empty_section_is_rejected(tmp_path: Path) -> None:
    write_prompt(tmp_path, "bad", "[system]\n\n\n[user]\n{contexts}{question}\n")

    with pytest.raises(PromptError):
        load_prompt("bad", prompt_dir=tmp_path)


def test_missing_placeholder_is_rejected(tmp_path: Path) -> None:
    write_prompt(tmp_path, "bad", "[system]\ns\n\n[user]\n只有 {question}\n")

    with pytest.raises(PromptError) as excinfo:
        load_prompt("bad", prompt_dir=tmp_path)
    assert "contexts" in str(excinfo.value)


def test_unknown_placeholder_is_rejected_at_load_time(tmp_path: Path) -> None:
    """占位符拼错若留到运行时, 会表现为"生成失败"混进死信, 极难定位。

    注意: 只校验"形如 {name} 的标识符", 所以 prompt 里的 JSON 示例(如 {"claims":[]})
    不会被误判 —— 这是 M4-2 引入 judge prompt 后的必要放宽。
    """
    write_prompt(tmp_path, "bad", "[system]\ns\n\n[user]\n{contexts}\n{question}\n注意 {anwser}\n")

    with pytest.raises(PromptError) as excinfo:
        load_prompt("bad", prompt_dir=tmp_path)
    assert "未知占位符" in str(excinfo.value)


def test_json_examples_inside_prompt_are_not_treated_as_placeholders(tmp_path: Path) -> None:
    """judge prompt 必须能内嵌 JSON 示例(如 {"claims":[...]}) —— str.format 会在这里炸掉。"""
    body = '[system]\ns\n\n[user]\n{contexts}\n{question}\n输出 {"claims":[{"id":1}]}\n'
    write_prompt(tmp_path, "jsonish", body)
    prompt = load_prompt("jsonish", prompt_dir=tmp_path)

    rendered = prompt.render(contexts="资料", question="问题")[1]["content"]
    assert '{"claims":[{"id":1}]}' in rendered, "JSON 示例必须原样保留"
    assert "资料" in rendered and "问题" in rendered


# ---- 上下文拼装 ----


def test_contexts_follow_retrieval_order_with_labels() -> None:
    hits = [FakeHit("第一段", "B02"), FakeHit("第二段", "A04"), FakeHit("第三段", "C05")]
    bundle = build_contexts(hits, max_chars=1000)

    assert bundle.chunks == 3
    assert bundle.dropped == 0
    assert bundle.text.index("[1] (B02)") < bundle.text.index("[2] (A04)") < bundle.text.index("[3] (C05)")
    assert bundle.chars == len(bundle.text)


def test_contexts_respect_char_budget_without_cutting_chunks() -> None:
    hits = [FakeHit("x" * 100, "B02"), FakeHit("y" * 100, "A04"), FakeHit("z" * 100, "C05")]
    bundle = build_contexts(hits, max_chars=250)

    assert bundle.chunks == 2, "第三个 chunk 放不下就整块丢弃, 而不是截半个"
    assert bundle.dropped == 1
    assert bundle.chars <= 250
    assert "y" * 100 in bundle.text
    assert "z" * 10 not in bundle.text


def test_empty_hits_produce_explicit_placeholder() -> None:
    """资料为空时要给模型一个可回应的落点, 否则它容易凭常识开编。"""
    bundle = build_contexts([], max_chars=500)

    assert bundle.text == EMPTY_CONTEXT_TEXT
    assert bundle.chunks == 0
    assert bundle.chars == len(EMPTY_CONTEXT_TEXT)


def test_blank_chunk_text_is_skipped_and_counted() -> None:
    hits = [FakeHit("   ", "B02"), FakeHit("有效内容", "A04")]
    bundle = build_contexts(hits, max_chars=500)

    assert bundle.chunks == 1
    assert bundle.dropped == 1
    assert "有效内容" in bundle.text


def test_zero_budget_is_rejected() -> None:
    with pytest.raises(ValueError):
        build_contexts([FakeHit("x")], max_chars=0)


def test_contexts_render_into_prompt_end_to_end() -> None:
    prompt: PromptTemplate = load_prompt(REAL_PROMPT_ID)
    bundle = build_contexts([FakeHit("线索超过 7 天无跟进会回到未分配池。", "B02")], max_chars=1000)
    messages = prompt.render(question="线索多久无跟进会被回收?", contexts=bundle.text)

    user_content = messages[1]["content"]
    assert "线索超过 7 天无跟进会回到未分配池。" in user_content
    assert "[1] (B02)" in user_content
    assert "线索多久无跟进会被回收?" in user_content