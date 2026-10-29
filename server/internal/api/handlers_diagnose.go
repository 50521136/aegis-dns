package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/diagnose"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// 这个文件把 diagnose 包接到 HTTP 上。
//
// 三个端点：
//   POST /admin/feedback/{id}/diagnose  触发（异步，立即返回 202）
//   POST /admin/feedback/{id}/apply     采纳建议（真正写规则）
//   GET  /admin/ai/settings             读模型配置
//   PUT  /admin/ai/settings             改模型配置
//
// 「诊断」与「采纳」严格分开：诊断只写建议，采纳才动规则。
// 这不是过度保守 —— 一条错误的全局放行就能让整站拦截失效。

// storeSource 把 *store.Store 适配成 diagnose.Source。
type storeSource struct {
	srv *Server
}

func (ss storeSource) UserRules(ctx context.Context, userID string) ([]model.Rule, error) {
	return ss.srv.store.UserRulesForDiagnose(ctx, userID)
}

func (ss storeSource) GlobalRules(ctx context.Context) ([]model.Rule, error) {
	return ss.srv.store.GlobalRulesForDiagnose(ctx)
}

func (ss storeSource) UserSubDomains(ctx context.Context, userID string) (block, allow []string, err error) {
	return ss.srv.store.SubEntriesByUser(ctx, userID)
}

func (ss storeSource) GlobalSubDomains(ctx context.Context) (block, allow []string, err error) {
	return ss.srv.store.AllGlobalSubEntries(ctx)
}

func (ss storeSource) SubSourcesOf(ctx context.Context, domains []string) (map[string][]diagnose.SubSource, error) {
	rows, err := ss.srv.store.SubSourcesOf(ctx, domains)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]diagnose.SubSource, len(rows))
	for d, list := range rows {
		for _, r := range list {
			out[d] = append(out[d], diagnose.SubSource{
				SubID: r.SubID, Name: r.Name, URL: r.URL, Kind: r.Kind, Global: r.Global,
			})
		}
	}
	return out, nil
}

func (ss storeSource) RecentQueries(ctx context.Context, userID, domain string, limit int) ([]diagnose.QuerySample, int, error) {
	since := nowUnix() - diagnoseQueryWindowSec
	rows, blocked, err := ss.srv.store.RecentQueriesOfDomain(ctx, userID, domain, since, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]diagnose.QuerySample, 0, len(rows))
	for _, r := range rows {
		out = append(out, diagnose.QuerySample{
			Action: r.Action, RCode: r.RCode, CacheHit: r.CacheHit,
			LatencyMS: r.LatencyMS, Proto: r.Proto, UnixTime: r.TS,
		})
	}
	return out, blocked, nil
}

func (ss storeSource) UserEnabled(ctx context.Context, userID string) (bool, error) {
	u, err := ss.srv.store.GetUser(ctx, userID)
	if err != nil {
		return false, err
	}
	return u.Enabled, nil
}

// handleAdminDiagnoseFeedback 触发一次 AI 诊断。
//
// 异步执行：模型响应可能要几秒到几十秒，阻塞 HTTP 会让面板以为卡死。
// 立即置 ai_status=pending 返回 202，前端轮询列表看结果。
func (s *Server) handleAdminDiagnoseFeedback(w http.ResponseWriter, r *http.Request) {
	if s.diagnoser == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal,
			"AI 诊断未启用（请先在「AI 诊断」里配置模型）", nil)
		return
	}
	fid := chi.URLParam(r, "id")
	fb, err := s.store.GetFeedback(r.Context(), fid)
	if err != nil {
		writeStoreError(w, err, "反馈不存在")
		return
	}
	if err := s.store.ResetAIDiagnosis(r.Context(), fid); err != nil {
		writeStoreError(w, err, "")
		return
	}
	fb.AIStatus = model.AIDiagPending
	s.diagnoser.Enqueue(*fb)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id": fid, "ai_status": model.AIDiagPending,
	})
}

// handleAdminApplyDiagnosis 采纳 AI 建议：把建议的域名写进放行规则。
//
// 这是唯一会真正改动规则的端点，因此这里要重新跑一遍闸门 ——
// 不能信任库里存的 ai_domains（它可能是旧版本闸门的产物，
// 也可能被人手工改过）。
func (s *Server) handleAdminApplyDiagnosis(w http.ResponseWriter, r *http.Request) {
	fid := chi.URLParam(r, "id")
	fb, err := s.store.GetFeedback(r.Context(), fid)
	if err != nil {
		writeStoreError(w, err, "反馈不存在")
		return
	}
	if fb.AIAction == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "这条反馈还没有诊断结果", nil)
		return
	}

	domains := splitCommaList(fb.AIDomains)
	if len(domains) == 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"诊断建议里没有可执行的域名（当前动作为 "+fb.AIAction+"）", nil)
		return
	}

	// 重新校验每个域名：确认它确实是一个完整域名，且不是公共后缀。
	// 复用 diagnose 包的闸门规则（通过公开函数），保证与诊断时口径一致。
	valid := make([]string, 0, len(domains))
	for _, d := range domains {
		if !diagnose.IsSafeAllowTarget(d) {
			s.log.Warn("拒绝写入诊断建议的域名", "feedback", fid, "domain", d)
			continue
		}
		valid = append(valid, d)
	}
	if len(valid) == 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"建议的域名都未通过安全校验，请人工处理", nil)
		return
	}

	created := 0
	for _, d := range valid {
		rule := &model.Rule{
			UserID:   fb.UserID,
			Kind:     "allow",
			Pattern:  "||" + d + "^",
			Enabled:  true,
			IsGlobal: fb.AIAction == model.AIActGlobalAllow,
		}
		if err := s.store.CreateRule(r.Context(), rule); err != nil {
			s.log.Warn("写入诊断建议的规则失败", "feedback", fid, "domain", d, "err", err)
			continue
		}
		created++
	}
	if created == 0 {
		writeError(w, http.StatusInternalServerError, CodeInternal, "没有写入任何规则", nil)
		return
	}

	// 规则变了要重建快照，否则 dnsd 不会生效。
	if err := s.touched(r.Context(), fb.UserID, fb.AIAction == model.AIActGlobalAllow); err != nil {
		s.log.Warn("诊断采纳后重建快照失败", "feedback", fid, "err", err)
	}

	// 采纳即视为处理完毕：置 replied 并把 AI 的建议回复发给用户。
	reply := fb.AIReply
	if reply == "" {
		reply = "已处理，请重连网络后重试。"
	}
	if err := s.store.UpdateFeedback(r.Context(), fid, model.FeedbackReplied, reply); err != nil {
		s.log.Warn("更新反馈状态失败", "feedback", fid, "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id": fid, "applied": valid, "rule_count": created,
		"global": fb.AIAction == model.AIActGlobalAllow,
	})
}

// handleAdminGetAISettings 读模型配置（Key 不回显）。
func (s *Server) handleAdminGetAISettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ai_enabled":      set.AIEnabled,
		"ai_base_url":     set.AIBaseURL,
		"ai_model":        set.AIModel,
		"ai_timeout_sec":  set.AITimeoutSec,
		"ai_max_per_hour": set.AIMaxPerHour,
		"ai_trigger_mode": set.AITriggerMode,
		"ai_auto_handle":  set.AIAutoHandle,
		"ai_key_set":      set.AIKeyEncrypted != "",
	})
}

// handleAdminUpdateAISettings 改模型配置。
//
// Key 传空字符串表示「保持不变」，传 null/不传也表示不变 ——
// 前端不会拿到明文 Key，所以「留空」不能理解成「清空」。
// 要清空必须显式传 clear_key=true。
func (s *Server) handleAdminUpdateAISettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled     *bool   `json:"ai_enabled"`
		BaseURL     *string `json:"ai_base_url"`
		Model       *string `json:"ai_model"`
		APIKey      *string `json:"ai_key"`
		TimeoutSec  *int    `json:"ai_timeout_sec"`
		MaxPerHour  *int    `json:"ai_max_per_hour"`
		TriggerMode *string `json:"ai_trigger_mode"`
		AutoHandle  *bool   `json:"ai_auto_handle"`
		ClearKey    bool    `json:"clear_key"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if req.Enabled != nil {
		set.AIEnabled = *req.Enabled
	}
	if req.BaseURL != nil {
		set.AIBaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Model != nil {
		set.AIModel = strings.TrimSpace(*req.Model)
	}
	if req.TimeoutSec != nil && *req.TimeoutSec > 0 {
		set.AITimeoutSec = *req.TimeoutSec
	}
	if req.MaxPerHour != nil && *req.MaxPerHour >= 0 {
		set.AIMaxPerHour = *req.MaxPerHour
	}
	if req.TriggerMode != nil {
		switch *req.TriggerMode {
		case "off", "on_submit", "manual":
			set.AITriggerMode = *req.TriggerMode
		default:
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed,
				"ai_trigger_mode 只能是 off / on_submit / manual", nil)
			return
		}
	}
	// AI 自动处理开关。
	//
	// 打开它会真实改写放行规则并自动结单，所以这里额外记一条日志：
	// 事后要能查到「是谁、什么时候把这个开关打开的」。
	if req.AutoHandle != nil && *req.AutoHandle != set.AIAutoHandle {
		set.AIAutoHandle = *req.AutoHandle
		s.log.Info("AI 自动处理开关已变更", "auto_handle", set.AIAutoHandle)
	}
	if req.ClearKey {
		set.AIKeyEncrypted = ""
	} else if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		enc, encErr := s.encryptSecret(strings.TrimSpace(*req.APIKey))
		if encErr != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal,
				"保存密钥失败: "+encErr.Error(), nil)
			return
		}
		set.AIKeyEncrypted = enc
	}
	if err := s.store.SaveSettingsAndInvalidate(r.Context(), set); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 配置变了要重建 diagnoser（endpoint/key/model 都可能改了）。
	s.rebuildDiagnoser(set)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminTestAI 用一条假数据跑一次完整链路，验证模型连通性。
//
// 用假证据而不是真实反馈：测试不该依赖库里恰好有未处理反馈，
// 也不该因为「诊断成功」而被误认为「建议可用」。
func (s *Server) handleAdminTestAI(w http.ResponseWriter, r *http.Request) {
	if s.diagnoser == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal,
			"AI 诊断未启用（请先保存模型配置并打开开关）", nil)
		return
	}
	cfg := s.currentLLMConfig()
	if cfg.BaseURL == "" || cfg.Model == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "模型地址或模型名未填写", nil)
		return
	}
	ev := &diagnose.Evidence{
		FeedbackID: "test",
		Kind:       model.FeedbackWrongBlock,
		Message:    "连通性测试",
		Domains: []diagnose.DomainEvidence{
			{
				Domain: "test.example.com", QType: 1,
				Action: "block", Layer: "sub_block",
				LayerLabel: "用户订阅黑名单",
				SubSources: []diagnose.SubSource{
					{SubID: "sub_test", Name: "示例列表", Kind: "block"},
				},
			},
		},
		UserRuleCount: 1, SubBlockCount: 100, UserEnabled: true,
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.Timeout)+5*time.Second)
	defer cancel()
	start := time.Now()
	sug, err := diagnose.Diagnose(ctx, cfg, ev)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "model": cfg.Model, "base_url": cfg.BaseURL,
			"elapsed_ms": elapsed, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "model": cfg.Model, "base_url": cfg.BaseURL,
		"elapsed_ms": elapsed,
		"sample":     sug,
	})
}

// currentLLMConfig 从当前设置拼出模型配置。
func (s *Server) currentLLMConfig() diagnose.LLMConfig {
	set, err := s.store.GetSettings(context.Background())
	if err != nil {
		return diagnose.LLMConfig{}
	}
	return diagnose.LLMConfig{
		BaseURL:    set.AIBaseURL,
		APIKey:     s.decryptSecret(set.AIKeyEncrypted),
		Model:      set.AIModel,
		Timeout:    time.Duration(set.AITimeoutSec) * time.Second,
		MaxPerHour: set.AIMaxPerHour,
	}
}

// validateAllowTarget 复用的安全校验（供 diagnose 包导出同名能力的账号层包装）。
var _ = rules.SplitPattern

// splitCommaList 拆分逗号分隔的域名列表。
func splitCommaList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// errDiagnoseDisabled 用于统一判断。
var errDiagnoseDisabled = errors.New("AI 诊断未启用")
