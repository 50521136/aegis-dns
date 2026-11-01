package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/splitrule"
	"github.com/50521136/aegis-dns/server/internal/subsub"
)

// handleRefreshSplitRule 立即拉取一次分流规则。
//
// 为什么要手动触发：自动更新是每天一次，但管理员改完地址或换了预设后
// 想立刻看到效果。等一天不现实。
func (s *Server) handleRefreshSplitRule(w http.ResponseWriter, r *http.Request) {
	n, err := s.refreshSplitRule(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, CodeInvalidRequest, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"count":   n,
		"message": "已更新 " + strconv.Itoa(n) + " 条分流规则，快照重建中",
	})
}

// handlePreviewSplitRule 试解析一个规则地址，返回前若干条结果。
//
// 用途：管理员填了自定义地址后，先确认「能不能拉到、解析出多少条、
// 内容对不对」再保存。直接保存然后发现是空的，排查成本高得多。
//
// 不落库、不触发重建，纯只读。
func (s *Server) handlePreviewSplitRule(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	url := strings.TrimSpace(q.Get("url"))
	preset := strings.TrimSpace(q.Get("preset"))
	if url == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "url 不能为空", nil)
		return
	}

	format := "adblock"
	if p, ok := splitrule.PresetByID(preset); ok {
		format = p.Format
		if url == "" {
			url = p.URL
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	f := subsub.NewFetcher()
	res, err := f.Fetch(ctx, url, "", "")
	if err != nil {
		writeError(w, http.StatusBadGateway, CodeInvalidRequest,
			"拉取失败: "+err.Error(), nil)
		return
	}

	domains, perr := splitrule.Parse(res.Content, format)
	if perr != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"解析失败: "+perr.Error(), nil)
		return
	}

	// 只回前 50 条做抽样展示：11 万条全回会把响应撑到几 MB，
	// 而管理员要判断的只是「内容对不对」。
	sample := domains
	if len(sample) > 50 {
		sample = sample[:50]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"format":      format,
		"total":       len(domains),
		"inverse":     splitrule.IsInverseFormat(format),
		"size_bytes":  len(res.Content),
		"sample":      sample,
		"truncated":   len(domains) > len(sample),
		"fetched_url": url,
	})
}
