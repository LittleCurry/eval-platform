-- 000009_user_auth(up): 认证与 RBAC(M7-1)
--
-- users 表在 M1 就建好了(email/name/password_hash/role), 当时 password_hash 留空等 M7 启用。
-- 这一版只补认证真正需要的三个东西, 不加"将来可能有用"的字段:
--   * disabled      —— 停用账号(同事离职/换岗时要能立刻断掉, 而不是删行丢历史);
--   * last_login_at —— 出问题时能看出"这个账号还在被用吗";
--   * email 小写约束 —— 登录是拿 email 查唯一行的, Alice@x.com 与 alice@x.com 必须
--     是同一个人, 否则会出现"注册成功但登录不上"这种最难查的问题。
--
-- 为什么不做 refresh token / 会话表: 内部工具, 单实例部署, token 12 小时够用;
-- 无状态 JWT 的代价是**无法服务端撤销**(只能改 JWT_SECRET 让所有 token 失效),
-- 这个代价写进 process.md 的决策记录里, 而不是靠"以后再加"含糊过去。

ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;

-- 历史数据里可能有大小写混杂的 email(理论上没有: 表之前是空的), 先归一化再上约束,
-- 否则迁移会直接失败在 CHECK 上。
UPDATE users SET email = lower(btrim(email)) WHERE email <> lower(btrim(email));

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_lower_check;
ALTER TABLE users ADD CONSTRAINT users_email_lower_check
    CHECK (email = lower(btrim(email)) AND email <> '');

-- 登录查询走 email(唯一索引已存在), 停用过滤走部分索引
CREATE INDEX IF NOT EXISTS idx_users_enabled ON users (role) WHERE NOT disabled;

-- 核对: \d users
