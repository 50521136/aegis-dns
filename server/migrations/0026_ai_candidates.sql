-- AI 诊断：区分「按域名查」与「按内容查」。
--
-- ai_candidates：用户没填域名字段时，模型从正文/近期拦截记录推断出的
--   建议核查域名。与 ai_domains（管理员点「采纳」就写成放行规则）严格
--   分开 —— 把猜测放进可执行清单会造成误放行。
-- ai_lookup_mode：by_domain / by_content。落库是为了管理员列表里能分清
--   「这条是按内容推断的，只给建议等人工」，以及自动处理回调能据此
--   跳过结单。
ALTER TABLE feedback ADD COLUMN ai_candidates TEXT NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN ai_lookup_mode TEXT NOT NULL DEFAULT '';
