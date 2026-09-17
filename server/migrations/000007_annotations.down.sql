-- 回滚 M6: 删除标注表。
-- 标注是人工判断, 不参与 run 的复现(指标与归因都在 runs/case_results 上), 删除它不影响历史实验。
DROP TABLE IF EXISTS annotations;