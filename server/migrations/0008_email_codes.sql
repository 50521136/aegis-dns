-- 邮箱验证码。
--
-- 一张表覆盖两个用途（注册验证、找回密码），用 purpose 区分：
-- 分开建表会让「验证码校验」这段逻辑写两遍，而它恰恰是安全相关的。
CREATE TABLE IF NOT EXISTS email_codes (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  email      TEXT NOT NULL,
  code       TEXT NOT NULL,
  purpose    TEXT NOT NULL,                -- register | reset
  expires_at INTEGER NOT NULL,
  used       INTEGER NOT NULL DEFAULT 0,
  attempts   INTEGER NOT NULL DEFAULT 0,   -- 已尝试校验次数，防止暴力猜 6 位码
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_codes_lookup ON email_codes(email, purpose, created_at DESC);
