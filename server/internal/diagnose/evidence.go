// Package diagnose 为「用户反馈的误杀」做自动诊断。
//
// 设计原则（这一条决定了整个包的结构）：
//
//	规则是否误判是**确定性问题** —— rules.Match(domain) 能给出唯一正确答案。
//	所以绝不把「哪个订阅拦了它」交给模型去猜。分工是：
//	  证据收集（纯 Go，确定性） → 模型只做归因与措辞 → 人工审批
//
// 模型拿到的是一份已经算好的结构化证据，它的职责只是把事实翻译成
// 「误杀 / 规则正确 / 需人工」和一句给用户看的话。这样即使模型很弱、
// 甚至胡言乱语，最坏结果也只是建议不合理 —— 因为最终动作仍需人工确认，
// 且所有建议都要过 guard.go 的域名合法性闸门。
package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// 反馈里可能写多个域名（用户习惯一次性把几个都贴上）。
const maxDomainsPerFeedback = 6

// DomainEvidence 是单个域名的证据。
type DomainEvidence struct {
	Domain string `json:"domain"`
	QType  uint16 `json:"qtype"`
	// Action 是规则引擎的判定：allow / block / rewrite / none。
	Action string `json:"action"`
	// Layer 是命中的层，取自 rules.Decision.Source，例如
	// user_allow / global_allow / sub_block / global_sub_block。
	Layer string `json:"layer"`
	// LayerLabel 是给人看的层名。
	LayerLabel string `json:"layer_label"`
	// MatchedRule 是命中用户/全局规则时的条目原文（订阅层通常为空，
	// 因为订阅是压缩成域名数组进快照的，拿不到原始行）。
	MatchedRule string `json:"matched_rule,omitempty"`
	// SubSources 是命中订阅层时，该域名来自哪些订阅。
	SubSources []SubSource `json:"sub_sources,omitempty"`
	// RecentQueries 是近一小时该域名的实际查询记录（最多 5 条）。
	RecentQueries []QuerySample `json:"recent_queries,omitempty"`
	// BlockedRecently 汇总：近一小时被拦截过多少次。
	BlockedRecently int `json:"blocked_recently"`
}

// SubSource 指出某个域名来自哪个订阅。
type SubSource struct {
	SubID string `json:"sub_id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Kind  string `json:"kind"` // block / allow
	// Global 表示这是全局订阅（所有租户共享）。
	Global bool `json:"global"`
}

// QuerySample 是一条近期查询记录（已脱敏，不含客户端 IP）。
type QuerySample struct {
	Action    string `json:"action"`
	RCode     string `json:"rcode"`
	CacheHit  bool   `json:"cache_hit"`
	LatencyMS int64  `json:"latency_ms"`
	Proto     string `json:"proto"`
	UnixTime  int64  `json:"at"`
}

// Evidence 是一次诊断的完整输入。
type Evidence struct {
	FeedbackID string `json:"feedback_id"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	// Domains 是逐个域名的确定性结论。
	Domains []DomainEvidence `json:"domains"`
	// UserRuleCount / SubBlockCount 给出规模，帮助模型判断「是不是订阅太激进」。
	UserRuleCount int `json:"user_rule_count"`
	SubBlockCount int `json:"sub_block_count"`
	SubAllowCount int `json:"sub_allow_count"`
	// Enabled 表示该用户是否启用了服务。
	UserEnabled bool `json:"user_enabled"`
}

// Source 是本包需要的外部数据来源，由 api 层实现。
//
// 抽出接口的目的一是让本包可以用假数据做单测（不依赖 sqlite），
// 二是避免 diagnose 直接依赖 store 的具体实现。
type Source interface {
	// UserRules 返回该用户自己的规则（含全局规则需另行取，这里只要用户的）。
	UserRules(ctx context.Context, userID string) ([]model.Rule, error)
	// GlobalRules 返回全局规则。
	GlobalRules(ctx context.Context) ([]model.Rule, error)
	// UserSubDomains 返回该用户的订阅域名（block / allow 各一组）。
	UserSubDomains(ctx context.Context, userID string) (block, allow []string, err error)
	// GlobalSubDomains 返回全局订阅域名。
	GlobalSubDomains(ctx context.Context) (block, allow []string, err error)
	// SubSourcesOf 反查这些域名分别来自哪些订阅。
	SubSourcesOf(ctx context.Context, domains []string) (map[string][]SubSource, error)
	// RecentQueries 返回某用户某域名的近期查询（最多 limit 条，倒序）。
	RecentQueries(ctx context.Context, userID, domain string, limit int) ([]QuerySample, int, error)
	// UserEnabled 返回用户是否可用。
	UserEnabled(ctx context.Context, userID string) (bool, error)
}

// layerLabels 把 rules.Decision.Source 翻成人话。
var layerLabels = map[string]string{
	"user_allow":       "用户自定义放行",
	"user_rewrite":     "用户自定义改写",
	"user_block":       "用户自定义拦截",
	"global_allow":     "全局放行（管理员）",
	"global_rewrite":   "全局改写（管理员）",
	"global_block":     "全局拦截（管理员）",
	"sub_allow":        "用户订阅白名单",
	"sub_block":        "用户订阅黑名单",
	"global_sub_allow": "全局订阅白名单",
	"global_sub_block": "全局订阅黑名单",
}

// Collect 收集证据。整个过程不调用任何模型，可重复、可审计。
func Collect(ctx context.Context, src Source, fb model.Feedback, window int64) (*Evidence, error) {
	ev := &Evidence{
		FeedbackID: fb.ID,
		Kind:       fb.Kind,
		Message:    fb.Message,
	}

	userRules, err := src.UserRules(ctx, fb.UserID)
	if err != nil {
		return nil, fmt.Errorf("读取用户规则失败: %w", err)
	}
	globalRules, err := src.GlobalRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取全局规则失败: %w", err)
	}
	userBlock, userAllow, err := src.UserSubDomains(ctx, fb.UserID)
	if err != nil {
		return nil, fmt.Errorf("读取用户订阅失败: %w", err)
	}
	gBlock, gAllow, err := src.GlobalSubDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取全局订阅失败: %w", err)
	}

	ev.UserRuleCount = len(userRules)
	ev.SubAllowCount = len(userAllow)
	ev.SubBlockCount = len(userBlock)
	if ev.UserEnabled, err = src.UserEnabled(ctx, fb.UserID); err != nil {
		// 启用状态取不到不影响诊断，按可用处理。
		ev.UserEnabled = true
	}

	// 构建与 dnsd 完全一致的规则集：全局层共享 + 用户层。
	gl := rules.BuildGlobal(&model.GlobalSnap{
		Rules:    globalRules,
		SubAllow: gAllow,
		SubBlock: gBlock,
	})
	rs, err := rules.BuildWithGlobal(gl, rules.Layers{
		UserRules: userRules,
		SubAllow:  userAllow,
		SubBlock:  userBlock,
	})
	if err != nil {
		return nil, fmt.Errorf("构建规则集失败: %w", err)
	}

	domains := ExtractDomains(fb.Domain, fb.Message)
	// 只对可能命中的域名反查订阅来源，减少一次慢查询的输入量。
	if _, err := src.SubSourcesOf(ctx, domains); err != nil {
		return nil, fmt.Errorf("反查订阅来源失败: %w", err)
	}
	sources, _ := src.SubSourcesOf(ctx, domains)
	ruleIndex := indexRules(userRules, globalRules)

	for _, d := range domains {
		dec := rs.Match(d, 1) // 1 = A 记录
		de := DomainEvidence{
			Domain: d,
			QType:  1,
			Action: actionName(dec.Action),
			Layer:  dec.Source,
		}
		if lbl, ok := layerLabels[dec.Source]; ok {
			de.LayerLabel = lbl
		} else if dec.Source == "" {
			de.LayerLabel = "未命中任何规则（放行到上游）"
		} else {
			de.LayerLabel = dec.Source
		}
		if r, ok := ruleIndex[dec.Source+"|"+d]; ok {
			de.MatchedRule = r.Pattern
		}
		if subs, ok := sources[d]; ok {
			de.SubSources = subs
		}
		if qs, blocked, err := src.RecentQueries(ctx, fb.UserID, d, 5); err == nil {
			de.RecentQueries = qs
			de.BlockedRecently = blocked
		}
		ev.Domains = append(ev.Domains, de)
	}
	return ev, nil
}

func actionName(a rules.Action) string {
	switch a {
	case rules.ActionAllow:
		return "allow"
	case rules.ActionBlock:
		return "block"
	case rules.ActionRewrite:
		return "rewrite"
	default:
		return "none"
	}
}

// indexRules 建「层|域名 → 规则」的索引，供证据里回填命中条目原文。
//
// 订阅层不进这个索引：订阅在快照里被压平成纯域名数组，原始行早就没了，
// 硬要反推只会给出假信息。订阅来源由 SubSourcesOf 从数据库查，那是真数据。
func indexRules(userRules, globalRules []model.Rule) map[string]*model.Rule {
	out := make(map[string]*model.Rule, len(userRules)+len(globalRules))
	put := func(rs []model.Rule, prefix string) {
		for i := range rs {
			r := &rs[i]
			if !r.Enabled {
				continue
			}
			d, _, err := SplitPattern(r.Pattern)
			if err != nil || d == "" {
				continue
			}
			// 一个域名可能同时被同层的不同 kind 命中，用 kind 区分子键不会
			// 带来收益（同层同域名只会命中一条），这里按 kind 覆盖写入即可。
			out[prefix+"_"+r.Kind+"|"+d] = r
		}
	}
	put(userRules, "user")
	put(globalRules, "global")
	return out
}

// domainRe 允许的域名字符集（与 rules 包的合法性判断保持一致口径）。
func looksLikeDomain(s string) bool {
	if len(s) < 3 || len(s) > 253 {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	if !strings.Contains(s, ".") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_':
		default:
			return false
		}
	}
	return true
}

// ExtractDomains 从反馈的域名字段与正文里抽出待查域名。
//
// 反馈里的书写方式很杂（"github.com   apkpure.com"、"https://x.com/a"、
// "app 打开一直转圈"），所以要同时扫两个字段并去重。
func ExtractDomains(domainField, message string) []string {
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)

	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "www.")
		if i := strings.IndexAny(s, "/?#"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSuffix(s, ".")
		if !looksLikeDomain(s) {
			return
		}
		// 去掉明显的文件扩展名误识别（app.apk 之类）。
		if strings.HasSuffix(s, ".apk") || strings.HasSuffix(s, ".exe") ||
			strings.HasSuffix(s, ".zip") || strings.HasSuffix(s, ".html") {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	for _, f := range strings.FieldsFunc(domainField, func(r rune) bool {
		return r == ' ' || r == ',' || r == '，' || r == ';' || r == '；' ||
			r == '\n' || r == '\r' || r == '\t' || r == '、'
	}) {
		add(f)
	}
	for _, f := range strings.Fields(message) {
		add(f)
	}
	// 稳定输出：短的（通常是主域）排前面，便于人一眼看到关键项。
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := strings.Count(out[i], "."), strings.Count(out[j], ".")
		if di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	if len(out) > maxDomainsPerFeedback {
		out = out[:maxDomainsPerFeedback]
	}
	return out
}

// SplitPattern 复用 rules 包的解析，供索引构建用。
func SplitPattern(pattern string) (domain string, wildcard bool, err error) {
	return rules.SplitPattern(pattern)
}

// MarshalEvidence 把证据转成入库用的 JSON（保留原始审计信息）。
func MarshalEvidence(ev *Evidence) string {
	b, err := json.Marshal(ev)
	if err != nil {
		return "{}"
	}
	return string(b)
}
