// Package rules 实现规则解析、匹配与规则集构建。
//
// 支持的语法（文档第五章 5.1）：
//
//	||ads.example.com^                     block   拦截该域及其所有子域
//	@@||safe.example.com^                  allow   白名单例外（优先级最高）
//	ads.example.com                        block   裸域名，等价于 ||ads.example.com^
//	0.0.0.0 ads.example.com                block   hosts 格式
//	127.0.0.1 ads.example.com              block   hosts 格式
//	||ads.example.com^$dnstype=AAAA        block   仅拦截指定记录类型
//	||old.example.com^$dnsrewrite=1.2.3.4  rewrite A 记录改写
//	||old.example.com^$dnsrewrite=new.com  rewrite CNAME 改写
//	*.dev.local^$dnsrewrite=127.0.0.1      rewrite 通配改写
//	# 或 ! 开头                            注释    忽略
//
// 另外兼容 dnsmasq 的 address=/domain/ip 与纯域名列表。
package rules

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// ErrSkip 表示这一行应当被跳过（注释、空行、无法识别的修饰符）。
//
// 与「解析失败」区分开：订阅源里混有注释和统计行是常态，
// 把跳过当成错误会让 last_status 满屏 error，反而掩盖真正的坏规则。
var ErrSkip = errors.New("跳过该行")

// Parsed 是解析后的一条规则。
type Parsed struct {
	Kind string // model.Kind*
	// Domain 是规范化后的域名（小写、无尾点、无通配前缀）。
	Domain string
	// Wildcard 为 true 表示模式是 *.domain 形式：只匹配子域，不匹配 domain 本身。
	Wildcard bool
	// Value 是改写目标（仅 rewrite_* 有值）。
	Value string
	// QTypes 为空表示适用于所有记录类型。
	QTypes []string
}

// ParseLine 解析一行规则文本。
//
// 返回 ErrSkip 表示该行应被忽略而不是报错。
func ParseLine(raw string) (*Parsed, error) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return nil, ErrSkip
	}
	// 注释：AdGuard/AdBlock 用 ! 与 #，hosts 文件也用 #。
	if strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") {
		return nil, ErrSkip
	}

	// dnsmasq: address=/ads.example.com/0.0.0.0
	if strings.HasPrefix(line, "address=/") {
		parts := strings.Split(strings.TrimPrefix(line, "address=/"), "/")
		if len(parts) < 2 {
			return nil, fmt.Errorf("dnsmasq 规则格式错误: %q", line)
		}
		domain, err := normalizeDomain(parts[0])
		if err != nil {
			return nil, err
		}
		target := strings.TrimSpace(parts[1])
		if target == "" || target == "0.0.0.0" || target == "::" || target == "127.0.0.1" {
			return &Parsed{Kind: model.KindBlock, Domain: domain}, nil
		}
		p := &Parsed{Domain: domain, Value: target}
		p.Kind = rewriteKindFor(target)
		return p, nil
	}

	// hosts 格式：行首是 IP，后面跟一个或多个域名。
	if fields := strings.Fields(line); len(fields) >= 2 && net.ParseIP(fields[0]) != nil {
		domain, err := normalizeDomain(fields[1])
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(fields[0])
		// 0.0.0.0 / :: 是标准的「拦截」写法；其他 IP 视为改写。
		if ip.IsUnspecified() || ip.Equal(net.IPv4bcast) {
			return &Parsed{Kind: model.KindBlock, Domain: domain}, nil
		}
		if ip.IsLoopback() && (fields[0] == "127.0.0.1" || fields[0] == "::1") {
			// 127.0.0.1 在 hosts 语境下就是「拦到本地」，按 block 处理，
			// 这与文档 5.1 表格一致。
			return &Parsed{Kind: model.KindBlock, Domain: domain}, nil
		}
		p := &Parsed{Domain: domain, Value: fields[0]}
		p.Kind = rewriteKindFor(fields[0])
		return p, nil
	}

	body := line
	kind := model.KindBlock

	// 白名单前缀 @@
	if strings.HasPrefix(body, "@@") {
		kind = model.KindAllow
		body = strings.TrimPrefix(body, "@@")
	}

	// 修饰符：从第一个 $ 开始（AdGuard 的 $ 不会出现在域名里）。
	var modifiers string
	if idx := strings.IndexByte(body, '$'); idx >= 0 {
		modifiers = body[idx+1:]
		body = body[:idx]
	}

	// 去掉锚点：|| 前缀、^ 与 | 后缀。
	body = strings.TrimPrefix(body, "||")
	body = strings.TrimSuffix(body, "^")
	body = strings.TrimSuffix(body, "|")
	body = strings.TrimPrefix(body, "|")
	// 路径规则（含 /）在 DNS 层无意义，丢弃路径部分。
	if idx := strings.IndexByte(body, '/'); idx >= 0 {
		body = body[:idx]
	}

	wildcard := false
	if strings.HasPrefix(body, "*.") {
		wildcard = true
		body = strings.TrimPrefix(body, "*.")
	}
	// 其他位置的 * 无法在 DNS 层表达（如 ad*.example.com），保守跳过。
	if strings.Contains(body, "*") {
		return nil, ErrSkip
	}

	domain, err := normalizeDomain(body)
	if err != nil {
		return nil, err
	}

	p := &Parsed{Kind: kind, Domain: domain, Wildcard: wildcard}

	// 解析修饰符。
	if modifiers != "" {
		for _, m := range strings.Split(modifiers, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			key, val, _ := strings.Cut(m, "=")
			switch strings.ToLower(key) {
			case "dnstype":
				p.QTypes = normalizeQTypes(strings.Split(val, "|"))
			case "dnsrewrite":
				if val == "" {
					return nil, fmt.Errorf("$dnsrewrite 缺少目标值")
				}
				p.Value = val
				// $dnsrewrite 的语义优先于 @@：即使同时出现，改写也是更强的意图。
				p.Kind = rewriteKindFor(val)
			case "important":
				// 本实现是 first-match-wins，没有优先级叠加语义，忽略即可。
			case "client", "ctag", "denyallow", "badfilter", "app":
				// 这些修饰符需要请求上下文（客户端标识、应用识别），
				// 纯 DNS 层拿不到，静默忽略修饰符但保留规则本体。
			default:
				// 未知修饰符：忽略修饰符而不是丢弃整条规则，
				// 避免订阅源里的新语法让大量规则失效。
			}
		}
	}

	if p.Kind == model.KindAllow {
		// 白名单规则带 value 是无意义的（改写优先已在上方处理）。
		p.Value = ""
	}
	if isRewriteKind(p.Kind) && p.Value == "" {
		return nil, fmt.Errorf("改写规则缺少目标值")
	}
	if !isRewriteKind(p.Kind) && p.Value != "" {
		return nil, fmt.Errorf("非改写规则不应带目标值 %q", p.Value)
	}
	return p, nil
}

// rewriteKindFor 根据改写目标推断具体的 rewrite 类型。
func rewriteKindFor(target string) string {
	if ip := net.ParseIP(target); ip != nil {
		if ip.To4() != nil {
			return model.KindRewriteA
		}
		return model.KindRewriteAAAA
	}
	return model.KindRewriteCNAME
}

// isRewriteKind 判断是否为改写类规则。
func isRewriteKind(k string) bool {
	switch k {
	case model.KindRewriteA, model.KindRewriteAAAA, model.KindRewriteCNAME:
		return true
	}
	return false
}

// normalizeQTypes 规范化记录类型列表（大写、去重、去掉 "ALL"）。
func normalizeQTypes(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToUpper(strings.TrimSpace(v))
		if v == "" || v == "ALL" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeDomain 校验并规范化域名。
//
// 拒绝：空串、含非法字符、长度超限、是 IP 字面量（IP 不该出现在域名规则里）。
func normalizeDomain(s string) (string, error) {
	d := strings.TrimSpace(strings.ToLower(s))
	d = strings.Trim(d, ".")
	if d == "" {
		return "", fmt.Errorf("规则缺少域名")
	}
	// 去掉可能残留的 IPv6 方括号。
	d = strings.Trim(d, "[]")
	if net.ParseIP(d) != nil {
		return "", fmt.Errorf("%q 是 IP 字面量，不能作为域名规则", d)
	}
	if len(d) > 253 {
		return "", fmt.Errorf("域名 %q 超过 253 字节", d)
	}
	// 只允许 DNS 标签里合法出现的字符。下划线虽不合规范但现实中大量存在，
	// 放行以免误杀内网域名。
	for _, c := range d {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
		default:
			return "", fmt.Errorf("域名 %q 含非法字符 %q", d, string(c))
		}
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" {
			return "", fmt.Errorf("域名 %q 含空标签", d)
		}
		if len(label) > 63 {
			return "", fmt.Errorf("域名 %q 的标签 %q 超过 63 字节", d, label)
		}
	}
	return d, nil
}

// NormalizeDomain 供包外调用（订阅解析、API 校验）。
func NormalizeDomain(s string) (string, error) { return normalizeDomain(s) }

// ParseContent 批量解析多行文本，返回解析成功的规则与统计。
//
// 返回值 errors 只包含「看起来是规则但解析失败」的行，注释与空行不计入。
func ParseContent(content string) (rules []*Parsed, skipped int, errs []error) {
	for i, line := range strings.Split(content, "\n") {
		p, err := ParseLine(line)
		if err != nil {
			if errors.Is(err, ErrSkip) {
				skipped++
				continue
			}
			// 最多保留 20 条错误，避免一个坏订阅产生几十万条错误信息把响应撑爆。
			if len(errs) < 20 {
				errs = append(errs, fmt.Errorf("第 %d 行: %w", i+1, err))
			}
			skipped++
			continue
		}
		rules = append(rules, p)
	}
	return rules, skipped, errs
}
