package bjtime

import (
	"testing"
	"time"
)

// 日界必须是北京时间 0 点，不是 UTC 0 点。
// 这里把一天里几个关键 UTC 时刻钉住，防止有人把实现改回 t.Unix()/86400。
func TestDayIsBeijingMidnight(t *testing.T) {
	cases := []struct {
		utc  string
		want string // 期望落在哪个北京日期
	}{
		{"2026-10-20T15:59:59Z", "2026-10-20"}, // 北京 23:59:59，仍是 20 号
		{"2026-10-20T16:00:00Z", "2026-10-21"}, // 北京 00:00:00，跨到 21 号
		{"2026-10-20T16:00:01Z", "2026-10-21"},
		{"2026-10-20T00:00:00Z", "2026-10-20"}, // 北京 08:00，20 号
		{"2026-10-21T00:23:00Z", "2026-10-21"},
	}
	ref := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	refDay := Day(ref)
	for _, c := range cases {
		tm, err := time.Parse(time.RFC3339, c.utc)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", c.utc, err)
		}
		got := ref.AddDate(0, 0, int(Day(tm)-refDay)).Format("2006-01-02")
		if got != c.want {
			t.Errorf("%s → 北京日 %s，期望 %s", c.utc, got, c.want)
		}
	}
}

// 相邻两个北京日必须相差正好 1，且 StartOfDay 能还原出北京 0 点。
func TestStartOfDayRoundTrip(t *testing.T) {
	d := Day(time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)) // 北京 11:00
	if got := Day(time.Unix(StartOfDay(d), 0)); got != d {
		t.Errorf("StartOfDay/Day 不闭合: %d → %d", d, got)
	}
	if got := Day(time.Unix(StartOfDay(d)-1, 0)); got != d-1 {
		t.Errorf("0 点前 1 秒应属于前一天，得到 %d", got)
	}
	if got := Day(time.Unix(StartOfDay(d+1), 0)); got != d+1 {
		t.Errorf("次日 0 点应属于次日，得到 %d", got)
	}
}
