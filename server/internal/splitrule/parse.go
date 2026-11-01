package splitrule

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
)

// Parse 按格式解析规则文件，返回域名列表。
//
// 三种格式都在线上跑过，各有各的坑：
//
//	adblock           AdGuard/Adblock 语法，可能带 [/domain/] 上游前缀
//	gfwlist           gfwlist 原始文件，整体 Base64 编码，内含 Adblock 语法
//	dnsmasq           dnsmasq 的 server=/domain/ip 格式，语义是白名单
//	plain             每行一个裸域名
func Parse(content []byte, format string) ([]string, error) {
	switch format {
	case "adblock":
		return parseAdblock(content), nil
	case "gfwlist":
		return parseGFWList(content)
	case "dnsmasq", "dnsmasq-inverse":
		return parseDnsmasq(content), nil
	case "plain":
		return parsePlain(content), nil
	default:
		return parseAdblock(content), nil
	}
}

// parsePlain 每行一个域名，忽略空行与 # 注释。
func parsePlain(content []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// parseAdblock 解析 Adblock / AdGuard 语法。
//
// 需要处理的实际形态（都来自线上真实的 gfwlist 转换产物）：
//
//	[/0rz.tw/]tls://1.1.1.1      ← AdGuard 的「按域名指定上游」，域名在方括号里
//	||example.com^               ← 标准 Adblock 域名规则
//	|http://example.com/         ← 带协议的规则
//	@@||example.com^             ← 例外规则（白名单），跳过
//	! comment                    ← 注释
//	example.com                  ← 裸域名
func parseAdblock(content []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") {
			continue
		}
		// 例外规则不参与「该走境外」的判定。
		if strings.HasPrefix(line, "@@") {
			continue
		}

		// 形态一：[/domain/]upstream —— 域名在方括号里，后面是上游地址。
		// 这是 AdGuard 的域名分流语法，恰好也是 gfwlist 转换工具的输出格式。
		if strings.HasPrefix(line, "[/") {
			if end := strings.Index(line, "/]"); end > 2 {
				for _, d := range strings.Split(line[2:end], "/") {
					if d = cleanRuleDomain(d); d != "" {
						out = append(out, d)
					}
				}
			}
			continue
		}

		// 形态二：标准规则。剥掉 || 前缀、^ 后缀、协议头。
		d := cleanRuleDomain(line)
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// cleanRuleDomain 从一条规则里提取出域名。
//
// 处理：|| 前缀、^ 与 | 后缀、http(s):// 协议头、路径部分、* 通配。
// 提取不出合法域名时返回空串（调用方跳过）。
func cleanRuleDomain(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// 去掉修饰符（$third-party 之类）
	if i := strings.IndexByte(s, '$'); i >= 0 {
		s = s[:i]
	}
	// 先剥锚点再剥协议头。顺序不能反：gfwlist 里有 |http://example.com/
	// 这种叠加写法，先查协议头会因为开头的 | 匹配不上，整条规则被丢掉。
	s = strings.TrimPrefix(s, "||")
	s = strings.TrimPrefix(s, "|")
	// 去掉协议头
	for _, p := range []string{"https://", "http://", "wss://", "ws://"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			s = s[len(p):]
			break
		}
	}
	s = strings.TrimSuffix(s, "^")
	s = strings.TrimSuffix(s, "|")
	// 去掉路径
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	// 去掉端口
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[i:], "]") {
		s = s[:i]
	}
	// 通配前缀 *.example.com → example.com
	s = strings.TrimPrefix(s, "*.")
	// 含剩余通配符的规则无法在后缀集合里表达，跳过
	if strings.ContainsAny(s, "*?") {
		return ""
	}
	// 必须含点且不含空格，才算域名
	if !strings.Contains(s, ".") || strings.ContainsAny(s, " \t") {
		return ""
	}
	return s
}

// parseGFWList 解析 gfwlist 原始文件。
//
// 整个文件是 Base64 编码的 Adblock 语法。有些镜像会直接给明文，
// 所以先尝试解码，解不开就按明文处理 —— 两种情况线上都遇到过。
func parseGFWList(content []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(content)
	// 去掉可能的换行（Base64 内容常带换行）
	compact := bytes.ReplaceAll(trimmed, []byte("\n"), nil)
	compact = bytes.ReplaceAll(compact, []byte("\r"), nil)

	decoded, err := base64.StdEncoding.DecodeString(string(compact))
	if err != nil {
		// 也试 URL-safe 变体
		if d2, err2 := base64.URLEncoding.DecodeString(string(compact)); err2 == nil {
			return parseAdblock(d2), nil
		}
		// 解不开就当明文 Adblock 处理
		if bytes.Contains(trimmed, []byte("||")) || bytes.Contains(trimmed, []byte("[/")) {
			return parseAdblock(trimmed), nil
		}
		return nil, fmt.Errorf("gfwlist 解析失败（既不是有效 Base64 也不是明文规则）: %w", err)
	}
	return parseAdblock(decoded), nil
}

// parseDnsmasq 解析 dnsmasq 的 server=/domain/ip 格式。
//
// 这类文件是「国内域名白名单」，语义与黑名单相反，调用方需按 inverse 处理。
func parseDnsmasq(content []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 形如 server=/example.com/114.114.114.114
		if !strings.HasPrefix(line, "server=/") {
			continue
		}
		rest := strings.TrimPrefix(line, "server=/")
		if i := strings.IndexByte(rest, '/'); i > 0 {
			if d := cleanRuleDomain(rest[:i]); d != "" {
				out = append(out, d)
			}
		}
	}
	return out
}

// IsInverseFormat 判断某格式是否为白名单语义。
func IsInverseFormat(format string) bool {
	return format == "dnsmasq-inverse"
}
