-- 回滚 M5-2: 删除配置模板表。
-- 模板只是配置的便利封装, 不参与任何 run 的复现(指纹与快照都在 runs 上),
-- 因此删除它不会影响历史实验的可复现性。
DROP TABLE IF EXISTS pipeline_profiles;