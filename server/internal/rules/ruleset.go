package rules

import (
	"fmt"
	"net"
	"strings"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// RewriteTTL 是改写记录使用的 TTL（秒）。
//
// 取 60 而不是 0：为 0 会让部分客户端每次查询都回源，失去本地改写的意义；
// 太长又不利于用户改完立刻生效。60s 是经验值。
const RewriteTTL = 60

// RuleSet 是单个用户的完整规则集，构建完成后只读。
//
// 五个 Trie 分别对应文档 5.3 的优先级顺序，按顺序查询，首个命中即返回。
type RuleSet struct {
	// 优先级 1：用户自定义 allow
	userAllow *trie
	// 优先级 2：用户自定义 rewrite
	userRewrite *trie
	// 优先级 3：用户自定义 block
	userBlock *trie
	// 优先级 4：订阅 allowlist
	subAllow *trie
	// 优先级 5：订阅 blocklist
	subBlock *trie

	// Stats 是规则条数统计，供 UI 展示。
	Stats Stats
}

// Stats 记录规则集规模。
type Stats struct {
	UserAllow   int `json:"user_allow"`
	UserBlock   int `json:"user_block"`
	UserRewrite int `json:"user_rewrite"`
	SubAllow    int `json:"sub_allow"`
	SubBlock    int `json:"sub_block"`
}

// Total 返回规则总条数。
func (s Stats) Total() int {
	return s.UserAllow + s.UserBlock + s.UserRewrite + s.SubAllow + s.SubBlock
}

// Decision 是一次规则匹配的结论。
type Decision struct {
	// Action 是最终动作。
	Action Action
	// Rewrite 是命中的改写记录（仅 ActionRewrite 时非空）。
	Rewrite []dns.RR
	// Value 是改写目标的原文，用于日志。
	Value string
	// Source 标识命中来源，便于调试与 UI 展示：
	// user_allow | user_rewrite | user_block | sub_allow | sub_block
	Source string
}

// Build 从规则与订阅条目构建规则集。
//
// 非法规则被跳过而不是让整个构建失败：一条坏规则不该让用户完全失去防护
// （文档 5.4「非法规则跳过并记录，不中断构建」）。
func Build(userRules []model.Rule, subAllow, subBlock []string) (*RuleSet, error) {
	rs := &RuleSet{
		userAllow:   newTrie(),
		userRewrite: newTrie(),
		userBlock:   newTrie(),
		subAllow:    newTrie(),
		subBlock:    newTrie(),
	}

	for _, r := range userRules {
		if err := rs.addUserRule(r); err != nil {
			// 记录型错误不影响其它规则；这里不返回错误，只跳过。
			continue
		}
	}

	for _, d := range subAllow {
		if dom, err := normalizeDomain(d); err == nil {
			rs.subAllow.insert(dom, false, nil, nil, "")
		}
	}
	for _, d := range subBlock {
		if dom, err := normalizeDomain(d); err == nil {
			rs.subBlock.insert(dom, false, nil, nil, "")
		}
	}

	rs.Stats = Stats{
		UserAllow:   rs.userAllow.size,
		UserBlock:   rs.userBlock.size,
		UserRewrite: rs.userRewrite.size,
		SubAllow:    rs.subAllow.size,
		SubBlock:    rs.subBlock.size,
	}
	return rs, nil
}

// addUserRule 把一条用户规则插入对应的 Trie。
func (rs *RuleSet) addUserRule(r model.Rule) error {
	domain, wildcard, err := splitPattern(r.Pattern)
	if err != nil {
		return err
	}
	qtypes := normalizeQTypes(r.QTypes)

	switch r.Kind {
	case model.KindAllow:
		rs.userAllow.insert(domain, wildcard, qtypes, nil, "")
	case model.KindBlock:
		rs.userBlock.insert(domain, wildcard, qtypes, nil, "")
	case model.KindRewriteA, model.KindRewriteAAAA, model.KindRewriteCNAME:
		rrs, err := buildRewriteRR(domain, r.Value, r.Kind)
		if err != nil {
			return err
		}
		rs.userRewrite.insert(domain, wildcard, qtypes, rrs, r.Value)
	default:
		return fmt.Errorf("未知规则类型 %q", r.Kind)
	}
	return nil
}

// splitPattern 把规则 pattern 拆成「域名 + 是否通配」。
//
// 同时兼容 AdGuard 的 ||domain^ 写法与裸域名、*.domain 写法。
func splitPattern(pattern string) (string, bool, error) {
	p := strings.TrimSpace(pattern)
	// 去掉锚点与修饰符，允许 API 直接传完整 AdGuard 语法。
	if idx := strings.IndexByte(p, '$'); idx >= 0 {
		p = p[:idx]
	}
	p = strings.TrimPrefix(p, "@@")
	p = strings.TrimPrefix(p, "||")
	p = strings.TrimSuffix(p, "^")
	p = strings.TrimSuffix(p, "|")
	p = strings.TrimPrefix(p, "|")
	if idx := strings.IndexByte(p, '/'); idx >= 0 {
		p = p[:idx]
	}

	wildcard := false
	if strings.HasPrefix(p, "*.") {
		wildcard = true
		p = strings.TrimPrefix(p, "*.")
	}
	if strings.Contains(p, "*") {
		return "", false, fmt.Errorf("模式 %q 含无法在 DNS 层表达的通配符", pattern)
	}
	dom, err := normalizeDomain(p)
	if err != nil {
		return "", false, err
	}
	return dom, wildcard, nil
}

// buildRewriteRR 在构建期预生成应答记录，使查询路径零分配。
func buildRewriteRR(domain, target, kind string) ([]dns.RR, error) {
	if target == "" {
		return nil, fmt.Errorf("改写目标为空")
	}
	fqdn := dns.Fqdn(domain)

	if ip := net.ParseIP(strings.Trim(target, "[]")); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return []dns.RR{&dns.A{
				Hdr: dns.RR_Header{Name: fqdn, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: RewriteTTL},
				A:   v4,
			}}, nil
		}
		return []dns.RR{&dns.AAAA{
			Hdr:  dns.RR_Header{Name: fqdn, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: RewriteTTL},
			AAAA: ip.To16(),
		}}, nil
	}

	// 非 IP：当作 CNAME 目标。
	targetDom, err := normalizeDomain(target)
	if err != nil {
		return nil, fmt.Errorf("改写目标 %q 既不是 IP 也不是合法域名: %w", target, err)
	}
	if kind == model.KindRewriteA || kind == model.KindRewriteAAAA {
		return nil, fmt.Errorf("改写目标 %q 不是 IP，无法用于 %s", target, kind)
	}
	return []dns.RR{&dns.CNAME{
		Hdr:    dns.RR_Header{Name: fqdn, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: RewriteTTL},
		Target: dns.Fqdn(targetDom),
	}}, nil
}

// Match 按文档 5.3 的优先级顺序匹配域名。
//
// 顺序：用户 allow > 用户 rewrite > 用户 block > 订阅 allow > 订阅 block > 未命中。
// 采用 first-match-wins，不做多规则聚合。
func (rs *RuleSet) Match(qname string, qtype uint16) Decision {
	domain := strings.ToLower(strings.TrimSuffix(qname, "."))
	if domain == "" {
		return Decision{Action: ActionNone}
	}
	if rs == nil {
		return Decision{Action: ActionNone}
	}

	// 优先级 1：白名单必须绝对优先，用户显式放行的域名不能被任何订阅规则拦住。
	if ok, _, _ := rs.userAllow.lookup(domain, qtype); ok {
		return Decision{Action: ActionAllow, Source: "user_allow"}
	}
	// 优先级 2：改写优先于拦截（改写是更具体的意图）。
	if ok, rrs, val := rs.userRewrite.lookup(domain, qtype); ok {
		return Decision{Action: ActionRewrite, Rewrite: rrs, Value: val, Source: "user_rewrite"}
	}
	// 优先级 3：用户自定义拦截。
	if ok, _, _ := rs.userBlock.lookup(domain, qtype); ok {
		return Decision{Action: ActionBlock, Source: "user_block"}
	}
	// 优先级 4：订阅白名单。
	if ok, _, _ := rs.subAllow.lookup(domain, qtype); ok {
		return Decision{Action: ActionAllow, Source: "sub_allow"}
	}
	// 优先级 5：订阅黑名单。
	if ok, _, _ := rs.subBlock.lookup(domain, qtype); ok {
		return Decision{Action: ActionBlock, Source: "sub_block"}
	}
	return Decision{Action: ActionNone}
}

// Empty 判断规则集是否为空（用于跳过无意义的匹配开销）。
func (rs *RuleSet) Empty() bool { return rs == nil || rs.Stats.Total() == 0 }

// Validate 校验一条规则是否可以成功构建，供 POST /rules/validate 使用。
func Validate(kind, pattern, value string, qtypes []string) error {
	domain, wildcard, err := splitPattern(pattern)
	if err != nil {
		return err
	}
	switch kind {
	case model.KindAllow, model.KindBlock:
		if value != "" {
			return fmt.Errorf("类型 %s 不应带改写目标", kind)
		}
	case model.KindRewriteA, model.KindRewriteAAAA, model.KindRewriteCNAME:
		if _, err := buildRewriteRR(domain, value, kind); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知规则类型 %q", kind)
	}
	if wildcard && domain == "" {
		return fmt.Errorf("通配模式缺少域名")
	}
	_ = qtypes
	return nil
}
