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
	// Errors 是「看起来是规则但解析失败」的前若干条错误。
	Errors []error
	// Detected 是嗅探出的格式（format=auto 时）。
	Detected Format
	// RewriteDowngraded 是被降级为拦/放（而非丢弃）的 $dnsrewrite 行数。
	//
	// 公共拦截列表大量用 ||x^$dnsrewrite=<黑洞域名> 表达「解析到拦截页」，
	// 语义上就是拦截。原实现把带 value 的行一律丢弃，实测某个源 1072 行
	// 全被跳过、订阅直接变成 0 条 —— 规则不是被拒绝，是**静默消失**，
	// 从状态里也看不出少了什么。这里改成按列表类型降级，并把这个数字
	// 带到状态里，至少让人知道「这份源是靠降级才生效的」。
	RewriteDowngraded int
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

		// 先按目标格式做一次快速筛选，避免对纯域名列表走完整的 Adblock 解析。
		switch format {
		case FormatHosts:
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				res.Skipped++
				continue
			}
			if !looksLikeHostsLine(trimmed) {
				res.Skipped++
				continue
			}
		case FormatDomain:
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
				res.Skipped++
				continue
			}
		}

		p, err := rules.ParseLine(line)
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
		// 订阅只承载「拦/放」两类，不承载改写能力。但带 $dnsrewrite 的行
		// 不能直接丢：公共列表用它表达「把这个域名解析到黑洞域名」，
		// 本质上就是拦截。按列表类型降级成对应的类别。
		kind := "block"
		if p.Kind == "allow" {
			kind = "allow"
		}
		if p.Value != "" {
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
