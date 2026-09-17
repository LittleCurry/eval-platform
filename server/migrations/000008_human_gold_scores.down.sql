-- 回滚 M6: 删除人工金标表。
-- 金标是人工判断, 不参与 run 的复现(指标在 runs/case_results 上), 删除不影响历史实验。
DROP TABLE IF EXISTS human_gold_scores;