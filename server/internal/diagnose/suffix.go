package diagnose

import "strings"

// IsSafeAllowTarget 判断一个域名是否适合作为「放行规则」的目标。
//
// 导出的原因：采纳阶段（api 层）要重新校验一次库里存的建议，
// 不能信任历史数据 —— 它可能是旧版本闸门的产物，也可能被手工改过。
// 与 Guard 内部用的是同一套判断，避免两处口径漂移。
func IsSafeAllowTarget(d string) bool {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "||")
	d = strings.TrimSuffix(d, "^")
	d = strings.TrimSuffix(d, ".")
	if d == "" || !looksLikeDomain(d) || isPublicSuffix(d) {
		return false
	}
	return true
}

// isPublicSuffix 判断域名是否属于「不该被放行」的公共后缀/大范围域名。
//
// 除了精确匹配表，还要挡住「放行整个 TLD」这类退化形式：
// 例如模型给出 `cn` 或 `com.cn` 时，精确表能命中；但给出
// `all.cn` 这种就得靠标签数判断 —— 只有两个标签且后缀是常见 TLD 时，
// 说明它在放行一个整站根域，同样要拦。
func isPublicSuffix(d string) bool {
	if _, ok := publicSuffixLike[d]; ok {
		return true
	}
	labels := strings.Split(d, ".")
	// 两个标签 = 典型的主域（example.com）。这一类本来是合法的放行目标，
	// 所以不能一概拒绝；只有后缀落入高危 TLD 时才拒绝。
	if len(labels) == 2 {
		tld := labels[1]
		switch tld {
		case "cn", "io", "dev", "co", "me", "tv", "cc", "xyz", "top", "info", "biz", "site", "online":
			// 这些 TLD 下的两段域名也可能是真实业务域（如 xxx.io），
			// 但从「公共规则集误杀」的角度看，放行整个两段主域风险过高，
			// 一律要求人工确认。
			return true
		}
	}
	// 单标签（com、net）已经在 looksLikeDomain 里被拒，这里再兜一次。
	return !strings.Contains(d, ".")
}
