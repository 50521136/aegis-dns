-- 0002_global_layer.sql —— 全局规则层、用户偏好、反馈
--
-- 设计取舍：全局规则/订阅**不新建表**，而是给现有表加 is_global 标记。
--
-- 原因：rules.user_id / subscriptions.user_id 上有
-- `REFERENCES users(id) ON DELETE CASCADE`，而 DeleteUser 完全依赖这个级联
-- 来清理用户数据。想用 user_id='' 表示全局就得重建这两张表，而
-- sub_entries.sub_id 又引用 subscriptions(id) —— 在 foreign_keys=ON 的迁移
-- 上下文里 DROP 旧表要么报错要么把条目一起删掉。
--
-- ALTER TABLE ADD COLUMN 不触发重建，FK 与级联全部保留，代价只是多一个
-- 布尔列。全局条目归属于创建它的管理员（管理员被删则一并消失，可接受）。

-- === 规则：区分全局与个人 ===
ALTER TABLE rules ADD COLUMN is_global INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_rules_global ON rules(is_global, enabled);

-- === 订阅：同上 ===
ALTER TABLE subscriptions ADD COLUMN is_global INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_subs_global ON subscriptions(is_global, enabled);

-- === 用户偏好：是否叠加全局规则 ===
--
-- 默认 1（叠加）。关掉之后该用户只用自己的规则与订阅，全局层完全不参与匹配，
-- 用于「全局规则误伤了某个用户」时的临时规避。
ALTER TABLE users ADD COLUMN use_global_rules INTEGER NOT NULL DEFAULT 1;

-- === 反馈 ===
--
-- 用户对被拦截/放行的域名有异议时提交，管理员在后台回复与结单。
-- 不做外键到 users：用户被删后反馈仍有审计价值，保留 user_id 与用户名快照。
CREATE TABLE IF NOT EXISTS feedback (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,
  username    TEXT NOT NULL DEFAULT '',
  domain      TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'wrong_block', -- wrong_block | wrong_allow | other
  message     TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',        -- open | replied | closed
  admin_reply TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_feedback_user ON feedback(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_feedback_status ON feedback(status, created_at);
