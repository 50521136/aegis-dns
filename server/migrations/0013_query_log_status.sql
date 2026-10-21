-- 查询状态改成三分类：blocked（已拦截）/ allowed（允许项）/ handled（已处理）。
--
-- 「缓存」原来是一个独立 action，导致「允许项」和「已处理」被它挤掉：
-- 用户看不出某条到底是放行规则放过的、还是普通解析的。
-- 现在缓存只是 cache_hit 标记，显示成「已处理（缓存）」「允许项（缓存）」。
ALTER TABLE query_log ADD COLUMN cache_hit INTEGER NOT NULL DEFAULT 0;

-- 老数据迁移：
--   cached    → handled + cache_hit=1（老代码里缓存命中的都是上游应答）
--   rewritten → handled（改写属于已处理）
--   allowed   → handled（老代码的 allowed 表示「正常解析」，
--                        新的 allowed 专指「命中放行规则」，语义不同不能混）
UPDATE query_log SET cache_hit = 1, action = 'handled' WHERE action = 'cached';
UPDATE query_log SET action = 'handled' WHERE action = 'rewritten';
UPDATE query_log SET action = 'handled' WHERE action = 'allowed';
