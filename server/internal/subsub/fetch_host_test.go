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
