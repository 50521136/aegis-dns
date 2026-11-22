package rules

import (
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// oldLookup 对应 `trie.lookup` 旧版实现：把域名先 Split 成标签，再从 TLD
// 向第一个标签方向扫描。它和 trie.lookup 应该在任何情况下结果一致。
func oldLookup(tn *trie, domain string, qtype uint16) (matched bool, rrs []dns.RR, value string) {
	var labels []string
	cur := ""
	for _, c := range domain {
		if c == '.' {
			labels = append(labels, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	labels = append(labels, cur)

	var (
		best   bool
		bestRR []dns.RR
		bestV  string
	)
	n := tn.root
	for i := len(labels) - 1; i >= 0; i-- {
		child, ok := n.children[labels[i]]
		if !ok {
			// 路径断了，已记录的 best 就是最终结果。
			return best, bestRR, bestV
		}
		n = child
		if i > 0 {
			if !child.sub {
				continue
			}
		} else if !child.exact {
			continue
		}
		if !qtypeAllowed(child.qtypes, qtype) {
			continue
		}
		best, bestRR, bestV = true, child.rewrite, child.value
	}
	return best, bestRR, bestV
}

// TestTrieLookupScanEqualsSplit 验证 trie.lookup 的自右向左标签扫描与旧版
// strings.Split 版本完全一致：扫描顺序与作用域（exact / sub）都不能变，
// 否则漏一层的规则会漏拦，多一层的规则会误拦。
//
// 条款（按规则集）：
//
//	*.example          通配：example 本身 + 全部子域
//	exact.example      exact 写法：只拦 example 本身，`|x|` 语义
//	own.example        allow 有更高优先级，前两层不测
//	|only.example|     精确规则：只拦 only.example 本身
//	deep.a-b.example   三标签精确写法
func TestTrieLookupScanEqualsSplit(t *testing.T) {
	rs, err := Build(Layers{
		UserRules: []model.Rule{
			{Kind: model.KindBlock, Pattern: "*.example", Enabled: true},
			{Kind: model.KindBlock, Pattern: "exact.example", Enabled: true},
			{Kind: model.KindAllow, Pattern: "own.example", Enabled: true},
			{Kind: model.KindBlock, Pattern: "|only.example|", Enabled: true},
			{Kind: model.KindBlock, Pattern: "deep.a-b.example", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	userBlock := rs.userBlock

	domains := []string{
		"a", "a.b", "a.b.c", "a.b.c.d", "a..b", "", ".",
		"example", "own.example", "only.example", "only.sub.example",
		"exact.example", "deep.a-b.example", "deep.a-b.c.example", "deep.example",
		"stone.exampple", "exmaple", "may.Apple.com", "first.example.com.",
		"x..example", "sub.example.com", "a.example", "a.b.example",
		// 关税：查到了深层节点上才会在 exact 层翻船（ads.example.com 在 safe.ads.example.com
		// 之后没有 "ads" 精确规则，old 版不应命中）。
		"ads.example.com", "ads.example.com.a",
	}
	for _, d := range domains {
		m1, _, v1 := userBlock.lookup(d, 1)
		m2, _, v2 := oldLookup(userBlock, d, 1)
		if m1 != m2 || v1 != v2 {
			t.Errorf("domain %q: scan=%v/split=%v", d, m1, m2)
		}
	}
	for _, d := range domains {
		// 同样验证白名单 Trie（safe.ads.example.com 里有 "ads" 中间节点，
		// 正好用来测 exact 层翻船）。
		m1, _, v1 := rs.userAllow.lookup(d, 1)
		m2, _, v2 := oldLookup(rs.userAllow, d, 1)
		if m1 != m2 || v1 != v2 {
			t.Errorf("allow trie, domain %q: scan=%v/split=%v", d, m1, m2)
		}
	}
	// 公共的动作口径（不带模板）
	if d := rs.Match("own.example", 1); d.Action != ActionAllow {
		t.Errorf("own.example 应被 allow 层放行，得到 %v", d.Action)
	}
	if d := rs.Match("exact.example", 1); d.Action != ActionBlock {
		t.Errorf("exact.example 应被精确规则拦住，得到 %v", d.Action)
	}
	if d := rs.Match("only.example", 1); d.Action != ActionBlock {
		t.Errorf("only.example 只能精确拦截自身，得到 %v", d.Action)
	}
	if d := rs.Match("only.sub.example", 1); d.Action != ActionBlock {
		// 它被通配规则 `*.example` 拦（它是 example 的子域），不是精确规则的事。
		t.Errorf("only.sub.example 应被通配规则拦截，得到 %v", d.Action)
	}
	if d := rs.Match("deep.a-b.c.example", 1); d.Action != ActionBlock {
		// 同上：通配规则 `*.example` 覆盖所有 example 子域。
		t.Errorf("deep.a-b.c.example 应被通配规则拦截，得到 %v", d.Action)
	}
}
