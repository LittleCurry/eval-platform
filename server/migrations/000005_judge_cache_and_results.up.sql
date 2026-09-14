-- M4-2: judge 结果落库 + judge 缓存(D5/D8)。
--
-- 幂等说明: 本迁移用 IF NOT EXISTS —— 因为 (a) 全新库要能建出这些对象,
-- (b) 已经手工执行过等价 DDL 的环境(本项目开发期发生过)再跑一遍也不该失败。
-- 注意: IF NOT EXISTS 会**静默跳过形状不一致的对象**, 所以下面第 4 节的核对 SQL
-- 应当在新环境上跑一次, 确认列/索引与设计一致。
ALTER TABLE case_results ADD COLUMN IF NOT EXISTS judge JSONB NOT NULL DEFAULT '{}'::jsonb;

-- judge 缓存键 = sha256(键排序紧凑 JSON of (stage, prompt_hash, model, payload))
--   stage: claims | rubric
--   payload 里**必须含 contexts**(同一答案在不同检索上下文下的判定本来就不同),
--           否则会串味, 幻觉率直接失真。
CREATE TABLE IF NOT EXISTS judge_cache (
                                           id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                                           cache_key         TEXT NOT NULL UNIQUE,
                                           stage             TEXT NOT NULL,
                                           model             TEXT NOT NULL,
                                           prompt_hash       TEXT NOT NULL,
                                           response          JSONB NOT NULL,           -- judge 的已解析结果(复用时不重新解析)
                                           prompt_tokens     INT NOT NULL DEFAULT 0,   -- 首次调用时的真实消耗(命中不重复计费)
                                           completion_tokens INT NOT NULL DEFAULT 0,
                                           hits              INT NOT NULL DEFAULT 0,
                                           created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_hit_at       TIMESTAMPTZ
    );
CREATE INDEX IF NOT EXISTS idx_judge_cache_stage ON judge_cache (stage);