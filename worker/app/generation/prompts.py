"""prompt 作为**版本化资产**(M4-1 S2; M4-2 扩展占位符校验)。

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
    ...用户模板, 含 {contexts}/{question}/{answer}/{reference_answer} 中的若干占位符...

M4-2 变更: 占位符从"固定两个"改为**按 prompt 声明**:
- 生成 prompt 需要 {contexts} + {question}(默认 required);
- judge 的 claims prompt 需要 {contexts} + {question} + {answer};
- judge 的 rubric prompt 需要 {question} + {answer}(**不需要** contexts);
  M4-2 的 rubric v2 额外用 {claims_summary}(断言核查结果) —— 它是**可选**占位符:
  模板没声明就不传、也不进缓存键, 保证 v1 判定记录与旧缓存条目完全不受影响。
渲染**不用 `str.format`**: judge 的 prompt 必须内嵌 JSON 示例(`{"claims":[...]}`),
而 `str.format` 会把 `{"claims":...}` 当成字段名直接抛 KeyError。改为"**只替换白名单里的
`{name}` 占位符, 其余花括号原样保留**", 于是 prompt 文件可以自由地写 JSON 示例。

加载时仍挡住两类错误(把错误挡在评测开始之前, 而不是第 37 道题上):
1. 必需占位符缺失;
2. 出现**形如 `{name}` 但不在白名单**的占位符(如 `{anwser}` 拼错) —— 这类错误若留到运行时,
   会以"生成/判定失败"的形式混进死信里, 极难定位。
"""
from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Iterable, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

PROMPT_DIR = Path(__file__).resolve().parents[2] / "prompts"
_SECTION_RE = re.compile(r"^\[(system|user)\]\s*$", re.MULTILINE)
# 只认"形如 {name} 的整个标识符" —— 因此 {"claims":[...]} 这类 JSON 示例不会被误当作占位符
_PLACEHOLDER_RE = re.compile(r"\{([a-zA-Z_][a-zA-Z0-9_]*)\}")
# 允许出现的占位符(白名单): 新增占位符时在此登记, 并在 load_prompt 的 required 里声明
KNOWN_PLACEHOLDERS = ("contexts", "question", "answer", "reference_answer", "claims_summary")
DEFAULT_REQUIRED_PLACEHOLDERS = ("contexts", "question")
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
    # 该模板实际用到的占位符(按白名单顺序)与声明的必需项, 供 render 校验与排障
    placeholders: tuple[str, ...] = field(default_factory=tuple)
    required: tuple[str, ...] = DEFAULT_REQUIRED_PLACEHOLDERS

    def render(self, **values: str) -> list[dict[str, str]]:
        """渲染成 OpenAI 兼容的 messages(可直接丢给 ChatClient)。

        只接受模板声明过的占位符; 缺值直接报错 —— 而不是渲染出 `{answer}` 字面量,
        那会让模型把占位符当内容, 判出一堆莫名其妙的结论。
        """
        unknown = [name for name in values if name not in self.placeholders]
        if unknown:
            raise PromptError(
                f"{self.prompt_id}: 传入了模板未使用的占位符 {unknown}; 模板用到的是 {self.placeholders}"
            )
        missing = [name for name in self.required if name not in values]
        if missing:
            raise PromptError(f"{self.prompt_id}: 缺少必需占位符 {missing}")
        rendered = _PLACEHOLDER_RE.sub(lambda m: values.get(m.group(1), m.group(0)), self.user)
        return [
            {"role": "system", "content": self.system},
            {"role": "user", "content": rendered},
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


def load_prompt(
        prompt_id: str,
        *,
        prompt_dir: Path | None = None,
        required: Sequence[str] = DEFAULT_REQUIRED_PLACEHOLDERS,
) -> PromptTemplate:
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
    placeholders = _validate_placeholders(user, path, required)
    return PromptTemplate(
        prompt_id=prompt_id,
        system=system,
        user=user,
        prompt_hash=prompt_hash(system, user),
        path=path,
        placeholders=placeholders,
        required=tuple(required),
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


def _validate_placeholders(user_template: str, path: Path, required: Sequence[str]) -> tuple[str, ...]:
    """返回模板实际用到的占位符(白名单顺序); 缺失必需项或出现未知占位符时报错。"""
    found = {m.group(1) for m in _PLACEHOLDER_RE.finditer(user_template)}
    unknown = sorted(found - set(KNOWN_PLACEHOLDERS))
    if unknown:
        raise PromptError(
            f"{path} 的 [user] 段出现未知占位符 {unknown}(白名单: {KNOWN_PLACEHOLDERS})"
        )
    detected = tuple(name for name in KNOWN_PLACEHOLDERS if name in found)
    missing = [name for name in required if name not in detected]
    if missing:
        raise PromptError(f"{path} 的 [user] 段缺少必需占位符: {missing}")
    return detected