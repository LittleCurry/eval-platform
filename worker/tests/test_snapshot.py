"""配置快照与指纹单测(离线): 结构完整性 / 指纹稳定性与敏感性 / git_sha 降级。

M4 起增加两组**跨语言锁定**常量(与 Go 侧 snapshot_test.go 同值):
不启用生成/judge 时快照必须字节级不变(D14), 启用时是新的可区分指纹。
rubric v2 成为默认后: 默认组合锁新常量, **v1 组合单独锁定**以证明 run #155 仍可复算。
"""
from __future__ import annotations

import json
from typing import Any

from app.config import Settings
from app.eval.snapshot import build_config_snapshot, git_sha, snapshot_hash
from app.retrieval.chunker import ChunkingConfig


def make_snapshot(**over: Any) -> dict[str, Any]:
    settings = over.pop("settings", None) or Settings(_env_file=None)
    cfg = over.pop("cfg", None) or ChunkingConfig()
    params: dict[str, Any] = {
        "settings": settings,
        "cfg": cfg,
        "corpus_id": 4,
        "dataset_id": 3,
        "top_k": 5,
    }
    params.update(over)
    return build_config_snapshot(**params)


def test_snapshot_contains_required_sections():
    snap = make_snapshot()
    assert set(snap) == {"chunking", "embedding", "retrieval", "data", "extras"}
    assert snap["data"] == {"corpus_id": 4, "dataset_id": 3}
    assert snap["retrieval"]["top_k"] == 5
    assert snap["retrieval"]["reranker"]["enabled"] is False
    assert snap["embedding"]["model"] == "BAAI/bge-m3"
    assert snap["embedding"]["dim"] == 1024
    assert snap["chunking"]["strategy"] == "headings"
    assert json.dumps(snap, ensure_ascii=False)  # 必须可序列化(jsonb 落库前提)


def test_hash_is_key_order_independent():
    snap = make_snapshot()
    reordered = {key: snap[key] for key in reversed(list(snap))}
    assert snapshot_hash(snap) == snapshot_hash(reordered)
    assert len(snapshot_hash(snap)) == 64


def test_hash_changes_when_any_config_changes():
    base = snapshot_hash(make_snapshot())
    assert snapshot_hash(make_snapshot(top_k=10)) != base
    assert snapshot_hash(make_snapshot(cfg=ChunkingConfig(chunk_size=300))) != base
    assert snapshot_hash(make_snapshot(reranker_enabled=True)) != base
    assert (
            snapshot_hash(make_snapshot(settings=Settings(_env_file=None, embedding_model="other-model")))
            != base
    )
    assert snapshot_hash(make_snapshot(extras={"note": "x"})) != base
    assert snapshot_hash(make_snapshot(corpus_id=5)) != base


def test_git_sha_env_override(monkeypatch):
    monkeypatch.setenv("EVAL_GIT_SHA", "abc1234")
    assert git_sha() == "abc1234"


def test_git_sha_falls_back_to_unknown(monkeypatch):
    monkeypatch.delenv("EVAL_GIT_SHA", raising=False)
    assert git_sha(repo_root="/nonexistent-path-for-test") == "unknown"

# ---- M4 / D14: 生成段与跨语言锁定 ----
#
# 这两个常量由 Python 侧真实算出后回填, 并在 Go 侧(snapshot_test.go)以同样的值锁定。
# 任何一侧改了 generation 段的字段名/类型/浮点表示, 都会立刻在这里失败 ——
# 这正是"同配置必须同指纹"这条红线的守门人。

WANT_CONFIG_HASH_BASELINE = "b6598ed95e12f5d919ac26a969173b7a067d070ea88b752ebff91d523b2b9d0b"
WANT_CONFIG_HASH_WITH_GENERATION = "e45129b63931df44bf17a1cdf04d475f157764aa4a4bafbc1e9d7edb1a36d31c"
WANT_CONFIG_HASH_WITH_FRACTIONAL_TEMP = "ce02ebea0ba067f944f2fd4357d3aad5da16c6dd5fc09bd2777b779fdd76de60"


def generation_section(temperature: float = 0.0) -> dict[str, Any]:
    return {
        "provider": "siliconflow",
        "base_url": "https://api.siliconflow.cn/v1",
        "model": "deepseek-ai/DeepSeek-V3.2",
        "prompt_id": "qa_zh_v1",
        "temperature": temperature,
        "max_tokens": 512,
        "max_context_chars": 3000,
    }


def test_baseline_hash_is_unchanged_without_generation():
    """D14 红线: 不启用生成时快照与历史完全一致(M2/M3 的 run 指纹仍可复现)。"""
    snap = make_snapshot()
    assert "generation" not in snap
    assert snapshot_hash(snap) == WANT_CONFIG_HASH_BASELINE


def test_generation_hash_matches_go_side():
    snap = make_snapshot(generation=generation_section(0.0))
    assert snapshot_hash(snap) == WANT_CONFIG_HASH_WITH_GENERATION
    # 与 Go 侧 GenerationConfig 的字段一一对应
    assert set(snap["generation"]) == {
        "provider", "base_url", "model", "prompt_id", "temperature", "max_tokens", "max_context_chars",
    }
    assert snap["generation"]["temperature"] == 0, "整数温度应归一化为 int(与 Go 的 JSON 表示一致)"
    assert snap["generation"]["prompt_id"] == "qa_zh_v1"


def test_fractional_temperature_also_matches_go_side():
    """浮点表示不是"只对了整数"的巧合: 0.3 两侧也必须一致。"""
    snap = make_snapshot(generation=generation_section(0.3))
    assert snapshot_hash(snap) == WANT_CONFIG_HASH_WITH_FRACTIONAL_TEMP
    assert snap["generation"]["temperature"] == 0.3


def test_generation_section_is_sensitive_to_every_experiment_field():
    base = snapshot_hash(make_snapshot(generation=generation_section()))
    for field, value in (
            ("model", "deepseek-ai/DeepSeek-V3.1-Terminus"),
            ("prompt_id", "qa_zh_v2"),
            ("temperature", 0.7),
            ("max_tokens", 256),
            ("max_context_chars", 1500),
            ("provider", "deepseek"),
            ("base_url", "https://api.deepseek.com"),
    ):
        section = generation_section()
        section[field] = value
        assert snapshot_hash(make_snapshot(generation=section)) != base, f"{field} 未参与指纹"


# ---- M4-2 / D14: judge 段与跨语言锁定 ----
#
# 与 generation 段同样的规矩: 不启用 judge 时快照必须**字节级不变**;
# 启用时是一枚可区分的新指纹。下面两个常量同样由 Python 算出后回填 Go 侧。

# rubric v2(负控实验暴露 v1 缺陷后修订)成为当前默认, 因此默认配置的指纹变了;
# v1 的组合**单独锁定**, 用来证明 run #155 那份基线仍然可复算(向后兼容红线)。
WANT_CONFIG_HASH_WITH_JUDGE_V2 = "a4a043bd122ebc0ce35e947bdf9cc2e20f6473a455f3ae3325456ef0e8a84cea"
WANT_CONFIG_HASH_WITH_GENERATION_AND_JUDGE_V2 = (
    "b617560443b1937fed0f91cd1da337886065a48131e9a3ded126a5746a84ca3f"
)
WANT_CONFIG_HASH_WITH_JUDGE_V1 = "a73747993f6a79ea20ce02e43f1d82d06079a029e24a9c85a295747e151788b1"
WANT_CONFIG_HASH_WITH_GENERATION_AND_JUDGE_V1 = (
    "e1789b5b9c480444d7273e096bfa0dfd7ab0c6ee28a5d46c374b68a229b0f57b"
)
DEFAULT_RUBRIC_PROMPT_ID = "judge_rubric_zh_v2"


def judge_section(rubric_prompt_id: str = DEFAULT_RUBRIC_PROMPT_ID) -> dict[str, Any]:
    return {
        "provider": "siliconflow",
        "base_url": "https://api.siliconflow.cn/v1",
        "model": "deepseek-ai/DeepSeek-V3.2",
        "claims_prompt_id": "judge_claims_zh_v1",
        "rubric_prompt_id": rubric_prompt_id,
        "temperature": 0.0,
        "max_tokens": 1024,
        "max_context_chars": 3000,
        "enable_rubric": True,
        "max_claims": 12,
    }


def test_baseline_hash_is_unchanged_without_judge():
    """D14 红线(judge 版): 只跑检索的历史 run 指纹不受 judge 代码影响。"""
    snap = make_snapshot()
    assert "judge" not in snap
    assert snapshot_hash(snap) == WANT_CONFIG_HASH_BASELINE


def test_judge_hash_matches_go_side():
    snap = make_snapshot(judge=judge_section())
    assert snapshot_hash(snap) == WANT_CONFIG_HASH_WITH_JUDGE_V2
    assert set(snap["judge"]) == {
        "provider", "base_url", "model", "claims_prompt_id", "rubric_prompt_id",
        "temperature", "max_tokens", "max_context_chars", "enable_rubric", "max_claims",
    }
    assert snap["judge"]["temperature"] == 0, "整数温度归一化为 int(与 Go 的 JSON 表示一致)"
    assert snap["judge"]["enable_rubric"] is True
    assert snap["judge"]["max_claims"] == 12


def test_generation_and_judge_together_match_go_side():
    """复合场景才是真实用法(既生成又判定), 必须单独锁一组常量。"""
    snap = make_snapshot(generation=generation_section(), judge=judge_section())
    assert snapshot_hash(snap) == WANT_CONFIG_HASH_WITH_GENERATION_AND_JUDGE_V2
    assert set(snap) >= {"generation", "judge"}


def test_judge_v1_config_stays_reproducible():
    """向后兼容红线: rubric v1 的组合指纹必须不变 —— run #155 的快照里记录的就是 v1。"""
    v1_only = make_snapshot(judge=judge_section("judge_rubric_zh_v1"))
    v1_both = make_snapshot(generation=generation_section(), judge=judge_section("judge_rubric_zh_v1"))

    assert snapshot_hash(v1_only) == WANT_CONFIG_HASH_WITH_JUDGE_V1
    assert snapshot_hash(v1_both) == WANT_CONFIG_HASH_WITH_GENERATION_AND_JUDGE_V1
    # 换 rubric 版本必须换指纹(否则"换了裁判"这件事在报告里看不出来)
    assert snapshot_hash(v1_only) != snapshot_hash(make_snapshot(judge=judge_section()))


def test_judge_section_is_sensitive_to_every_experiment_field():
    base = snapshot_hash(make_snapshot(judge=judge_section()))
    for field, value in (
            ("model", "Qwen/Qwen3-8B"),
            ("claims_prompt_id", "judge_claims_zh_v2"),
            ("rubric_prompt_id", "judge_rubric_zh_v1"),
            ("temperature", 0.5),
            ("max_tokens", 256),
            ("max_context_chars", 1500),
            ("enable_rubric", False),
            ("max_claims", 3),
            ("provider", "deepseek"),
            ("base_url", "https://api.deepseek.com"),
    ):
        section = judge_section()
        section[field] = value
        assert snapshot_hash(make_snapshot(judge=section)) != base, f"{field} 未参与指纹"


def test_default_settings_use_rubric_v2():
    """默认值必须与 Go 侧 DefaultJudge 一致, 否则两侧默认提交会得到不同指纹。"""
    assert Settings(_env_file=None).judge_rubric_prompt_id == DEFAULT_RUBRIC_PROMPT_ID


def test_judge_prompt_ids_are_recorded_independently():
    """claims 与 rubric 是两套可独立演进的资产: 换掉其中一个必须产生新指纹。"""
    base = snapshot_hash(make_snapshot(judge=judge_section()))
    only_claims = dict(judge_section(), claims_prompt_id="judge_claims_zh_v2")
    only_rubric = dict(judge_section(), rubric_prompt_id="judge_rubric_zh_v1")

    assert snapshot_hash(make_snapshot(judge=only_claims)) != base
    assert snapshot_hash(make_snapshot(judge=only_rubric)) != base
    assert snapshot_hash(make_snapshot(judge=only_claims)) != snapshot_hash(make_snapshot(judge=only_rubric))