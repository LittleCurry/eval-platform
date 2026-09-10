"""配置快照与指纹单测(离线): 结构完整性 / 指纹稳定性与敏感性 / git_sha 降级。"""
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