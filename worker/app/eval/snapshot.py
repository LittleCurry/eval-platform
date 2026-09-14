"""实验配置快照与指纹(process.md D7 可复现四件套)。

可复现一次 run 需要四样东西齐备:
1. 代码版本      -> git_sha
2. 配置          -> config_snapshot(全量) + config_hash(规范化指纹, 用于"同配置"检索与 A/B 对比)
3. 数据          -> dataset_id + corpus_id
4. 模型/协议版本 -> embedding model + dim + (M4 起) generation/judge 的模型与 prompt 版本

注意区分两个指纹:
- chunking_hash : 只标识"切分方案", 用于 Qdrant collection 命名(索引隔离);
- config_hash   : 标识"整个实验配置", 用于 runs 表(实验对比)。

D14 规则(M4 起): `generation` 与 `judge` 两段**只在启用时写入** ——
不启用时快照必须字节级不变, 否则 M2/M3 全部历史 run 的 config_hash 作废;
启用时它们是一枚可区分的新指纹(那本来就是另一场实验)。
"""
from __future__ import annotations

import hashlib
import json
import os
import subprocess
from collections.abc import Mapping
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


def normalize_float(value: float) -> int | float:
    """整数化浮点: 0.0 -> 0。

    跨语言指纹的第一个浮点陷阱: Go 的 encoding/json 把 0.0 编成 `0`, Python 的 json 编成 `0.0`,
    同一份配置在两侧会算出不同的 config_hash。统一"整数化的浮点写成整数",
    非整数(如 0.3)两侧的短表示一致("0.3"), 于是指纹可对齐。
    """
    number = float(value)
    return int(number) if number.is_integer() else number


def build_config_snapshot(
        *,
        settings: Any,
        cfg: ChunkingConfig,
        corpus_id: int,
        dataset_id: int,
        top_k: int,
        reranker_enabled: bool = False,
        extras: dict[str, Any] | None = None,
        generation: dict[str, Any] | None = None,
        judge: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """构建"这次 run 用了什么配置"的全量快照。

    `generation` / `judge` 为 None 时**不写对应键**(D14): 只跑检索的历史 run 指纹保持不变。
    """
    snapshot: dict[str, Any] = {
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
    if generation is not None:
        snapshot["generation"] = build_generation_section(generation)
    if judge is not None:
        snapshot["judge"] = build_judge_section(judge)
    return snapshot


def build_generation_section(generation: Mapping[str, Any]) -> dict[str, Any]:
    """规范化生成段(字段名/类型必须与 Go 侧 eval.GenerationConfig 一一对应)。"""
    return {
        "provider": str(generation["provider"]),
        "base_url": str(generation["base_url"]),
        "model": str(generation["model"]),
        "prompt_id": str(generation["prompt_id"]),
        "temperature": normalize_float(generation.get("temperature", 0.0)),
        "max_tokens": int(generation.get("max_tokens", 512)),
        "max_context_chars": int(generation.get("max_context_chars", 3000)),
    }


def build_judge_section(judge: Mapping[str, Any]) -> dict[str, Any]:
    """规范化 judge 段(必须与 Go 侧 eval.JudgeConfig 一一对应)。

    两个 prompt id 分开记录: claims 与 rubric 是**两套**可独立演进的资产,
    各自的 hash 落在 case_results.judge.meta 里, 但快照里要能看出"这次用了哪两个版本"。
    `max_claims` 是成本护栏, 也属于实验参数(改了截断行为就变了), 因此进快照。
    """
    return {
        "provider": str(judge["provider"]),
        "base_url": str(judge["base_url"]),
        "model": str(judge["model"]),
        "claims_prompt_id": str(judge.get("claims_prompt_id", "")),
        "rubric_prompt_id": str(judge.get("rubric_prompt_id", "")),
        "temperature": normalize_float(judge.get("temperature", 0.0)),
        "max_tokens": int(judge.get("max_tokens", 1024)),
        "max_context_chars": int(judge.get("max_context_chars", 3000)),
        "enable_rubric": bool(judge.get("enable_rubric", True)),
        "max_claims": int(judge.get("max_claims", 12)),
    }


def snapshot_hash(snapshot: dict[str, Any]) -> str:
    """配置指纹: 键序无关, 任一字段变化必产生新指纹(同指纹 = 同配置, 可定位历史 run)。"""
    payload = json.dumps(snapshot, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()