package api

import (
	"context"
	"fmt"
	"time"

	"github.com/50521136/aegis-dns/server/internal/bjtime"
	"github.com/50521136/aegis-dns/server/internal/mail"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// dailyReportLoop 每分钟醒一次，给「推送时间正好是这一分钟」的用户发日报。
//
// 为什么用「每分钟自检」而不是给每个用户开一个 Timer：
//   - 服务器重启、改时间、改时区都会让 Timer 失准，每分钟自检永远能自愈；
//   - 用户改了推送时间也不需要重建任何调度结构，下一分钟自然按新值判断。
//
// 时间的唯一来源是 users.report_time（北京时间 "HH:MM"），
// 管理员那边只有总开关 —— 两处都能改时间的话，
// 谁生效说不清，之前配额就栽在这上面。
func (s *Server) dailyReportLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// 启动后先等一分钟：让快照、订阅拉取这些启动任务先跑完。
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}

	s.trySendDailyReports(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.trySendDailyReports(ctx)
		}
	}
}

// trySendDailyReports 找出这一分钟该发日报的用户并逐个发送。
func (s *Server) trySendDailyReports(ctx context.Context) {
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return
	}
	if !set.DailyReportEnabled {
		return
	}

	cfg := mailConfig(set)
	if err := cfg.Validate(); err != nil {
		// 没配 SMTP 就静默跳过：日报是可选功能，
		// 每分钟刷一条告警日志只会淹没有用的信息。
		return
	}

	now := time.Now()
	hhmm := fmt.Sprintf("%02d:%02d", bjHour(now), bjMinute(now))
	users, err := s.store.UsersDueDailyReport(ctx, hhmm)
	if err != nil || len(users) == 0 {
		return
	}

	// 日报统计「昨天」：北京时间昨天 0 点到今天 0 点。
	// StartOfDay 要的是「自然日序号」，不是秒数（传 today-86400 会跑到 1790 年）。
	today := bjtime.Today()
	from := bjtime.StartOfDay(today - 1)
	to := bjtime.StartOfDay(today)
	dateStr := time.Unix(from+8*3600, 0).UTC().Format("2006-01-02")

	sent := 0
	for _, u := range users {
		// 每个用户一个幂等键：同一分钟被重复触发、
		// 或进程重启后又跑了一次，都不会让同一个人一天收到两封。
		key := fmt.Sprintf("report_sent_%s_%d", u.ID, today)
		if v, _ := s.store.GetMeta(ctx, key); v != "" {
			continue
		}
		if err := s.sendDailyReport(ctx, cfg, set, u, from, to, dateStr, false); err != nil {
			s.log.Warn("发送日报失败", "user", u.Username, "err", err)
			continue
		}
		_ = s.store.SetMeta(ctx, key, "1")
		sent++
	}
	if sent > 0 {
		s.log.Info("每日日报已发送", "count", sent, "date", dateStr, "time", hhmm)
	}
}

// sendDailyReport 汇总并发送一封日报。test 为真时是「测试推送」。
func (s *Server) sendDailyReport(
	ctx context.Context,
	cfg mail.Config,
	set *model.GlobalSettings,
	u *model.User,
	from, to int64,
	dateStr string,
	test bool,
) error {
	d, err := s.store.DailyReport(ctx, u.ID, from, to, 5)
	if err != nil {
		return err
	}
	// 正式推送时，零查询的用户不发：空报表发几次，用户就会去退订。
	// 但「测试推送」是用户主动点的，没数据也要发 ——
	// 他点这个按钮就是为了看邮件长什么样，回一句「没数据」等于没测。
	if d.Total == 0 && !test {
		return nil
	}

	var avg int64
	if d.Requests > 0 {
		avg = d.LatencySum / d.Requests
	}
	rep := mail.DailyReport{
		Username:     u.Username,
		Domain:       set.Domain,
		Date:         dateStr,
		Total:        d.Total,
		Blocked:      d.Blocked,
		Cached:       d.Cached,
		AvgLatencyMS: avg,
		DailyQuota:   s.dailyQuotaOf(ctx, u, set),
		IsTest:       test,
	}
	for _, t := range d.TopDomains {
		rep.TopDomains = append(rep.TopDomains, mail.ReportRow{Name: t.Domain, Count: t.Hits})
	}

	subject := fmt.Sprintf("【%s】%s 解析日报", set.Domain, dateStr)
	if test {
		subject = fmt.Sprintf("【%s】日报测试推送", set.Domain)
	}
	return mail.Send(cfg, mail.Message{
		To:      u.Email,
		Subject: subject,
		Body:    mail.DailyReportText(rep),
		HTML:    mail.DailyReportEmail(rep),
	})
}

// bjHour 返回某个时刻的北京时间小时（0-23），不依赖服务器 TZ。
//
// 不能用 time.Now().Add(8h).Hour()：那假设服务器是 UTC，
// 而实际机器 TZ 常常已是 Asia/Shanghai，再加 8 小时就变成 17 点。
func bjHour(t time.Time) int {
	return int(((t.Unix()+8*3600)%86400 + 86400) % 86400 / 3600)
}

// bjMinute 返回某个时刻的北京时间分钟（0-59），不依赖服务器 TZ。
func bjMinute(t time.Time) int {
	return int(((t.Unix()+8*3600)%86400 + 86400) % 86400 % 3600 / 60)
}

// dailyQuotaOf 返回该用户当前的每日请求量配额（0 = 不限）。
func (s *Server) dailyQuotaOf(ctx context.Context, u *model.User, set *model.GlobalSettings) int64 {
	tiers, err := s.store.TierMap(ctx)
	if err != nil {
		return 0
	}
	return model.QuotaForUser(u, set, tiers, time.Now()).DailyQueries
}

// StartDailyReportLoop 供 main 启动后台日报任务。
//
// 定时任务需要 store 和配置，都在 Server 上，所以由 Server 对外暴露一个入口，
// main 里不用再自己拼依赖。
func (s *Server) StartDailyReportLoop(ctx context.Context) {
	s.dailyReportLoop(ctx)
}
