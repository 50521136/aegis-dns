package rules

import (
	"testing"

	"github.com/miekg/dns"
)

// TestGlobMatchSegs 覆盖 * 匹配的基础语义。
func TestGlobMatchSegs(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"at*.doubanio.com", "at1.doubanio.com", true},
		{"at*.doubanio.com", "at.doubanio.com", true},
		{"at*.doubanio.com", "atxyz.doubanio.com", true},
		// * 可以跨点：Adblock 里 * 匹配任意字符，包含点。
		{"at*.doubanio.com", "at.x.doubanio.com", true},
		{"at*.doubanio.com", "doubanio.com", false},
		{"at*.doubanio.com", "bt1.doubanio.com", false},
		{"at*.doubanio.com", "xat1.doubanio.com", false},
		{"163487*.qmgmw.com", "16348789.qmgmw.com", true},
		{"163487*.qmgmw.com", "16348789.qmgmw.com.cn", false},
		// 多个 * ：中间段按顺序出现即可。
		{"js.*2023*.live", "js.a2023b.live", true},
		{"js.*2023*.live", "js.2023.live", true},
		{"js.*2023*.live", "jsx.2023.live", false},
		{"js.*2023*.live", "js.2024.live", false},
		// 通配落在中间标签
		{"ad*.example.org", "adserver.example.org", true},
	}
	for _, c := range cases {
		p := newGlobPattern(c.pattern)
		if got := p.matchHost(c.host); got != c.want {
			t.Errorf("%q 匹配 %q = %v，期望 %v", c.pattern, c.host, got, c.want)
		}
	}
}

// TestGlobSetSubdomainSemantics 验证 `||pattern^` 的锚点语义：
// 模式可以从任意标签边界开始，所以子域同样命中。
func TestGlobSetSubdomainSemantics(t *testing.T) {
	s := newGlobSet()
	s.add([]string{"at*.doubanio.com"})
	s.finish()

	for _, host := range []string{
		"at1.doubanio.com",
		"at.doubanio.com",
		"www.at1.doubanio.com",
		"a.b.at1.doubanio.com",
	} {
		if ok, _, _ := s.lookup(host, dns.TypeA); !ok {
			t.Errorf("%q 应命中 ||at*.doubanio.com^", host)
		}
	}
	for _, host := range []string{
		"doubanio.com",
		"ad.doubanio.com",
		"xat1.doubanio.com",
		"at1.doubanio.com.cn",
		"at1.doubanio.org",
	} {
		if ok, _, _ := s.lookup(host, dns.TypeA); ok {
			t.Errorf("%q 不应命中 ||at*.doubanio.com^", host)
		}
	}
}

// TestNormalizeGlobDomainRejects 验证危险的写法会被拒绝。
//
// 放行一条 `*` 或 `com*` 这类模式等于把整个后缀打掉，必须拦在入口。
func TestNormalizeGlobDomainRejects(t *testing.T) {
	bad := []string{
		"",                 // 空
		"at1.doubanio.com", // 不含 *
		"*",                // 只有通配
		"com*",             // 单标签
		"at*.",             // 空标签
		"at*.com/x",        // 含路径字符
		"at*.com:80",       // 含端口
		"at*.com^",         // 残留锚点
		"at*.com$",         // 残留修饰符
	}
	for _, in := range bad {
		if got, err := normalizeGlobDomain(in); err == nil {
			t.Errorf("%q 应被拒绝，却得到 %q", in, got)
		}
	}
	good := map[string]string{
		"||at*.doubanio.com^": "at*.doubanio.com",
		"AT*.DoubanIO.COM":    "at*.doubanio.com",
		"gtlpa*.com":          "gtlpa*.com",
		"*.example.com":       "*.example.com",
	}
	for in, want := range good {
		got, err := normalizeGlobDomain(stripAdblockAnchors(in))
		if err != nil {
			t.Errorf("%q 应通过，却报错 %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q 规范化得到 %q，期望 %q", in, got, want)
		}
	}
}

// stripAdblockAnchors 只用于测试：把 || 与 ^ 剥掉，模拟解析器的处理结果。
func stripAdblockAnchors(s string) string {
	if len(s) > 2 && s[:2] == "||" {
		s = s[2:]
	}
	if n := len(s); n > 0 && s[n-1] == '^' {
		s = s[:n-1]
	}
	return s
}

// TestGlobSetWildcardTLD 验证「最后一段是通配」的模式也能命中。
//
// `missav.*`、`ulog*.*` 这类模式没有确定的 TLD，不能按 TLD 分桶，
// 否则永远查不到 —— 实测第一版就是这样把 missav.* 静默漏掉的。
func TestGlobSetWildcardTLD(t *testing.T) {
	s := newGlobSet()
	s.add([]string{"missav.*", "ulog*.*"})
	s.finish()

	hits := []string{"missav.com", "missav.tv", "missav.net", "ulog.x.com", "a.ulog.x.io",
		// `||` 锚点允许从任意标签边界开始，所以子域同样命中。
		"www.missav.com",
		// `ulog*.*` 的 * 可以跨字符，ulogx.com 也算命中。
		"ulogx.com"}
	for _, h := range hits {
		if ok, _, _ := s.lookup(h, dns.TypeA); !ok {
			t.Errorf("%q 应命中通配 TLD 模式", h)
		}
	}
	misses := []string{"missavx.com", "example.com", "xmissav.com"}
	for _, h := range misses {
		if ok, _, _ := s.lookup(h, dns.TypeA); ok {
			t.Errorf("%q 不应命中，实际命中", h)
		}
	}
}

// BenchmarkGlobSetLookupMiss 衡量「未命中」这条热路径的开销。
//
// 未命中是最常见的情况：拦截规则命中的查询在更早的层就返回了，
// 走到 glob 层的绝大多数是正常域名。
func BenchmarkGlobSetLookupMiss(b *testing.B) {
	s := newGlobSet()
	// 按真实订阅的规模构造：258 条模式，集中在 .com/.net/.xyz 三个桶。
	pats := make([]string, 0, 258)
	// 与真实订阅同形：绝大多数模式带 2~3 段的字面后缀，
	// 少数落在同一后缀下（同一份列表常按站点批量写）。
	for i := 0; i < 180; i++ {
		pats = append(pats, "ad"+string(rune('a'+i%26))+"*.site"+string(rune('a'+i%20))+".com")
	}
	for i := 0; i < 40; i++ {
		pats = append(pats, "trk"+string(rune('a'+i%26))+"*.cdn.net")
	}
	for i := 0; i < 30; i++ {
		pats = append(pats, "px"+string(rune('a'+i%18))+"*.live")
	}
	for i := 0; i < 8; i++ {
		pats = append(pats, "sink"+string(rune('a'+i))+"*.example.org")
	}
	s.add(pats)
	s.finish()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.lookup("www.example.com", dns.TypeA)
	}
}

// BenchmarkGlobSetLookupHit 衡量命中的开销。
func BenchmarkGlobSetLookupHit(b *testing.B) {
	s := newGlobSet()
	s.add([]string{"at*.doubanio.com"})
	s.finish()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.lookup("at1.doubanio.com", dns.TypeA)
	}
}
