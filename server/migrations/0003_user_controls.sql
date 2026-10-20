-- 0003: 用户自助能力与开关
--
-- id_reset_count  记录该用户已经重置过几次 client_id，用于「重置次数有限制」
-- rules_enabled / subs_enabled  按用户开关个人规则与订阅，管理员可单独关掉某个用户的
--                               （全局设置里另有总开关，两者取与）
--
-- 都用 ALTER TABLE ADD COLUMN：不重建表，避免触发 rules/subscriptions 的外键级联删除。

ALTER TABLE users ADD COLUMN id_reset_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN rules_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN subs_enabled INTEGER NOT NULL DEFAULT 1;
