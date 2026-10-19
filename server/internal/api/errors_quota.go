package api

import (
	"net/http"
	"strconv"
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
