-- 公告系统。
--
-- 两种投递方式：
--   silent —— 只进公告栏，未读时显示红点/数量
--   popup  —— 同时弹窗，用户可点「不再弹出」永久静音
--
-- 已读与「不再弹出」分开记：
--   读过 ≠ 不想再看到。把两者合并成一个字段，就再也做不出
--   「我知道了，但下次登录还想再看一眼」这种合理需求。
CREATE TABLE IF NOT EXISTS announcements (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  title       TEXT NOT NULL,
  body        TEXT NOT NULL DEFAULT '',
  level       TEXT NOT NULL DEFAULT 'info',    -- info | warning | danger
  mode        TEXT NOT NULL DEFAULT 'silent',  -- silent | popup
  dismissible INTEGER NOT NULL DEFAULT 1,      -- popup 模式下是否提供「不再弹出」
  enabled     INTEGER NOT NULL DEFAULT 1,
  pinned      INTEGER NOT NULL DEFAULT 0,      -- 置顶
  starts_at   INTEGER NOT NULL DEFAULT 0,      -- 0 = 立即生效
  ends_at     INTEGER NOT NULL DEFAULT 0,      -- 0 = 永不过期
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ann_enabled ON announcements(enabled, starts_at, ends_at);

CREATE TABLE IF NOT EXISTS announcement_reads (
  user_id    TEXT NOT NULL,
  ann_id     INTEGER NOT NULL,
  read_at    INTEGER NOT NULL DEFAULT 0,
  dismissed_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, ann_id)
);
