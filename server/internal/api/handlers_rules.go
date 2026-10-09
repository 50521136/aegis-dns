package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// validateRule 校验一条规则是否可入库。
//
// 复用规则引擎的解析逻辑（而不是在 API 层另写一套），
// 保证「接口接受了」等价于「引擎能构建出来」。
func validateRule(r *model.Rule) error {
	switch r.Kind {
	case model.KindBlock, model.KindAllow, model.KindRewriteA, model.KindRewriteAAAA, model.KindRewriteCNAME:
	default:
		return errValidation("未知规则类型 " + r.Kind + "，可选值：block / allow / rewrite_a / rewrite_aaaa / rewrite_cname")
	}
	if strings.TrimSpace(r.Pattern) == "" {
		return errValidation("规则内容不能为空")
	}
	if err := rules.Validate(r.Kind, r.Pattern, r.Value, r.QTypes); err != nil {
		return errValidation(err.Error())
	}
	return nil
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	page, size, offset := pagination(r, 20)

	kind := r.URL.Query().Get("kind")
	q := r.URL.Query().Get("q")
	enabled := parseBoolParam(r.URL.Query().Get("enabled"))

	list, total, err := s.store.ListRules(r.Context(), u.ID, kind, q, enabled, size, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, rule := range list {
		items = append(items, map[string]any{
			"id":         rule.ID,
			"kind":       rule.Kind,
			"pattern":    rule.Pattern,
			"value":      rule.Value,
			"qtypes":     orEmptySlice(rule.QTypes),
			"enabled":    rule.Enabled,
			"created_at": rfc3339OrEmpty(rule.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": size,
	})
}

func orEmptySlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string   `json:"kind"`
		Pattern string   `json:"pattern"`
		Value   string   `json:"value"`
		QTypes  []string `json:"qtypes"`
		Enabled *bool    `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := currentUser(r)

	rule := &model.Rule{
		UserID:  u.ID,
		Kind:    req.Kind,
		Pattern: strings.TrimSpace(req.Pattern),
		Value:   strings.TrimSpace(req.Value),
		QTypes:  req.QTypes,
		Enabled: true,
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if err := validateRule(rule); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRule, err.Error(), map[string]any{"input": req.Pattern})
		return
	}

	if err := s.store.CreateRule(r.Context(), rule); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 只有启用中的规则才需要重建快照；停用的规则不影响解析。
	if rule.Enabled {
		if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
			writeStoreError(w, err, "")
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         rule.ID,
		"kind":       rule.Kind,
		"pattern":    rule.Pattern,
		"value":      rule.Value,
		"qtypes":     orEmptySlice(rule.QTypes),
		"enabled":    rule.Enabled,
		"created_at": rfc3339OrEmpty(rule.CreatedAt),
	})
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string   `json:"kind"`
		Pattern string   `json:"pattern"`
		Value   string   `json:"value"`
		QTypes  []string `json:"qtypes"`
		Enabled *bool    `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := currentUser(r)
	rid := chi.URLParam(r, "id")

	existing, err := s.store.GetRule(r.Context(), u.ID, rid)
	if err != nil {
		writeStoreError(w, err, "规则不存在")
		return
	}

	// 部分更新：未提供的字段保持原值。
	if req.Kind != "" {
		existing.Kind = req.Kind
	}
	if req.Pattern != "" {
		existing.Pattern = strings.TrimSpace(req.Pattern)
	}
	existing.Value = strings.TrimSpace(req.Value)
	if req.QTypes != nil {
		existing.QTypes = req.QTypes
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if err := validateRule(existing); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRule, err.Error(), nil)
		return
	}
	if err := s.store.UpdateRule(r.Context(), existing); err != nil {
		writeStoreError(w, err, "规则不存在")
		return
	}
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": existing.ID, "kind": existing.Kind, "pattern": existing.Pattern,
		"value": existing.Value, "qtypes": orEmptySlice(existing.QTypes), "enabled": existing.Enabled,
	})
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rid := chi.URLParam(r, "id")
	if err := s.store.DeleteRule(r.Context(), u.ID, rid); err != nil {
		writeStoreError(w, err, "规则不存在")
		return
	}
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBulkDeleteRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "ids 不能为空", nil)
		return
	}
	if len(req.IDs) > 5000 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "单次最多删除 5000 条规则", nil)
		return
	}
	u := currentUser(r)
	n, err := s.store.DeleteRules(r.Context(), u.ID, req.IDs)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if n > 0 {
		if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
			writeStoreError(w, err, "")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// handleValidateRule 只做语法校验，不落库（文档 9.4）。
func (s *Server) handleValidateRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string   `json:"kind"`
		Pattern string   `json:"pattern"`
		Value   string   `json:"value"`
		QTypes  []string `json:"qtypes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rule := &model.Rule{Kind: req.Kind, Pattern: req.Pattern, Value: req.Value, QTypes: req.QTypes}
	if err := validateRule(rule); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	// 顺带回显解析出的规范化域名，让用户确认「我写的规则被理解成了什么」。
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":   true,
		"normalized": map[string]any{
			"kind":    rule.Kind,
			"pattern": rule.Pattern,
			"value":   rule.Value,
			"qtypes":  orEmptySlice(rule.QTypes),
		},
	})
}

// canonicalPattern 把解析结果还原成 AdGuard 形式的 pattern。
//
// 统一写法而不是保留原文：同一条规则可能来自 hosts、dnsmasq 或 Adblock 语法，
// 存成一致形式后，列表、去重、导出都能按同一套规则比较。
func canonicalPattern(p *rules.Parsed) string {
	domain := p.Domain
	if p.Wildcard {
		domain = "*." + domain
	}
	switch p.Kind {
	case model.KindAllow:
		return "@@||" + domain + "^"
	default:
		return "||" + domain + "^"
	}
}

// handleImportRules 批量导入规则文本（文档 9.4 POST /rules/import）。
func (s *Server) handleImportRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
		Format  string `json:"format"`
		Enabled *bool  `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "content 不能为空", nil)
		return
	}
	u := currentUser(r)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	parsed, skipped, parseErrs := rules.ParseContent(req.Content)

	// 最多一次导入 5 万条：再大应该走订阅而不是逐条规则。
	if len(parsed) > 50000 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"单次导入上限 50000 条规则，更多内容请使用「订阅」功能", nil)
		return
	}

	out := make([]*model.Rule, 0, len(parsed))
	for _, p := range parsed {
		out = append(out, &model.Rule{
			UserID: u.ID,
			Kind:   p.Kind,
			// 规范化 pattern：解析器已经把 hosts / dnsmasq / 裸域名统一成
			// 域名 + 通配标记，这里还原成 AdGuard 形式，便于用户在列表里
			// 看到一致的写法（也避免把 `0.0.0.0 x` 这种 hosts 行原样存库）。
			Pattern: canonicalPattern(p),
			Value:   p.Value,
			QTypes:  p.QTypes,
			Enabled: enabled,
		})
	}

	created := 0
	if len(out) > 0 {
		n, err := s.store.BulkCreateRules(r.Context(), out)
		if err != nil {
			writeStoreError(w, err, "")
			return
		}
		created = n
		if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
			writeStoreError(w, err, "")
			return
		}
	}

	errs := make([]string, 0, len(parseErrs))
	for _, e := range parseErrs {
		errs = append(errs, e.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created,
		"skipped": skipped,
		"errors":  errs,
	})
}
