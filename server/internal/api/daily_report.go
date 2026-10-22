package api

import (
	"context"
	"fmt"
	"time"

	"github.com/50521136/aegis-dns/server/internal/bjtime"
	"github.com/50521136/aegis-dns/server/internal/mail"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// dailyReportLoop 每天在配置的小时给订阅了日报的用户发一封邮件。
//
// 为什么用「每分钟醒一次 + 判断当前小时」而不是 time.Timer 精确到点：
//   - 服务器重启、改时区、改配置都会让 Timer 失准，而每分钟自检永远能自愈；
//   - 一天只需要命中一次，多醒 1439 次的开销可以忽略（每次只读一行 meta）。
//
// 用 meta 表记「今天发过了」，避免同一小时内多次触发重复发送。
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

// trySendDailyReports 判断现在是不是该发日报了，是就发。
func (s *Server) trySendDailyReports(ctx context.Context) {
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return
	}
	if !set.DailyReportEnabled {
		return
	}
	hour := set.DailyReportHour
	if hour < 0 || hour > 23 {
		hour = 9
	}
	now := time.Now()
	// 北京时间的小时。
	//
	// 必须从 Unix 时间戳直接算，不能用 time.Now().Add(8h).Hour()：
	// 那玩意儿假设服务器是 UTC，而实际机器上 TZ 常常已经是 Asia/Shanghai，
	// 再加 8 小时就变成 17 点，于是「设 9 点」永远匹配不上、一封都不发。
	// 用 (unix+8h) % 86400 跟 bjtime 的算法一致，也不受 TZ 影响。
	if bjHour(now) != hour {
		return
	}

	// 北京时间的「今天」，作为幂等键。
	today := bjtime.Today()
	key := fmt.Sprintf("daily_report_sent_%d", today)
	if sent, _ := s.store.GetMeta(ctx, key); sent != "" {
		return
	}

	cfg := mailConfig(set)
	if err := cfg.Validate(); err != nil {
		// 没配 SMTP 就静默跳过：日报是可选功能，
		// 每分钟刷一条告警日志只会淹没有用的信息。
		return
	}

	users, err := s.store.UsersForDailyReport(ctx)
	if err != nil {
		s.log.Warn("读取日报订阅用户失败", "err", err)
		return
	}
	if len(users) == 0 {
		// 没人订阅也记一笔，否则每分钟都会重试到整点结束。
		_ = s.store.SetMeta(ctx, key, "0")
		return
	}

	// 日报统计「昨天」：北京时间昨天 0 点到今天 0 点。
	// StartOfDay 要的是「自然日序号」，不是秒数。
	// 写成 today-86400 会跑到 1790 年（日志里 date=1790-04-02 就是这么来的），
	// 统计范围变成「从 1790 年到现在」，数字看着对、窗口完全错了。
	from := bjtime.StartOfDay(today - 1)
	to := bjtime.StartOfDay(today)
	dateStr := time.Unix(from+8*3600, 0).UTC().Format("2006-01-02")

	s.log.Info("开始发送每日日报", "date", dateStr, "users", len(users))

	sent := 0
	for _, u := range users {
		if u.Email == "" {
			continue
		}
		d, err := s.store.DailyReport(ctx, u.ID, from, to, 5)
		if err != nil {
			s.log.Warn("汇总日报失败", "user", u.Username, "err", err)
			continue
		}
		// 一条查询都没有的用户不发：空的日报只会变成垃圾邮件，
		// 用户收到「总查询量 0」几次之后就会去退订。
		if d.Total == 0 {
			continue
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
		}
		for _, t := range d.TopDomains {
			rep.TopDomains = append(rep.TopDomains, mail.ReportRow{Name: t.Domain, Count: t.Hits})
		}

		msg := mail.Message{
			To:      u.Email,
			Subject: fmt.Sprintf("【%s】%s 解析日报", set.Domain, dateStr),
			Body:    mail.DailyReportText(rep),
			HTML:    mail.DailyReportEmail(rep),
		}
		if err := mail.Send(cfg, msg); err != nil {
			s.log.Warn("发送日报失败", "user", u.Username, "err", err)
			continue
		}
		sent++
	}

	// 无论发成功几个都记一笔：失败的下次启动也不会重发同一封，
	// 免得用户在某天收到十几封重复日报。
	_ = s.store.SetMeta(ctx, key, fmt.Sprint(sent))
	s.log.Info("每日日报已发送", "count", sent, "date", dateStr)
}

// bjHour 返回某个时刻的北京时间小时（0-23），不依赖服务器 TZ。
func bjHour(t time.Time) int {
	return int(((t.Unix()+8*3600)%86400 + 86400) % 86400 / 3600)
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
