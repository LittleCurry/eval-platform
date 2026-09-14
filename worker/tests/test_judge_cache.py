"""judge 缓存单测(离线): 缓存键敏感性与内存缓存语义。

缓存是把 judge 从"非确定"变成"可复现"的手段(温度 0 也不保证判定稳定),
但它同时是最容易**悄悄串味**的地方:
- 键里漏了 contexts -> 同一答案在 A 上下文下的判定被用在 B 上下文上, 幻觉率直接失真;
- 键里漏了 prompt_hash / model -> 换了裁判却复用旧判决, 实验被污染;
- 写入不是"首写为准" -> 重复实验拿到不同判定, "可复现"又丢了。
这四条都在下面的用例里钉住。
"""
from __future__ import annotations

from app.judge.cache import (
    STAGE_CLAIMS,
    STAGE_RUBRIC,
    CacheEntry,
    InMemoryJudgeCache,
    NullJudgeCache,
    judge_cache_key,
)

PAYLOAD = {
    "question": "线索多久无跟进会被回收?",
    "answer": "默认 7 天。",
    "contexts": "[1] (B02)\n超过 7 天无任何跟进会自动回到未分配池。",
}


def key(**overrides: object) -> str:
    params: dict = {
        "stage": STAGE_CLAIMS,
        "prompt_hash": "22d446d0398cdaeb",
        "model": "deepseek-ai/DeepSeek-V3.2",
        "payload": PAYLOAD,
    }
    params.update(overrides)
    return judge_cache_key(**params)  # type: ignore[arg-type]


# ---- 键语义 ----


def test_key_is_stable_across_calls():
    assert key() == key()
    assert len(key()) == 64


def test_key_is_independent_of_payload_dict_order():
    reordered = {k: PAYLOAD[k] for k in reversed(list(PAYLOAD))}
    assert key(payload=reordered) == key()


def test_不同的_contexts_必须产生不同的键():
    changed = dict(PAYLOAD, contexts="[1] (D02)\n企微消息通知包含自动回收通知。")
    assert key(payload=changed) != key()


def test_answer_or_question_change_changes_key():
    assert key(payload=dict(PAYLOAD, answer="默认 30 天。")) != key()
    assert key(payload=dict(PAYLOAD, question="另一个问题?")) != key()


def test_prompt_hash_and_model_change_key():
    assert key(prompt_hash="ffffffffffffffff") != key()
    assert key(model="Qwen/Qwen3-8B") != key()


def test_stage_change_key():
    assert key(stage=STAGE_RUBRIC) != key()


def test_rubric_payload_with_reference_key():
    rubric_payload = {"question": "q", "answer": "a", "reference_answer": "r"}
    assert judge_cache_key(
        stage=STAGE_RUBRIC, prompt_hash="abc", model="m", payload=rubric_payload
    ) != judge_cache_key(stage=STAGE_RUBRIC, prompt_hash="abc", model="m", payload={"question": "q", "answer": "a"})


# ---- 内存缓存语义 ----


def test_miss_then_hit():
    cache = InMemoryJudgeCache()
    assert cache.get("k") is None

    cache.put(key="k", stage=STAGE_CLAIMS, model="m", prompt_hash="p", response={"claims": []})
    entry = cache.get("k")

    assert isinstance(entry, CacheEntry) and entry.response == {"claims": []}
    assert cache.hit_count == 1


def test_首写为准_重复写入被忽略():
    """同 payload 重复写入不覆盖: 让重复实验/续跑拿到同一份判定。"""
    cache = InMemoryJudgeCache()
    cache.put(key="k", stage=STAGE_CLAIMS, model="m", prompt_hash="p", response={"v": 1})
    cache.put(key="k", stage=STAGE_CLAIMS, model="m", prompt_hash="p", response={"v": 2})

    assert cache.get("k").response == {"v": 1}  # type: ignore[union-attr]
    assert cache.put_calls == 2


def test_token_usage_is_stored_with_entry():
    cache = InMemoryJudgeCache()
    cache.put(key="k", stage=STAGE_CLAIMS, model="m", prompt_hash="p", response={},
              prompt_tokens=311, completion_tokens=27)

    entry = cache.get("k")
    assert (entry.prompt_tokens, entry.completion_tokens) == (311, 27)  # type: ignore[union-attr]


def test_hit_count_increments_per_hit():
    cache = InMemoryJudgeCache()
    cache.put(key="k", stage=STAGE_CLAIMS, model="m", prompt_hash="p", response={})

    assert cache.get("k").hits == 1        # type: ignore[union-attr]
    assert cache.get("k").hits == 2        # type: ignore[union-attr]
    assert cache.hit_count == 2


def test_null_cache_never_hits_and_discards_writes():
    cache = NullJudgeCache()
    cache.put(key="k", stage=STAGE_CLAIMS, model="m", prompt_hash="p", response={"v": 1})
    assert cache.get("k") is None