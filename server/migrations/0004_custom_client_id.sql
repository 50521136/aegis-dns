-- 0004: 自定义接入 ID
--
-- custom_id_used  标记该用户是否已经用过「自定义 client_id」这一次机会。
--                 需求是每个用户只能自己指定一次，之后只能找管理员。
--
-- 用 ALTER TABLE ADD COLUMN 而不是重建表：users 被 rules / subscriptions
-- 以 ON DELETE CASCADE 引用，重建会连带清空用户数据。

ALTER TABLE users ADD COLUMN custom_id_used INTEGER NOT NULL DEFAULT 0;
