-- 000003_add_experiment_tables(up): 实验/任务/结果表(M2-5 落库; M3 队列与断点续跑; M5 A/B 对比)
CREATE TABLE runs (
                      id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                      project_id      BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
                      dataset_id      BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
                      corpus_id       BIGINT REFERENCES corpora(id) ON DELETE SET NULL,
                      status          TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
                      config_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
                      config_hash     TEXT NOT NULL DEFAULT '',
                      git_sha         TEXT NOT NULL DEFAULT '',
                      metrics         JSONB NOT NULL DEFAULT '{}'::jsonb,
                      created_by      BIGINT REFERENCES users(id) ON DELETE SET NULL,
                      error           TEXT,
                      started_at      TIMESTAMPTZ,
                      finished_at     TIMESTAMPTZ,
                      created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
                      updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_runs_project_id  ON runs (project_id);
CREATE INDEX idx_runs_dataset_id  ON runs (dataset_id);
CREATE INDEX idx_runs_config_hash ON runs (config_hash);

CREATE TABLE jobs (
                      id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                      run_id       BIGINT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
                      status       TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
                      progress     JSONB NOT NULL DEFAULT '{}'::jsonb,
                      heartbeat_at TIMESTAMPTZ,
                      error        TEXT,
                      created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
                      updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_jobs_run_id ON jobs (run_id);
CREATE INDEX idx_jobs_status ON jobs (status);

CREATE TABLE job_items (
                           id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                           job_id      BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
                           case_id     BIGINT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
                           status      TEXT NOT NULL DEFAULT 'pending'
                               CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
                           retry_count INT NOT NULL DEFAULT 0,
                           last_error  TEXT,
                           created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                           updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
                           UNIQUE (job_id, case_id)
);

CREATE INDEX idx_job_items_job_status ON job_items (job_id, status);

CREATE TABLE case_results (
                              id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                              run_id     BIGINT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
                              case_id    BIGINT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
                              retrieved  JSONB NOT NULL DEFAULT '[]'::jsonb,
                              metrics    JSONB NOT NULL DEFAULT '{}'::jsonb,
                              flags      JSONB NOT NULL DEFAULT '[]'::jsonb,
                              latency_ms INT,
                              created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                              updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                              UNIQUE (run_id, case_id)
);

CREATE INDEX idx_case_results_run_id ON case_results (run_id);