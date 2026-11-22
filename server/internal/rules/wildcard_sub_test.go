package rules

import (
	"slices"
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// TestSubscriptionWildcardBlock 验证订阅里的通配模式真的会拦截。
//
// 线上实测：一份 33554 行的源里有 258 行是 `||at*.doubanio.com^` 这种写法，
// 改之前解析阶段就返回 ErrSkip，整条规则静默消失 —— 订阅状态是 ok、
// 条数也对得上，只是 `at1.doubanio.com` 照样解析出去。
func TestSubscriptionWildcardBlock(t *testing.T) {
	rs, err := Build(Layers{SubBlock: []string{
		"at*.doubanio.com",
		"163487*.qmgmw.com",
		"js.*2023*.live",
		"ad.doubanio.com",
	}})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	blocked := []string{
		"at1.doubanio.com",
		"at.doubanio.com",
		"www.at1.doubanio.com",
		"16348789.qmgmw.com",
		"js.a2023b.live",
		"ad.doubanio.com",
	}
	for _, h := range blocked {
		if d := rs.Match(h, dns.TypeA); d.Action != ActionBlock {
			t.Errorf("%q 应被拦截，实际 %v", h, d.Action)
		}
	}

	allowed := []string{
		"doubanio.com",
		"qnmob3.doubanio.com",
		"16348789.qmgmw.com.cn",
		"js.2024.live",
		"example.com",
	}
	for _, h := range allowed {
		if d := rs.Match(h, dns.TypeA); d.Action != ActionNone {
			t.Errorf("%q 不应被拦截，实际 %v", h, d.Action)
		}
	}
}

// TestSubscriptionWildcardAllowBeatsBlock 验证同层里 allow 先于 block。
func TestSubscriptionWildcardAllowBeatsBlock(t *testing.T) {
	rs, err := Build(Layers{
		SubBlock: []string{"at*.doubanio.com", "*.doubanio.com"},
		SubAllow: []string{"at*.safe.doubanio.com", "good.doubanio.com"},
	})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	// 通配白名单命中：放行。
	if d := rs.Match("at1.safe.doubanio.com", dns.TypeA); d.Action != ActionAllow {
		t.Errorf("at1.safe.doubanio.com 应放行，实际 %v", d.Action)
	}
	// 精确白名单命中：放行。
	if d := rs.Match("good.doubanio.com", dns.TypeA); d.Action != ActionAllow {
		t.Errorf("good.doubanio.com 应放行，实际 %v", d.Action)
	}
	// 只有黑名单覆盖：拦截。
	if d := rs.Match("bad.doubanio.com", dns.TypeA); d.Action != ActionBlock {
		t.Errorf("bad.doubanio.com 应拦截，实际 %v", d.Action)
	}
}

// TestGlobalSubscriptionWildcard 验证全局订阅层的通配模式同样生效。
func TestGlobalSubscriptionWildcard(t *testing.T) {
	g := BuildGlobal(&model.GlobalSnap{
		SubBlock: []string{"trk*.global.example"},
		SubAllow: []string{"trk*.ok.global.example"},
	})
	rs, err := BuildWithGlobal(g, Layers{})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if d := rs.Match("trk1.global.example", dns.TypeA); d.Action != ActionBlock {
		t.Errorf("trk1.global.example 应拦截，实际 %v", d.Action)
	}
	if d := rs.Match("trk1.ok.global.example", dns.TypeA); d.Action != ActionAllow {
		t.Errorf("trk1.ok.global.example 应放行，实际 %v", d.Action)
	}
	// 统计要算上通配条目，否则「只有通配规则的租户」会被 Empty() 判成空规则集。
	if rs.Stats.GSubBlock == 0 {
		t.Errorf("全局订阅黑名单计数不应为 0")
	}
	if rs.Empty() {
		t.Errorf("只有通配规则的规则集不应被判为空")
	}
}

// TestSubscriptionWildcardCountedInStats 验证通配条目计入统计。
//
// Stats 是面板展示与 Empty() 判定的依据：漏算会让「规则全在通配层」
// 的租户被当成没有规则，整个匹配阶段被跳过。
func TestSubscriptionWildcardCountedInStats(t *testing.T) {
	rs, err := Build(Layers{
		SubBlock: []string{"at*.doubanio.com", "plain.example.com"},
		SubAllow: []string{"ok*.example.net"},
	})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if rs.Stats.SubBlock != 2 {
		t.Errorf("SubBlock 应为 2（1 条整域名 + 1 条通配），实际 %d", rs.Stats.SubBlock)
	}
	if rs.Stats.SubAllow != 1 {
		t.Errorf("SubAllow 应为 1，实际 %d", rs.Stats.SubAllow)
	}
}

// TestGlobPatternDoesNotEnterSuffixSet 验证通配模式不会被 suffixSet 静默吃掉。
//
// 构建时字面索引与通配索引各自只接受对应类型的条目。
func TestGlobPatternDoesNotEnterSuffixSet(t *testing.T) {
	rs, err := Build(Layers{SubBlock: []string{"at*.doubanio.com"}})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if got := len(rs.subBlock.sorted); got != 0 {
		t.Errorf("suffixSet 不该收到通配条目，实际 %d 条", got)
	}
	if got := rs.subWildBlock.size; got != 1 {
		t.Errorf("globSet 应收到 1 条，实际 %d", got)
	}
}

// TestSubscriptionBuildPreservesMixedInput 验证直接消费原始切片不会改变
// 混合订阅的分类、去重、非法项处理和匹配优先级。
func TestSubscriptionBuildPreservesMixedInput(t *testing.T) {
	block := []string{
		"blocked.example",
		"trk*.other.example",
		"BLOCKED.example.",
		"trk*.other.example",
		"bad domain",
		"bad*",
	}
	allow := []string{
		"ok.blocked.example",
		"safe*.blocked.example",
		"OK.blocked.example.",
		"bad*",
	}
	originalBlock := append([]string(nil), block...)
	originalAllow := append([]string(nil), allow...)

	rs, err := Build(Layers{SubBlock: block, SubAllow: allow})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	global, err := BuildWithGlobal(BuildGlobal(&model.GlobalSnap{SubBlock: block, SubAllow: allow}), Layers{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(block, originalBlock) || !slices.Equal(allow, originalAllow) {
		t.Fatal("构建不应修改输入切片")
	}
	if rs.Empty() || rs.Stats != (Stats{SubBlock: 3, SubAllow: 2}) {
		t.Fatalf("tenant stats = %+v", rs.Stats)
	}
	if global.Empty() || global.Stats != (Stats{GSubBlock: 3, GSubAllow: 2}) {
		t.Fatalf("global stats = %+v", global.Stats)
	}
	for _, tc := range []struct {
		domain string
		want   Action
	}{
		{"blocked.example", ActionBlock},
		{"www.trk1.other.example", ActionBlock},
		{"ok.blocked.example", ActionAllow},
		{"safe1.blocked.example", ActionAllow},
		{"other.example", ActionNone},
	} {
		if got := rs.Match(tc.domain, dns.TypeA).Action; got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.domain, got, tc.want)
		}
		if got := global.Match(tc.domain, dns.TypeA).Action; got != tc.want {
			t.Errorf("global %s: got %v, want %v", tc.domain, got, tc.want)
		}
	}
}
