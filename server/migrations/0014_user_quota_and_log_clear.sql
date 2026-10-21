-- 1) 每用户配额覆盖：0 表示「按档位默认」，非 0 就用手填的值。
--    放在 users 上而不是单独配额表：这三个值天然是一对一，单独建表只会多一次 JOIN。
-- 2) log_cleared_at：用户「清空自己的日志」只记录一个时间点，
--    查询时过滤 ts > log_cleared_at。管理员视角不加这个条件，
--    所以用户清完之后管理员照样能查 —— 用户清的是自己的视图，不是删数据。
ALTER TABLE users ADD COLUMN custom_max_rules INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN custom_max_subs INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN custom_daily_queries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN log_cleared_at INTEGER NOT NULL DEFAULT 0;
