-- 签到日界从 UTC 改为北京时间（UTC+8）。
--
-- 原来 day 是 UTC 自然日序号（unix/86400），导致签到在北京时间早上 8 点才更新，
-- 凌晨签到会被算成前一天。改成 (unix + 28800) / 86400 后，0 点即更新。
--
-- 历史行的 day 必须按 created_at 重算，否则「今天签过没有」会判错，
-- 用户可能被允许重复签到，也可能连着好几天签不了。
--
-- 重算后可能出现两行落到同一个新 day（原来 UTC 相邻两天、其中一天在北京时间
-- 16:00 之后签的，会合并到同一北京日）。主键是 (user_id, day)，直接 UPDATE 会撞键，
-- 所以先按新 day 去重、只保留最后一次，再改 day。

DELETE FROM checkins
WHERE rowid NOT IN (
  SELECT MAX(rowid) FROM checkins GROUP BY user_id, (created_at + 28800) / 86400
);

UPDATE checkins SET day = (created_at + 28800) / 86400;
