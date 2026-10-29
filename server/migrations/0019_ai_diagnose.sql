-- AI 诊断：让 Agent 先给出「是否误杀 + 建议动作」，管理员一键采纳。
--
-- 设计要点：
--   * 三列各存各的，不合并成一个大 JSON —— 管理者要看的是「结论」和
--     「为什么」，而 ai_evidence 只在追责/复盘时才翻，分开存便于索引与清理；
--   * ai_status 独立于 status：诊断是异步的，不能因为诊断没跑完就把反馈
--     卡在 open；反过来，反馈已结案也不影响事后补一次诊断；
--   * 全部 NOT NULL DEFAULT ''，老数据无痛升级。

ALTER TABLE feedback ADD COLUMN ai_status      TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_verdict     TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_reason      TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_action      TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_reply       TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_domains     TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_guard_notes TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_evidence    TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_model       TEXT    NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_ran_at      INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_feedback_ai_status ON feedback(ai_status, created_at);

-- 诊断要按域名反查「这个域名来自哪个订阅」。
-- 这里原本还有一条 CREATE INDEX idx_sub_entries_domain ON sub_entries(domain)，
-- 实测执行计划走 SCAN e + 主键接 JOIN，索引从没被用上，0023 已删。

-- 诊断要按域名查近期查询记录（判断「用户说的到底发生没发生」）。
CREATE INDEX IF NOT EXISTS idx_query_log_domain ON query_log(domain, ts);
