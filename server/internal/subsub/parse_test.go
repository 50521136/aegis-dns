package subsub

import (
	"strings"
	"testing"
)

func TestSniff(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Format
	}{
		{
			name:    "adblock",
			content: "! Title: test\n||ads.example.com^\n||tracker.example.net^\n",
			want:    FormatAdblock,
		},
		{
			name:    "hosts",
			content: "# hosts file\n0.0.0.0 ads.example.com\n127.0.0.1 tracker.example.net\n",
			want:    FormatHosts,
		},
		{
			name:    "dnsmasq",
			content: "address=/ads.example.com/0.0.0.0\naddress=/tracker.example.net/0.0.0.0\n",
			want:    FormatDnsmasq,
		},
		{
			name:    "domain list",
			content: "ads.example.com\ntracker.example.net\nanalytics.example.io\n",
			want:    FormatDomain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sniff([]byte(tc.content)); got != tc.want {
				t.Fatalf("嗅探结果 %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestParseFormats(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantN   int
	}{
		{
			name:    "adblock",
			content: "||ads.example.com^\n@@||safe.example.com^\n",
			wantN:   2,
		},
		{
			name:    "hosts",
			content: "0.0.0.0 ads.example.com\n127.0.0.1 tracker.net\n",
			wantN:   2,
		},
		{
			name:    "纯域名列表",
			content: "ads.example.com\ntracker.net\n",
			wantN:   2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse([]byte(tc.content), FormatAuto)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(res.Entries) != tc.wantN {
				t.Fatalf("期望 %d 条，实际 %d", tc.wantN, len(res.Entries))
			}
		})
	}
}

// TestParseRejectsEmpty 验证「内容为空视为异常」（文档 6.4）。
//
// 这是订阅系统最重要的一条契约：解析不出任何规则时必须报错，
// 让调用方保留旧数据，绝不能返回空列表把用户的规则集清空。
func TestParseRejectsEmpty(t *testing.T) {
	for _, content := range []string{
		"",
		"# 只有注释\n! 也是注释\n",
		"\n\n\n",
	} {
		if _, err := Parse([]byte(content), FormatAuto); err == nil {
			t.Fatalf("内容 %q 应返回错误，而不是空列表", content)
		}
	}
}

// TestParseDeduplicates 验证去重与 allow 优先。
func TestParseDeduplicates(t *testing.T) {
	content := "||dup.example.com^\n||dup.example.com^\n@@||dup.example.com^\n"
	res, err := Parse([]byte(content), FormatAdblock)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("期望去重后 1 条，实际 %d", len(res.Entries))
	}
	if res.Entries[0].Kind != "allow" {
		t.Fatalf("allow 应优先于 block，实际 %q", res.Entries[0].Kind)
	}
}

// TestParseSkipsRewrites 验证订阅不承载改写规则。
func TestParseSkipsRewrites(t *testing.T) {
	content := "||ads.example.com^\n||old.example.com^$dnsrewrite=1.2.3.4\n"
	res, err := Parse([]byte(content), FormatAdblock)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("改写规则应被跳过，期望 1 条，实际 %d", len(res.Entries))
	}
	if res.Skipped == 0 {
		t.Error("跳过的行数应被统计")
	}
}

// TestSplitByKind 验证按类型拆分。
func TestSplitByKind(t *testing.T) {
	block, allow := SplitByKind([]Entry{
		{Domain: "a.com", Kind: "block"},
		{Domain: "b.com", Kind: "allow"},
		{Domain: "c.com", Kind: "block"},
	})
	if len(block) != 2 || len(allow) != 1 {
		t.Fatalf("拆分错误: block=%d allow=%d", len(block), len(allow))
	}
}

// TestDescribeStatus 验证状态文本不会过长。
func TestDescribeStatus(t *testing.T) {
	if got := DescribeStatus(nil); got != "ok" {
		t.Fatalf("nil 应为 ok，实际 %q", got)
	}
	long := DescribeStatus(errStr(strings.Repeat("x", 500)))
	if len(long) > 210 {
		t.Fatalf("状态文本应被截断，实际长度 %d", len(long))
	}
	if !strings.HasPrefix(long, "error: ") {
		t.Fatalf("应以 error: 开头，实际 %q", long)
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
