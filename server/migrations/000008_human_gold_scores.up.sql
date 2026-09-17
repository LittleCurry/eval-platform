-- M6: 人工金标打分(human_gold_scores)。
--
-- 为什么金标挂在 (run, case) 而不是只挂 case:
--   judge 评的是**某次 run 产出的那份答案**(claims 与 rubric 都基于当时的检索上下文),
--   所以"自动评测 vs 人工金标"的校准必须逐 (run, case) 对齐 —— 拿题级金标去比对
--   某次 run 的 judge 输出, 比的是两个不同对象。
--
-- 为什么 annotator 进唯一键: 双人独立打分的意义就是**金标本身可信吗**。
--   UNIQUE (run_id, case_id, annotator) 允许两人各打一份, 校准报告里再算
--   inter-annotator agreement(人工之间都不一致时, 先别急着怪 judge)。
--
-- verdict 是三值: faithful / hallucinated / unclear —— "看不清"必须能表达,
--   否则标注员会被迫在二选一里瞎猜, 而这会直接污染 κ。
-- relevance/helpfulness 可空: 只判"有没有幻觉"、不判分是完全合法的用法。
CREATE TABLE IF NOT EXISTS human_gold_scores
(
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id  BIGINT      NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    run_id      BIGINT      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    case_id     BIGINT      NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    annotator   TEXT        NOT NULL,
    verdict     TEXT        NOT NULL DEFAULT 'unclear',
    relevance   INT,
    helpfulness INT,
    note        TEXT        NOT NULL DEFAULT '',
    reviewed    BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT human_gold_verdict_check CHECK (verdict IN ('faithful', 'hallucinated', 'unclear')),
    CONSTRAINT human_gold_relevance_check CHECK (relevance IS NULL OR (relevance BETWEEN 1 AND 5)),
    CONSTRAINT human_gold_helpfulness_check CHECK (helpfulness IS NULL OR (helpfulness BETWEEN 1 AND 5)),
    CONSTRAINT human_gold_annotator_not_blank CHECK (btrim(annotator) <> ''),
    CONSTRAINT human_gold_key UNIQUE (run_id, case_id, annotator)
    );

CREATE INDEX IF NOT EXISTS idx_human_gold_run ON human_gold_scores (run_id);
CREATE INDEX IF NOT EXISTS idx_human_gold_case ON human_gold_scores (case_id);

-- 核对: \d human_gold_scores