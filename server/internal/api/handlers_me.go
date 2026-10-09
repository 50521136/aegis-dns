package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/id"
	"github.com/50521136/aegis-dns/server/internal/model"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	writeJSON(w, http.StatusOK, toUserJSON(u.ID, u.Username, u.Email, u.Role, u.ClientID, u.Enabled, u.CreatedAt))
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := currentUser(r)

	if err := auth.CheckPassword(u.PasswordHash, req.OldPassword); err != nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "原密码不正确", nil)
		return
	}
	if err := auth.ValidatePasswordStrength(req.NewPassword); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if req.OldPassword == req.NewPassword {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "新密码不能与原密码相同", nil)
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), u.ID, hash); err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	// 改密码后撤销所有 refresh token：否则旧令牌还能换出新 access token，
	// 改密码就形同虚设。
	if err := s.store.RevokeAllUserTokens(r.Context(), u.ID); err != nil {
		s.log.Warn("撤销用户令牌失败", "user", u.ID, "err", err)
	}
	s.store.LogAudit(r.Context(), u.ID, "password_changed", "", clientIP(r, s.cfg.API.TrustedProxies))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDNSConfig(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	writeJSON(w, http.StatusOK, buildDNSConfig(s.domain(r), u.ClientID, s.fingerprint(), s.dotPort()))
}

// exportPayload 是配置导出的结构（文档 9.3 /me/export）。
type exportPayload struct {
	ExportedAt    string               `json:"exported_at"`
	Version       string               `json:"version"`
	ClientID      string               `json:"client_id"`
	Rules         []model.Rule         `json:"rules"`
	Subscriptions []model.Subscription `json:"subscriptions"`
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)

	rules, err := s.store.ListAllRules(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	subs, err := s.store.ListSubs(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 导出时不带订阅条目（那是几十万行），只带订阅定义，
	// 导入后重新拉取即可 —— 这样导出文件保持在几十 KB 量级。
	writeJSON(w, http.StatusOK, exportPayload{
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Version:       "1",
		ClientID:      u.ClientID,
		Rules:         rules,
		Subscriptions: derefSubs(subs),
	})
}

func derefSubs(in []*model.Subscription) []model.Subscription {
	out := make([]model.Subscription, 0, len(in))
	for _, sub := range in {
		out = append(out, *sub)
	}
	return out
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules         []model.Rule         `json:"rules"`
		Subscriptions []model.Subscription `json:"subscriptions"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := currentUser(r)

	createdRules, errs := s.importRules(r, u.ID, req.Rules)
	createdSubs := 0
	for _, sub := range req.Subscriptions {
		sub.UserID = u.ID
		sub.ID = ""
		if err := validateSub(&sub); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if err := s.store.CreateSub(r.Context(), &sub); err != nil {
			errs = append(errs, "订阅 "+sub.Name+" 导入失败: "+err.Error())
			continue
		}
		createdSubs++
	}

	if createdRules > 0 || createdSubs > 0 {
		if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
			writeStoreError(w, err, "")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rules_created":         createdRules,
		"subscriptions_created": createdSubs,
		"errors":                errs,
	})
}

// importRules 校验并批量插入规则，返回成功条数与错误信息。
func (s *Server) importRules(r *http.Request, uid string, in []model.Rule) (int, []string) {
	var (
		valid []*model.Rule
		errs  []string
	)
	for i := range in {
		rule := in[i]
		rule.UserID = uid
		rule.ID = ""
		if err := validateRule(&rule); err != nil {
			if len(errs) < 20 {
				errs = append(errs, err.Error())
			}
			continue
		}
		valid = append(valid, &rule)
	}
	if len(valid) == 0 {
		return 0, errs
	}
	n, err := s.store.BulkCreateRules(r.Context(), valid)
	if err != nil {
		errs = append(errs, err.Error())
		return 0, errs
	}
	return n, errs
}

// === 长期 API Token ===

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	list, err := s.store.ListPATs(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, p := range list {
		items = append(items, map[string]any{
			"id":         p.ID,
			"name":       p.Name,
			"prefix":     p.Prefix,
			"created_at": time.Unix(p.CreatedAt, 0).UTC().Format(time.RFC3339),
			"last_used":  rfc3339OrEmpty(p.LastUsed),
			"expires_at": rfc3339OrEmpty(p.ExpiresAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		// ExpiresInDays 为 0 表示永不过期。
		ExpiresInDays int `json:"expires_in_days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "有效期需在 0（永久）到 3650 天之间", nil)
		return
	}
	u := currentUser(r)

	token := id.NewPAT()
	expiresAt := int64(0)
	if req.ExpiresInDays > 0 {
		expiresAt = time.Now().AddDate(0, 0, req.ExpiresInDays).Unix()
	}
	pid, err := s.store.CreatePAT(r.Context(), u.ID, strings.TrimSpace(req.Name), auth.HashPAT(token), auth.PATPprefix(token), expiresAt)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	s.store.LogAudit(r.Context(), u.ID, "pat_created", pid, clientIP(r, s.cfg.API.TrustedProxies))

	// 原文只在这里返回一次，之后服务端只有哈希，无法再展示。
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         pid,
		"name":       req.Name,
		"prefix":     auth.PATPprefix(token),
		"token":      token,
		"expires_at": rfc3339OrEmpty(expiresAt),
		"warning":    "请立即保存，该令牌只会显示这一次",
	})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	pid := chi.URLParam(r, "id")
	if err := s.store.DeletePAT(r.Context(), u.ID, pid); err != nil {
		writeStoreError(w, err, "API Token 不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func rfc3339OrEmpty(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// === 来源 IP 归属 ===

func (s *Server) handleListMyIPs(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	ips, err := s.store.ListUserIPs(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if ips == nil {
		ips = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ips})
}

func (s *Server) handleAddMyIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CIDR string `json:"cidr"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := currentUser(r)
	cidr, err := normalizeCIDR(req.CIDR)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.store.AddUserIP(r.Context(), u.ID, cidr); err != nil {
		writeStoreError(w, err, "")
		return
	}
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"cidr": cidr})
}

func (s *Server) handleDeleteMyIP(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	cidr := r.URL.Query().Get("cidr")
	if cidr == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "缺少 cidr 参数", nil)
		return
	}
	if err := s.store.DeleteUserIP(r.Context(), u.ID, cidr); err != nil {
		writeStoreError(w, err, "")
		return
	}
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// normalizeCIDR 把裸 IP 补成 /32 或 /128，并校验合法性。
func normalizeCIDR(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", errValidation("IP 或 CIDR 不能为空")
	}
	if strings.Contains(in, "/") {
		ip, n, err := net.ParseCIDR(in)
		if err != nil {
			return "", errValidation("CIDR 格式不正确: " + err.Error())
		}
		// 规范化：用网络地址而不是原始输入，避免 192.168.1.5/24 这种
		// 语义含糊的写法在比较时产生歧义。
		_ = ip
		return n.String(), nil
	}
	ip := net.ParseIP(in)
	if ip == nil {
		return "", errValidation("IP 格式不正确")
	}
	if ip.To4() != nil {
		return ip.String() + "/32", nil
	}
	return ip.String() + "/128", nil
}

// 保留 json 导入以避免在未使用时不报错（导出结构体用了它）。
var _ = json.Marshal
