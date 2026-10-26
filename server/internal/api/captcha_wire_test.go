package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/captcha"
	"github.com/50521136/aegis-dns/server/internal/config"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这组测试守的是「验证开关必须真的能分别控制」这条。
//
// 一刀切的后果很具体：只想挡注册被刷，结果老用户每次登录也要滑一次，
// 用户会直接走人。所以每个入口的开关都要单独生效。

func newCaptchaTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: &config.Config{}}
	box, err := newSecretBox(nil)
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	s.secretBox = box
	return s, st
}

// 未填 captcha_id / 私钥时，即使开关全开也必须判「不需要验证」。
//
// 否则管理员打开开关却忘了填参数，注册和登录会被永久锁死 ——
// 而界面上开关显示「已开启」，看起来一切正常。
func TestCaptchaNotRequiredWithoutCredentials(t *testing.T) {
	s, _ := newCaptchaTestServer(t)
	set := &model.GlobalSettings{
		GeetestEnabled:    true,
		GeetestID:         "", // 没填
		GeetestOnRegister: true,
	}
	if s.captchaRequired(set, CaptchaSceneRegister) {
		t.Fatal("没填 captcha_id 时不该要求验证")
	}
}

// 总开关关掉时，任何场景都不要求验证。
func TestCaptchaMasterSwitchOff(t *testing.T) {
	s, _ := newCaptchaTestServer(t)
	set := &model.GlobalSettings{
		GeetestEnabled:      false,
		GeetestID:           "cid",
		GeetestKeyEncrypted: "x",
		GeetestOnRegister:   true,
		GeetestOnLogin:      true,
	}
	for _, scene := range []string{CaptchaSceneRegister, CaptchaSceneLogin, CaptchaSceneForgot, CaptchaSceneFeedback} {
		if s.captchaRequired(set, scene) {
			t.Errorf("总开关关闭时 %s 不该要求验证", scene)
		}
	}
}

// 每个场景的开关必须独立生效。
func TestCaptchaPerSceneSwitches(t *testing.T) {
	s, _ := newCaptchaTestServer(t)
	enc, err := s.encryptSecret("private-key")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	set := &model.GlobalSettings{
		GeetestEnabled:      true,
		GeetestID:           "cid",
		GeetestKeyEncrypted: enc,
		GeetestOnRegister:   true,
		GeetestOnLogin:      false,
		GeetestOnForgot:     true,
		GeetestOnFeedback:   false,
	}
	want := map[string]bool{
		CaptchaSceneRegister: true,
		CaptchaSceneLogin:    false,
		CaptchaSceneForgot:   true,
		CaptchaSceneFeedback: false,
	}
	for scene, exp := range want {
		if got := s.captchaRequired(set, scene); got != exp {
			t.Errorf("%s 期望 %v，实际 %v", scene, exp, got)
		}
	}
}

// 极验接口挂掉时必须放行（fail-open），否则第三方故障会连带
// 全站无法注册登录 —— 那比放进几个机器人的代价大得多。
func TestCaptchaFailOpenOnServiceError(t *testing.T) {
	s, _ := newCaptchaTestServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	enc, err := s.encryptSecret("key")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	set := &model.GlobalSettings{
		GeetestEnabled: true, GeetestID: "cid", GeetestKeyEncrypted: enc,
		GeetestOnRegister: true,
	}
	// 把客户端指向会 500 的测试服务器。
	client := &captcha.Client{CaptchaID: "cid", CaptchaKey: "key", APIBase: srv.URL}
	ok, err := client.Verify(context.Background(), captcha.Validation{
		LotNumber: "l", CaptchaOutput: "o", PassToken: "p", GenTime: "1",
	})
	if err == nil {
		t.Fatal("前置条件：HTTP 500 应产生错误")
	}
	if ok {
		t.Fatal("前置条件：HTTP 500 不应判通过")
	}
	// verifyCaptcha 在 err != nil 时应放行。
	got, _ := s.verifyCaptcha(context.Background(), set, CaptchaSceneRegister, captcha.Validation{
		LotNumber: "l", CaptchaOutput: "o", PassToken: "p", GenTime: "1",
	})
	if !got {
		t.Fatal("极验服务异常时必须放行（fail-open），否则全站无法注册")
	}
}

// 场景开关关掉时，即使带的是垃圾参数也必须放行 —— 不能因为
// 前端还留着验证组件的残留参数就把人拦在门外。
func TestCaptchaSceneOffIgnoresPayload(t *testing.T) {
	s, _ := newCaptchaTestServer(t)
	enc, _ := s.encryptSecret("key")
	set := &model.GlobalSettings{
		GeetestEnabled: true, GeetestID: "cid", GeetestKeyEncrypted: enc,
		GeetestOnLogin: false,
	}
	ok, _ := s.verifyCaptcha(context.Background(), set, CaptchaSceneLogin, captcha.Validation{
		LotNumber: "garbage",
	})
	if !ok {
		t.Fatal("场景开关关闭时必须放行")
	}
}

// captchaPublicConfig 只暴露 captcha_id，绝不能带私钥。
func TestCaptchaPublicConfigHidesSecret(t *testing.T) {
	s, _ := newCaptchaTestServer(t)
	enc, _ := s.encryptSecret("super-secret-key")
	set := &model.GlobalSettings{
		GeetestEnabled: true, GeetestID: "cid", GeetestKeyEncrypted: enc,
		GeetestOnRegister: true,
	}
	cfg := s.captchaPublicConfig(set)
	for k, v := range cfg {
		if k == "captcha_key" || k == "geetest_key" || k == "secret" {
			t.Fatalf("公开配置里出现了私钥字段 %q", k)
		}
		if str, ok := v.(string); ok && str == "super-secret-key" {
			t.Fatalf("公开配置泄露了私钥明文（字段 %q）", k)
		}
	}
	if cfg["captcha_id"] != "cid" {
		t.Errorf("captcha_id 应公开，实际 %v", cfg["captcha_id"])
	}
}
