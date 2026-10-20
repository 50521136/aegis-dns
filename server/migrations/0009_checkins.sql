-- 每日签到。
--
-- day 用「UTC 自然日序号」(unix/86400) 而不是日期字符串：
-- 判断「今天签过没有」就是一次整数比较，也不用担心时区格式解析出错。
CREATE TABLE IF NOT EXISTS checkins (
  user_id    TEXT NOT NULL,
  day        INTEGER NOT NULL,
  streak     INTEGER NOT NULL DEFAULT 1,   -- 含当天在内已连续签到多少天
  days_granted INTEGER NOT NULL DEFAULT 0, -- 本次实际发放的会员天数（含连签奖励）
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, day)
);
CREATE INDEX IF NOT EXISTS idx_checkins_user ON checkins(user_id, day DESC);
