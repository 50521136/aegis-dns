package subsub

import "testing"

// isGitHubRaw 必须按 host 判断：已经套了代理的地址里也含 raw.githubusercontent.com
// 这个子串，用子串匹配会把代理地址再套一层镜像。
func TestIsGitHubRaw(t *testing.T) {
	yes := []string{
		"https://raw.githubusercontent.com/a/b/master/c.txt",
		"https://github.com/a/b/raw/main/c.txt",
	}
	no := []string{
		"https://big.oisd.nl/domainswild",
		"https://cdn.akaere.online/https://raw.githubusercontent.com/a/b/c.txt",
		"https://gh-proxy.com/https://raw.githubusercontent.com/a/b/c.txt",
		"https://github.com/a/b",
	}
	for _, u := range yes {
		if !isGitHubRaw(u) {
			t.Errorf("应识别为 GitHub raw: %s", u)
		}
	}
	for _, u := range no {
		if isGitHubRaw(u) {
			t.Errorf("不应识别为 GitHub raw: %s", u)
		}
	}
}

// TestGitHubPagesCandidates 验证 GitHub Pages → raw 的映射。
//
// 这个映射是 AdGuard 官方规则库能不能用的前提：那些源全在
// adguardteam.github.io 上，国内机房直连不通、镜像也不支持该域名。
func TestGitHubPagesCandidates(t *testing.T) {
	got := githubPagesCandidates(
		"https://adguardteam.github.io/HostlistsRegistry/assets/filter_51.txt")
	want := []string{
		"https://raw.githubusercontent.com/adguardteam/HostlistsRegistry/main/assets/filter_51.txt",
		"https://raw.githubusercontent.com/adguardteam/HostlistsRegistry/master/assets/filter_51.txt",
	}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 个候选，实际 %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("候选 %d = %q，期望 %q", i, got[i], want[i])
		}
	}

	// 非 Pages 地址必须返回 nil，否则会把普通订阅也改写掉
	for _, u := range []string{
		"https://example.com/filter.txt",
		"https://raw.githubusercontent.com/a/b/main/c.txt",
		"https://adguardteam.github.io/", // 用户主页，没有 repo 段
	} {
		if c := githubPagesCandidates(u); c != nil {
			t.Errorf("%q 不该被映射，却得到 %v", u, c)
		}
	}
}
