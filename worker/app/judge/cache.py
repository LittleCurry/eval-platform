"""judge 结果缓存(D5/D8): 按 (stage, prompt_hash, model, payload) 复用判定, 避免重跑烧钱。

三条设计要点:
1. **缓存键必须包含 contexts**(claims 阶段): 同一答案在不同检索上下文下的判定**本来就该不同**。
   漏掉 contexts 会把 A 上下文的判定用到 B 上 —— 幻觉率直接失真且无人察觉。
   同理必须含 prompt_hash 与 model: 换了 prompt/模型就是换了裁判。
2. **命中不重复计费**: 复用时不产生 token 消耗, 但 run 指标要单独报 cache 命中数,
   否则成本被高估、也会掩盖"缓存没生效"。
3. **首写为准(INSERT ... ON CONFLICT DO NOTHING)**: 同一 payload 第二次写入被忽略。
   这让重复实验/续跑拿到**同一份判定** —— judge 本身有非确定性(见 M4-1 实测: 温度 0 下答案逐字相同率仅 57%),
   缓存是把它变成"可复现"的最直接手段。

实现分三档, 便于离线测试与对照实验:
- `JudgeCache`: 协议(接口)
- `NullJudgeCache`: 关闭缓存
- `InMemoryJudgeCache`: 单测用(零 IO)
- `PostgresJudgeCache`: 生产用(judge_cache 表)
"""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from typing import Any, Protocol

import psycopg

# 缓存阶段名(与 case_results.judge.meta 的 stage 计数对应)
STAGE_CLAIMS = "claims"
STAGE_RUBRIC = "rubric"


@dataclass(frozen=True)
class CacheEntry:
    response: dict[str, Any]
    prompt_tokens: int = 0
    completion_tokens: int = 0
    hits: int = 0


class JudgeCache(Protocol):
    def get(self, key: str) -> CacheEntry | None: ...

    def put(
            self,
            *,
            key: str,
            stage: str,
            model: str,
            prompt_hash: str,
            response: dict[str, Any],
            prompt_tokens: int = 0,
            completion_tokens: int = 0,
    ) -> None: ...


def judge_cache_key(*, stage: str, prompt_hash: str, model: str, payload: dict[str, Any]) -> str:
    """缓存键 = sha256(键排序紧凑 JSON of (stage, prompt_hash, model, payload))。

    与项目其它指纹同风格(sort_keys + ensure_ascii=False + 紧凑分隔符), 保证跨进程稳定。
    """
    canonical = json.dumps(
        {"stage": stage, "prompt_hash": prompt_hash, "model": model, "payload": payload},
        sort_keys=True,
        ensure_ascii=False,
        separators=(",", ":"),
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


class NullJudgeCache:
    """关闭缓存(对照实验/排障用): 永远 miss, 写入丢弃。"""

    def get(self, key: str) -> CacheEntry | None:
        return None

    def put(self, **kwargs: Any) -> None:
        return None


class InMemoryJudgeCache:
    """内存缓存: 离线单测用, 同时便于断言"到底调了几次模型"。"""

    def __init__(self) -> None:
        self.entries: dict[str, CacheEntry] = {}
        self.put_calls = 0
        self.hit_count = 0

    def get(self, key: str) -> CacheEntry | None:
        entry = self.entries.get(key)
        if entry is None:
            return None
        self.hit_count += 1
        bumped = CacheEntry(
            response=entry.response,
            prompt_tokens=entry.prompt_tokens,
            completion_tokens=entry.completion_tokens,
            hits=entry.hits + 1,
        )
        self.entries[key] = bumped
        return bumped

    def put(
            self,
            *,
            key: str,
            stage: str,
            model: str,
            prompt_hash: str,
            response: dict[str, Any],
            prompt_tokens: int = 0,
            completion_tokens: int = 0,
    ) -> None:
        self.put_calls += 1
        if key in self.entries:  # 首写为准
            return
        self.entries[key] = CacheEntry(
            response=response, prompt_tokens=prompt_tokens, completion_tokens=completion_tokens
        )


class PostgresJudgeCache:
    """生产缓存: judge_cache 表(见 migrations/000005)。"""

    def __init__(self, dsn: str) -> None:
        self.dsn = dsn

    def get(self, key: str) -> CacheEntry | None:
        with psycopg.connect(self.dsn, connect_timeout=5) as conn, conn.cursor() as cur:
            cur.execute(
                """
                UPDATE judge_cache
                SET hits = hits + 1, last_hit_at = now()
                WHERE cache_key = %s
                    RETURNING response::text, prompt_tokens, completion_tokens, hits
                """,
                (key,),
            )
            row = cur.fetchone()
        if row is None:
            return None
        return CacheEntry(
            response=json.loads(row[0]),
            prompt_tokens=int(row[1] or 0),
            completion_tokens=int(row[2] or 0),
            hits=int(row[3] or 0),
        )

    def put(
            self,
            *,
            key: str,
            stage: str,
            model: str,
            prompt_hash: str,
            response: dict[str, Any],
            prompt_tokens: int = 0,
            completion_tokens: int = 0,
    ) -> None:
        with psycopg.connect(self.dsn, connect_timeout=5) as conn, conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO judge_cache
                (cache_key, stage, model, prompt_hash, response, prompt_tokens, completion_tokens)
                VALUES (%s, %s, %s, %s, %s::jsonb, %s, %s)
                    ON CONFLICT (cache_key) DO NOTHING
                """,
                (key, stage, model, prompt_hash, json.dumps(response, ensure_ascii=False),
                 prompt_tokens, completion_tokens),
            )


def cache_stats(dsn: str) -> dict[str, int]:
    """缓存总览(运维/测试用): 条目数与总命中次数。"""
    with psycopg.connect(dsn, connect_timeout=5) as conn, conn.cursor() as cur:
        cur.execute("SELECT count(*), COALESCE(sum(hits), 0) FROM judge_cache")
        row = cur.fetchone()
    return {"entries": int(row[0] or 0), "hits": int(row[1] or 0)}