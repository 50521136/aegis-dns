package rules

import (
	"fmt"
	"strings"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// NormalizePattern 把用户提交的任意写法收敛成「纯域名 + 可选通配」。
//
// 为什么需要它：规则表里原本同时存在三种写法 ——
//
//	@@||public1.alidns.com^   批量导入（canonicalPattern 拼出来的）
//	||public1.alidns.com^     单条创建（原样存）
//	public1.alidns.com        手工录入 / 老数据
//
// 而匹配时 splitPattern 会无条件剥掉 @@ 和 ||，也就是说这三种写法的匹配
// 结果完全一样，前缀是纯冗余。冗余本身还不致命，致命的是它能和 kind 矛盾：
// kind=block 配 pattern="@@||x^" 时，@@ 被剥掉、kind 生效，实际结果是
// 「拦截 x」—— 和 pattern 的字面意思完全相反，而且不会有任何报错。
//
// 所以约定：kind 是语义的唯一真相来源，pattern 只存域名，不带任何语法前缀。
// 订阅层（sub_entries）早就是这个形态（283 万条里 0 条带前缀），这里只是
// 让规则层跟上。
//
// 返回的第二个值用于提示调用方「输入被改写了」，前端可以据此回显。
func NormalizePattern(pattern string) (string, error) {
	domain, wildcard, err := splitPattern(pattern)
	if err != nil {
		return "", err
	}
	if domain == "" {
		return "", fmt.Errorf("规则内容里没有可用的域名: %q", pattern)
	}
	if wildcard {
		return "*." + domain, nil
	}
	return domain, nil
}

// NormalizeRule 就地规范化一条规则的 pattern。
//
// 同时挡掉 kind 与 pattern 互相矛盾的输入 —— 这类输入目前能悄悄入库，
// 然后在匹配时按 kind 生效，跟写规则的人的意图正好相反。
func NormalizeRule(r *model.Rule) error {
	if r == nil {
		return fmt.Errorf("规则为空")
	}
	raw := strings.TrimSpace(r.Pattern)

	// 先识别输入里的语法前缀，用来发现与 kind 的矛盾。
	// 只在「明确冲突」时报错：@@ 是 allow 专用标记，配 block 一定是写错了。
	if strings.HasPrefix(raw, "@@") {
		switch r.Kind {
		case model.KindAllow:
			// 一致，正常剥掉
		case model.KindBlock:
			return fmt.Errorf("规则类型是拦截，但内容以 @@ 开头（@@ 表示放行）；" +
				"请改用 allow 类型，或去掉 @@ 前缀")
		default:
			return fmt.Errorf("规则类型 %s 不支持 @@ 前缀", r.Kind)
		}
	}

	norm, err := NormalizePattern(raw)
	if err != nil {
		return err
	}
	r.Pattern = norm
	return nil
}

// IsNormalized 判断 pattern 是否已经是规范化形态。
// 迁移脚本和测试用它来断言存量数据已清理干净。
func IsNormalized(pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.ContainsAny(pattern, "@^|$") {
		return false
	}
	dom, _, err := splitPattern(pattern)
	if err != nil || dom == "" {
		return false
	}
	// splitPattern 会把 "||a.com^" 还原成 "a.com"，所以再比一次原串。
	return pattern == dom || pattern == "*."+dom
}
