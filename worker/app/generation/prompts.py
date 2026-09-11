"""prompt 作为**版本化资产**(M4-1 S2)。

为什么 prompt 要进版本控制并算 hash:
- 生成侧的所有结论都建立在"用了哪个 prompt"之上。换 prompt 就是换实验(D7), 所以
  `prompt_id` + `prompt_hash` 必须落进 `case_results.generation` 与配置快照;
- M4-2 的 judge 要按 `(prompt_hash, model, claim)` 做缓存(D5/D8), 缓存键的前半截就是它;
- 因此 hash **只覆盖影响行为的文本**(`[system]` + `[user]` 两段正文), 文件顶部的注释、
  排版调整都不算新实验 —— 否则每次补一句说明就会让历史 run 的 prompt_hash 对不上。

文件格式(刻意零依赖, 不引 YAML/pydantic):
    # 顶部注释随意写(不参与 hash)
    [system]
    ...系统提示...

    [user]
    ...用户模板, 含 {contexts} 与 {question} 两个占位符...

加载时会做两件校验(把错误挡在评测开始之前, 而不是第 37 道题上):
1. 两个段都存在且非空;
2. `{user}` 段除了两个约定占位符之外**没有其它花括号** —— 否则 `str.format` 会抛
   KeyError/IndexError, 而那会伪装成"生成失败"混进死信里。
"""
from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

PROMPT_DIR = Path(__file__).resolve().parents[2] / "prompts"
_SECTION_RE = re.compile(r"^\[(system|user)\]\s*$", re.MULTILINE)
PLACEHOLDERS = ("contexts", "question")
EMPTY_CONTEXT_TEXT = "（无检索结果）"


class PromptError(RuntimeError):
    """prompt 资产缺失或格式不合法 —— 属于配置错误, 应立刻暴露而不是重试。"""


@dataclass(frozen=True)
class PromptTemplate:
    """一个已加载并校验过的 prompt 模板。"""

    prompt_id: str
    system: str
    user: str
    prompt_hash: str
    path: Path

    def render(self, *, question: str, contexts: str) -> list[dict[str, str]]:
        """渲染成 OpenAI 兼容的 messages(可直接丢给 ChatClient)。"""
        return [
            {"role": "system", "content": self.system},
            {"role": "user", "content": self.user.format(contexts=contexts, question=question)},
        ]


@dataclass(frozen=True)
class ContextBundle:
    """检索上下文拼装结果(带用量, 用于写进 case_results.generation)。"""

    text: str
    chunks: int
    chars: int
    dropped: int


def prompt_hash(system: str, user: str) -> str:
    """prompt 指纹: 键排序 + 紧凑 JSON + sha256 前 16 位(与项目其它指纹同风格)。"""
    payload = json.dumps(
        {"system": system.strip(), "user": user.strip()},
        sort_keys=True,
        ensure_ascii=False,
        separators=(",", ":"),
    )
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()[:16]


def load_prompt(prompt_id: str, *, prompt_dir: Path | None = None) -> PromptTemplate:
    """按 id 加载 prompt(文件名即 id, 如 `qa_zh_v1` 对应 `prompts/qa_zh_v1.md`)。"""
    directory = prompt_dir or PROMPT_DIR
    path = directory / f"{prompt_id}.md"
    if not path.is_file():
        available = sorted(p.stem for p in directory.glob("*.md")) if directory.is_dir() else []
        raise PromptError(f"prompt 不存在: {path}(可用: {available or '无'})")

    raw = path.read_text(encoding="utf-8")
    sections = _split_sections(raw)
    for name in ("system", "user"):
        if not sections.get(name, "").strip():
            raise PromptError(f"{path} 缺少 [{name}] 段或该段为空")

    system = sections["system"].strip()
    user = sections["user"].strip()
    _validate_placeholders(user, path)
    return PromptTemplate(
        prompt_id=prompt_id,
        system=system,
        user=user,
        prompt_hash=prompt_hash(system, user),
        path=path,
    )


def build_contexts(
    hits: Iterable[Any],
    max_chars: int,
    *,
    with_doc_id: bool = True,
) -> ContextBundle:
    """把检索结果按**排名顺序**拼成上下文文本, 按字符预算截断。

    约定:
    - 顺序 = 检索排名, 与 `case_results.retrieved` 一致, 便于复盘"模型看到了什么";
    - 每个 chunk 带 `[n]` 标号与 doc_id, 供人工复核;
    - 预算按字符(不引 tokenizer 依赖), 实际用量通过 ContextBundle 落库;
    - 一个 chunk 都放不下时**不放半个**(避免截断出的半句话成为幻觉来源);
      完全没有可用结果时给出明确占位文本, 让模型有机会说"资料中未提及"。
    """
    if max_chars <= 0:
        raise ValueError("max_chars 必须为正整数")

    parts: list[str] = []
    used = 0
    chunks = 0
    dropped = 0

    for index, hit in enumerate(hits, start=1):
        text = str(getattr(hit, "text", "") or "").strip()
        if not text:
            dropped += 1
            continue
        doc_id = str(getattr(hit, "doc_id", "") or "")
        label = f"[{index}]"
        header = f"{label} ({doc_id})" if (with_doc_id and doc_id) else label
        block = f"{header}\n{text}"
        separator = "\n\n" if parts else ""
        if used + len(separator) + len(block) > max_chars:
            dropped += 1
            continue
        parts.append(block)
        used += len(separator) + len(block)
        chunks += 1

    text_out = "\n\n".join(parts) if parts else EMPTY_CONTEXT_TEXT
    return ContextBundle(text=text_out, chunks=chunks, chars=len(text_out), dropped=dropped)


# ---- 内部 ----


def _split_sections(raw: str) -> dict[str, str]:
    """按 `[system]` / `[user]` 标记切段; 第一个标记之前的内容(注释)被忽略。"""
    matches: Sequence[re.Match[str]] = list(_SECTION_RE.finditer(raw))
    sections: dict[str, str] = {}
    for position, match in enumerate(matches):
        name = match.group(1)
        start = match.end()
        end = matches[position + 1].start() if position + 1 < len(matches) else len(raw)
        sections[name] = raw[start:end]
    return sections


def _validate_placeholders(user_template: str, path: Path) -> None:
    for name in PLACEHOLDERS:
        if "{" + name + "}" not in user_template:
            raise PromptError(f"{path} 的 [user] 段缺少 {{{name}}} 占位符")
    try:
        user_template.format(**{name: "" for name in PLACEHOLDERS})
    except (KeyError, IndexError, ValueError) as exc:
        raise PromptError(f"{path} 的 [user] 段存在非法花括号(只允许 {{contexts}} 与 {{question}}): {exc}") from exc
