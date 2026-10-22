-- 每个用户自己的日报开关，默认关闭。
-- 这是「用户主动订阅的通知」，不是系统公告，所以必须由用户自己开。
ALTER TABLE users ADD COLUMN daily_report INTEGER NOT NULL DEFAULT 0;
