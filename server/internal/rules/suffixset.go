package rules

import (
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// suffixSet 是一组「域名及其所有子域」的匹配集合，用排序数组 + 二分实现。
//
// 为什么不用 trie：订阅压平后动辄几十万条域名，而 trie 的每个节点带一个
// map[string]*node —— 空 map 本身就要 48 字节，六十万域名撑出约一百二十万
// 节点，实测吃到 640MB。线上 1.7G 的机器直接 OOM 被杀。
//
// 排序数组的内存是「每个域名一条字符串」，六十万条约 40MB，量级差 15 倍。
// 查询代价是每级后缀一次二分：qname 最多十来个标签，614k 条时每次二分约
// 20 次比较，单次匹配仍是微秒级 —— 相对一次上游查询完全可以忽略。
//
// 语义与原来的 trie 保持一致：插入 ads.example 后，
// ads.example 与 sub.ads.example 都命中（末尾标签逐级剥离后比对）。
type suffixSet struct {
	// sorted 是去重并排序后的域名（小写、无尾点）。
	sorted []string
	// size 是去重后的条目数，用于 UI 展示与统计。
	size int
}

// newSuffixSet 创建一个空集合。
func newSuffixSet() *suffixSet { return &suffixSet{} }

// add 批量插入域名。构建期允许重复，Finish 时统一去重排序。
func (s *suffixSet) add(domains []string) {
	if len(domains) == 0 {
		return
	}
	if cap(s.sorted)-len(s.sorted) < len(domains) {
		grown := make([]string, len(s.sorted), len(s.sorted)+len(domains))
		copy(grown, s.sorted)
		s.sorted = grown
	}
	for _, d := range domains {
		if dom, err := normalizeDomain(d); err == nil {
			s.sorted = append(s.sorted, dom)
		}
	}
}

// finish 去重 + 排序。必须在查询前调用一次。
//
// 去重不能省：订阅之间大量重叠（同一份列表被多个源收录），
// 不去重会让数组白白变大，二分也要多走几层。
func (s *suffixSet) finish() {
	if len(s.sorted) == 0 {
		s.size = 0
		return
	}
	sort.Strings(s.sorted)
	// 原地去重
	out := s.sorted[:1]
	for i := 1; i < len(s.sorted); i++ {
		if s.sorted[i] != out[len(out)-1] {
			out = append(out, s.sorted[i])
		}
	}
	// 把尾部残留清空，让 GC 能回收被去重掉的字符串引用
	for i := len(out); i < len(s.sorted); i++ {
		s.sorted[i] = ""
	}
	s.sorted = out
	s.size = len(out)
}

// lookup 判断域名是否命中本集合。
//
// qtype 参数是为了和 trie 的签名保持一致（订阅层不区分记录类型），
// 这里忽略它。
func (s *suffixSet) lookup(domain string, _ uint16) (bool, []dns.RR, string) {
	if s == nil || len(s.sorted) == 0 || domain == "" {
		return false, nil, ""
	}
	d := domain
	for {
		if i := sort.SearchStrings(s.sorted, d); i < len(s.sorted) && s.sorted[i] == d {
			return true, nil, ""
		}
		dot := strings.IndexByte(d, '.')
		if dot < 0 {
			return false, nil, ""
		}
		d = d[dot+1:]
		// 只剩顶级域（或为空）时没必要再查
		if !strings.Contains(d, ".") {
			return false, nil, ""
		}
	}
}
