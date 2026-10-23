package resolver

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// 这个文件锁住「客户端主动取消不算上游故障」这条判定。
//
// 背景：线上 7 小时 599 条 ERROR 全是
// msg="response received" status="... context canceled"，
// 属于客户端自己不等了，不是上游出问题。这类日志必须降到 Debug，
// 否则真正的故障会被噪音淹没。

func record(t *testing.T, fn func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fn(upstreamLogger(base))
	return buf.String()
}

// TestCanceledLogDowngraded 断言 context canceled 被降级为 DEBUG。
func TestCanceledLogDowngraded(t *testing.T) {
	out := record(t, func(l *slog.Logger) {
		l.Error("response received",
			"proto", "tcp",
			"status", "exchanging: requesting https://dns.alidns.com/dns-query: Get \"...\": context canceled")
	})
	if out == "" {
		t.Fatal("降级不等于丢弃：应仍以 DEBUG 写出")
	}
	if !strings.Contains(out, "level=DEBUG") {
		t.Fatalf("应降级为 DEBUG，实际：%s", out)
	}
	if !strings.Contains(out, "客户端主动取消") {
		t.Fatalf("应补一句降级说明，实际：%s", out)
	}
}

// TestRealFailureStaysError 断言真正的上游故障仍记 ERROR。
func TestRealFailureStaysError(t *testing.T) {
	cases := []string{
		"exchanging: requesting https://dns.alidns.com/dns-query: context deadline exceeded",
		"dial tcp 1.2.3.4:53: connect: connection refused",
		"tls: failed to verify certificate: x509: certificate has expired",
		"unexpected status code 503",
	}
	for _, msg := range cases {
		out := record(t, func(l *slog.Logger) {
			l.Error("response received", "status", msg)
		})
		if !strings.Contains(out, "level=ERROR") {
			t.Errorf("真故障仍应是 ERROR（%q），实际：%s", msg, out)
		}
	}
}

// TestWarnUnaffected 断言非 ERROR 级别不受影响（降级只针对 ERROR）。
func TestWarnUnaffected(t *testing.T) {
	out := record(t, func(l *slog.Logger) {
		l.Warn("upstream slow", "err", "context canceled")
	})
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("WARN 不该被改写，实际：%s", out)
	}
}

// TestInfoPassthrough 断言正常日志照常输出。
func TestInfoPassthrough(t *testing.T) {
	out := record(t, func(l *slog.Logger) {
		l.Info("upstream ready", "addr", "223.5.5.5:53")
	})
	if !strings.Contains(out, "upstream ready") || !strings.Contains(out, "level=INFO") {
		t.Fatalf("INFO 日志应原样透传，实际：%s", out)
	}
}

// TestCancelFilterWithAttrs 断言 WithAttrs 之后过滤仍然生效。
//
// dnsproxy 会用 With(...) 派生带固定字段的 logger，如果派生后的
// handler 丢了过滤能力，线上噪音还是会在。
func TestCancelFilterWithAttrs(t *testing.T) {
	out := record(t, func(l *slog.Logger) {
		l.With("addr", "https://dns.alidns.com/dns-query").
			Error("response received", "status", "exchanging: context canceled")
	})
	if !strings.Contains(out, "level=DEBUG") {
		t.Fatalf("派生 logger 也应过滤，实际：%s", out)
	}
	if !strings.Contains(out, "addr=") {
		t.Fatalf("派生字段不该丢失，实际：%s", out)
	}
}

var _ = context.Background
