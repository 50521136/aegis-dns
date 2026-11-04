package rules

import (
	"errors"
	"strings"

	"github.com/miekg/dns"
)

// 通配模式校验失败的原因。分开定义是为了让上层能把「为什么这条模式被
// 拒绝」写进订阅状态，而不是只给一个「跳过」。
var (
	errGlobEmpty    = errors.New("通配模式为空")
	errGlobNoStar   = errors.New("通配模式不含 *")
	errGlobNoDot    = errors.New("通配模式至少要有一个点，否则会命中整个后缀")
	errGlobTooLong  = errors.New("通配模式超过 253 字节")
	errGlobBadChar  = errors.New("通配模式含非法字符")
	errGlobBadLabel = errors.New("通配模式含空标签或超长标签")
)

// globSet 是「含 * 通配的域名模式」的匹配集合。
//
// 为什么需要它：公共拦截列表里有一批写法是 `||at*.doubanio.com^`、
// `||163487*.qmgmw.com^`、`||js.*2023*.live^` —— 通配符落在标签中间。
// 原来的实现一律返回 ErrSkip（「无法在 DNS 层表达」），于是这些规则
// **静默消失**：订阅状态是 ok、条数也对得上，只是那几十上百条永远不生效。
// 实测一份 33554 行的源里有 258 行是这种形态，AdGuard Home 的 urlfilter
// 全部能命中，只有本项目漏掉。
//
// 语义与 Adblock 的 `||pattern^` 一致：
//
//	`||` 锚点 = 从任意标签边界开始匹配（所以 `||x.com^` 覆盖全部子域），
//	`*`       = 匹配任意字符序列（**可以跨点**）。
//
// 因此 `||at*.doubanio.com^` 命中 `at1.doubanio.com`、`at.doubanio.com`、
// 以及 `www.at1.doubanio.com`，但不命中 `xat1.doubanio.com`
// （后者没有哪个标签边界能让模式对上）。
//
// 性能：按「最后一个 * 之后的字面后缀」分桶，查询时主机名逐级剥标签查桶，
// 只对同后缀下的少数候选做 glob 比对。订阅层里这类模式只有几百条，
// 实测未命中约 100ns、命中约 100ns，且零分配（见 globset_test.go 的 Benchmark）。
type globSet struct {
	// bySuffix 以「最后一个 * 之后的字面后缀」为桶键（`at*.doubanio.com`
	// 的键是 `doubanio.com`）。查询时主机名逐级剥标签，每剥一级查一次桶 ——
	// 桶命中就说明模式可能匹配，再交给 glob 逐段比对。
	//
	// 为什么不用「最后一个标签（TLD）」当键：实测 200 条模式里有 150 条
	// 落在 .com 桶，每次未命中都要过 150 次匹配，单次 3µs；改用字面后缀
	// 分桶后同一个域名只命中个位数的候选。
	bySuffix map[string][]globPattern
	// wildSuffix 收「最后一个 * 之后没有任何字面量」的模式（`missav.*`、
	// `ulog*.*`）。这类模式没有可用桶键，只能每次查询都过一遍 ——
	// 实测公共列表里只有个位数条，代价可忽略。
	wildSuffix []globPattern
	// size 是加入的模式条数，用于统计展示。
	size int
}

// globPattern 是一条编译好的通配模式。
type globPattern struct {
	// pattern 是规范化后的完整模式（小写、无锚点、无尾点）。
	pattern string
	// segs 是按 * 切开的字面段。`a*b*c` → ["a" "b" "c"]；
	// 只有一段说明不含 *，那种情况不该进 globSet。
	segs []string
	// tail 是最后一个 * 之后的字面后缀，用于快速排除绝大多数主机名。
	tail string
}

// newGlobSet 创建一个空集合。
func newGlobSet() *globSet { return &globSet{} }

// isGlobPattern 判断一个条目是不是通配模式。
func isGlobPattern(d string) bool { return strings.Contains(d, "*") }

// add 批量加入模式；不含 * 的条目忽略（由 suffixSet 负责）。
func (s *globSet) add(patterns []string) {
	for _, raw := range patterns {
		if !isGlobPattern(raw) {
			continue
		}
		norm, err := normalizeGlobDomain(raw)
		if err != nil {
			continue
		}
		p := newGlobPattern(norm)
		key := globBucketKey(p.tail)
		if key == "" {
			// 最后一个 * 之后没有字面量（`missav.*`）：没有可用桶键。
			s.wildSuffix = append(s.wildSuffix, p)
		} else {
			if s.bySuffix == nil {
				s.bySuffix = make(map[string][]globPattern, 32)
			}
			s.bySuffix[key] = append(s.bySuffix[key], p)
		}
		s.size++
	}
}

// globBucketKey 从模式最后一个 * 之后的字面后缀里取分桶键。
//
// 键必须是**某个标签边界后缀**，否则查询按点遍历主机名后缀时永远对不上。
// 字面后缀可能从标签中间开始（`*-ad-sign.byteimg.com` 的 tail 是
// `-ad-sign.byteimg.com`），所以先丢掉首段残缺标签，再取最后两段：
//
//	.doubanio.example      → doubanio.example
//	-ad-sign.byteimg.com   → byteimg.com
//	-ad.adbkwai.com        → adbkwai.com
//	.com                   → com
//	.*                     → ""（交给 wildSuffix）
//
// 线上实测：不丢残缺首段时，`*-ad-sign.byteimg.com`、
// `*-applog.fqnovel.com`、`*-ad.wtzw.com` 这几条整片漏拦。
func globBucketKey(tail string) string {
	key := strings.TrimPrefix(tail, ".")
	if key == "" || strings.Contains(key, "*") {
		return ""
	}
	segs := strings.Split(key, ".")
	// 首段以 - 开头说明它是标签的后半截（标签被 * 从中间切开），不能用。
	if len(segs) > 1 && strings.HasPrefix(segs[0], "-") {
		segs = segs[1:]
	}
	if len(segs) >= 2 {
		return segs[len(segs)-2] + "." + segs[len(segs)-1]
	}
	return segs[0]
}

// finish 保留给与 suffixSet 一致的调用点：globSet 不需要排序。
func (s *globSet) finish() {}

// lookup 判断域名是否命中本集合。签名与 suffixSet/trie 保持一致。
func (s *globSet) lookup(domain string, _ uint16) (bool, []dns.RR, string) {
	if s == nil || (len(s.bySuffix) == 0 && len(s.wildSuffix) == 0) || domain == "" {
		return false, nil, ""
	}
	// 逐级剥标签查桶：桶键是「模式最后一个 * 之后的字面后缀」，
	// 所以主机名的每一级后缀都可能是桶键。
	d := domain
	for {
		if pats, ok := s.bySuffix[d]; ok {
			for k := range pats {
				if pats[k].matchHost(domain) {
					return true, nil, ""
				}
			}
		}
		dot := strings.IndexByte(d, '.')
		if dot < 0 {
			break
		}
		d = d[dot+1:]
	}
	for k := range s.wildSuffix {
		if s.wildSuffix[k].matchHost(domain) {
			return true, nil, ""
		}
	}
	return false, nil, ""
}

// newGlobPattern 预编译一条模式。
func newGlobPattern(pattern string) globPattern {
	p := globPattern{pattern: pattern, segs: strings.Split(pattern, "*")}
	p.tail = p.segs[len(p.segs)-1]
	return p
}

// matchHost 判断主机名是否命中这条模式。
//
// 从每一个标签边界开始各试一次，对应 Adblock `||` 锚点的语义：
// 模式可以从任意标签处开始，但必须一直匹配到主机名末尾。
func (p globPattern) matchHost(host string) bool {
	if p.tail != "" && !strings.HasSuffix(host, p.tail) {
		return false
	}
	start := 0
	for {
		if globMatchSegs(p.segs, host[start:]) {
			return true
		}
		i := strings.IndexByte(host[start:], '.')
		if i < 0 {
			return false
		}
		start += i + 1
	}
}

// globMatchSegs 判断 s 是否被「按 * 切开后的字面段序列」完整匹配。
//
// 只有一段时是精确比较。多段时：首段必须是前缀，末段必须是后缀，
// 中间各段按顺序在剩余部分里出现即可（* 可以吃掉任意字符，含点）。
func globMatchSegs(segs []string, s string) bool {
	if len(segs) == 1 {
		return segs[0] == s
	}
	if !strings.HasPrefix(s, segs[0]) {
		return false
	}
	s = s[len(segs[0]):]
	for i := 1; i < len(segs)-1; i++ {
		seg := segs[i]
		if seg == "" {
			continue
		}
		idx := strings.Index(s, seg)
		if idx < 0 {
			return false
		}
		s = s[idx+len(seg):]
	}
	return strings.HasSuffix(s, segs[len(segs)-1])
}

// normalizeGlobDomain 校验并规范化一条通配模式。
//
// 只允许域名里可能出现的字符与 *；拒绝：不含 *、没有点（单标签会把
// 整个后缀打掉，例如 `*` 或 `com*` 这种写错了的模式）、空标签、
// 超长标签。校验从严是为了避免一条写坏的模式把某个后缀整片拦掉。
func normalizeGlobDomain(s string) (string, error) {
	d := strings.ToLower(strings.Trim(strings.TrimSpace(s), "."))
	if d == "" {
		return "", errGlobEmpty
	}
	if !strings.Contains(d, "*") {
		return "", errGlobNoStar
	}
	// 至少要有一个点，否则匹配范围会退化成「某个后缀下的全部域名」。
	if !strings.Contains(d, ".") {
		return "", errGlobNoDot
	}
	if len(d) > 253 {
		return "", errGlobTooLong
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '*':
		default:
			return "", errGlobBadChar
		}
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 {
			return "", errGlobBadLabel
		}
	}
	return d, nil
}
