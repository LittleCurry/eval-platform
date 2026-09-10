-- 000003_add_experiment_tables(down): 按依赖倒序删表
DROP TABLE IF EXISTS case_results;
DROP TABLE IF EXISTS job_items;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS runs;