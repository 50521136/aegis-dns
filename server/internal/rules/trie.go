package rules

import (
	"strings"

	"github.com/miekg/dns"
)

// Action 是规则匹配的结果。
type Action uint8

const (
	// ActionNone 表示没有规则命中，应转发上游。
	ActionNone Action = 0
	// ActionAllow 表示白名单命中，放行到上游。
	ActionAllow Action = 1
	// ActionBlock 表示拦截。
	ActionBlock Action = 2
	// ActionRewrite 表示命中改写规则。
	ActionRewrite Action = 3
)

func (a Action) String() string {
	switch a {
	case ActionAllow:
		return "allow"
	case ActionBlock:
		return "block"
	case ActionRewrite:
		return "rewrite"
	default:
		return "none"
	}
}

// node 是后缀 Trie 的一个节点。
//
// 树按「反转的域名标签」组织：ads.example.com 的路径是
// root -> "com" -> "example" -> "ads"。
// 匹配 tracker.ads.example.com 时从 TLD 往下走，走到 "ads" 节点就落在
// 它的 sub 标记上，无需为每个子域单独建节点。
type node struct {
	children map[string]*node
	// exact 为 true 表示「本节点代表的域名本身」被规则命中。
	//
	// 本实现里 exact 与 sub 总是同时置位（`*.domain` 也匹配 domain 自身，
	// 见 insert 的注释），保留两个字段是为了让「末节点看 exact、中间节点看 sub」
	// 这个匹配规则保持显式，将来若要支持子域-only 规则不必重构。
	exact bool
	// sub 为 true 表示本节点代表的域名及其所有子域都在规则范围内。
	sub bool
	// qtypes 非空时限定生效的记录类型；空表示适用全部类型。
	qtypes []string
	// rewrite 是预构建好的应答记录（仅改写 Trie 使用）。
	rewrite []dns.RR
	// value 保留原始改写目标，用于日志与回显。
	value string
}

// trie 是一棵后缀 Trie。构建完成后只读，可被任意 goroutine 并发查询。
type trie struct {
	root *node
	// size 记录插入的规则条数（用于 UI 展示与统计）。
	size int
}

func newTrie() *trie { return &trie{root: &node{}} }

// insert 把一条规则插入 Trie。
//
// 关于 `*.domain` 的语义：本实现让它等价于 `||domain^`，即**同时匹配 domain
// 自身与其所有子域**。
//
// 为什么不按「* 只匹配子域」的严格 glob 语义：
// OISD、1Hosts 这类主流列表用的是 `*.example.com` 写法，且列表头部明确写明
// 「should block access to "example.com" and "subdomain.example.com"」。
// 若按子域-only 处理，`*.doubleclick.net` 这类条目永远不会拦住
// `doubleclick.net` 本身 —— 整个列表等于失效，而用户只会看到「订阅加了但没效果」。
//
// wildcard 参数保留下来只用于生成规范化 pattern 时的回显，不参与匹配。
func (t *trie) insert(domain string, wildcard bool, qtypes []string, rrs []dns.RR, value string) {
	labels := strings.Split(domain, ".")
	n := t.root
	// 从 TLD 向主机名方向建树，所以倒序插入。
	for i := len(labels) - 1; i >= 0; i-- {
		if n.children == nil {
			n.children = make(map[string]*node, 2)
		}
		child, ok := n.children[labels[i]]
		if !ok {
			child = &node{}
			n.children[labels[i]] = child
		}
		n = child
	}
	// exact 管「完整域名命中」，sub 管「该域及其所有子域命中」。
	// 两者都置位：既匹配自身，也匹配子域。
	n.sub = true
	n.exact = true
	if qtypes != nil {
		n.qtypes = qtypes
	}
	if rrs != nil {
		n.rewrite = rrs
		n.value = value
	}
	t.size++
}

// lookup 在 Trie 中查找匹配。
//
// 返回命中的节点信息：动作由调用方根据所在 Trie 决定，改写记录直接从节点取。
//
// 匹配策略：沿路径下行，记录「最深的一个满足记录类型约束的命中节点」。
// 取最深是因为域名越长越具体，具体规则应覆盖宽泛规则；
// 若某层节点因 $dnstype 约束不适用，继续向更深层找而不是直接放弃。
func (t *trie) lookup(domain string, qtype uint16) (matched bool, rrs []dns.RR, value string) {
	if t == nil || t.root == nil || domain == "" {
		return false, nil, ""
	}
	labels := strings.Split(domain, ".")
	n := t.root

	for i := len(labels) - 1; i >= 0; i-- {
		child, ok := n.children[labels[i]]
		if !ok {
			// 路径断了，已记录的 best 就是最终结果。
			break
		}
		n = child

		// 中间节点（还有更深的标签要走）：只有 sub 语义适用。
		// 末节点（完整域名）：只有 exact 语义适用，这样 *.domain 不会匹配自身。
		var applies bool
		if i > 0 {
			applies = child.sub
		} else {
			applies = child.exact
		}
		if !applies || !qtypeAllowed(child.qtypes, qtype) {
			continue
		}
		matched, rrs, value = true, child.rewrite, child.value
	}
	return matched, rrs, value
}

// qtypeAllowed 判断记录类型是否满足规则约束。
//
// qtypes 为空 = 适用所有类型（文档 5.1 的 $dnstype 语义）。
func qtypeAllowed(qtypes []string, qtype uint16) bool {
	if len(qtypes) == 0 {
		return true
	}
	name := dns.TypeToString[qtype]
	for _, q := range qtypes {
		if q == name {
			return true
		}
	}
	return false
}
