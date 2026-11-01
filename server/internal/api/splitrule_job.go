package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/splitrule"
	"github.com/50521136/aegis-dns/server/internal/subsub"
)

// 分流规则的拉取与落库（双表）。
//
// 为什么单独一套而不是复用订阅机制：分流规则是「系统配置」的一部分，
// 不是用户数据。塞进 subscriptions 表会带来三个问题 ——
// 它会出现在用户的订阅列表里、会占用用户的订阅配额、用户误删后
// 整站分流失效。独立字段更清晰，失败也不影响用户订阅。

// ruleKind 区分两张表，决定读哪些配置字段。
type ruleKind int

const (
	// ruleDomestic 是国内白名单：命中走国内上游。
	ruleDomestic ruleKind = iota
	// ruleGfw 是被墙黑名单：白名单未命中时再查，命中走境外上游。
	ruleGfw
)

// fetchRule 拉取并解析一张表，返回域名列表与原始字节数。
func (s *Server) fetchRule(ctx context.Context, cur *model.GlobalSettings, kind ruleKind) ([]string, int, error) {
	var url, preset, defFormat string
	if kind == ruleDomestic {
		url, preset, defFormat = strings.TrimSpace(cur.SplitRuleURL), cur.SplitRulePreset, "adblock"
	} else {
		url, preset, defFormat = strings.TrimSpace(cur.GfwRuleURL), cur.GfwRulePreset, "plain"
	}
	if url == "" {
		return nil, 0, fmt.Errorf("未配置规则地址")
	}

	format := defFormat
	if p, ok := splitrule.PresetByID(preset); ok {
		format = p.Format
	}

	f := subsub.NewFetcher()
	if cur.ProxyEnabled && strings.TrimSpace(cur.ProxyURL) != "" {
		_ = f.SetProxy(cur.ProxyURL)
	}
	res, err := f.Fetch(ctx, url, "", "")
	if err != nil {
		return nil, 0, fmt.Errorf("拉取失败: %w", err)
	}

	domains, perr := splitrule.Parse(res.Content, format)
	if perr != nil {
		return nil, 0, fmt.Errorf("解析失败: %w", perr)
	}
	if len(domains) == 0 {
		return nil, 0, fmt.Errorf("解析结果为空（格式可能不匹配）")
	}
	return domains, len(res.Content), nil
}

// refreshSplitRule 拉取全部分流规则（白名单 + 黑名单）并写入设置。
//
// 两张表独立处理：一张失败不影响另一张。失败时保留旧名单 ——
// 名单空了意味着全部走国内，境外域名会重新变回解析失败。
func (s *Server) refreshSplitRule(ctx context.Context) (int, error) {
	cur, err := s.store.GetSettings(ctx)
	if err != nil {
		return 0, fmt.Errorf("读取设置失败: %w", err)
	}

	total := 0
	var firstErr error

	// 白名单
	if strings.TrimSpace(cur.SplitRuleURL) != "" {
		domains, _, ferr := s.fetchRule(ctx, cur, ruleDomestic)
		if ferr != nil {
			cur.SplitRuleError = truncateStr(ferr.Error(), 300)
			if firstErr == nil {
				firstErr = fmt.Errorf("白名单: %w", ferr)
			}
		} else {
			cur.OverseasDomains = domains
			cur.SplitRuleUpdatedAt = time.Now().Unix()
			cur.SplitRuleCount = len(domains)
			cur.SplitRuleError = ""
			total += len(domains)
		}
	}

	// 黑名单
	if strings.TrimSpace(cur.GfwRuleURL) != "" {
		domains, _, ferr := s.fetchRule(ctx, cur, ruleGfw)
		if ferr != nil {
			cur.GfwRuleError = truncateStr(ferr.Error(), 300)
			if firstErr == nil {
				firstErr = fmt.Errorf("黑名单: %w", ferr)
			}
		} else {
			cur.GfwDomains = domains
			cur.GfwRuleUpdatedAt = time.Now().Unix()
			cur.GfwRuleCount = len(domains)
			cur.GfwRuleError = ""
			total += len(domains)
		}
	}

	if err := s.store.SaveSettingsAndInvalidate(ctx, cur); err != nil {
		return total, fmt.Errorf("保存规则失败: %w", err)
	}
	// 规则变了要让 dnsd 拿到新名单，否则不生效。
	// touchedGlobalAsync 只表示「已接受」，不阻塞等重建完成 ——
	// 重建要 4-5 秒，拉取任务没必要干等。
	if total > 0 {
		s.touchedGlobalAsync()
	}
	if total == 0 && firstErr != nil {
		return 0, firstErr
	}
	return total, nil
}

// StartBackgroundJobs 启动需要随进程常驻的后台任务。
//
// 集中成一个入口而不是散落在各处 go func()：进程退出时只需要管一处，
// 而且测试里可以不调用它，避免后台任务干扰测试。
func (s *Server) StartBackgroundJobs(ctx context.Context) {
	s.startSplitRuleScheduler(ctx)
}

// startSplitRuleScheduler 起一个周期性刷新分流规则的循环。
//
// 用简单 ticker 而不是 cron 表达式：一天一次，不需要精确时刻。
// 启动后先等 30 秒再跑第一次，避开进程启动时的初始化高峰。
func (s *Server) startSplitRuleScheduler(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}

		// 启动时先跑一次：进程可能刚重启，内存里的名单是空的。
		s.maybeRefreshSplitRule(ctx)

		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.maybeRefreshSplitRule(ctx)
			}
		}
	}()
}

// maybeRefreshSplitRule 判断是否到了该刷新的时间再执行。
//
// 用 6 小时 ticker + 内部判断「距上次更新是否超过 20 小时」，
// 而不是直接 24 小时 ticker：进程重启会重置 ticker，
// 高频重启的部署里 24 小时 ticker 可能永远等不到触发。
func (s *Server) maybeRefreshSplitRule(ctx context.Context) {
	cur, err := s.store.GetSettings(ctx)
	if err != nil {
		return
	}
	if !cur.SplitRuleAutoUpdate {
		return
	}
	if strings.TrimSpace(cur.SplitRuleURL) == "" && strings.TrimSpace(cur.GfwRuleURL) == "" {
		return
	}
	const minInterval = 20 * time.Hour
	last := cur.SplitRuleUpdatedAt
	if cur.GfwRuleUpdatedAt > last {
		last = cur.GfwRuleUpdatedAt
	}
	if last > 0 && time.Since(time.Unix(last, 0)) < minInterval {
		return
	}
	_, _ = s.refreshSplitRule(ctx)
}

// truncateStr 按字符截断，避免把多字节字符切成半个。
func truncateStr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
