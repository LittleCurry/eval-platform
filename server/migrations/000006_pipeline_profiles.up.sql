-- M5-2: 配置模板(pipeline_profiles)。
--
-- 模板里放的**只有提交请求的旋钮**: chunking / retrieval.top_k / generation / judge,
-- 不多不少 —— 语料库与评测集是提交时才选的, 因此不属于模板。这样"模板 → 提交请求"
-- 是一个无损的一一映射, 不会出现"模板里存了、提交时却不生效"的幽灵字段。
--
-- 指纹**不落库**: config_hash 含 corpus_id/dataset_id(D7), 而模板没有这两项,
-- 存一个"模板自己的 hash"只会误导(同一个模板配不同评测集会得到不同指纹)。
-- 要看指纹就走 POST /api/v1/pipeline-preview 现算, 它返回的 hash 与真正提交后
-- 落库的 config_hash **逐字相同** —— 这正是"提交前先看指纹"的价值。
CREATE TABLE IF NOT EXISTS pipeline_profiles
(
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id  BIGINT      NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    config      JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pipeline_profiles_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT pipeline_profiles_project_name_key UNIQUE (project_id, name)
    );

CREATE INDEX IF NOT EXISTS idx_pipeline_profiles_project ON pipeline_profiles (project_id);

-- 核对: 新环境上跑一次, 确认列/约束与设计一致(IF NOT EXISTS 会静默跳过形状不符的对象)
--   \d pipeline_profiles