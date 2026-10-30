package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/store"
)

func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	s.statsSummary(w, r, currentUser(r).ID)
}

// handleAdminStatsSummary 返回全站汇总。
//
// uid 传空串表示「所有用户」—— store 层已经支持，管理员的「总览」
// 和普通用户的「总览」就是同一套逻辑换个作用域，不必写两遍。
func (s *Server) handleAdminStatsSummary(w http.ResponseWriter, r *http.Request) {
	s.statsSummary(w, r, "")
}

func (s *Server) statsSummary(w http.ResponseWriter, r *http.Request, uid string) {
	since := timeRange(r.URL.Query().Get("range"), time.Now())

	sum, err := s.store.Summary(r.Context(), uid, since.Unix())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	unique, err := s.store.UniqueDomains(r.Context(), uid, since.Unix())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"range":          orDefault(r.URL.Query().Get("range"), "24h"),
		"total":          sum.Total,
		"blocked":        sum.Blocked,
		"cached":         sum.Cached,
		"allowed":        sum.Allowed,
		"block_rate":     sum.BlockRate,
		"cache_hit_rate": sum.CacheRate,
		"avg_latency_ms": sum.AvgLatency,
		"unique_domains": unique,
	})
}

func (s *Server) handleStatsTimeseries(w http.ResponseWriter, r *http.Request) {
	s.statsTimeseries(w, r, currentUser(r).ID)
}

func (s *Server) handleAdminStatsTimeseries(w http.ResponseWriter, r *http.Request) {
	s.statsTimeseries(w, r, "")
}

func (s *Server) statsTimeseries(w http.ResponseWriter, r *http.Request, uid string) {
	q := r.URL.Query()
	since := timeRange(q.Get("range"), time.Now())

	// 默认粒度跟着区间走：24h 用 1h 桶，7d 用 6h，30d+ 用 1d。
	interval := intervalSeconds(q.Get("interval"), defaultIntervalFor(q.Get("range")))

	points, err := s.store.Timeseries(r.Context(), uid, since.Unix(), interval)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(points))
	for _, p := range points {
		items = append(items, map[string]any{
			"ts":      p.TS,
			"time":    time.Unix(p.TS, 0).UTC().Format(time.RFC3339),
			"total":   p.Total,
			"blocked": p.Blocked,
			"cached":  p.Cached,
			"allowed": p.Allowed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":    orDefault(q.Get("range"), "24h"),
		"interval": interval,
		"points":   items,
	})
}

func defaultIntervalFor(rng string) int64 {
	switch rng {
	case "1h":
		return 300
	case "7d":
		return 6 * 3600
	case "30d", "90d":
		return 86400
	default:
		return 3600
	}
}

func (s *Server) handleStatsTop(w http.ResponseWriter, r *http.Request) {
	s.statsTop(w, r, currentUser(r).ID)
}

func (s *Server) handleAdminStatsTop(w http.ResponseWriter, r *http.Request) {
	s.statsTop(w, r, "")
}

func (s *Server) statsTop(w http.ResponseWriter, r *http.Request, uid string) {
	q := r.URL.Query()
	since := timeRange(q.Get("range"), time.Now())

	limit := atoiDefault(q.Get("limit"), 10)
	if limit < 1 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	blockedOnly := q.Get("type") == "blocked"

	list, err := s.store.TopDomains(r.Context(), uid, since.Unix(), limit, blockedOnly)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, t := range list {
		items = append(items, map[string]any{
			"domain":  t.Domain,
			"hits":    t.Hits,
			"blocked": t.Blocked,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "type": orDefault(q.Get("type"), "all")})
}

func (s *Server) handleQueryLog(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	q := r.URL.Query()

	limit := atoiDefault(q.Get("limit"), 100)
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	logs, total, err := s.store.QueryLogs(r.Context(), store.QueryLogFilter{
		UserID: u.ID,
		Domain: q.Get("domain"),
		Action: q.Get("action"),
		Limit:  limit,
		Offset: offset,
		// 用户自己清空过日志的话，只从那时之后开始看。
		SinceTS: u.LogClearedAt,
	})
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		items = append(items, map[string]any{
			"id":     l.ID,
			"ts":     l.TS,
			"time":   time.Unix(l.TS, 0).UTC().Format(time.RFC3339),
			"domain": l.Domain,
			"qtype":  l.QType,
			"rcode":  l.RCode,
			"action": l.Action,
			// 缓存命中是状态的修饰，不是状态本身
			"cache_hit":  l.CacheHit,
			"client":     l.Client,
			"latency_ms": l.LatencyMS,
			// 详情字段：列表页不显示，点开单条时才用到。
			// 老记录这些列是空串，前端按空处理。
			"proto":       l.Proto,
			"ecs":         l.ECS,
			// 上游只给协议类型：完整地址里带接入方的账号标识
			// （个人版 DoH 的订阅 ID 就在主机名里），不属于用户数据。
			"upstream":    redactUpstream(l.Upstream),
			"answer":      l.Answer,
			"tracker":     l.Tracker,
			"client_name": l.ClientName,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// handleAdminStatsUsers 返回按用户汇总的请求量排行榜。
//
// 这是管理员看全局流量的入口：谁的解析量最大、拦截率多少。
// 只给管理员，普通用户拿到的是自己那一行，没有横向比较的意义。
func (s *Server) handleAdminStatsUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := timeRange(q.Get("range"), time.Now())
	// 默认给 100 而不是 20：实例里几十个租户时，TOP 20 会让管理员以为
	// 「只有 20 个用户在用」。上限仍是 200（store 层也有同样的钳制）。
	limit := atoiDefault(q.Get("limit"), 100)

	rows, err := s.store.UserTraffic(r.Context(), since.Unix(), limit)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	// activeUsers 统计「窗口内真实有流量的租户」，不含未识别来源那个空 user_id 桶。
	// 必须单独算：items 会被 limit 截断，len(items) 在租户多的实例上会少报。
	activeUsers := 0
	for _, it := range rows {
		// user_id 为空的桶是「识别不出租户」的汇总：全网扫描器、
		// 直接拿 IP 打端口的探测、没绑来源 IP 的设备。
		//
		// 它不该在排行榜上显示成一行没有名字的幽灵用户（用户看到会以为
		// 数据坏了）。给它一个明确的名字 + 标记，前端据此渲染成灰色徽章，
		// 并且可以按 anonymous 字段过滤掉。
		name := it.Username
		anonymous := it.UserID == ""
		if anonymous {
			name = "未识别来源"
		} else {
			activeUsers++
		}
		items = append(items, map[string]any{
			"user_id":    it.UserID,
			"username":   name,
			"anonymous":  anonymous,
			"total":      it.Total,
			"blocked":    it.Blocked,
			"allowed":    it.Allowed,
			"cached":     it.Cached,
			"block_rate": it.BlockRate,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":       orDefault(q.Get("range"), "24h"),
		"items":       items,
		"total_users": len(items),
		// active_users 是「窗口内有流量的租户总数」（不含未识别来源那个桶）。
		// total_users 其实是本次返回的行数，被 limit 截断过，拿它当「多少人在用」
		// 会在租户超过 limit 时少报 —— 界面上要显示总量就得用这个字段。
		"active_users": activeUsers,
	})
}

// handleAdminQueryLog 返回全站查询日志（可指定 user_id 过滤）。
//
// 与用户版 handleQueryLog 的区别：不强制按当前用户过滤，
// 所以能看到所有租户的解析记录，用于排查投诉和滥用。
func (s *Server) handleAdminQueryLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := atoiDefault(q.Get("limit"), 100)
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	logs, total, err := s.store.QueryLogs(r.Context(), store.QueryLogFilter{
		UserID: q.Get("user_id"), // 空 = 全部用户
		Domain: q.Get("domain"),
		Action: q.Get("action"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		items = append(items, map[string]any{
			"id":       l.ID,
			"user_id":  l.UserID,
			"username": l.Username,
			"ts":       l.TS,
			"time":     time.Unix(l.TS, 0).UTC().Format(time.RFC3339),
			"domain":   l.Domain,
			"qtype":    l.QType,
			"rcode":    l.RCode,
			"action":   l.Action,
			// 缓存命中是状态的修饰，不是状态本身
			"cache_hit":  l.CacheHit,
			"client":     l.Client,
			"latency_ms": l.LatencyMS,
			// 详情字段：列表页不显示，点开单条时才用到。
			// 老记录这些列是空串，前端按空处理。
			"proto":    l.Proto,
			"ecs":      l.ECS,
			// 管理员看完整地址：他要判断的是「主上游还是备用在答」，
			// 只给协议类型就没法定位故障源。脱敏只针对普通用户。
			"upstream":    l.Upstream,
			"answer":      l.Answer,
			"tracker":     l.Tracker,
			"client_name": l.ClientName,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// handlePublicStats 给首页用的公开统计（无需登录）。
//
// 只暴露聚合后的总量与比率，不含任何域名、租户、IP 信息 ——
// 首页要的是「这个服务在跑、拦了多少」的说服力，不是运营数据。
func (s *Server) handlePublicStats(w http.ResponseWriter, r *http.Request) {
	// 固定 24 小时窗口：首页数字每天滚动一次，不给前端传参的余地，
	// 免得有人拿它当免费统计接口刷。
	since := time.Now().Add(-24 * time.Hour).Unix()
	sum, err := s.store.Summary(r.Context(), "", since)
	if err != nil {
		// 首页不该因为统计读失败而报错
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false, "window": "24h",
		})
		return
	}
	unique, _ := s.store.UniqueDomains(r.Context(), "", since)
	writeJSON(w, http.StatusOK, map[string]any{
		"available":      true,
		"window":         "24h",
		"total":          sum.Total,
		"blocked":        sum.Blocked,
		"allowed":        sum.Allowed,
		"block_rate":     sum.BlockRate,
		"cache_hit_rate": sum.CacheRate,
		"unique_domains": unique,
	})
}

// handlePublicConfig 给登录/注册页用的公开配置。
//
// 注册页需要知道「要不要邀请码」「要不要邮箱验证」「能不能发信」，
// 否则只能等用户填完提交才报错 —— 而那时候他可能已经等了半分钟收信。
// 只暴露布尔与站点名，不含任何密钥或内部设置。
func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"allow_register":       set.AllowRegister,
		"require_invite":       set.InviteCode != "",
		"require_email_verify": set.EmailVerifyRegister,
		"email_available":      set.SMTPEnabled,
		"domain":               s.domain(r),
		// 注册页用来说明「填邀请码能拿什么」，以及是否展示邀请入口
		"allow_user_invite":  set.AllowUserInvite,
		"invite_reward_days": set.MemberRewardDays,
		"signup_member_days": set.SignupMemberDays,
		"allow_email_change": set.AllowEmailChange,
		// 允许的邮箱后缀：注册页据此把后缀做成下拉，只能选不能填。
		// 后台没配就返回空数组，注册页退化成自由输入。
		"email_domains": splitDomainList(set.EmailDomainWhitelist),
		// 极验验证配置：登录/注册/找回密码页据此决定要不要初始化验证组件。
		// 并进这个接口而不是单开一个，省掉首屏一次额外往返。
		// 只暴露 captcha_id，私钥永不出后端。
		"captcha": s.captchaPublicConfig(set),
		// 用户协议。登录/注册页要能**展示全文**（注册时弹窗、登录时勾选框旁的
		// 链接），所以正文整段下发 —— 单开一个 /public/terms 只会让首屏多一次
		// 往返，而这两个页面本来就必然要拿这份配置。
		// terms_enabled=false 时前端完全不渲染勾选框，老站点行为不变。
		"terms_enabled": set.TermsEnabled,
		"terms_title":   set.TermsTitle,
		"terms_version": set.TermsVersion,
		"terms_content": set.TermsContent,
	})
}

// splitDomainList 把逗号分隔的后缀列表拆成数组给前端用。
//
// 和 normalizeDomainList 的区别：那个用于写库（规范化成字符串），
// 这个只用于下发，不改变存储形态。
func splitDomainList(raw string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	}) {
		d := strings.ToLower(strings.TrimSpace(f))
		d = strings.TrimPrefix(d, "@")
		d = strings.TrimPrefix(d, ".")
		if d == "" || !strings.Contains(d, ".") {
			continue
		}
		out = append(out, d)
	}
	return out
}
