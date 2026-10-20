package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// CodeQuotaExceeded 表示超出配额（规则条数、订阅个数）。
const CodeQuotaExceeded = "QUOTA_EXCEEDED"

// quotaError 是配额超限错误。
//
// 单独一个类型而不是复用 validationError：配额与「参数写错」是两件事，
// 前端要据此显示「去删规则 / 找管理员调额」，而不是在表单字段上标红。
type quotaError string

func (e quotaError) Error() string { return string(e) }

func errQuota(msg string) error { return quotaError(msg) }

// writeQuotaError 把配额错误写成 403。
//
// 不用 429：429 的语义是「请求太快，稍后重试」，而配额超限重试永远不会成功。
func writeQuotaError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusForbidden, CodeQuotaExceeded, err.Error(), nil)
}

// itoa 是 strconv.Itoa 的短别名，仅用于拼提示语。
func itoa(n int) string { return strconv.Itoa(n) }

// CodeFeatureDisabled 表示该功能被关闭（全局总开关或该用户单独开关）。
const CodeFeatureDisabled = "FEATURE_DISABLED"

// CodeCustomIDUsed 表示「自定义接入 ID」那一次机会已经用掉了。
const CodeCustomIDUsed = "CUSTOM_ID_USED"

// CodeClientIDTaken 表示目标接入 ID 已被别的用户占用。
const CodeClientIDTaken = "CLIENT_ID_TAKEN"

// checkUserFeature 检查租户侧功能是否对该用户开放。
//
// 两层开关取「与」：全局设置里的总开关（管理员一键停掉所有人的规则/订阅），
// 以及 users 表上的单用户开关（只关某个人）。任一为关都不允许新建。
// 已有数据不受影响 —— 关开关是止血，不是删数据。
func (s *Server) checkUserFeature(ctx context.Context, u *model.User, kind string) error {
	if u.Role == model.RoleAdmin {
		return nil // 管理员不受租户侧开关限制
	}
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	switch kind {
	case "rules":
		if !set.AllowUserRules {
			return quotaError("管理员已关闭普通用户的规则功能")
		}
		if !u.RulesEnabled {
			return quotaError("你的账号已被禁止添加规则，请联系管理员")
		}
	case "subs":
		if !set.AllowUserSubs {
			return quotaError("管理员已关闭普通用户的订阅功能")
		}
		if !u.SubsEnabled {
			return quotaError("你的账号已被禁止添加订阅，请联系管理员")
		}
	}
	return nil
}

// writeFeatureError 把功能关闭错误写成 403。
func writeFeatureError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusForbidden, CodeFeatureDisabled, err.Error(), nil)
}
