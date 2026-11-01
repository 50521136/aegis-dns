-- 查询日志增加「解析来源」列：domestic（国内上游）/ overseas（境外上游）。
--
-- 为什么要单独记一列而不是从 upstream 地址反推：upstream 里存的是具体
-- 服务器地址，而管理员真正想知道的是「这个域名被判成了国内还是境外」。
-- 分流规则有三层（白名单→黑名单→默认国内），只看上游地址反推不出来
-- 它到底命中了哪一层；判定结果一旦落库，排障时一眼就能看到。
--
-- 老记录该列为空串，前端按「未知」展示。
ALTER TABLE query_log ADD COLUMN side TEXT NOT NULL DEFAULT '';
