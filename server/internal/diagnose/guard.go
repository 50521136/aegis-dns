package diagnose

import (
	"fmt"
	"strings"
)

// 这个文件是「自动化」与「把站点拦死」之间的唯一一道墙。
//
// 为什么必须有它：模型的输出是不可信输入。一句 `add_global_allow:
// ["com"]` 就能让全站拦截率归零，而这种错误在概率上是必然出现的
// —— 不是「会不会」，是「什么时候」。
//
// 所以采纳前要过三道校验：
//  1. 域名形态合法（不是顶级域、不是单标签、不是公共后缀）；
//  2. 该域名在证据里确实被判为 block（模型不能凭空造域名）；
//  3. 数量与影响面上限。
//
// 任何一条不满足就降级为 need_human，而不是「尽力执行」。

// maxSuggestDomains 是单次建议允许的最大域名数。
//
// 超过这个数基本可以断定模型在「尽量多放行」，而不是精准修复。
const maxSuggestDomains = 5

// publicSuffixLike 是绝不能作为放行目标的「准公共后缀」。
//
// 这些不是完整 PSL，而是实际踩过或一眼能看出的高危项：
// 放行它们等于放行整个 TLD 或某个大厂的全部域名。
var publicSuffixLike = map[string]struct{}{
	"com": {}, "net": {}, "org": {}, "cn": {}, "io": {}, "dev": {},
	"co": {}, "me": {}, "tv": {}, "cc": {}, "xyz": {}, "top": {},
	"info": {}, "biz": {}, "app": {}, "site": {}, "online": {},
	"com.cn": {}, "net.cn": {}, "org.cn": {}, "gov.cn": {}, "edu.cn": {},
	"co.uk": {}, "co.jp": {}, "com.au": {}, "co.kr": {},
	// 二级公共后缀里最容易被误放行、后果最严重的几个
	"github.io": {}, "github.dev": {}, "githubusercontent.com": {},
	"cloudfront.net": {}, "amazonaws.com": {}, "aliyuncs.com": {},
	"myqcloud.com": {}, "azureedge.net": {}, "cloudflare.net": {},
}

// GuardResult 是闸门校验结果。
type GuardResult struct {
	// Action 是校验后的动作（可能从 add_* 降级成 need_human）。
	Action Action
	// Notes 记录被拒绝的域名与原因，供管理员在界面上看到
	// —— 让「AI 建议了什么、为什么被挡」可审计。
	Notes []string
	// Downgraded 表示因为校验不通过而降级为 need_human。
	Downgraded bool
}

// Guard 校验并收紧模型给出的动作。
//
// blockedDomains 是证据里 action=block 的域名集合：模型只能在这些里面选，
// 不能放行证据里没提过、或本身就没被拦的域名。
func Guard(sug *Suggestion, ev *Evidence, allowedRuleDomains map[string]bool) GuardResult {
	res := GuardResult{Action: sug.Action}

	if sug.Action.Type == "no_action" || sug.Action.Type == "need_human" {
		return res
	}

	blocked := make(map[string]DomainEvidence, len(ev.Domains))
	for _, d := range ev.Domains {
		if d.Action == "block" {
			blocked[d.Domain] = d
		}
	}

	kept := make([]string, 0, len(sug.Action.Domains))
	seen := make(map[string]struct{}, len(sug.Action.Domains))
	for _, raw := range sug.Action.Domains {
		d := strings.ToLower(strings.TrimSpace(raw))
		d = strings.TrimPrefix(d, "http://")
		d = strings.TrimPrefix(d, "https://")
		d = strings.TrimPrefix(d, "*.")
		d = strings.TrimSuffix(d, ".")
		if i := strings.IndexAny(d, "/?#"); i >= 0 {
			d = d[:i]
		}

		switch {
		case d == "":
			res.Notes = append(res.Notes, "跳过空域名")
			continue
		case isPublicSuffix(d):
			res.Notes = append(res.Notes, fmt.Sprintf("拒绝放行 %s：这是公共后缀/大范围域名，放行会波及大量无关站点", d))
			continue
		case !looksLikeDomain(d):
			res.Notes = append(res.Notes, fmt.Sprintf("拒绝放行 %s：域名形态不合法", d))
			continue
		case !strings.Contains(d, "."):
			res.Notes = append(res.Notes, fmt.Sprintf("拒绝放行 %s：不是完整域名", d))
			continue
		}

		// 模型不能放行证据里没被判成 block 的域名。
		de, ok := blocked[d]
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf("拒绝放行 %s：证据里它并没有被拦截", d))
			continue
		}
		// add_user_allow 只对「拦在订阅层」有意义；如果是用户自己写的拦截规则
		// 拦住自己，加白名单会造成同层冲突，交给人工判断。
		if sug.Action.Type == "add_user_allow" &&
			(de.Layer == "user_block" || de.Layer == "global_block") {
			res.Notes = append(res.Notes, fmt.Sprintf("拒绝放行 %s：它被自定义规则明确拦截（%s），应人工确认意图",
				d, de.LayerLabel))
			continue
		}
		// 全局放行前多一道：这个域名是否已被某条更宽的放行规则覆盖。
		// 覆盖了还去加，只会让规则表越来越脏。
		//
		// allowedRuleDomains 的语义是「域名 → 覆盖它的那条更宽规则」，
		// 这里只报警不给建议 —— 是否真的已经覆盖，管理员肉眼判断更快。
		if sug.Action.Type == "add_global_allow" && allowedRuleDomains != nil {
			if _, covered := allowedRuleDomains[d]; covered {
				res.Notes = append(res.Notes, fmt.Sprintf("注意 %s：疑似已被更宽的放行规则覆盖，请确认是否仍需新增", d))
			}
		}

		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		kept = append(kept, d)
		if len(kept) >= maxSuggestDomains {
			res.Notes = append(res.Notes, fmt.Sprintf("建议域名过多，已截断到 %d 个", maxSuggestDomains))
			break
		}
	}

	if len(kept) == 0 {
		// 一个都没通过：不执行，转人工。
		res.Downgraded = true
		res.Action = Action{Type: "need_human"}
		if len(res.Notes) == 0 {
			res.Notes = append(res.Notes, "模型没有给出可执行的域名建议")
		}
		return res
	}
	res.Action.Domains = kept
	return res
}
