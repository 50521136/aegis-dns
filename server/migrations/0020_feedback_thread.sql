-- 反馈会话化：一条反馈变成一串消息，用户与管理员在同一串里往返。
--
-- 为什么不再用 feedback.admin_reply 单字段：
-- 用户看完管理员回复往往要追问，而此前**唯一的追问方式是再提交一条新反馈** ——
-- 于是列表里出现「同一个问题的第 2 条、第 3 条」，谁也看不出它们是一件事。
-- 管理员那边同样难受：回复散在多个条目里，结单时只能一条条翻。
--
-- 设计要点：
--   * 一条反馈一条时间线，role 区分 user / admin，前端按时间铺成一串气泡；
--   * 保留 feedback.message / admin_reply 两列不动，作为「首条正文」与
--     「最近一次管理员回复」的冗余快照 —— 老接口、AI 诊断、邮件通知都还在读它们，
--     一次性全改会把改动面放大好几倍；
--   * author 存提交时的用户名快照，用户改名或注销后时间线仍然可读。

CREATE TABLE IF NOT EXISTS feedback_messages (
	id          TEXT    NOT NULL PRIMARY KEY,
	feedback_id TEXT    NOT NULL,
	role        TEXT    NOT NULL,              -- user | admin
	author      TEXT    NOT NULL DEFAULT '',   -- 提交时的用户名快照
	content     TEXT    NOT NULL,
	created_at  INTEGER NOT NULL
) WITHOUT ROWID;

-- 时间线按 (feedback_id, created_at) 顺序读，直接建复合索引。
CREATE INDEX IF NOT EXISTS idx_fbmsg_feedback ON feedback_messages(feedback_id, created_at);

-- ── 回填历史数据 ──
-- 老数据没有时间线，按「原正文 → 管理员回复」两条补出来。
-- id 用确定性拼接，重复执行不会插重（迁移只跑一次，但本地反复建库时会遇到）。

INSERT OR IGNORE INTO feedback_messages (id, feedback_id, role, author, content, created_at)
SELECT 'fbm-' || id || '-u0', id, 'user', username, message, created_at
FROM feedback
WHERE message <> '';

INSERT OR IGNORE INTO feedback_messages (id, feedback_id, role, author, content, created_at)
SELECT 'fbm-' || id || '-a0', id, 'admin', '管理员', admin_reply,
       CASE WHEN updated_at > 0 THEN updated_at ELSE created_at END
FROM feedback
WHERE admin_reply <> '';
