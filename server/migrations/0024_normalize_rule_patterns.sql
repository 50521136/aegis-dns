-- 把规则 pattern 统一成「纯域名 + 可选通配」。
--
-- 背景：规则表里原本并存三种写法（@@||x^ / ||x^ / x），而匹配时 splitPattern
-- 会把 @@ 和 || 无条件剥掉，前缀对结果零影响 —— 纯冗余。更糟的是它能和 kind
-- 打架：kind=block 配 "@@||x^" 时 @@ 被剥、kind 生效，实际变成「拦截」，
-- 与字面意思相反且不报错。
--
-- 约定改为：kind 是语义唯一来源，pattern 只存域名。订阅层（sub_entries）
-- 283 万条早就是这个形态。
--
-- SQLite 没有正则替换，分三步做：剥 @@、剥 ||、剥结尾的 ^ 和 |。
-- 顺序不能反：先剥 @@ 再剥 ||，因为 "@@||x^" 两层前缀叠加。

UPDATE rules SET pattern = replace(pattern, '@@', '') WHERE pattern LIKE '@@%';
UPDATE rules SET pattern = replace(pattern, '||', '') WHERE pattern LIKE '||%';
UPDATE rules SET pattern = rtrim(pattern, '^|') WHERE pattern LIKE '%^' OR pattern LIKE '%|';
UPDATE rules SET pattern = replace(pattern, '|', '') WHERE pattern LIKE '|%';
