package captcha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 签名必须是 HMAC-SHA256(私钥, lot_number) 的十六进制小写。
//
// 这条一旦错了，所有验证都会失败，而极验只回一个笼统的 fail ——
// 排查时看不出是签名算法问题还是用户操作问题。
func TestSignTokenMatchesSpec(t *testing.T) {
	const key = "5dbc25f70b124a67f9f3bf58fd048bc3"
	const lot = "4dc3cfc2cdff448cad8d13107198d473"
	c := &Client{CaptchaID: "id", CaptchaKey: key}

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(lot))
	want := hex.EncodeToString(mac.Sum(nil))

	if got := c.SignToken(lot); got != want {
		t.Fatalf("签名不对\n got=%s\nwant=%s", got, want)
	}
}

// 签名必须随 lot_number 变化 —— 否则同一个签名能重复使用，
// 等于验证形同虚设。
func TestSignTokenVariesWithLotNumber(t *testing.T) {
	c := &Client{CaptchaID: "id", CaptchaKey: "k"}
	if c.SignToken("a") == c.SignToken("b") {
		t.Fatal("不同 lot_number 必须得到不同签名")
	}
}

// 通过 / 不通过 / 服务异常三种结果要能分开。
func TestVerifyResultClassification(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		status  int
		wantOK  bool
		wantErr bool
	}{
		{"成功", `{"result":"success","reason":""}`, 200, true, false},
		{"用户答错", `{"result":"fail","reason":"pass_token expire"}`, 200, false, false},
		{"极验报错", `{"status":"error","code":"-50005","msg":"illegal gen_time"}`, 200, false, true},
		{"HTTP 500", `oops`, 500, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := &Client{CaptchaID: "id", CaptchaKey: "key", APIBase: srv.URL}
			ok, err := c.Verify(context.Background(), Validation{
				LotNumber: "lot", CaptchaOutput: "out", PassToken: "tok", GenTime: "123",
			})
			if ok != tc.wantOK {
				t.Errorf("ok=%v，期望 %v（err=%v）", ok, tc.wantOK, err)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err=%v，期望有错=%v", err, tc.wantErr)
			}
		})
	}
}

// 请求必须带上 sign_token，且五个字段齐全 —— 少一个极验就会拒。
func TestVerifySendsAllFieldsAndSignature(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = map[string]string{
			"lot_number":     r.PostForm.Get("lot_number"),
			"captcha_output": r.PostForm.Get("captcha_output"),
			"pass_token":     r.PostForm.Get("pass_token"),
			"gen_time":       r.PostForm.Get("gen_time"),
			"sign_token":     r.PostForm.Get("sign_token"),
			"captcha_id_q":   r.URL.Query().Get("captcha_id"),
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success"})
	}))
	defer srv.Close()

	c := &Client{CaptchaID: "cid", CaptchaKey: "key", APIBase: srv.URL}
	if _, err := c.Verify(context.Background(), Validation{
		LotNumber: "L1", CaptchaOutput: "O1", PassToken: "P1", GenTime: "G1",
	}); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	for _, k := range []string{"lot_number", "captcha_output", "pass_token", "gen_time", "sign_token"} {
		if got[k] == "" {
			t.Errorf("请求缺少字段 %s", k)
		}
	}
	if got["captcha_id_q"] != "cid" {
		t.Errorf("captcha_id 应放在 query 上，实际 %q", got["captcha_id_q"])
	}
	if got["sign_token"] != c.SignToken("L1") {
		t.Errorf("sign_token 与本地计算不一致")
	}
}

// 没配置 id/私钥时必须明确报 ErrNotConfigured，而不是静默放行或崩掉。
func TestVerifyNotConfigured(t *testing.T) {
	c := &Client{}
	if _, err := c.Verify(context.Background(), Validation{LotNumber: "x"}); err != ErrNotConfigured {
		t.Fatalf("期望 ErrNotConfigured，实际 %v", err)
	}
}

// 前端一个参数都没带时直接判失败，不要白发一次网络请求。
func TestVerifyEmptyValidationFailsWithoutRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success"})
	}))
	defer srv.Close()

	c := &Client{CaptchaID: "id", CaptchaKey: "k", APIBase: srv.URL}
	ok, err := c.Verify(context.Background(), Validation{})
	if ok || err != nil {
		t.Fatalf("空参数应判失败且无错误，实际 ok=%v err=%v", ok, err)
	}
	if called {
		t.Fatal("空参数不该发出请求")
	}
}
