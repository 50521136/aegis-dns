package api

import (
	"net/http"
	"time"

	"github.com/50521136/aegis-dns/server/internal/store"
)

func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	since := timeRange(r.URL.Query().Get("range"), time.Now())

	sum, err := s.store.Summary(r.Context(), u.ID, since.Unix())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	unique, err := s.store.UniqueDomains(r.Context(), u.ID, since.Unix())
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
	u := currentUser(r)
	q := r.URL.Query()
	since := timeRange(q.Get("range"), time.Now())

	// 默认粒度跟着区间走：24h 用 1h 桶，7d 用 6h，30d+ 用 1d。
	interval := intervalSeconds(q.Get("interval"), defaultIntervalFor(q.Get("range")))

	points, err := s.store.Timeseries(r.Context(), u.ID, since.Unix(), interval)
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
	u := currentUser(r)
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

	list, err := s.store.TopDomains(r.Context(), u.ID, since.Unix(), limit, blockedOnly)
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
	})
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		items = append(items, map[string]any{
			"id":         l.ID,
			"ts":         l.TS,
			"time":       time.Unix(l.TS, 0).UTC().Format(time.RFC3339),
			"domain":     l.Domain,
			"qtype":      l.QType,
			"rcode":      l.RCode,
			"action":     l.Action,
			"client":     l.Client,
			"latency_ms": l.LatencyMS,
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
	limit := atoiDefault(q.Get("limit"), 20)

	rows, err := s.store.UserTraffic(r.Context(), since.Unix(), limit)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		items = append(items, map[string]any{
			"user_id":    it.UserID,
			"username":   it.Username,
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
			"id":         l.ID,
			"user_id":    l.UserID,
			"username":   l.Username,
			"ts":         l.TS,
			"time":       time.Unix(l.TS, 0).UTC().Format(time.RFC3339),
			"domain":     l.Domain,
			"qtype":      l.QType,
			"rcode":      l.RCode,
			"action":     l.Action,
			"client":     l.Client,
			"latency_ms": l.LatencyMS,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}
