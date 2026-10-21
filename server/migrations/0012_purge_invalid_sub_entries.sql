-- 清掉订阅里解析出来的非法「域名」。
--
-- 线上事故：AdGuard Allowlist 解析出 `com` / `net` 两个单标签条目。
-- 后缀匹配下 `com` 会命中**所有 .com 域名**，一条 allow 就把整个 TLD 放行，
-- 拦截率直接归零，而列表本身只有 1028 条、看着完全正常。
--
-- 解析器已加 plausibleDomain 校验，这里是给存量数据兜底
-- （订阅下次刷新会整体替换，但刷新前不能让脏数据继续生效）。
DELETE FROM sub_entries WHERE domain NOT LIKE '%.%';
DELETE FROM sub_entries WHERE domain LIKE '%\_%' ESCAPE '\';
DELETE FROM sub_entries WHERE domain LIKE '% %' OR domain LIKE '%/%' OR domain LIKE '%:%';
DELETE FROM sub_entries WHERE domain LIKE '-%' OR domain LIKE '%-';
