-- 0001_init.sql —— 初始 schema（与设计文档第八章一致）
--
-- 约定：
--   * 时间统一用 Unix 秒（INTEGER），避免 SQLite 日期字符串比较的坑；
--   * 布尔用 INTEGER 0/1；
--   * 所有业务表带 user_id，多租户隔离在 SQL 层强制。

-- === 用户 ===
CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,              -- usr_ + UUID v7（时间有序）
  username      TEXT NOT NULL UNIQUE,
  email         TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,                 -- bcrypt cost=12
  role          TEXT NOT NULL DEFAULT 'user',  -- admin | user
  client_id     TEXT NOT NULL UNIQUE,          -- 子域名标识，10 位 base32
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_client_id ON users(client_id);

-- === 来源 IP 归属（UDP/TCP 53 场景）===
CREATE TABLE IF NOT EXISTS user_ips (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  cidr    TEXT NOT NULL,                       -- "203.0.113.0/24" 或 "198.51.100.7/32"
  PRIMARY KEY (user_id, cidr)
);
CREATE INDEX IF NOT EXISTS idx_user_ips_cidr ON user_ips(cidr);

-- === 自定义规则 ===
CREATE TABLE IF NOT EXISTS rules (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,                    -- block|allow|rewrite_a|rewrite_aaaa|rewrite_cname
  pattern    TEXT NOT NULL,                    -- ||ads.com^ / example.com / *.dev.local
  value      TEXT NOT NULL DEFAULT '',         -- 改写目标
  qtypes     TEXT NOT NULL DEFAULT '',         -- "A,AAAA"，空表示全部
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rules_user ON rules(user_id, enabled);

-- === 订阅 ===
CREATE TABLE IF NOT EXISTS subscriptions (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  list_type     TEXT NOT NULL,                 -- blocklist | allowlist
  name          TEXT NOT NULL,
  url           TEXT NOT NULL,
  format        TEXT NOT NULL DEFAULT 'auto',  -- auto|adblock|hosts|dnsmasq|domain
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_etag     TEXT NOT NULL DEFAULT '',
  last_modified TEXT NOT NULL DEFAULT '',
  last_fetched  INTEGER NOT NULL DEFAULT 0,
  last_count    INTEGER NOT NULL DEFAULT 0,
  last_status   TEXT NOT NULL DEFAULT '',      -- ok | error:xxx
  fail_count    INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  UNIQUE(user_id, url)
);
CREATE INDEX IF NOT EXISTS idx_subs_user ON subscriptions(user_id, enabled);
CREATE INDEX IF NOT EXISTS idx_subs_enabled ON subscriptions(enabled);

-- === 订阅条目（解析后扁平化）===
CREATE TABLE IF NOT EXISTS sub_entries (
  sub_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  domain TEXT NOT NULL,                        -- 规范化域名（小写、无尾点）
  kind   TEXT NOT NULL,                        -- block | allow
  PRIMARY KEY (sub_id, domain)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_sub_entries_domain ON sub_entries(domain);

-- === 统计聚合（5 分钟粒度）===
CREATE TABLE IF NOT EXISTS query_rollup (
  user_id TEXT NOT NULL,
  ts      INTEGER NOT NULL,                    -- 5 分钟对齐的 Unix 秒
  total   INTEGER NOT NULL DEFAULT 0,
  blocked INTEGER NOT NULL DEFAULT 0,
  cached  INTEGER NOT NULL DEFAULT 0,
  allowed INTEGER NOT NULL DEFAULT 0,
  latency_sum INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_rollup_ts ON query_rollup(ts);

-- === TOP 域名 ===
CREATE TABLE IF NOT EXISTS top_domains (
  user_id TEXT NOT NULL,
  ts      INTEGER NOT NULL,
  domain  TEXT NOT NULL,
  hits    INTEGER NOT NULL DEFAULT 0,
  blocked INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, ts, domain)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_top_ts ON top_domains(ts);

-- === 查询日志 ===
CREATE TABLE IF NOT EXISTS query_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    TEXT NOT NULL,
  ts         INTEGER NOT NULL,
  domain     TEXT NOT NULL,
  qtype      TEXT NOT NULL DEFAULT '',
  rcode      TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL DEFAULT '',          -- allowed|blocked|cached|rewritten
  client     TEXT NOT NULL DEFAULT '',          -- 来源 IP（脱敏后的 /24）
  latency_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_qlog_user_ts ON query_log(user_id, ts DESC);

-- === 元信息 ===
CREATE TABLE IF NOT EXISTS meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);

-- === 在线更新清单 ===
CREATE TABLE IF NOT EXISTS app_versions (
  channel     TEXT PRIMARY KEY,                 -- stable | beta
  version     TEXT NOT NULL,
  url         TEXT NOT NULL,
  sha256      TEXT NOT NULL,
  notes       TEXT NOT NULL DEFAULT '',
  released_at INTEGER NOT NULL DEFAULT 0
);

-- === Refresh Token 撤销表 ===
CREATE TABLE IF NOT EXISTS refresh_tokens (
  jti        TEXT PRIMARY KEY,                  -- JWT ID
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_rt_user ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_rt_expires ON refresh_tokens(expires_at);

-- === 长期 API Token（PAT）===
-- 设计文档第十章要求「前后端对接的兜底方式」，原文未给 DDL，此处补齐。
-- 只保存 SHA-256，原文仅在创建时返回一次。
CREATE TABLE IF NOT EXISTS pat_tokens (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,              -- sha256 hex
  prefix     TEXT NOT NULL DEFAULT '',          -- agx_ + 前 6 位，用于 UI 辨识
  created_at INTEGER NOT NULL,
  last_used  INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL DEFAULT 0         -- 0 表示永不过期
);
CREATE INDEX IF NOT EXISTS idx_pat_user ON pat_tokens(user_id);

-- === 审计日志（管理接口越权访问等）===
CREATE TABLE IF NOT EXISTS audit_log (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  ts      INTEGER NOT NULL,
  user_id TEXT NOT NULL DEFAULT '',
  action  TEXT NOT NULL,
  detail  TEXT NOT NULL DEFAULT '',
  ip      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts DESC);
