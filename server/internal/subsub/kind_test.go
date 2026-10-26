package subsub

import "testing"

// allowlist 订阅用纯域名列表（没有 @@ 前缀）必须判成 allow。
//
// 旧实现 kind 恒默认 block，于是 allowlist 源解析出 block=全部、allow=0；
// 而 scheduler 对 allowlist 只取 allow —— 整份订阅入库 0 条，
// 状态还是 ok，用户看到「订阅成功」但一条规则都没生效。
func TestAllowlistPlainDomainsBecomeAllow(t *testing.T) {
	content := []byte("example.com\nexample.org\n")
	p, err := Parse(content, FormatAuto, "allowlist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	block, allow := SplitByKind(p.Entries)
	if len(allow) != 2 || len(block) != 0 {
		t.Fatalf("allowlist 的纯域名应全部判成 allow，实际 block=%d allow=%d", len(block), len(allow))
	}
}

// blocklist 订阅里的 @@ 条目必须保留为 allow，不能丢。
//
// 报障的那份 rules.txt 有 3002 条 @@ 放行条目，旧实现按单一 kind
// 整批写入，它们被静默丢弃 —— 用户写了放行却仍被拦。
func TestBlocklistKeepsAllowExceptions(t *testing.T) {
	content := []byte("||ads.example^\n@@||ok.example^\n0.0.0.0 bad.example\n")
	p, err := Parse(content, FormatAuto, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	block, allow := SplitByKind(p.Entries)
	if len(block) != 2 {
		t.Errorf("block 应为 2（ads.example, bad.example），实际 %d: %v", len(block), block)
	}
	if len(allow) != 1 || allow[0] != "ok.example" {
		t.Errorf("@@ 放行条目必须保留，实际 allow=%v", allow)
	}
}

// allowlist 里显式写 @@ 也不出错（重复表达放行）。
func TestAllowlistWithExplicitAllowPrefix(t *testing.T) {
	content := []byte("@@||a.example^\nplain.example\n")
	p, err := Parse(content, FormatAuto, "allowlist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	block, allow := SplitByKind(p.Entries)
	if len(allow) != 2 || len(block) != 0 {
		t.Fatalf("应全部为 allow，实际 block=%v allow=%v", block, allow)
	}
}
