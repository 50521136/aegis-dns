package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 这个文件锁住 captchaBody 的「宽容解析」行为。
//
// 线上事故（2026-11）：极验 v4 会把 captcha_id 一起回传（官方文档里它是必填参数），
// 而 captchaBody 只声明了 4 个字段、外层 decodeJSON 又开了 DisallowUnknownFields，
// 于是登录直接 400：
//
//	{"code":"INVALID_REQUEST","message":"请求体不是合法的 JSON: json: unknown field \"captcha_id\""}
//
// 报错指向 JSON 解析，跟人机验证毫无关系，线上极难定位；而且用户完全无法登录。

// TestCaptchaBodyToleratesExtraFields 断言极验多回传的字段不会让解析失败。
func TestCaptchaBodyToleratesExtraFields(t *testing.T) {
	raw := `{
		"captcha_id": "f32bed9e0c4803ebe2165dd142c33582",
		"lot_number": "lot-123",
		"captcha_output": "out-456",
		"pass_token": "pass-789",
		"gen_time": "1700000000",
		"some_future_field": {"nested": true}
	}`
	var b captchaBody
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("captcha 里多带字段不应导致解析失败，实际: %v", err)
	}
	if b.LotNumber != "lot-123" || b.CaptchaOutput != "out-456" ||
		b.PassToken != "pass-789" || b.GenTime != "1700000000" {
		t.Fatalf("四个字段应被正确取出，实际: %+v", b)
	}
}

// TestCaptchaBodyNullAndEmpty 断言 captcha 为 null 或缺字段时不报错。
//
// 前端在「该场景不需要验证」时会把 captcha 传成 undefined，序列化后
// 可能是 null，也可能整个字段不出现。两种都必须能过。
func TestCaptchaBodyNullAndEmpty(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"lot_number":"only"}`} {
		var b captchaBody
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			t.Fatalf("%s 不应解析失败，实际: %v", raw, err)
		}
	}
	// 整个字段缺失时指针为 nil，这是调用方要处理的正常情况。
	var req struct {
		Captcha *captchaBody `json:"captcha"`
	}
	if err := json.Unmarshal([]byte(`{"username":"a"}`), &req); err != nil {
		t.Fatalf("缺少 captcha 字段不应报错: %v", err)
	}
	if req.Captcha != nil {
		t.Fatal("缺少 captcha 字段时指针应为 nil")
	}
}

// TestLoginBodyWithCaptchaIDAccepted 在 decodeJSON 这一层复现原始故障并验证已修复。
//
// 外层结构体仍是严格的（自己的字段写错要立刻报错），但 captcha 子对象
// 必须宽容 —— 这两件事同时成立才算修对。
func TestLoginBodyWithCaptchaIDAccepted(t *testing.T) {
	type loginReq struct {
		Username string       `json:"username"`
		Password string       `json:"password"`
		Captcha  *captchaBody `json:"captcha"`
	}

	body := `{"username":"alice","password":"pw","captcha":{
		"captcha_id":"cid","lot_number":"l","captcha_output":"o",
		"pass_token":"p","gen_time":"g"}}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))

	var req loginReq
	if !decodeJSON(rec, r, &req) {
		t.Fatalf("带 captcha_id 的登录请求应被接受，实际响应: %s", rec.Body.String())
	}
	if req.Captcha == nil || req.Captcha.LotNumber != "l" {
		t.Fatalf("captcha 应被正确解析，实际: %+v", req.Captcha)
	}

	// 反向断言：外层多带一个未知字段仍然要报错（严格性没有被削弱）。
	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"a","password":"b","totally_unknown":1}`))
	var req2 loginReq
	if decodeJSON(rec2, r2, &req2) {
		t.Fatal("外层未知字段仍应被拒绝，否则拼错的字段会被静默忽略")
	}
	if !strings.Contains(rec2.Body.String(), "totally_unknown") {
		t.Fatalf("错误信息应指出具体字段，实际: %s", rec2.Body.String())
	}
}
