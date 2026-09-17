-- 回滚 000009: 去掉认证补列。
-- 注意: 账号本身(users 行)保留 —— 认证是"能不能登录", 删列不该连带删掉操作历史的主体。
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_lower_check;
DROP INDEX IF EXISTS idx_users_enabled;
ALTER TABLE users DROP COLUMN IF EXISTS last_login_at;
ALTER TABLE users DROP COLUMN IF EXISTS disabled;
