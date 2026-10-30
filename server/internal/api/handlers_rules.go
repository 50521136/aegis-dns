package api

import (
	"context"
	"net/http"
	"strings"
	"time"

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

// ruleJSON 是规则的对外表示。抽出来避免每个 handler 各写一份字段清单 ——
// 之前漏过 is_global，前端因此分不清一条规则属于哪一层。
func ruleJSON(rule *model.Rule) map[string]any {
	return map[string]any{
		"id":         rule.ID,
		"kind":       rule.Kind,
		"pattern":    rule.Pattern,
		"value":      rule.Value,
		"qtypes":     orEmptySlice(rule.QTypes),
		"enabled":    rule.Enabled,
		"is_global":  rule.IsGlobal,
		"created_at": rfc3339OrEmpty(rule.CreatedAt),
	}
}

func orEmptySlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// ruleQuota 返回指定 scope 下的规则配额上限，0 表示不限制。
//
// 全局层不设上限：全局规则是管理员自己维护的一份，条数由他负责，
// 再套一层配额只会让「加不进去却不知道为什么」变成日常问题。
// 个人层按会员档位算，档位→配额的换算统一走 model.QuotaFor。
func (s *Server) ruleQuota(ctx context.Context, uid string, global bool) int {
	if global {
		return 0
	}
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return 0 // 读不到设置就不拦，避免因为一次读库失败把用户锁死
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil || u == nil {
		return 0
	}
	tiers, _ := s.store.TierMap(ctx)
	return model.QuotaForUser(u, set, tiers, time.Now()).MaxRules
}

// checkRuleQuota 在新增规则前检查配额。
//
// 配额是硬上限：个人层与全局层分开计数，管理员自己的个人规则不会挤占全局额度。
// adding 是本批要新增的条数（导入时可能一次很多条）。
func (s *Server) checkRuleQuota(ctx context.Context, uid string, global bool, adding int) error {
	limit := s.ruleQuota(ctx, uid, global)
	if limit <= 0 {
		return nil
	}
	cur, err := s.store.CountRules(ctx, uid, global)
	if err != nil {
		return err
	}
	if cur+adding > limit {
		scope := "个人"
		if global {
			scope = "全局"
		}
		return errQuota(scope + "规则已达上限 " + itoa(limit) + " 条（当前 " + itoa(cur) +
			"，本次需新增 " + itoa(adding) + "）。请先删除部分规则，或让管理员调整配额。")
	}
	return nil
}

// touched 触发快照重建。
//
// 个人层改动只需要重建该用户；**全局层改动必须全量重建** —— 全局规则对所有
// 开启全局的用户生效，只把创建者标记为变更会让其他人的规则集停留在旧版本。
//
// coord 为 nil 时直接返回 nil（不 panic）：
// apid 在某些只读部署/单测装配里不创建协调器，此时规则照样可以写库，
// 只是没有快照可重建。让 touched 在这里崩掉会把「写完规则」的整个请求
// 变成 500，而规则其实已经落库了 —— 用户重试会撞唯一约束，问题被放大。
func (s *Server) touched(ctx context.Context, uid string, global bool) error {
	if s.coord == nil {
		return nil
	}
	if global {
		// 全局层变更走异步。
		//
		// 全局规则对所有开启全局的用户生效，所以这里**必然是全量重建**
		// （见上面的注释），而全量重建线上要 4.3~5.3 秒。同步等它的后果
		// 是管理员逐条保存 N 条规则就要等 N × 5 秒，且每次都触发一次
		// 「全局层指纹变化 → 全站缓存 Flush」——2026-10-29 17:47 线上连续
		// 9 次整体清空、单次丢 28594 条，就是这条路径造成的。
		//
		// 改成异步后，连续改动在 debounce 窗口内并成一批：只重建一次、
		// 只清一次缓存。返回值的语义从「已生效」变成「已接受」，这个代价
		// 只在管理员后台可接受 —— 规则确实已经落库，只是快照晚几百毫秒。
		s.coord.TouchedAsync()
		return nil
	}
	_, err := s.coord.Changed(ctx, uid)
	return err
}

// touchedGlobalAsync 用于**后台批量**的全局规则写入。
//
// 与 touched(global=true) 的唯一区别是不等重建完成。为什么全局规则需要
// 这个：全局层一变，所有租户的缓存都得清（缓存键里没有规则版本号，见
// loader.go 的说明），而一次全量重建线上要 4.3~5.3 秒。管理员逐条保存时
// 前端是串行请求，等于每改一条就「等 5 秒 + 清一次全站缓存」。
//
// 用 async 之后：连续改动在 debounce 窗口内并成一批，只重建一次、只清
// 一次缓存。代价是接口不能再保证「返回即生效」—— 所以只在管理员后台的
// 单条编辑／批量操作入口使用，用户侧的改动仍然走同步的 touched。
func (s *Server) touchedGlobalAsync() {
	if s.coord == nil {
		return
	}
	s.coord.TouchedAsync()
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	s.listRules(w, r, false)
}

func (s *Server) handleAdminListRules(w http.ResponseWriter, r *http.Request) {
	s.listRules(w, r, true)
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request, global bool) {
	u := currentUser(r)
	page, size, offset := pagination(r, 20)

	kind := r.URL.Query().Get("kind")
	q := r.URL.Query().Get("q")
	enabled := parseBoolParam(r.URL.Query().Get("enabled"))

	list, total, err := s.store.ListRules(r.Context(), u.ID, global, kind, q, enabled, size, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, rule := range list {
		items = append(items, ruleJSON(rule))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": size,
		"limit": s.ruleQuota(r.Context(), currentUser(r).ID, global),
	})
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	s.createRule(w, r, false)
}

func (s *Server) handleAdminCreateRule(w http.ResponseWriter, r *http.Request) {
	s.createRule(w, r, true)
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request, global bool) {
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
		UserID:   u.ID,
		Kind:     req.Kind,
		Pattern:  strings.TrimSpace(req.Pattern),
		Value:    strings.TrimSpace(req.Value),
		QTypes:   req.QTypes,
		Enabled:  true,
		IsGlobal: global,
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	// 先规范化再校验：把 "@@||x^" / "||x^" 统一成 "x"，并挡掉
	// kind 与 @@ 前缀矛盾的输入（那种输入能悄悄入库，然后按 kind 生效，
	// 和写规则的人的意图正好相反）。
	if err := rules.NormalizeRule(rule); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRule, err.Error(),
			map[string]any{"input": req.Pattern})
		return
	}
	if err := validateRule(rule); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRule, err.Error(), map[string]any{"input": req.Pattern})
		return
	}
	if !global {
		if err := s.checkUserFeature(r.Context(), u, "rules"); err != nil {
			writeFeatureError(w, err)
			return
		}
	}
	if err := s.checkRuleQuota(r.Context(), u.ID, global, 1); err != nil {
		writeQuotaError(w, err)
		return
	}

	if err := s.store.CreateRule(r.Context(), rule); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 只有启用中的规则才需要重建快照；停用的规则不影响解析。
	if rule.Enabled {
		if err := s.touched(r.Context(), u.ID, global); err != nil {
			writeStoreError(w, err, "")
			return
		}
	}
	writeJSON(w, http.StatusCreated, ruleJSON(rule))
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	s.updateRule(w, r, false)
}

func (s *Server) handleAdminUpdateRule(w http.ResponseWriter, r *http.Request) {
	s.updateRule(w, r, true)
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request, global bool) {
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
	// 作用域必须与路由一致：拿个人路由去改一条全局规则应当 404，
	// 否则普通用户就能通过 /rules/{id} 篡改管理员的全局规则。
	if existing.IsGlobal != global {
		writeError(w, http.StatusNotFound, CodeNotFound, "规则不存在", nil)
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
	// 更新路径同样要规范化：否则「新建时规范化、修改时原样存」会让
	// 同一张表里再次出现多种写法，正是这次要消掉的冗余。
	if err := rules.NormalizeRule(existing); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRule, err.Error(), nil)
		return
	}
	if err := validateRule(existing); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRule, err.Error(), nil)
		return
	}
	if err := s.store.UpdateRule(r.Context(), existing); err != nil {
		writeStoreError(w, err, "规则不存在")
		return
	}
	if err := s.touched(r.Context(), u.ID, global); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, ruleJSON(existing))
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	s.deleteRule(w, r, false)
}

func (s *Server) handleAdminDeleteRule(w http.ResponseWriter, r *http.Request) {
	s.deleteRule(w, r, true)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request, global bool) {
	u := currentUser(r)
	rid := chi.URLParam(r, "id")

	// 先取一次确认作用域，避免用个人路由删掉全局规则。
	existing, err := s.store.GetRule(r.Context(), u.ID, rid)
	if err != nil {
		writeStoreError(w, err, "规则不存在")
		return
	}
	if existing.IsGlobal != global {
		writeError(w, http.StatusNotFound, CodeNotFound, "规则不存在", nil)
		return
	}
	if err := s.store.DeleteRule(r.Context(), u.ID, rid); err != nil {
		writeStoreError(w, err, "规则不存在")
		return
	}
	if err := s.touched(r.Context(), u.ID, global); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBulkDeleteRules(w http.ResponseWriter, r *http.Request) {
	s.bulkDeleteRules(w, r, false)
}

func (s *Server) handleAdminBulkDeleteRules(w http.ResponseWriter, r *http.Request) {
	s.bulkDeleteRules(w, r, true)
}

func (s *Server) bulkDeleteRules(w http.ResponseWriter, r *http.Request, global bool) {
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

	// 只删本作用域的：逐条确认归属，把跨作用域的 ID 过滤掉而不是整体报错。
	allowed := make([]string, 0, len(req.IDs))
	for _, rid := range req.IDs {
		rule, err := s.store.GetRule(r.Context(), u.ID, rid)
		if err != nil || rule.IsGlobal != global {
			continue
		}
		allowed = append(allowed, rid)
	}
	if len(allowed) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": 0})
		return
	}

	n, err := s.store.DeleteRules(r.Context(), u.ID, allowed)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if n > 0 {
		if err := s.touched(r.Context(), u.ID, global); err != nil {
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
		"valid": true,
		"normalized": map[string]any{
			"kind":    rule.Kind,
			"pattern": rule.Pattern,
			"value":   rule.Value,
			"qtypes":  orEmptySlice(rule.QTypes),
		},
	})
}

// canonicalPattern 把解析结果收敛成「纯域名 + 可选通配」。
//
// 以前这里会拼成 AdGuard 形式（allow 拼 @@||x^、其余拼 ||x^），于是规则表里
// 同一件事有三种写法并存，而匹配时 splitPattern 又会把前缀无条件剥掉 ——
// 前缀对结果零影响，纯冗余。更糟的是它能和 kind 打架：kind=block 配
// "@@||x^" 时，@@ 被剥掉、kind 生效，实际变成「拦截 x」，与字面意思相反
// 且不报错。
//
// 现在 kind 是语义的唯一来源，pattern 只存域名。订阅层一直是这个形态
// （283 万条里 0 条带前缀），规则层跟上。
func canonicalPattern(p *rules.Parsed) string {
	domain := p.Domain
	if p.Wildcard {
		return "*." + domain
	}
	return domain
}

func (s *Server) handleImportRules(w http.ResponseWriter, r *http.Request) {
	s.importRulesScoped(w, r, false)
}

func (s *Server) handleAdminImportRules(w http.ResponseWriter, r *http.Request) {
	s.importRulesScoped(w, r, true)
}

// handleImportRules 批量导入规则文本（文档 9.4 POST /rules/import）。
func (s *Server) importRulesScoped(w http.ResponseWriter, r *http.Request, global bool) {
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
	// 配额按本批实际要新增的条数检查，导入路径同样受限 ——
	// 否则「逐条新建有配额、批量导入没有」就是一个明显的绕过口子。
	if !global {
		if err := s.checkUserFeature(r.Context(), u, "rules"); err != nil {
			writeFeatureError(w, err)
			return
		}
	}
	if err := s.checkRuleQuota(r.Context(), u.ID, global, len(parsed)); err != nil {
		writeQuotaError(w, err)
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
			Pattern:  canonicalPattern(p),
			Value:    p.Value,
			QTypes:   p.QTypes,
			Enabled:  enabled,
			IsGlobal: global,
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
		if err := s.touched(r.Context(), u.ID, global); err != nil {
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
