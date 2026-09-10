"""实验配置快照与指纹(process.md D7 可复现四件套)。

可复现一次 run 需要四样东西齐备:
1. 代码版本      -> git_sha
2. 配置          -> config_snapshot(全量) + config_hash(规范化指纹, 用于"同配置"检索与 A/B 对比)
3. 数据          -> dataset_id + corpus_id
4. 模型/协议版本 -> embedding model + dim(+ M4 起的 judge model / prompt 版本)

注意区分两个指纹:
- chunking_hash : 只标识"切分方案", 用于 Qdrant collection 命名(索引隔离);
- config_hash   : 标识"整个实验配置", 用于 runs 表(实验对比)。
"""
from __future__ import annotations

import hashlib
import json
import os
import subprocess
from pathlib import Path
from typing import Any

from app.retrieval.chunker import ChunkingConfig


def git_sha(repo_root: str | Path | None = None) -> str:
    """当前代码版本(短 sha)。优先取环境变量 EVAL_GIT_SHA(容器/CI 场景), 否则调 git。"""
    env_value = os.getenv("EVAL_GIT_SHA")
    if env_value:
        return env_value

    root = Path(repo_root) if repo_root is not None else Path(__file__).resolve().parents[3]
    try:
        completed = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=root,
            capture_output=True,
            text=True,
            timeout=5,
            check=True,
        )
    except (OSError, subprocess.SubprocessError):
        return "unknown"
    return completed.stdout.strip() or "unknown"


def build_config_snapshot(
        *,
        settings: Any,
        cfg: ChunkingConfig,
        corpus_id: int,
        dataset_id: int,
        top_k: int,
        reranker_enabled: bool = False,
        extras: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """构建"这次 run 用了什么配置"的全量快照。"""
    return {
        "chunking": {
            "strategy": cfg.strategy,
            "chunk_size": cfg.chunk_size,
            "overlap": cfg.overlap,
            "min_chars": cfg.min_chars,
        },
        "embedding": {
            "provider": "siliconflow",
            "base_url": settings.embedding_base_url,
            "model": settings.embedding_model,
            "dim": settings.embedding_dim,
            "batch_size": settings.embedding_batch_size,
        },
        "retrieval": {"top_k": top_k, "reranker": {"enabled": reranker_enabled}},
        "data": {"corpus_id": corpus_id, "dataset_id": dataset_id},
        "extras": extras or {},
    }


def snapshot_hash(snapshot: dict[str, Any]) -> str:
    """配置指纹: 键序无关, 任一字段变化必产生新指纹(同指纹 = 同配置, 可定位历史 run)。"""
    payload = json.dumps(snapshot, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()