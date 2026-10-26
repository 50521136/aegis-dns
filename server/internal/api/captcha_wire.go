package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/50521136/aegis-dns/server/internal/captcha"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// 极验验证在各入口上的接线。
//
// 分工：
//   - 本文件负责「这个入口要不要验」「参数怎么从请求里取」「失败怎么回」；
//   - internal/captcha 负责真正的签名与二次校验。
//
// 三条设计原则：
//  1. **只挡人机边界，不挡业务**。注册 / 登录 / 找回密码 / 提交反馈会被脚本刷，
//     解析请求（53/853/443）绝不上验证 —— 那是每毫秒都在跑的路径。
//  2. **没配参数就当没开**。管理员开了开关但没填 captcha_id/私钥时，
//     宁可放行也不能把注册登录全锁死 —— 那等于把自己锁在门外。
//  3. **校验服务不可用时按「未启用」处理**（fail-open）。极验自身故障导致
//     全站无法注册登录，比放进来几个机器人的代价大得多。这一点与极验
//     官方示例代码一致。

// 验证场景常量。
const (
	CaptchaSceneRegister = "register"
	CaptchaSceneLogin    = "login"
	CaptchaSceneForgot   = "forgot"
	CaptchaSceneFeedback = "feedback"
)

// captchaClient 按当前设置构造极验客户端。未配置时返回 nil。
func (s *Server) captchaClient(set *model.GlobalSettings) *captcha.Client {
	if set == nil || !set.GeetestEnabled {
		return nil
	}
	id := strings.TrimSpace(set.GeetestID)
	if id == "" || set.GeetestKeyEncrypted == "" {
		return nil
	}
	// decryptSecret 失败时返回空串（它自己已经记了 WARN）。私钥解不出来
	// 就当没配置 —— 让业务照常走，而不是把注册登录锁死。
	key := s.decryptSecret(set.GeetestKeyEncrypted)
	if strings.TrimSpace(key) == "" {
		s.log.Warn("极验私钥为空或解密失败，本次按未启用处理")
		return nil
	}
	return &captcha.Client{CaptchaID: id, CaptchaKey: key}
}

// captchaRequired 判断某个场景当前是否需要验证。
//
// 三道闸门全开才算「需要」：总开关 → 该场景开关 → 参数齐全。
// 任何一道没开都返回 false，让业务照常走。
func (s *Server) captchaRequired(set *model.GlobalSettings, scene string) bool {
	if s.captchaClient(set) == nil {
		return false
	}
	switch scene {
	case CaptchaSceneRegister:
		return set.GeetestOnRegister
	case CaptchaSceneLogin:
		return set.GeetestOnLogin
	case CaptchaSceneForgot:
		return set.GeetestOnForgot
	case CaptchaSceneFeedback:
		return set.GeetestOnFeedback
	}
	return false
}

// verifyCaptcha 校验一次请求携带的验证参数。
//
// 返回 (ok, message)：ok=false 时 message 是给用户看的原因，
// 调用方直接用 422 返回即可。校验服务本身故障时返回 ok=true 并记日志
// （fail-open，见文件头第 3 条）。
func (s *Server) verifyCaptcha(ctx context.Context, set *model.GlobalSettings, scene string, v captcha.Validation) (bool, string) {
	if !s.captchaRequired(set, scene) {
		return true, ""
	}
	client := s.captchaClient(set)
	if client == nil {
		return true, ""
	}
	ok, err := client.Verify(ctx, v)
	if err != nil {
		// 极验自己挂了：放行并留下明确日志。运维据此判断是「被攻击」
		// 还是「第三方故障」。
		s.log.Warn("极验校验不可用，本次放行", "scene", scene, "err", err)
		return true, ""
	}
	if !ok {
		s.log.Info("极验校验未通过", "scene", scene)
		return false, captcha.FailureReason(nil)
	}
	return true, ""
}

// captchaConfigJSON 是对外公开的验证配置（前端初始化用）。
//
// 只暴露 captcha_id 与「哪些场景要验」，私钥永不出后端。
// 这个接口必须免登录：注册页在登录之前就要用。
func (s *Server) handleCaptchaConfig(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		// 读不到设置时按「全不验」回，让注册登录继续可用。
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false, "captcha_id": "", "scenes": map[string]bool{},
		})
		return
	}
	client := s.captchaClient(set)
	scenes := map[string]bool{
		CaptchaSceneRegister: s.captchaRequired(set, CaptchaSceneRegister),
		CaptchaSceneLogin:    s.captchaRequired(set, CaptchaSceneLogin),
		CaptchaSceneForgot:   s.captchaRequired(set, CaptchaSceneForgot),
		CaptchaSceneFeedback: s.captchaRequired(set, CaptchaSceneFeedback),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    client != nil,
		"captcha_id": set.GeetestID,
		"scenes":     scenes,
	})
}

// captchaFromRequest 从请求体结构里取验证参数。
//
// 前端统一放在顶层字段 captcha 下（{captcha: {lot_number, ...}}），
// 这样各个 handler 的请求结构只要嵌一个可选字段即可，不用改签名。
func captchaFromBody(b *captchaBody) captcha.Validation {
	if b == nil {
		return captcha.Validation{}
	}
	return captcha.Validation{
		LotNumber:     strings.TrimSpace(b.LotNumber),
		CaptchaOutput: strings.TrimSpace(b.CaptchaOutput),
		PassToken:     strings.TrimSpace(b.PassToken),
		GenTime:       strings.TrimSpace(b.GenTime),
	}
}

// captchaBody 是请求体里那段验证参数。
type captchaBody struct {
	LotNumber     string `json:"lot_number"`
	CaptchaOutput string `json:"captcha_output"`
	PassToken     string `json:"pass_token"`
	GenTime       string `json:"gen_time"`
}

// captchaPublicConfig 返回给前端的验证配置。
//
// 与 handleCaptchaConfig 的区别：这个不单独占一个路由，直接并进
// /public/config —— 登录注册页本来就要拉那个接口，再单开一个等于
// 让首屏多等一次往返。
func (s *Server) captchaPublicConfig(set *model.GlobalSettings) map[string]any {
	client := s.captchaClient(set)
	return map[string]any{
		"enabled":    client != nil,
		"captcha_id": set.GeetestID,
		"scenes": map[string]bool{
			CaptchaSceneRegister: s.captchaRequired(set, CaptchaSceneRegister),
			CaptchaSceneLogin:    s.captchaRequired(set, CaptchaSceneLogin),
			CaptchaSceneForgot:   s.captchaRequired(set, CaptchaSceneForgot),
			CaptchaSceneFeedback: s.captchaRequired(set, CaptchaSceneFeedback),
		},
	}
}

// verifyCaptchaByScene 是 handler 里的便捷入口：自己读设置、自己判场景。
//
// 单独抽一层是因为四个 handler 都要做同样三步（读设置 → 判开关 → 校验），
// 写四遍必然有一处漏判 —— 而漏判的表现是「某个入口根本不验证」，
// 从日志里完全看不出来。
func (s *Server) verifyCaptchaByScene(r *http.Request, scene string, body *captchaBody) (bool, string) {
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		// 读不到设置：放行。锁死登录注册的代价远大于放进几个机器人。
		s.log.Warn("读取设置失败，跳过人机验证", "scene", scene, "err", err)
		return true, ""
	}
	return s.verifyCaptcha(r.Context(), set, scene, captchaFromBody(body))
}
