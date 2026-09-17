-- M6: Bad Case 标注(annotations)。
--
-- 设计要点:
-- 1) **标注挂在 run 上**: 工作台是从某次 run 的报告/flag 过滤进来的, "这道题在这次实验里
--    表现如何"才有意义; 跨 run 的结论由 M6 的闭环对比(改配置重跑后看同一题是否变好)给出,
--    而不是让标注脱离 run 漂浮。因此 run_id NOT NULL + UNIQUE(run_id, case_id):
--    同一题在同一次 run 里只有一条标注, 工作台再点一次是"更新"而不是"新增"。
-- 2) **状态流转有约束**(open → fixed → verified, wontfix = 确认不修): CHECK 只兜住枚举,
--    合法迁移由服务端状态机把关(见 http/annotations.go 的 canTransition) —— 数据库不适合
--    表达"从 verified 不能直接跳 fixed"这种业务规则, 否则改规则就要写迁移。
-- 3) **reason 是人工确认的归因**, 取值刻意与 M4-3 的标签族对齐
--    (retrieval/hallucination/generation/dataset), 另加 unknown 表示"看过了但说不清";
--    空串 = 还没归类。这样 M6 的校准报告可以把"机器标签"与"人工归因"放进同一张混淆矩阵。
CREATE TABLE IF NOT EXISTS annotations
(
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id BIGINT      NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    run_id     BIGINT      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    case_id    BIGINT      NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    status     TEXT        NOT NULL DEFAULT 'open',
    reason     TEXT        NOT NULL DEFAULT '',
    comment    TEXT        NOT NULL DEFAULT '',
    assignee   TEXT        NOT NULL DEFAULT '',
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT annotations_status_check CHECK (status IN ('open', 'fixed', 'verified', 'wontfix')),
    CONSTRAINT annotations_reason_check CHECK (reason IN ('', 'retrieval', 'hallucination', 'generation', 'dataset', 'unknown')),
    CONSTRAINT annotations_run_case_key UNIQUE (run_id, case_id)
    );

CREATE INDEX IF NOT EXISTS idx_annotations_run ON annotations (run_id);
CREATE INDEX IF NOT EXISTS idx_annotations_case ON annotations (case_id);
CREATE INDEX IF NOT EXISTS idx_annotations_status ON annotations (status);

-- 核对: \d annotations