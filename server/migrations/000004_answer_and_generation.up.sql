-- M4-1: 生成链路 —— answer 与生成元信息挂在 case_results 上。
-- 为什么挂这里: 它是"一次 run × 一道题"的唯一行, 已有 UNIQUE (run_id, case_id) + ON CONFLICT DO UPDATE
-- 的幂等 upsert(checkpoint 语义), answer 跟着同一事务写即天然可续跑; 新建表要再处理一次先后与幂等。
-- generation 字段约定(不写 prompt 全文, 只写 id/hash):
--   provider / base_url / model / prompt_id / prompt_hash / temperature / max_tokens
--   / context_chunks / context_chars / prompt_tokens / completion_tokens / latency_ms
ALTER TABLE case_results ADD COLUMN answer     TEXT;
ALTER TABLE case_results ADD COLUMN generation JSONB NOT NULL DEFAULT '{}'::jsonb;