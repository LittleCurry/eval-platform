"""judge 输出协议(M4-2): 严格 JSON 提取与字段校验。

为什么单独成模块:
- judge 的可靠性 = 协议可靠性。模型会给出 ```json 代码块、前后加解释、把 JSON 截断、
  或者在字段里塞换行/引号 —— 这些都必须在一个**被单测覆盖**的地方处理掉;
- 判定结果是后续所有归因的输入, 解析错误一旦被"将就着用", 幻觉率就成了噪声。

协议(与 prompts/judge_claims_zh_v1.md、judge_rubric_zh_v1.md 一一对应):
    claims: {"claims":[{"id":1,"text":"...","label":"supported|unsupported|irrelevant",
                        "evidence":"...","reason":"..."}]}
    rubric: {"relevance":1-5,"helpfulness":1-5,"reason":"..."}

约定:
- **多余字段容忍**(extra="ignore"): 模型多给一个 "confidence" 不该让整次判定作废;
- **缺字段/枚举非法/分数越界 = 协议失败**(JudgeProtocolError), 由调用方重试;
- claim 的 id 以**数组顺序**为准重新编号(1..n): 模型偶发重复/跳号时, 保证同一答案
  的 claim id 稳定 —— M6 人工标注要按 (run_id, case_id, claim_id) 关联, id 漂了就对不上。
"""
from __future__ import annotations

import json
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError

_PREVIEW_CHARS = 200


class JudgeProtocolError(RuntimeError):
    """judge 输出不符合协议: 提取不到 JSON、字段缺失、枚举非法、分数越界。属**可重试**失败。"""


class ClaimJudgement(BaseModel):
    model_config = ConfigDict(extra="ignore")

    id: int
    text: str
    label: Literal["supported", "unsupported", "irrelevant"]
    evidence: str = ""
    reason: str = ""


class ClaimsResponse(BaseModel):
    model_config = ConfigDict(extra="ignore")

    claims: list[ClaimJudgement]


class RubricResponse(BaseModel):
    model_config = ConfigDict(extra="ignore")

    relevance: int = Field(ge=1, le=5)
    helpfulness: int = Field(ge=1, le=5)
    reason: str = ""


def extract_json_object(text: str) -> dict[str, Any]:
    """从模型输出里提取**首个能解析成对象**的平衡 JSON。

    处理: ```json 代码块、前后废话、字符串内的花括号与转义、截断(提取不到就报错)。
    注意不能简单地"取第一个 { 到最后一个 }" —— 模型常在 JSON 之后又补一句解释,
    那样会把尾随文字一起吞进来导致解析失败。
    """
    raw = (text or "").strip()
    if not raw:
        raise JudgeProtocolError("judge 输出为空")

    start = raw.find("{")
    while start != -1:
        end = _matching_brace(raw, start)
        if end != -1:
            try:
                parsed = json.loads(raw[start : end + 1])
            except json.JSONDecodeError:
                pass
            else:
                if isinstance(parsed, dict):
                    return parsed
        start = raw.find("{", start + 1)
    raise JudgeProtocolError(f"未能从输出中提取 JSON 对象: {_preview(raw)}")


def parse_claims(text: str, *, max_claims: int | None = None) -> tuple[list[ClaimJudgement], int]:
    """解析 claims 协议; 返回 (claims, 被截断的条数)。id 按数组顺序重新编号。"""
    data = extract_json_object(text)
    try:
        claims = ClaimsResponse.model_validate(data).claims
    except ValidationError as exc:
        raise JudgeProtocolError(f"claims 字段不合法: {_summarize(exc)}") from exc

    truncated = 0
    if max_claims is not None and max_claims > 0 and len(claims) > max_claims:
        truncated = len(claims) - max_claims
        claims = claims[:max_claims]

    renumbered = [
        ClaimJudgement(id=index, text=c.text.strip(), label=c.label,
                       evidence=c.evidence.strip(), reason=c.reason.strip())
        for index, c in enumerate(claims, start=1)
    ]
    empty = [c for c in renumbered if not c.text]
    if empty:
        raise JudgeProtocolError(f"claims 里存在空 text: {[c.id for c in empty]}")
    return renumbered, truncated


def parse_rubric(text: str) -> RubricResponse:
    """解析 rubric 协议。"""
    data = extract_json_object(text)
    try:
        return RubricResponse.model_validate(data)
    except ValidationError as exc:
        raise JudgeProtocolError(f"rubric 字段不合法: {_summarize(exc)}") from exc


# ---- 内部 ----


def _matching_brace(text: str, start: int) -> int:
    """返回与 text[start]('{') 匹配的 '}' 下标; 找不到返回 -1(截断)。"""
    depth = 0
    in_string = False
    escaped = False
    for index in range(start, len(text)):
        char = text[index]
        if in_string:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                in_string = False
            continue
        if char == '"':
            in_string = True
        elif char == "{":
            depth += 1
        elif char == "}":
            depth -= 1
            if depth == 0:
                return index
    return -1


def _summarize(exc: ValidationError) -> str:
    """只保留前 3 条错误摘要 —— 错误信息要能进日志与死信原因, 不能是整页堆栈。"""
    return "; ".join(f"{'.'.join(str(p) for p in err['loc'])}: {err['msg']}" for err in exc.errors()[:3])


def _preview(text: str, limit: int = _PREVIEW_CHARS) -> str:
    flat = " ".join((text or "").split())
    return flat[:limit] + ("…" if len(flat) > limit else "")