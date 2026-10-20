-- 查询日志补充详情字段。
--
-- 日志列表只需要 domain/action/rcode，但「点开看这一条到底发生了什么」
-- 需要协议、ECS、应答上游、应答内容、追踪器识别 —— 这些在解析时都有，
-- 只是之前没落库。全部允许为空，老记录读出来就是空串，不报错。
ALTER TABLE query_log ADD COLUMN proto    TEXT NOT NULL DEFAULT '';  -- udp|tcp|dot|doh
ALTER TABLE query_log ADD COLUMN ecs      TEXT NOT NULL DEFAULT '';  -- EDNS Client Subnet
ALTER TABLE query_log ADD COLUMN upstream TEXT NOT NULL DEFAULT '';  -- 实际应答的上游
ALTER TABLE query_log ADD COLUMN answer   TEXT NOT NULL DEFAULT '';  -- 应答内容（截断）
ALTER TABLE query_log ADD COLUMN tracker  TEXT NOT NULL DEFAULT '';  -- 命中的已知追踪器
ALTER TABLE query_log ADD COLUMN client_name TEXT NOT NULL DEFAULT ''; -- 客户端备注名
