"""judge 协议解析单测(离线): 提取 / 校验 / 重编号 / 错误摘要。

这一层是 judge 可靠性的地基: 模型的真实输出会带 ```json 代码块、前后废话、JSON 之后又补解释、
字符串里含花括号、被 max_tokens 截断。这些都是**已知会发生**的情况, 必须逐条钉住,
否则解析失败会伪装成"判定不出来", 让幻觉率变成噪声。
"""
from __future__ import annotations

import pytest

from app.judge.protocol import (
    JudgeProtocolError,
    extract_json_object,
    parse_claims,
    parse_rubric,
)

VALID_CLAIMS = (
    '{"claims":[{"id":1,"text":"默认 7 天","label":"supported",'
    '"evidence":"超过 7 天(默认, 可配 3–30 天)无任何跟进","reason":"资料原文一致"}]}'
)


# ---- extract_json_object ----


def test_extracts_plain_object():
    assert extract_json_object('{"a":1}') == {"a": 1}


def test_extracts_from_json_code_fence():
    text = f"```json\n{VALID_CLAIMS}\n```"
    assert extract_json_object(text)["claims"][0]["label"] == "supported"


def test_extracts_when_model_adds_prose_before_and_after():
    text = f"好的，我来核查：\n{VALID_CLAIMS}\n以上是我的判断，请参考。"
    assert "claims" in extract_json_object(text)


def test_extracts_first_object_when_two_are_present():
    text = '{"step":1} 然后 {"step":2}'
    assert extract_json_object(text) == {"step": 1}


def test_handles_braces_and_escapes_inside_strings():
    text = '{"text":"用 {花括号} 和 \\"引号\\" 的字段","n":1}'
    parsed = extract_json_object(text)
    assert parsed["n"] == 1 and "花括号" in parsed["text"]


def test_raises_on_empty_output():
    with pytest.raises(JudgeProtocolError, match="为空"):
        extract_json_object("   ")


def test_raises_when_no_json_present():
    with pytest.raises(JudgeProtocolError, match="未能"):
        extract_json_object("这条判定我无法完成。")


def test_raises_on_truncated_json():
    with pytest.raises(JudgeProtocolError):
        extract_json_object('{"claims":[{"id":1,"text":"默认 7 天"')


def test_error_message_is_truncated():
    long_text = "抱歉" + "x" * 5000
    with pytest.raises(JudgeProtocolError) as excinfo:
        extract_json_object(long_text)
    assert len(str(excinfo.value)) < 400


# ---- parse_claims ----


def test_parses_valid_claims():
    claims, truncated = parse_claims(VALID_CLAIMS)
    assert truncated == 0
    assert len(claims) == 1
    assert claims[0].label == "supported"
    assert claims[0].id == 1
    assert claims[0].evidence.startswith("超过 7 天")


def test_empty_claims_array_is_valid():
    """只回答"资料中未提及"的答案没有可核查断言, 这是合法结果而不是错误。"""
    claims, truncated = parse_claims('{"claims":[]}')
    assert claims == [] and truncated == 0


def test_renumbers_ids_by_array_order():
    text = (
        '{"claims":['
        '{"id":7,"text":"A","label":"supported"},'
        '{"id":7,"text":"B","label":"unsupported"},'
        '{"id":3,"text":"C","label":"irrelevant"}]}'
    )
    claims, _ = parse_claims(text)
    assert [c.id for c in claims] == [1, 2, 3], "id 必须按数组顺序重编, 保证 M6 标注可关联"


def test_truncates_when_over_limit():
    text = (
        '{"claims":['
        '{"id":1,"text":"A","label":"supported"},'
        '{"id":2,"text":"B","label":"supported"},'
        '{"id":3,"text":"C","label":"supported"}]}'
    )
    claims, truncated = parse_claims(text, max_claims=2)
    assert len(claims) == 2 and truncated == 1


def test_missing_claims_key_is_protocol_error():
    with pytest.raises(JudgeProtocolError, match="claims 字段不合法"):
        parse_claims('{"judgements":[]}')


def test_invalid_label_is_protocol_error():
    with pytest.raises(JudgeProtocolError, match="claims 字段不合法"):
        parse_claims('{"claims":[{"id":1,"text":"A","label":"probably"}]}')


def test_missing_text_is_protocol_error():
    with pytest.raises(JudgeProtocolError):
        parse_claims('{"claims":[{"id":1,"label":"supported"}]}')


def test_blank_text_is_protocol_error():
    with pytest.raises(JudgeProtocolError, match="空 text"):
        parse_claims('{"claims":[{"id":1,"text":"   ","label":"supported"}]}')


def test_extra_fields_are_tolerated():
    """模型多给一个 confidence 不该让整次判定作废。"""
    text = '{"claims":[{"id":1,"text":"A","label":"supported","confidence":0.9}],"model":"x"}'
    claims, _ = parse_claims(text)
    assert claims[0].label == "supported"


def test_optional_fields_default_to_empty():
    claims, _ = parse_claims('{"claims":[{"id":1,"text":"A","label":"irrelevant"}]}')
    assert claims[0].evidence == "" and claims[0].reason == ""


def test_whitespace_in_fields_is_stripped():
    text = '{"claims":[{"id":1,"text":"  A  ","label":"supported","evidence":" B "}]}'
    claims, _ = parse_claims(text)
    assert claims[0].text == "A" and claims[0].evidence == "B"


# ---- parse_rubric ----


def test_parses_valid_rubric():
    rubric = parse_rubric('{"relevance":4,"helpfulness":3,"reason":"要点齐全"}')
    assert (rubric.relevance, rubric.helpfulness) == (4, 3)
    assert rubric.reason == "要点齐全"


def test_rubric_from_code_fence():
    rubric = parse_rubric('```json\n{"relevance":5,"helpfulness":5}\n```')
    assert rubric.relevance == 5 and rubric.helpfulness == 5


@pytest.mark.parametrize(
    "text",
    [
        '{"relevance":0,"helpfulness":3}',      # 下界
        '{"relevance":6,"helpfulness":3}',      # 上界
        '{"relevance":4}',                      # 缺 helpfulness
        '{"helpfulness":4}',                    # 缺 relevance
        '{"relevance":"高","helpfulness":3}',   # 类型错
    ],
)
def test_invalid_rubric_is_protocol_error(text: str):
    with pytest.raises(JudgeProtocolError, match="rubric 字段不合法"):
        parse_rubric(text)