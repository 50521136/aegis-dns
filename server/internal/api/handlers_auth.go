package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// registerRequest 是注册请求体。
type registerRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// InviteCode 在全局设置开启邀请码时必填。
	InviteCode string `json:"invite_code"`
}

// authResponse 是注册/登录的统一响应（文档 9.2）。
//
// 注册时直接把完整 DNS 配置一起返回，前端可以立刻展示
// 「你的专属地址」，不需要再发一次请求 —— 这是首次体验的关键。
type authResponse struct {
	User      userJSON        `json:"user"`
	DNSConfig dnsConfigJSON   `json:"dns_config"`
	Tokens    *auth.TokenPair `json:"tokens"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)

	if err := validateUsername(req.Username); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if req.Email != "" && !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "邮箱格式不正确", nil)
		return
	}
	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}

	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if !settings.AllowRegister {
		writeError(w, http.StatusForbidden, CodeForbidden, "当前已关闭公开注册，请联系管理员开通账号", nil)
		return
	}
	if settings.InviteCode != "" && req.InviteCode != settings.InviteCode {
		writeError(w, http.StatusForbidden, CodeForbidden, "邀请码不正确", nil)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}

	user, err := s.store.CreateUser(r.Context(), req.Username, req.Email, hash, model.RoleUser)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}

	// 新用户默认没有任何规则；但必须立刻重建快照，
	// 否则他的子域名在 dnsd 眼里不存在，DoT/DoH 会被当成未识别请求。
	if _, err := s.coord.Changed(r.Context(), user.ID); err != nil {
		s.log.Error("注册后重建快照失败", "user", user.ID, "err", err)
		// 不阻塞注册：用户已经建好了，快照重建可以靠后续操作补上。
	}

	pair, err := s.tokens.Issue(user.ID, user.Role)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if err := s.store.SaveRefreshToken(r.Context(), tokenJTI(s, pair.RefreshToken), user.ID, auth.RefreshExpiry().Unix()); err != nil {
		s.log.Warn("保存 refresh token 失败", "err", err)
	}

	writeJSON(w, http.StatusCreated, authResponse{
		User:      toUserJSON(user.ID, user.Username, user.Email, user.Role, user.ClientID, user.Enabled, user.CreatedAt),
		DNSConfig: buildDNSConfig(s.domain(r), user.ClientID, s.fingerprint(), s.dotPort()),
		Tokens:    pair,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	user, err := s.store.GetUserByUsername(r.Context(), strings.TrimSpace(req.Username))
	if err != nil {
		// 用户不存在与密码错误返回同一个响应，避免枚举账号。
		if isNotFound(err) {
			// 仍然做一次 bcrypt 运算，让两条路径的耗时接近，
			// 防止通过响应时间差判断用户名是否存在。
			_ = auth.CheckPassword("$2a$12$0000000000000000000000000000000000000000000000000000", req.Password)
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "用户名或密码错误", nil)
			return
		}
		writeStoreError(w, err, "")
		return
	}
	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "用户名或密码错误", nil)
		return
	}
	if !user.Enabled {
		writeError(w, http.StatusForbidden, CodeForbidden, "账号已被禁用", nil)
		return
	}

	pair, err := s.tokens.Issue(user.ID, user.Role)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if err := s.store.SaveRefreshToken(r.Context(), tokenJTI(s, pair.RefreshToken), user.ID, auth.RefreshExpiry().Unix()); err != nil {
		s.log.Warn("保存 refresh token 失败", "err", err)
	}

	writeJSON(w, http.StatusOK, authResponse{
		User:      toUserJSON(user.ID, user.Username, user.Email, user.Role, user.ClientID, user.Enabled, user.CreatedAt),
		DNSConfig: buildDNSConfig(s.domain(r), user.ClientID, s.fingerprint(), s.dotPort()),
		Tokens:    pair,
	})
}

// handleRefresh 用刷新令牌换一对新令牌（轮转）。
//
// 轮转是安全设计：旧 refresh token 立即作废。这样即使它被窃取，
// 攻击者用过一次之后，合法用户的下一次刷新就会失败并被迫重新登录，
// 从而暴露泄露事件（文档 R12）。
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	claims, err := s.tokens.ParseRefresh(req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "刷新令牌无效或已过期", nil)
		return
	}
	ok, err := s.store.IsRefreshTokenValid(r.Context(), claims.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if !ok {
		// 已撤销或不在库里：可能是重放，也可能是轮转后的旧令牌。
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "刷新令牌已失效，请重新登录", nil)
		return
	}

	user, err := s.store.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	if !user.Enabled {
		writeError(w, http.StatusForbidden, CodeForbidden, "账号已被禁用", nil)
		return
	}

	pair, err := s.tokens.Issue(user.ID, user.Role)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 旧令牌作废 + 新令牌入库，两步必须都做：
	// 只发新的不撤旧的，等于把 30 天有效期无限延长。
	if err := s.store.RevokeRefreshToken(r.Context(), claims.ID); err != nil {
		s.log.Warn("撤销旧 refresh token 失败", "err", err)
	}
	if err := s.store.SaveRefreshToken(r.Context(), tokenJTI(s, pair.RefreshToken), user.ID, auth.RefreshExpiry().Unix()); err != nil {
		s.log.Warn("保存 refresh token 失败", "err", err)
	}

	writeJSON(w, http.StatusOK, pair)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	// 允许空 body：前端可以在令牌已丢失时也调一次清理。
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	if req.RefreshToken != "" {
		if claims, err := s.tokens.ParseRefresh(req.RefreshToken); err == nil {
			if err := s.store.RevokeRefreshToken(r.Context(), claims.ID); err != nil {
				s.log.Warn("撤销 refresh token 失败", "err", err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// tokenJTI 从刷新令牌里取出 jti。
//
// 刚签发的令牌必然能解析；解析失败时返回空串会让撤销表里多一条
// 永远匹配不上的记录，因此这里退化为「用整串哈希」以保证唯一性。
func tokenJTI(s *Server, token string) string {
	claims, err := s.tokens.ParseRefresh(token)
	if err != nil || claims.ID == "" {
		return auth.HashPAT(token)
	}
	return claims.ID
}

// domain 返回主域名。
//
// 优先用数据库里的全局设置（管理员可在面板里改），
// 回落到配置文件里的 domain。
func (s *Server) domain(r *http.Request) string {
	if set, err := s.store.GetSettings(r.Context()); err == nil && set.Domain != "" {
		return set.Domain
	}
	return s.cfg.Domain
}

// fingerprint 返回当前证书指纹。
func (s *Server) fingerprint() string {
	if s.certs == nil {
		return ""
	}
	return s.certs.Fingerprint()
}

// dotPort 返回 DoT 端口号。
func (s *Server) dotPort() int {
	return portToInt(s.cfg.DNS.ListenDOT, 853)
}

func portToInt(addr string, def int) int {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return def
	}
	n := 0
	for _, c := range addr[i+1:] {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// validateUsername 校验用户名。
func validateUsername(name string) error {
	if name == "" {
		return errValidation("用户名不能为空")
	}
	if len([]rune(name)) < 3 {
		return errValidation("用户名至少 3 个字符")
	}
	if len([]rune(name)) > 32 {
		return errValidation("用户名最多 32 个字符")
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '-', c == '.':
		default:
			return errValidation("用户名只能包含字母、数字、下划线、连字符和点")
		}
	}
	return nil
}

type validationError string

func (e validationError) Error() string { return string(e) }

func errValidation(msg string) error { return validationError(msg) }

// isNotFound 判断是否为「记录不存在」。
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "记录不存在")
}

// uptimeSeconds 返回进程运行时长。
func (s *Server) uptimeSeconds() int64 {
	return int64(time.Since(s.startedAt).Seconds())
}
