-- 删掉 sub_entries(domain) 这个索引 —— 它没有任何查询在用。
--
-- 现状（线上实测）：
--   * 该索引 278MB，占整个库的三分之一；sub_entries 表本身 281MB。
--   * 表是 WITHOUT ROWID、PRIMARY KEY (sub_id, domain)，所有访问路径都是
--     「按 sub_id 取全部条目」（主键前缀直接命中），或「按 sub_id 做
--     DELETE / NOT IN」（同样主键前缀）。全库没有任何一条 SQL 只按 domain
--     过滤 sub_entries。
--   * 唯一按 domain 查的地方是 diagnose.go 的反查订阅来源，但它的 SQL 是
--     `WHERE e.domain IN (...)` + JOIN subscriptions —— 查询计划器选的是
--     `SEARCH e USING PRIMARY KEY (sub_id=?)`，因为它要拿 sub_id 再回表接
--     JOIN，全表扫反而更划算；实测执行计划里根本没有用到这个索引。
--   * 反面代价：每条 sub_entries 插入/删除都要多维护一棵 278MB 的 B 树。
--     线上 365 万行、每次订阅刷新都是整批 DELETE + INSERT，维护成本实打实。
--
-- 为什么不用 IF EXISTS 之外的保险：DROP INDEX 是幂等的（配合 IF EXISTS），
-- 在已经手工删过的库上执行是空操作。

DROP INDEX IF EXISTS idx_sub_entries_domain;
