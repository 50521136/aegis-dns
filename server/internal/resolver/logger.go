package resolver

import (
	"context"
	"log/slog"
	"strings"
)

// 这个文件过滤 dnsproxy 上游库打的「客户端主动放弃」类日志。
//
// 背景：线上 7 小时里 aegis-dnsd 打了 599 条 level=ERROR，全部是
//   msg="response received" status="exchanging: ... : context canceled"
// 也就是客户端（手机/路由器）发来查询后自己不等了、切网了、关页面了，
// Go 的 HTTP 客户端于是把请求取消掉。
//
// 这**不是故障**：请求是我们主动发出、对方先放弃。但它有两个实际危害：
//  1. 日志噪音把真正的错误淹掉 —— 599 条真错误里混进 599 条假错误，
//     没人会一条条翻；
//  2. 级别标成 ERROR 会让「有没有故障」这个判断本身失效。
//
// dnsproxy 的 upstream.Options 支持注入 Logger，所以在这里包一层：
// 判断错误文本属于「对端取消/连接被关」就降到 Debug，其余原样透传。
// 真正的上游故障（超时、连接被拒、TLS 失败、状态码异常）仍然记 ERROR。

// canceledSubstrings 表示「请求是对端主动取消的」，不是我们这边出错。
var canceledSubstrings = []string{
	"context canceled",
	"operation was canceled",
	"use of closed network connection",
	"connection reset by peer",
}

// upstreamLogger 返回给 dnsproxy 用的 logger。
func upstreamLogger(base *slog.Logger) *slog.Logger {
	if base == nil {
		base = slog.Default()
	}
	return slog.New(&cancelFilterHandler{next: base.Handler()})
}

// cancelFilterHandler 把「对端取消」类日志降到 Debug。
type cancelFilterHandler struct {
	next slog.Handler
}

func (h *cancelFilterHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	// 总是让 Debug 放行到下一层，由下一层自己决定是否输出。
	// 若这里跟着 next 的级别返回，Debug 会被提前挡掉，降级就没有意义了。
	return h.next.Enabled(ctx, slog.LevelDebug)
}

func (h *cancelFilterHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError && isCanceledRecord(&r) {
		r2 := r.Clone()
		r2.Level = slog.LevelDebug
		// 补一句说明，避免以后有人看到 Debug 记录一头雾水。
		r2.AddAttrs(slog.String("downgraded", "客户端主动取消，非上游故障"))
		return h.next.Handle(ctx, r2)
	}
	return h.next.Handle(ctx, r)
}

func (h *cancelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &cancelFilterHandler{next: h.next.WithAttrs(attrs)}
}

func (h *cancelFilterHandler) WithGroup(name string) slog.Handler {
	return &cancelFilterHandler{next: h.next.WithGroup(name)}
}

// isCanceledRecord 检查这条日志的所有属性里是否出现取消类关键词。
func isCanceledRecord(r *slog.Record) bool {
	hit := false
	r.Attrs(func(a slog.Attr) bool {
		if containsCanceled(a.Value.String()) {
			hit = true
			return false
		}
		return true
	})
	if hit {
		return true
	}
	return containsCanceled(r.Message)
}

func containsCanceled(s string) bool {
	if s == "" {
		return false
	}
	for _, sub := range canceledSubstrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
