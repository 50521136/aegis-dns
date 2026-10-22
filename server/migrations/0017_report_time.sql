-- 日报推送时间改成每个用户自己设，精确到分钟。
-- 存 'HH:MM'（北京时间）的文本：比拆成两列好读，也不用担心 0 点被当成「未设置」。
ALTER TABLE users ADD COLUMN report_time TEXT NOT NULL DEFAULT '09:00';
