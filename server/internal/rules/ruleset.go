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
	// 优先级 4~6 与 9~10 是全局层。它由所有租户共享同一份 ——
	// 曾经是每个用户各建一份，3 个用户就是 3 份 62 万条，
	// 用户数一多内存就线性爆炸。共享后无论多少租户都只有一份。
	global *GlobalLayer
	// 优先级 7~10 是订阅层：条目动辄几十万，用排序数组而不是 trie。
	// trie 每节点一个 map，六十万域名要吃到 640MB（线上因此 OOM），
	// 排序数组同样的数据只要约 40MB。见 suffixset.go。
	//
	// 优先级 7：用户自己的订阅 allowlist
	subAllow *suffixSet
	// 优先级 8：用户自己的订阅 blocklist
	subBlock *suffixSet

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

	// 全局层计数（UseGlobalRules 为真时才会计入）。
	GlobalAllow   int `json:"global_allow"`
	GlobalBlock   int `json:"global_block"`
	GlobalRewrite int `json:"global_rewrite"`
	GSubAllow     int `json:"global_sub_allow"`
	GSubBlock     int `json:"global_sub_block"`
}

// Total 返回规则总条数。
func (s Stats) Total() int {
	return s.UserAllow + s.UserBlock + s.UserRewrite + s.SubAllow + s.SubBlock +
		s.GlobalAllow + s.GlobalBlock + s.GlobalRewrite + s.GSubAllow + s.GSubBlock
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
	// user_allow | user_rewrite | user_block
	// global_allow | global_rewrite | global_block
	// sub_allow | sub_block | global_sub_allow | global_sub_block
	Source string
}

// Layers 是构建一个用户规则集所需的全部输入。
//
// 分层的意义在于优先级：用户自己写的规则压过管理员的全局规则，
// 而两者的显式规则都压过订阅条目（订阅是批量导入的，误杀概率最高）。
type Layers struct {
	// UserRules 是该用户自己的规则（is_global = 0）。
	UserRules []model.Rule
	// SubAllow / SubBlock 是该用户自己的订阅条目。
	SubAllow []string
	SubBlock []string
	// Global 是全局层。nil 表示该用户关闭了全局规则（UseGlobalRules = false）。
	Global *model.GlobalSnap
}

// Build 从分层输入构建规则集。
//
// 非法规则被跳过而不是让整个构建失败：一条坏规则不该让用户完全失去防护
// （文档 5.4「非法规则跳过并记录，不中断构建」）。
func Build(l Layers) (*RuleSet, error) {
	rs := &RuleSet{
		userAllow:   newTrie(),
		userRewrite: newTrie(),
		userBlock:   newTrie(),

		subAllow: newSuffixSet(),
		subBlock: newSuffixSet(),
	}

	for _, r := range l.UserRules {
		// 记录型错误不影响其它规则；这里不返回错误，只跳过。
		_ = rs.addRule(r, rs.userAllow, rs.userRewrite, rs.userBlock)
	}
	rs.subAllow.add(l.SubAllow)
	rs.subBlock.add(l.SubBlock)

	if l.Global != nil {
		rs.global = BuildGlobal(l.Global)
	}

	// 订阅层收尾：去重 + 排序。必须在返回前做，否则查询会走未排序数组，
	// 二分结果无意义。
	rs.subAllow.finish()
	rs.subBlock.finish()

	rs.Stats = Stats{
		UserAllow:   rs.userAllow.size,
		UserBlock:   rs.userBlock.size,
		UserRewrite: rs.userRewrite.size,
		SubAllow:    rs.subAllow.size,
		SubBlock:    rs.subBlock.size,
	}
	return rs, nil
}

// insertDomains 把一批域名插进指定的 Trie，非法域名跳过。
func insertDomains(t *trie, domains []string) {
	for _, d := range domains {
		if dom, err := normalizeDomain(d); err == nil {
			t.insert(dom, false, nil, nil, "")
		}
	}
}

// addRule 把一条规则插入给定的一组 Trie（按 kind 分派）。
//
// 用传入而非写死 rs.userXxx，是为了让同一套解析逻辑同时服务用户层与全局层。
func (rs *RuleSet) addRule(r model.Rule, allow, rewrite, block *trie) error {
	domain, wildcard, err := splitPattern(r.Pattern)
	if err != nil {
		return err
	}
	qtypes := normalizeQTypes(r.QTypes)

	switch r.Kind {
	case model.KindAllow:
		allow.insert(domain, wildcard, qtypes, nil, "")
	case model.KindBlock:
		block.insert(domain, wildcard, qtypes, nil, "")
	case model.KindRewriteA, model.KindRewriteAAAA, model.KindRewriteCNAME:
		rrs, err := buildRewriteRR(domain, r.Value, r.Kind)
		if err != nil {
			return err
		}
		rewrite.insert(domain, wildcard, qtypes, rrs, r.Value)
	default:
		return fmt.Errorf("未知规则类型 %q", r.Kind)
	}
	return nil
}

// SplitPattern 把规则 pattern 拆成「域名 + 是否通配」。
//
// 导出出来是给 diagnose 包用的：诊断证据要回填「命中的是哪条规则」，
// 需要把 pattern 里的域名叫出来。复用它而不是在新包重写一遍，是为了
// 保证「规则怎么解析」这件事只有一处实现 —— 一旦两处口径不同，
// 诊断结论就会和 dnsd 的实际行为对不上。
func SplitPattern(pattern string) (string, bool, error) {
	return splitPattern(pattern)
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

// Match 按优先级顺序匹配域名，first-match-wins，不做多规则聚合。
//
// 顺序（数字越小越优先）：
//
//	1  user_allow          用户显式放行
//	2  user_rewrite        用户显式改写
//	3  user_block          用户显式拦截
//	4  global_allow        管理员放行（用户可用自己的 allow 覆盖）
//	5  global_rewrite      管理员改写
//	6  global_block        管理员拦截
//	7  sub_allow           用户订阅白名单
//	8  sub_block           用户订阅黑名单
//	9  global_sub_allow    全局订阅白名单
//	10 global_sub_block    全局订阅黑名单
//
// 关键设计：**显式规则一律压过订阅条目**。订阅是整份导入的、误杀概率最高，
// 所以「管理员加的白名单」必须能盖住「用户订阅里的黑名单」，否则用户随便
// 订一份大列表就能把管理员的放行规则冲掉。反过来，用户自己写的 allow
// 排在最前面，所以「全局拦了某个域名、用户自己加白名单」是生效的。
func (rs *RuleSet) Match(qname string, qtype uint16) Decision {
	if rs == nil {
		return Decision{Action: ActionNone}
	}
	domain := strings.ToLower(strings.TrimSuffix(qname, "."))
	if domain == "" {
		return Decision{Action: ActionNone}
	}

	// 1-3：用户自己的规则。
	if ok, _, _ := rs.userAllow.lookup(domain, qtype); ok {
		return Decision{Action: ActionAllow, Source: "user_allow"}
	}
	if ok, rrs, val := rs.userRewrite.lookup(domain, qtype); ok {
		return Decision{Action: ActionRewrite, Rewrite: rrs, Value: val, Source: "user_rewrite"}
	}
	if ok, _, _ := rs.userBlock.lookup(domain, qtype); ok {
		return Decision{Action: ActionBlock, Source: "user_block"}
	}

	// 4-6：全局规则层（与所有租户共享同一份）。
	if rs.global == nil {
		goto subLayer
	}
	if ok, _, _ := rs.global.allow.lookup(domain, qtype); ok {
		return Decision{Action: ActionAllow, Source: "global_allow"}
	}
	if ok, rrs, val := rs.global.rewrite.lookup(domain, qtype); ok {
		return Decision{Action: ActionRewrite, Rewrite: rrs, Value: val, Source: "global_rewrite"}
	}
	if ok, _, _ := rs.global.block.lookup(domain, qtype); ok {
		return Decision{Action: ActionBlock, Source: "global_block"}
	}

subLayer:
	// 7-8：用户自己的订阅。
	if ok, _, _ := rs.subAllow.lookup(domain, qtype); ok {
		return Decision{Action: ActionAllow, Source: "sub_allow"}
	}
	if ok, _, _ := rs.subBlock.lookup(domain, qtype); ok {
		return Decision{Action: ActionBlock, Source: "sub_block"}
	}

	// 9-10：全局订阅（同样共享）。
	if rs.global != nil {
		if ok, _, _ := rs.global.subAllow.lookup(domain, qtype); ok {
			return Decision{Action: ActionAllow, Source: "global_sub_allow"}
		}
		if ok, _, _ := rs.global.subBlock.lookup(domain, qtype); ok {
			return Decision{Action: ActionBlock, Source: "global_sub_block"}
		}
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

// GlobalLayer 是全局规则层，由所有租户共享同一份实例。
//
// 为什么单独抽出来：全局层里是管理员那几条规则 + 订阅压平后的几十万域名，
// 而 RuleSet 是「每用户一个」。如果全局层跟着 RuleSet 走，
// 10 个租户就会把同一份 62 万条列表建 10 遍 —— 内存按租户数线性增长。
// 线上就是因为这个（3 个用户 × 62 万条）加上 trie 才把机器打爆的。
//
// 共享之后：无论多少租户，全局层都只有一份，内存与租户数无关。
type GlobalLayer struct {
	allow   *trie
	rewrite *trie
	block   *trie
	// 订阅层用排序数组（见 suffixset.go）
	subAllow *suffixSet
	subBlock *suffixSet

	globalAllowSize   int
	globalBlockSize   int
	globalRewriteSize int
	gsubAllowSize     int
	gsubBlockSize     int
}

// BuildGlobal 从全局快照构建一份可共享的全局层。
//
// 每次快照重建只调用一次，结果交给所有用户共用。
func BuildGlobal(g *model.GlobalSnap) *GlobalLayer {
	gl := &GlobalLayer{
		allow:    newTrie(),
		rewrite:  newTrie(),
		block:    newTrie(),
		subAllow: newSuffixSet(),
		subBlock: newSuffixSet(),
	}
	if g == nil {
		return gl
	}
	// 规则插入与用户层共用同一套 addRule 逻辑，保证优先级语义一致
	tmp := &RuleSet{}
	for _, r := range g.Rules {
		_ = tmp.addRule(r, gl.allow, gl.rewrite, gl.block)
	}
	gl.subAllow.add(g.SubAllow)
	gl.subBlock.add(g.SubBlock)
	gl.subAllow.finish()
	gl.subBlock.finish()

	gl.globalAllowSize = gl.allow.size
	gl.globalRewriteSize = gl.rewrite.size
	gl.globalBlockSize = gl.block.size
	gl.gsubAllowSize = gl.subAllow.size
	gl.gsubBlockSize = gl.subBlock.size
	return gl
}

// BuildWithGlobal 用一份共享的全局层构建某个用户的规则集。
//
// registry 在每次快照重建时先 BuildGlobal 一次，然后对每个用户调用本函数。
// 这样全局层只解析一次、只占一份内存。
// shared 为 nil 表示该用户关闭了全局规则（UseGlobalRules = false）。
func BuildWithGlobal(shared *GlobalLayer, l Layers) (*RuleSet, error) {
	l.Global = nil // 由 shared 提供，避免重复构建
	rs, err := Build(l)
	if err != nil {
		return rs, err
	}
	rs.global = shared
	// 必须在这里补算全局层计数。
	//
	// Build 内部算 Stats 时 rs.global 还是 nil，全局层计数一律是 0。
	// 而 handler 用 rs.Empty()（即 Stats.Total()==0）决定「要不要跑规则匹配」，
	// 于是「自己没有任何规则、全靠全局层」的用户会被判成空规则集，
	// 整个 Stage 4 被跳过 —— 表现就是该用户永远 0 拦截。
	// 线上 lz0423 就是这样：全局层 62 万条对他完全没生效。
	rs.refreshGlobalStats()
	return rs, nil
}

// refreshGlobalStats 把共享全局层的规模写进 Stats。
//
// Empty() 依赖 Stats.Total()，所以只要规则集挂了全局层，
// 这里就必须把 Global*/GSub* 填上，否则「只有全局规则」的租户会被误判为空。
func (rs *RuleSet) refreshGlobalStats() {
	if rs.global == nil {
		rs.Stats.GlobalAllow, rs.Stats.GlobalBlock, rs.Stats.GlobalRewrite = 0, 0, 0
		rs.Stats.GSubAllow, rs.Stats.GSubBlock = 0, 0
		return
	}
	rs.Stats.GlobalAllow = rs.global.globalAllowSize
	rs.Stats.GlobalBlock = rs.global.globalBlockSize
	rs.Stats.GlobalRewrite = rs.global.globalRewriteSize
	rs.Stats.GSubAllow = rs.global.gsubAllowSize
	rs.Stats.GSubBlock = rs.global.gsubBlockSize
}
