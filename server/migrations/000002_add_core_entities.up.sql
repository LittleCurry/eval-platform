-- 000002_add_core_entities(up): 核心业务表(M1)
-- 设计要点:
--   * id 统一 BIGINT 自增 (GENERATED ALWAYS AS IDENTITY)
--   * 删除策略: 项目被删 → 其下语料/数据集级联删除; created_by 用 SET NULL 保留历史
--   * gold_anchors 用 JSONB(结构灵活, 校验在导入层做, 见 D1)
--   * UNIQUE 约束在 DB 层兜底重复数据(第二道防线, 第一道在导入校验)

CREATE TABLE users (
                       id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                       email         TEXT NOT NULL UNIQUE,
                       name          TEXT NOT NULL DEFAULT '',
                       password_hash TEXT NOT NULL DEFAULT '',   -- M7 登录时启用
                       role          TEXT NOT NULL DEFAULT 'viewer'
                           CHECK (role IN ('admin', 'editor', 'viewer')),
                       created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
                       updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE projects (
                          id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                          name        TEXT NOT NULL,
                          description TEXT NOT NULL DEFAULT '',
                          created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
                          created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                          updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                          UNIQUE (name)
);

CREATE TABLE corpora (
                         id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                         project_id  BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
                         name        TEXT NOT NULL,
                         source_type TEXT NOT NULL DEFAULT 'manual'
                             CHECK (source_type IN ('manual', 'upload', 'external')),
                         created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
                         created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                         updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                         UNIQUE (project_id, name)
);

CREATE TABLE documents (
                           id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                           corpus_id  BIGINT NOT NULL REFERENCES corpora(id) ON DELETE CASCADE,
                           doc_id     TEXT NOT NULL,   -- 业务标识 = 语料文档 frontmatter 的 doc_id (如 A01)
                           title      TEXT NOT NULL,
                           raw_text   TEXT NOT NULL,
                           meta       JSONB NOT NULL DEFAULT '{}'::jsonb,  -- 板块/tags/版本等 frontmatter
                           created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                           updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                           UNIQUE (corpus_id, doc_id)
);

CREATE TABLE datasets (
                          id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                          project_id  BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
                          name        TEXT NOT NULL,
                          description TEXT NOT NULL DEFAULT '',
                          created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
                          created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                          updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                          UNIQUE (project_id, name)
);

CREATE TABLE cases (
                       id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                       dataset_id       BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
                       qid              TEXT NOT NULL,   -- 数据集内唯一的问题编号
                       question         TEXT NOT NULL,
                       gold_anchors     JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{doc, span, ...}] 见 D1
                       reference_answer TEXT,
                       category         TEXT,
                       difficulty       TEXT CHECK (difficulty IN ('易', '中', '难')),
                       notes            TEXT,
                       created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
                       updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
                       UNIQUE (dataset_id, qid)
);

-- FK 不会自动建索引, 手动补齐(按子表外键查询是主路径)
CREATE INDEX idx_corpora_project_id  ON corpora (project_id);
CREATE INDEX idx_documents_corpus_id ON documents (corpus_id);
CREATE INDEX idx_datasets_project_id ON datasets (project_id);
CREATE INDEX idx_cases_dataset_id    ON cases (dataset_id);