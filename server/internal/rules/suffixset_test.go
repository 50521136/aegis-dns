package rules

import (
	"fmt"
	"math/rand"
	"runtime"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 订阅层的匹配语义必须和原来 trie 完全一致，否则换成排序数组之后
// 「某条规则突然不生效了」这种问题极难发现。
func TestSuffixSetMatchesSubdomains(t *testing.T) {
	s := newSuffixSet()
	s.add([]string{"ads.example", "tracker.test", "ads.example"}) // 故意重复
	s.finish()

	cases := []struct {
		q    string
		want bool
	}{
		{"ads.example", true},
		{"sub.ads.example", true},
		{"a.b.c.ads.example", true},
		{"tracker.test", true},
		{"x.tracker.test", true},
		{"notads.example", false},
		{"example", false},
		{"example.com", false},
		{"ads.example.com", false}, // 是 ads.example 的「父域方向的另一条」，不该命中
	}
	for _, c := range cases {
		got, _, _ := s.lookup(c.q, 1)
		if got != c.want {
			t.Errorf("lookup(%q) = %v, 期望 %v", c.q, got, c.want)
		}
	}
	if s.size != 2 {
		t.Errorf("去重后应有 2 条，实际 %d", s.size)
	}

	// 大小写归一化在 Match 层做（DNS 大小写不敏感，客户端可能发混合大小写），
	// 这里确认整条链路仍然命中 —— 只在 suffixSet 内部测会漏掉这一层。
	rs, err := Build(Layers{SubBlock: []string{"ads.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := rs.Match("ADS.Example.", 1); d.Action != ActionBlock {
		t.Errorf("混合大小写的域名应命中订阅 block，实际 action=%q", d.Action)
	}
}

// 空集合与 nil 都不能 panic（用户没有任何订阅时走这条路）。
func TestSuffixSetEmpty(t *testing.T) {
	var nilSet *suffixSet
	if ok, _, _ := nilSet.lookup("a.example", 1); ok {
		t.Error("nil 集合不应命中")
	}
	s := newSuffixSet()
	s.finish()
	if ok, _, _ := s.lookup("a.example", 1); ok {
		t.Error("空集合不应命中")
	}
}

// 订阅层的优先级必须仍然低于用户规则：用户 allow 要能盖过订阅 block。
func TestSubLayerPriorityUnchanged(t *testing.T) {
	rs, err := Build(Layers{
		UserRules: []model.Rule{
			{Kind: model.KindAllow, Pattern: "||ok.example^", Enabled: true},
		},
		SubBlock: []string{"ok.example", "bad.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 用户 allow（优先级 1）应压过订阅 block（优先级 8）
	if d := rs.Match("ok.example", 1); d.Action != ActionAllow {
		t.Errorf("用户 allow 应压过订阅 block，实际 action=%q", d.Action)
	}
	if d := rs.Match("bad.example", 1); d.Action != ActionBlock {
		t.Errorf("订阅 block 应生效，实际 action=%q", d.Action)
	}
}

// 内存是这次改动的核心目的：六十万条域名必须远小于原来的 trie。
//
// 原来的 trie 在这种规模下实测吃到约 640MB，直接把 1.7G 的机器打爆。
// 这里断言「不超过 200MB」，留足余量但足以拦住回退到 trie 的改动。
func TestSuffixSetMemoryAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("规模测试")
	}
	const n = 600000

	// 基线必须在生成域名之前取：否则测到的是「集合相对输入切片的增量」，
	// 而输入切片随后会被丢弃，两者差值会变成负数（曾因此测出 0MB）。
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)

	r := rand.New(rand.NewSource(42))
	domains := make([]string, 0, n)
	for i := 0; i < n; i++ {
		domains = append(domains, fmt.Sprintf("host%d.domain%d.example", i, r.Intn(50000)))
	}
	sample := domains[12345]

	s := newSuffixSet()
	s.add(domains)
	s.finish()

	// 丢掉输入切片再测：否则测到的是 domains 本身，不是集合的占用
	domains = nil
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)

	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if delta < 0 {
		delta = 0
	}
	usedMB := float64(delta) / (1 << 20)
	t.Logf("%d 条域名占用 %.1f MB（去重后 %d 条）", n, usedMB, s.size)
	// 原来 trie 在同样规模下实测约 640MB，把 1.7G 的机器打爆。
	// 150MB 是排序数组的合理上限，超过就说明退回了内存密集实现。
	if usedMB > 150 {
		t.Errorf("60 万条域名占用了 %.0fMB，超过 150MB 预算", usedMB)
	}
	// 去重后 size 必须与输入条数一致（本用例生成的域名不重复）
	if s.size != n {
		t.Errorf("size=%d，期望 %d", s.size, n)
	}
	// 功能仍然正确：给它加个前缀应当命中
	if ok, _, _ := s.lookup("x."+sample, 1); !ok {
		t.Errorf("子域 %q 应命中", "x."+sample)
	}
	if ok, _, _ := s.lookup(sample, 1); !ok {
		t.Errorf("域名本身 %q 应命中", sample)
	}
}

func BenchmarkSuffixSetLookup(b *testing.B) {
	s := newSuffixSet()
	r := rand.New(rand.NewSource(1))
	ds := make([]string, 0, 600000)
	for i := 0; i < 600000; i++ {
		ds = append(ds, fmt.Sprintf("host%d.domain%d.example", i, r.Intn(50000)))
	}
	s.add(ds)
	s.finish()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.lookup("www.example.com", 1)
	}
}
