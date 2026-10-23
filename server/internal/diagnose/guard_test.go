package diagnose

import (
	"strings"
	"testing"
)

// 这个文件测闸门 —— 整个 Agent 里唯一能拦住「把站点拦死」的地方。
//
// 所有用例都在验证同一件事：模型给出危险建议时，闸门必须拒绝并降级为
// 人工处理，绝不能「尽力执行」。

func blockEvidence(domains ...string) *Evidence {
	ev := &Evidence{}
	for _, d := range domains {
		ev.Domains = append(ev.Domains, DomainEvidence{
			Domain: d, Action: "block", Layer: "sub_block", LayerLabel: "用户订阅黑名单",
		})
	}
	return ev
}

// TestGuardRejectsPublicSuffix 断言公共后缀一律拒绝。
//
// 这是最危险的一类：放行 `com` 等于全站不拦。
func TestGuardRejectsPublicSuffix(t *testing.T) {
	dangerous := []string{"com", "net", "cn", "io", "dev", "co.uk", "com.cn", "githubusercontent.com"}
	for _, d := range dangerous {
		ev := blockEvidence(d)
		sug := &Suggestion{
			Verdict: "wrong_block",
			Action:  Action{Type: "add_global_allow", Domains: []string{d}},
			Reply:   "已放行",
		}
		res := Guard(sug, ev, nil)
		if res.Action.Type != "need_human" {
			t.Errorf("放行 %q 必须被拒绝并降级，实际 action=%q", d, res.Action.Type)
		}
		if len(res.Action.Domains) != 0 {
			t.Errorf("放行 %q 被拒绝后不该残留域名，实际 %v", d, res.Action.Domains)
		}
		if !res.Downgraded {
			t.Errorf("放行 %q 应标记为已降级", d)
		}
	}
}

// TestGuardRejectsDomainNotBlocked 断言模型不能放行「证据里没被拦」的域名。
//
// 防的是模型幻觉：编一个看起来合理的域名，要求全局放行。
func TestGuardRejectsDomainNotBlocked(t *testing.T) {
	ev := blockEvidence("real-blocked.example.com")
	ev.Domains = append(ev.Domains, DomainEvidence{
		Domain: "actually-allowed.example.com", Action: "allow", Layer: "user_allow",
	})
	sug := &Suggestion{
		Verdict: "wrong_block",
		Action:  Action{Type: "add_global_allow", Domains: []string{"actually-allowed.example.com"}},
	}
	res := Guard(sug, ev, nil)
	if res.Action.Type != "need_human" || len(res.Action.Domains) != 0 {
		t.Fatalf("放行未被拦的域名必须拒绝，实际 %+v", res.Action)
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "并没有被拦截") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应说明拒绝原因，实际 notes=%v", res.Notes)
	}
}

// TestGuardKeepsValidDomains 断言合法建议被保留。
func TestGuardKeepsValidDomains(t *testing.T) {
	ev := blockEvidence("t.apkpure.net", "tapi.apkpure.net")
	sug := &Suggestion{
		Verdict: "wrong_block",
		Action: Action{Type: "add_global_allow",
			Domains: []string{"t.apkpure.net", "tapi.apkpure.net"}},
		Reply: "已放行",
	}
	res := Guard(sug, ev, nil)
	if res.Action.Type != "add_global_allow" {
		t.Fatalf("合法建议应保留原动作，实际 %q", res.Action.Type)
	}
	if len(res.Action.Domains) != 2 {
		t.Fatalf("应保留 2 个域名，实际 %v", res.Action.Domains)
	}
	if res.Downgraded {
		t.Fatal("合法建议不该被降级")
	}
}

// TestGuardCapsDomainCount 断言超量建议被截断。
func TestGuardCapsDomainCount(t *testing.T) {
	domains := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		domains = append(domains, "d"+itoa(i)+".example.com")
	}
	ev := blockEvidence(domains...)
	sug := &Suggestion{
		Verdict: "wrong_block",
		Action:  Action{Type: "add_global_allow", Domains: domains},
	}
	res := Guard(sug, ev, nil)
	if len(res.Action.Domains) > maxSuggestDomains {
		t.Fatalf("最多保留 %d 个，实际 %d", maxSuggestDomains, len(res.Action.Domains))
	}
}

// TestGuardRejectsUserBlockForUserAllow 断言「用户自己写的拦截」不自动放开。
//
// 用户自己的拦截规则代表明确意图，自动加白名单会和原规则打架，
// 这种冲突必须人来判断。
func TestGuardRejectsUserBlockForUserAllow(t *testing.T) {
	ev := &Evidence{Domains: []DomainEvidence{{
		Domain: "intentional.example.com", Action: "block", Layer: "user_block",
		LayerLabel: "用户自定义拦截",
	}}}
	sug := &Suggestion{
		Verdict: "unclear",
		Action:  Action{Type: "add_user_allow", Domains: []string{"intentional.example.com"}},
	}
	res := Guard(sug, ev, nil)
	if res.Action.Type != "need_human" {
		t.Fatalf("用户自定义拦截不该被自动放行，实际 %q", res.Action.Type)
	}
}

// TestGuardNoActionPassthrough 断言 no_action / need_human 原样通过。
func TestGuardNoActionPassthrough(t *testing.T) {
	for _, act := range []string{"no_action", "need_human"} {
		sug := &Suggestion{Verdict: "correct", Action: Action{Type: act}}
		res := Guard(sug, blockEvidence("a.example.com"), nil)
		if res.Action.Type != act {
			t.Errorf("%s 应原样通过，实际 %q", act, res.Action.Type)
		}
		if res.Downgraded {
			t.Errorf("%s 不该被标记降级", act)
		}
	}
}

// TestGuardNormalizesInput 断言模型给的脏输入被规范化后再判断。
func TestGuardNormalizesInput(t *testing.T) {
	ev := blockEvidence("ads.example.com")
	sug := &Suggestion{
		Verdict: "wrong_block",
		Action: Action{Type: "add_global_allow",
			Domains: []string{"  HTTPS://ADS.Example.com/path?q=1  "}},
	}
	res := Guard(sug, ev, nil)
	if len(res.Action.Domains) != 1 || res.Action.Domains[0] != "ads.example.com" {
		t.Fatalf("应规范化为 ads.example.com，实际 %v", res.Action.Domains)
	}
}

// TestIsSafeAllowTarget 断言采纳阶段的二次校验口径。
func TestIsSafeAllowTarget(t *testing.T) {
	ok := []string{"t.apkpure.net", "raw.githubusercontent.com", "||github.com^", "a.b.c.example.com"}
	for _, d := range ok {
		if !IsSafeAllowTarget(d) {
			t.Errorf("%q 应被判为安全", d)
		}
	}
	bad := []string{"", "com", "cn", "githubusercontent.com", ".example.com", "nodot"}
	for _, d := range bad {
		if IsSafeAllowTarget(d) {
			t.Errorf("%q 应被判为不安全", d)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
