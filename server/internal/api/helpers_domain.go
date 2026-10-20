package api

import "strings"

// normalizeDomainList 把「逗号/空格/换行分隔的邮箱后缀」规范成统一格式。
//
// 统一去掉前导 @ 与 .，小写，去重：管理员可能写 @qq.com、qq.com、QQ.COM，
// 三种写法必须等价，否则「白名单里明明有」却注册不了。
func normalizeDomainList(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		d := strings.ToLower(strings.TrimSpace(f))
		d = strings.TrimPrefix(d, "@")
		d = strings.TrimPrefix(d, ".")
		if d == "" || !strings.Contains(d, ".") {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return strings.Join(out, ",")
}

// emailAllowed 判断邮箱后缀是否在白名单内；白名单为空表示不限制。
func emailAllowed(email, whitelist string) bool {
	if whitelist == "" {
		return true
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range strings.Split(whitelist, ",") {
		if d == "" {
			continue
		}
		// 允许写成 qq.com 匹配任意子域 mail.qq.com
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}
