package store

import (
	"context"
	"fmt"
	"strings"
)

// UpstreamHit 是某个上游在统计区间内的使用情况。
type UpstreamHit struct {
	// Upstream 是上游地址原文（quic://... / tls://... / https://...）。
	// 展示时由 api 层按角色脱敏，不在存储层处理。
	Upstream string `json:"upstream"`
	// Hits 是该上游实际应答的次数（只统计未命中缓存、真的发出上游请求的查询）。
	Hits int64 `json:"hits"`
	// AvgMS 是平均响应时间，只算这个上游自己应答的那些查询。
	AvgMS float64 `json:"avg_latency_ms"`
	// MaxMS 是区间内最慢的一次，用来发现个别上游的抖动。
	MaxMS int64 `json:"max_latency_ms"`
	// Share 是占比，0~1。由调用方按总量算，避免 SQL 里做除法再除零。
	Share float64 `json:"share"`
}

// UpstreamStats 返回按上游聚合的请求次数与响应时间。
//
// 为什么查 query_log 而不是 query_rollup：rollup 是给「总量/拦截率/命中率」
// 这类按天累加的指标用的，粒度是 (user_id, ts)，没有 upstream 维度。
// 要在 rollup 里支持上游统计就得改主键并重建历史数据，代价远大于收益。
//
// 直接扫 query_log 的实测成本：1 小时窗口 3ms、24 小时窗口 2ms
// （走 idx_qlog_ts，且 where 里带了 cache_hit=0 把行数压得很低）。
// 总览页是人工打开的，不是每秒轮询，这个代价可以接受。
//
// 只统计 cache_hit=0 的行：命中缓存时根本没碰上游，把它算进「上游响应时间」
// 会严重低估 —— 线上命中率 34%，混进去会让所有上游看起来都快了三分之一。
func (s *Store) UpstreamStats(ctx context.Context, uid string, since int64, limit int) ([]UpstreamHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	where := []string{
		"ts >= ?",
		"cache_hit = 0",
		"upstream IS NOT NULL",
		"upstream != ''",
	}
	args := []any{since}
	if uid != "" {
		where = append(where, "user_id = ?")
		args = append(args, uid)
	}
	args = append(args, limit)

	rows, err := s.read.QueryContext(ctx, `
		SELECT upstream,
		       COUNT(*)                    AS hits,
		       COALESCE(AVG(latency_ms),0) AS avg_ms,
		       COALESCE(MAX(latency_ms),0) AS max_ms
		FROM query_log
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY upstream
		ORDER BY hits DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("统计上游使用情况失败: %w", err)
	}
	defer rows.Close()

	var out []UpstreamHit
	var total float64
	for rows.Next() {
		var h UpstreamHit
		if err := rows.Scan(&h.Upstream, &h.Hits, &h.AvgMS, &h.MaxMS); err != nil {
			return nil, fmt.Errorf("读取上游统计失败: %w", err)
		}
		total += float64(h.Hits)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历上游统计失败: %w", err)
	}

	// 占比在 Go 侧算：SQL 里做除法要处理除零，还要多一次窗口函数，
	// 而这里最多几十行，一次循环就够。
	if total > 0 {
		for i := range out {
			out[i].Share = float64(out[i].Hits) / total
		}
	}
	return out, nil
}
