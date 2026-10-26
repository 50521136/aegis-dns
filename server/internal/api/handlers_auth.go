package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// registerRequest 是注册请求体。
type registerRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// InviteCode 在全局设置开启邀请码时必填。
	InviteCode string `json:"invite_code"`
	// EmailCode 在开启「注册邮箱验证」时必填。
	EmailCode string `json:"email_code"`
	// Captcha 是极验验证参数；后台开了注册验证时必填。
	Captcha *captchaBody `json:"captcha"`
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

	// 人机验证放在最前面：脚本刷注册时不该消耗后面的任何资源
	// （bcrypt 一次就是几十毫秒，还带内存）。
	if ok, msg := s.verifyCaptchaByScene(r, CaptchaSceneRegister, req.Captcha); !ok {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, msg, nil)
		return
	}

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
	// 邮箱后缀白名单：挡掉一次性邮箱。
	// 在「注册送会员」的配置下，放开后缀等于把这笔赠送白送出去。
	if settings.EmailDomainWhitelist != "" {
		if !emailAllowed(strings.ToLower(strings.TrimSpace(req.Email)), settings.EmailDomainWhitelist) {
			writeError(w, http.StatusForbidden, CodeForbidden,
				"该邮箱后缀不在允许范围内，请使用常见邮箱（如 qq.com / 163.com / gmail.com）", nil)
			return
		}
	}

	// 邮箱验证：必须在建账号之前过。
	// 放在后面就等于「先占住用户名，再慢慢验证」，验证码失效时用户名已经被占用。
	if settings.EmailVerifyRegister {
		if strings.TrimSpace(req.Email) == "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "开启邮箱验证时必须填写邮箱", nil)
			return
		}
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if err := s.store.VerifyEmailCode(r.Context(), email, store.EmailPurposeRegister, strings.TrimSpace(req.EmailCode)); err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error(), nil)
			return
		}
	}

	// 邀请码校验：两条路都认。
	//   - 全局设置里的单个邀请码（老机制，只有一个，谁用都一样）
	//   - invite_codes 表里的码（带奖励天数、次数与有效期）
	// 先查表再回退到老机制，这样已有的部署不用改配置就能继续用。
	code := strings.ToUpper(strings.TrimSpace(req.InviteCode))
	codeInTable := false
	if code != "" {
		if c, err := s.store.GetInviteCode(r.Context(), code); err == nil && c != nil {
			codeInTable = true
			// 这里只做「存在且可用」的粗判，真正的核销在注册成功后由
			// RedeemInvite 在事务里做（含次数与有效期复查），
			// 避免校验与核销之间被并发挤过。
			if !c.Enabled {
				writeError(w, http.StatusForbidden, CodeForbidden, "邀请码已停用", nil)
				return
			}
			if c.ExpiresAt > 0 && time.Now().Unix() > c.ExpiresAt {
				writeError(w, http.StatusForbidden, CodeForbidden, "邀请码已过期", nil)
				return
			}
			if c.MaxUses > 0 && c.UsedCount >= c.MaxUses {
				writeError(w, http.StatusForbidden, CodeForbidden, "邀请码使用次数已用完", nil)
				return
			}
		}
	}
	if settings.InviteCode != "" && !codeInTable && code != strings.ToUpper(strings.TrimSpace(settings.InviteCode)) {
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

	// 首次注册赠送会员天数。
	// 放在邀请码核销之前：新人先拿到自己的见面礼，邀请奖励再叠加，
	// 顺序反过来也能加，但日志里「新人得到多少」会算不清。
	// 会员种类与天数一起发；tiers 后面还要用来算 userJSON 的生效种类，
	// 一次取好，别在两个地方各查一次。
	tiers, _ := s.store.TierMap(r.Context())
	if settings.SignupMemberDays > 0 {
		// 种类也要发：只加天数的话，管理员在「注册赠送」里选的
		// 「超级会员」不生效，新用户永远落在内置 vip 档上。
		if _, err := s.store.GrantMembership(r.Context(), user.ID, settings.SignupMemberTier, settings.SignupMemberDays); err != nil {
			s.log.Warn("注册赠送会员失败", "user", user.ID, "err", err)
		} else if fresh, err := s.store.GetUserByID(r.Context(), user.ID); err == nil && fresh != nil {
			user = fresh
		}
	}

	// 核销邀请码并给邀请人发奖励。
	// 失败不阻塞注册：账号已经建好了，为了「奖励没发出去」把人挡在门外
	// 是本末倒置；失败会记日志，由管理员补偿。
	if codeInTable {
		if days, err := s.store.RedeemInvite(r.Context(), code, user.ID, settings.MemberRewardTier); err != nil {
			s.log.Warn("核销邀请码失败", "code", code, "user", user.ID, "err", err)
		} else if days > 0 {
			// 邀请人的会员期变了，快照要跟着更新，否则他的配额还是旧的
			if _, err := s.coord.Changed(r.Context(), ""); err != nil {
				s.log.Warn("邀请奖励后重建快照失败", "err", err)
			}
		}
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
		User:      toUserJSON(user, tiers),
		DNSConfig: buildDNSConfig(s.domain(r), user.ClientID, s.fingerprint(), s.dnsPorts(), s.coversSubdomains(r), s.rootDomainReserved(r.Context()), s.protos(r.Context())),
		Tokens:    pair,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string       `json:"username"`
		Password string       `json:"password"`
		Captcha  *captchaBody `json:"captcha"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if ok, msg := s.verifyCaptchaByScene(r, CaptchaSceneLogin, req.Captcha); !ok {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, msg, nil)
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

	loginTiers, _ := s.store.TierMap(r.Context())
	writeJSON(w, http.StatusOK, authResponse{
		User:      toUserJSON(user, loginTiers),
		DNSConfig: buildDNSConfig(s.domain(r), user.ClientID, s.fingerprint(), s.dnsPorts(), s.coversSubdomains(r), s.rootDomainReserved(r.Context()), s.protos(r.Context())),
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

// coversSubdomains 判断当前证书是否覆盖 <任意>.<主域名>。
//
// 没有证书管理器（比如自签降级还没就绪）时保守返回 false，
// 宁可给路径式地址也不要给一个握手就会被拒的子域名地址。
func (s *Server) coversSubdomains(r *http.Request) bool {
	if s.certs == nil {
		return false
	}
	return s.certs.CoversSubdomains(s.domain(r))
}

// dotPort 返回 DoT 端口号。
func (s *Server) dotPort() int {
	return portToInt(s.cfg.DNS.ListenDOT, 853)
}

// dnsPorts 返回下发给客户端的实际端口。
//
// DoH 必须读 listen_doh：地址里带不带端口完全由它决定，
// 写死 443 会让「改了 listen_doh 但用户拿到的地址还是 443」这种
// 故障静默存在 —— dnsd 在新端口上正常服务，用户却连不上。
func (s *Server) dnsPorts() dnsPorts {
	return dnsPorts{
		DoH:  portToInt(s.cfg.DNS.ListenDOH, 443),
		DoT:  portToInt(s.cfg.DNS.ListenDOT, 853),
		DoQ:  portToInt(s.cfg.DNS.ListenDOQ, 853),
		DoH3: portToInt(s.cfg.DNS.ListenDOH3, 443),
	}
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
