// Package subsub 实现订阅系统的拉取、解析与调度（文档第六章）。
//
// 包名 subsub 而非 sub：避免与常用变量名 sub 冲突，也避免与
// 「sub」在其它上下文（subscription/subdomain）产生歧义。
package subsub

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"

	"github.com/50521136/aegis-dns/server/internal/rules"
)

// Format 是订阅源的格式。
type Format string

const (
	FormatAuto    Format = "auto"
	FormatAdblock Format = "adblock"
	FormatHosts   Format = "hosts"
	FormatDnsmasq Format = "dnsmasq"
	FormatDomain  Format = "domain"
)

// Entry 是从订阅里解析出的一条域名。
type Entry struct {
	Domain string
	// Kind 为 block 或 allow。
	Kind string
}

// ParseResult 是解析结果。
type ParseResult struct {
	Entries []Entry
	// Skipped 是被跳过的行数（注释、空行、不支持的语法）。
	Skipped int
	// Invalid 是被丢弃的非法域名条数（裸 TLD、CSS 片段、纯数字等）。
	Invalid int
	// Errors 是「看起来是规则但解析失败」的前若干条错误。
	Errors []error
	// Detected 是嗅探出的**主导**格式（format=auto 时），仅用于日志与展示。
	//
	// 解析本身不按它排他筛选：混合格式的列表很常见，按单一格式筛会把
	// 另一种写法的行整片丢掉（实测一份 32448 行的源因此只剩 6171 条）。
	Detected Format
	// RewriteDowngraded 是被降级为拦/放（而非丢弃）的 $dnsrewrite 行数。
	//
	// 公共拦截列表大量用 ||x^$dnsrewrite=<黑洞域名> 表达「解析到拦截页」，
	// 语义上就是拦截。原实现把带 value 的行一律丢弃，实测某个源 1072 行
	// 全被跳过、订阅直接变成 0 条 —— 规则不是被拒绝，是**静默消失**，
	// 从状态里也看不出少了什么。这里改成按列表类型降级，并把这个数字
	// 带到状态里，至少让人知道「这份源是靠降级才生效的」。
	RewriteDowngraded int
	// RewriteIgnored 是「改写目标是真实地址」而被忽略的行数。
	//
	// 与 RewriteDowngraded 的区别是语义方向相反：`$dnsrewrite=0.0.0.0`
	// 和 `0.0.0.0 x.com` 表达的是拦截，而 `142.250.157.188 mtalk.google.com`
	// 这种 hosts 行表达的是「把这个域名指到某个优选 IP」，本质是加速。
	// 订阅层只承载拦/放、不承载改写，把它降级成拦截等于**反过来把域名封掉** ——
	// 实测一份源里 147 行这种写法，导致 mtalk.google.com（Google 推送）、
	// dl.google.com、media.githubusercontent.com 全被解析成 0.0.0.0。
	// AdGuard Home 对这类行做的是 DNS 改写，不是拦截，这里也不该拦。
	RewriteIgnored int
}

// Sniff 嗅探订阅格式（文档 6.1）。
//
// 策略：取前 50 行非注释行，统计各格式的特征命中数，取最高者。
// 只统计前 50 行而不是全文件：十万行的文件全扫一遍纯属浪费，
// 而且格式在文件内通常是统一的。
func Sniff(content []byte) Format {
	var (
		adblock, hosts, dnsmasq, domain int
		seen                            int
	)
	sc := bufio.NewScanner(bytes.NewReader(content))
	// 单行最长 1MB，防止畸形文件把 Scanner 卡住。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() && seen < 50 {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		// 订阅头（[Adblock Plus 2.0]）不算内容行。
		if strings.HasPrefix(line, "[") {
			continue
		}
		seen++
		switch {
		case strings.Contains(line, "||") || strings.HasSuffix(line, "^") || strings.Contains(line, "$"):
			adblock++
		case strings.HasPrefix(line, "address=/"):
			dnsmasq++
		case looksLikeHostsLine(line):
			hosts++
		case looksLikeDomain(line):
			domain++
		}
	}
	// 按命中数从高到低挑。
	best, bestN := FormatDomain, domain
	if adblock > bestN {
		best, bestN = FormatAdblock, adblock
	}
	if hosts > bestN {
		best, bestN = FormatHosts, hosts
	}
	if dnsmasq > bestN {
		best, bestN = FormatDnsmasq, dnsmasq
	}
	if bestN == 0 {
		return FormatDomain
	}
	return best
}

func looksLikeHostsLine(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false
	}
	return net.ParseIP(fields[0]) != nil
}

func looksLikeDomain(line string) bool {
	if strings.ContainsAny(line, " \t/") {
		return false
	}
	if !strings.Contains(line, ".") {
		return false
	}
	_, err := rules.NormalizeDomain(line)
	return err == nil
}

// Parse 解析订阅内容。
//
// 关键约定（文档 6.4）：解析失败时**不返回部分结果**。
// 调用方看到 err 就保留旧数据；如果这里返回「解析了一半」的列表，
// 调用方会把规则集替换成残缺版本，那正是文档反复强调要避免的事。
func Parse(content []byte, format Format, listType string) (*ParseResult, error) {
	if format == FormatAuto || format == "" {
		format = Sniff(content)
	}

	res := &ParseResult{Detected: format}
	// 预分配：按平均每行 30 字节估算，减少切片扩容。
	est := len(content)/30 + 16
	if est > 500000 {
		est = 500000
	}
	res.Entries = make([]Entry, 0, est)

	// 去重：同一个域名在同一订阅里出现多次只保留一条，且 allow 优先。
	// 用「域名 -> kind」+ 首次出现顺序，保证结果稳定可复现
	// （快照内容稳定意味着 dnsd 的增量重建判断也稳定）。
	seen := make(map[string]string, est)
	order := make([]string, 0, est)

	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()

		// 逐行交给 rules.ParseLineAllForSubscription —— 它本身就能识别
		// adblock / dnsmasq / hosts / 裸域名四种写法，不需要先按「嗅探出的
		// 格式」做排他筛选；并且会保留 `||at*.doubanio.com^` 这类通配模式
		// （订阅层有 globSet 能匹配，以前这些行在这里就被丢掉了）。
		//
		// 这里曾经有一段按 format 的预筛选（FormatHosts 时非 hosts 行直接
		// Skipped），代价是**混格式的订阅会被砍掉一大半**：用户报障的那份
		// rules.txt 共 32448 行，前 50 行几乎全是 `0.0.0.0 domain`（判成
		// hosts），而全文 56% 是 `||domain^`（adblock）—— 排他筛选把 18372 条
		// adblock 规则全丢了，最终只剩 6171 条，状态还显示 ok，用户完全
		// 看不出少了什么。
		//
		// 现实里的公共列表经常混格式（hosts + adblock 混排很常见），
		// 所以格式嗅探只能用于**日志展示与优先级提示**，不能用来排他。
		parsedList, err := rules.ParseLineAllForSubscription(line)
		if err != nil {
			// rules.ErrSkip 表示注释/空行，不计入错误。
			if err != rules.ErrSkip {
				if len(res.Errors) < 20 {
					res.Errors = append(res.Errors, fmt.Errorf("第 %d 行: %w", lineNo, err))
				}
			}
			res.Skipped++
			continue
		}
		// 一行可能产生多条（hosts 写法 `0.0.0.0 a.com b.com`），逐条入库。
		for _, p := range parsedList {
			// 域名合法性校验。
			//
			// 公共列表里混着外观规则、CSS 片段、统计口径的垃圾行，
			// 解析出来会变成 "_468x60_"、"192.168" 这种「域名」。
			// 其中单标签条目（com / net / clicks）最致命：
			// 后缀匹配下 `com` 会命中**所有 .com 域名**，
			// 一条 allow 条目就能把整个 TLD 的拦截全部放行 ——
			// 线上实测 AdGuard Allowlist 里的 `com`/`net` 让拦截率直接归零。
			ok := false
			if p.Glob {
				ok = plausibleGlob(p.Domain)
			} else {
				ok = plausibleDomain(p.Domain)
			}
			if !ok {
				res.Invalid++
				res.Skipped++
				continue
			}

			// 订阅只承载「拦/放」两类，不承载改写能力。但带改写目标的行
			// 不能一律丢，也不能一律降级成拦截 —— 要按目标的性质分两类：
			//
			//   黑洞目标（0.0.0.0 / 127.0.0.1 / :: / NXDOMAIN…）
			//       `||x^$dnsrewrite=0.0.0.0`、`0.0.0.0 x` 表达的意图就是
			//       「别让它解析出去」，按列表类型降级成拦/放。
			//   真实地址（142.250.157.188 mtalk.google.com 这类）
			//       意图是「指到这个优选 IP」，是加速不是拦截。订阅层承载
			//       不了改写，那就**忽略这条**，绝不能反过来把它封掉 ——
			//       实测一份源里 147 行这种写法，把 mtalk.google.com、
			//       dl.google.com、media.githubusercontent.com 全拦成了
			//       0.0.0.0，Google 推送与 GitHub 媒体直接不可用。
			//
			// 默认类别由 **列表类型** 决定，不是恒为 block：
			// allowlist 源的裸域名就是「放行」（用户加一份白名单，列表里
			// 当然不会有 @@ 前缀）。此前恒按 block 判，于是 allowlist 订阅
			// 解析出 block=全部、allow=0，而 scheduler 对 allowlist 只取
			// allow —— 整份订阅入库 0 条，状态还是 ok，静默失效。
			kind := "block"
			if listType == "allowlist" {
				kind = "allow"
			}
			// 显式的 @@ 永远表示放行，与列表类型无关。
			if p.Kind == "allow" {
				kind = "allow"
			}
			if p.Value != "" {
				if !isBlackholeTarget(p.Value) {
					res.RewriteIgnored++
					res.Skipped++
					continue
				}
				kind = "block"
				if listType == "allowlist" {
					// 白名单源里出现改写，意图是「这条别拦」，按放行处理。
					kind = "allow"
				}
				res.RewriteDowngraded++
			}
			if prev, ok := seen[p.Domain]; ok {
				// 已存在：只在「新的这条是 allow 而旧的不是」时升级为白名单。
				// 白名单语义更强，后出现的 allow 必须能覆盖先出现的 block，
				// 否则订阅里「先拉黑后放行」的写法会失效。
				if kind == "allow" && prev != "allow" {
					seen[p.Domain] = "allow"
				}
				res.Skipped++
				continue
			}
			seen[p.Domain] = kind
			order = append(order, p.Domain)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取订阅内容失败: %w", err)
	}

	for _, d := range order {
		res.Entries = append(res.Entries, Entry{Domain: d, Kind: seen[d]})
	}

	// 内容为空视为异常（文档 6.4）：宁可报错让调用方保留旧数据，
	// 也不要把规则集清空。
	if len(res.Entries) == 0 {
		return nil, fmt.Errorf("订阅内容解析后没有任何有效规则（%d 行被跳过）", res.Skipped)
	}
	return res, nil
}

// SplitByKind 把条目按 kind 拆成两个域名切片。
func SplitByKind(entries []Entry) (block, allow []string) {
	for _, e := range entries {
		if e.Kind == "allow" {
			allow = append(allow, e.Domain)
		} else {
			block = append(block, e.Domain)
		}
	}
	return block, allow
}

// plausibleGlob 判断解析出来的通配模式是否安全。
//
// 与 plausibleDomain 的目的相同，但模式里允许 *。三条硬约束：
//
//   - 至少两个标签：单标签模式（`*`、`com*`）会把匹配范围放大到整个后缀；
//   - 除最后一段外，必须至少有一个**含字母的字面标签**。这条挡的是
//     `*.*.com` 这种写法 —— 它等价于「拦掉所有 .com」，而列表作者的真实
//     意图（`missav.*`、`ulog*.*`）都带着具体的字面段；
//   - 字面 TLD 必须含字母，挡住 `192.168*` 这类 IP 片段。
func plausibleGlob(d string) bool {
	if d == "" || len(d) > 253 || !strings.Contains(d, "*") {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		if strings.ContainsAny(l, "_ 	/\\:#?[]{}()<>@!$%^&'\"") {
			return false
		}
	}
	// 最后一段之前必须有一个含字母的字面标签。
	hasLiteral := false
	for _, l := range labels[:len(labels)-1] {
		if l == "*" {
			continue
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				hasLiteral = true
				break
			}
		}
		if hasLiteral {
			break
		}
	}
	if !hasLiteral {
		return false
	}
	// 字面 TLD 必须含字母；通配 TLD（`missav.*`）由上面的 hasLiteral 兜住。
	tld := labels[len(labels)-1]
	if strings.Contains(tld, "*") {
		return true
	}
	for i := 0; i < len(tld); i++ {
		c := tld[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// isBlackholeTarget 判断一个改写目标是不是「黑洞」语义。
//
// 是黑洞 → 这条规则的真实意图是拦截，可以降级成拦/放；
// 是真实 IP → 意图是「指到这个优选 IP」（加速），订阅层承载不了改写，只能忽略。
//
// 实测两类写法都会出现在公共列表里：
//
//	0.0.0.0 ads.example.com            hosts 拦截
//	142.250.157.188 mtalk.google.com   指定优选 IP（加速，绝不能当拦截）
//
// 判定口径：
//   - 非地址目标（NXDOMAIN / REFUSED / NOERROR / localhost）→ 黑洞；
//   - 未指定或回环 IP（0.0.0.0 / :: / 127.0.0.1 / ::1）→ 黑洞；
//   - 其它 IP → **不是**黑洞（这就是会把 Google 推送拦死的那个坑）；
//   - 域名目标（ad-block.dns.adguard.com 这类拦截页域名）→ 黑洞。
//     域名目标在拦截列表里几乎总是黑洞域名，且旧实现一律按拦截处理，
//     保持这一行为不动，只把「真实 IP」这一类挑出来。
func isBlackholeTarget(v string) bool {
	t := strings.ToLower(strings.TrimSpace(v))
	t = strings.Trim(t, "[]")
	// AdGuard 的非地址目标：$dnsrewrite=NXDOMAIN / REFUSED / NOERROR
	switch t {
	case "nxdomain", "refused", "noerror", "localhost", "localhost.localdomain":
		return true
	}
	if ip := net.ParseIP(t); ip != nil {
		return ip.IsUnspecified() || ip.IsLoopback()
	}
	// 非 IP：按黑洞域名处理（拦截页域名）。
	return true
}

// plausibleDomain 判断解析出来的字符串是否像一个真实域名。
//
// 为什么必须校验：公共列表里混着外观规则与 CSS 片段，解析后会得到
// "_468x60_"、"192.168" 这类「域名」。单标签条目（com / net）尤其危险 ——
// 后缀匹配下 `com` 命中所有 .com 域名，一条 allow 就能把整个 TLD 放行。
// 宁可丢掉这些行（计入 Invalid 便于排查），也不能让它们进规则集。
func plausibleDomain(d string) bool {
	if d == "" || len(d) > 253 {
		return false
	}
	// 至少两段：裸 TLD 一律拒绝
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		// 下划线是服务记录（_dmarc）或 CSS 片段（_ad_300），不是域名
		if strings.ContainsAny(l, "_ \t/\\:*?#[]{}()<>@!$%^&'\"") {
			return false
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
	}
	// 顶级域必须含字母：挡住 "192.168" 这种 IP 片段
	tld := labels[len(labels)-1]
	hasLetter := false
	for i := 0; i < len(tld); i++ {
		c := tld[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			hasLetter = true
			break
		}
	}
	return hasLetter
}
