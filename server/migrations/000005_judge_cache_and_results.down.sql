DROP INDEX IF EXISTS idx_judge_cache_stage;
DROP TABLE IF EXISTS judge_cache;
ALTER TABLE case_results DROP COLUMN IF EXISTS judge;