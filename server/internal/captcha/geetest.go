// Package captcha 实现极验 v4 行为验证的服务端二次校验。
//
// 流程分两段：
//  1. 前端用 captcha_id 初始化极验组件，用户通过后拿到
//     {lot_number, captcha_output, pass_token, gen_time}；
//  2. 服务端把这些参数 + 自己算的 sign_token 发到极验的校验接口，
//     由极验判定这次验证是否真实有效。
//
// sign_token = HMAC-SHA256(key=私钥, message=lot_number)，这一步是防伪的核心：
// 没有私钥就无法为一个 lot_number 造出合法签名，所以即使有人截获了前端参数，
// 也不能重复使用（lot_number 一次性）。
package captcha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 极验校验接口。生产固定用 gcaptcha4.geetest.com；
// 留一个变量是为了让测试能指向 httptest 服务器。
var DefaultAPIBase = "https://gcaptcha4.geetest.com"

// Client 是极验 v4 的服务端校验客户端。
type Client struct {
	CaptchaID  string
	CaptchaKey string
	// APIBase 留空时用 DefaultAPIBase。
	APIBase string
	// HTTPClient 留空时用带 8 秒超时的默认客户端。
	//
	// 超时不能给太长：这是挂在注册/登录请求上的同步调用，
	// 极验接口抖动时用户会一直转圈。
	HTTPClient *http.Client
}

// Validation 是前端 getValidate() 返回的四个字段。
type Validation struct {
	LotNumber     string `json:"lot_number"`
	CaptchaOutput string `json:"captcha_output"`
	PassToken     string `json:"pass_token"`
	GenTime       string `json:"gen_time"`
}

// Empty 判断前端是否根本没带验证参数。
func (v Validation) Empty() bool {
	return strings.TrimSpace(v.LotNumber) == "" &&
		strings.TrimSpace(v.PassToken) == ""
}

// SignToken 按极验要求生成签名：HMAC-SHA256(私钥, lot_number)。
func (c *Client) SignToken(lotNumber string) string {
	mac := hmac.New(sha256.New, []byte(c.CaptchaKey))
	mac.Write([]byte(lotNumber))
	return hex.EncodeToString(mac.Sum(nil))
}

// validateResponse 是极验的响应体。
type validateResponse struct {
	Result string `json:"result"` // success | fail
	Reason string `json:"reason"`
	// 请求本身出错时极验回这三个字段（HTTP 可能仍是 200）。
	Status string `json:"status"`
	Code   string `json:"code"`
	Msg    string `json:"msg"`
}

// ErrNotConfigured 表示没有配置 captcha_id / 私钥。
var ErrNotConfigured = errors.New("极验验证未配置")

// Verify 向极验发起二次校验。
//
// 返回值语义：
//   - (true, nil)：验证通过；
//   - (false, nil)：验证不通过（用户该重试），调用方应拒绝本次请求；
//   - (false, err)：**校验服务本身不可用**（网络、超时、配置缺失）。
//
// 第三类要不要放行是调用方的策略，这里不替它决定 —— 但这个区分必须做出来，
// 否则「用户答错了」和「极验挂了」在日志里长得一样，线上出问题根本分不清。
func (c *Client) Verify(ctx context.Context, v Validation) (bool, error) {
	if strings.TrimSpace(c.CaptchaID) == "" || strings.TrimSpace(c.CaptchaKey) == "" {
		return false, ErrNotConfigured
	}
	if v.Empty() {
		return false, nil
	}

	base := c.APIBase
	if base == "" {
		base = DefaultAPIBase
	}
	endpoint := strings.TrimRight(base, "/") + "/validate?captcha_id=" + url.QueryEscape(c.CaptchaID)

	form := url.Values{}
	form.Set("lot_number", v.LotNumber)
	form.Set("captcha_output", v.CaptchaOutput)
	form.Set("pass_token", v.PassToken)
	form.Set("gen_time", v.GenTime)
	form.Set("sign_token", c.SignToken(v.LotNumber))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("构造极验请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("请求极验校验接口失败: %w", err)
	}
	defer resp.Body.Close()
	// 限读 64KB：正常响应几百字节，防止异常响应把内存吃光。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false, fmt.Errorf("读取极验响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("极验校验接口返回 HTTP %d", resp.StatusCode)
	}

	var out validateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("解析极验响应失败: %w", err)
	}
	// 极验的「请求异常」用 status/code 表达，此时 result 为空。
	if out.Status == "error" {
		return false, fmt.Errorf("极验校验异常 %s: %s", out.Code, out.Msg)
	}
	return out.Result == "success", nil
}

// FailureReason 把一次失败翻译成给用户看的话。
//
// 直接回 "验证失败" 会让用户不知道是网络问题还是自己操作问题，
// 而这层验证恰恰在注册/登录的关键路径上，含糊的提示会直接变成客服工单。
func FailureReason(err error) string {
	switch {
	case err == nil:
		return "验证未通过，请重新完成验证"
	case errors.Is(err, ErrNotConfigured):
		return "站点未正确配置验证服务，请联系管理员"
	case errors.Is(err, context.DeadlineExceeded):
		return "验证服务响应超时，请稍后重试"
	default:
		return "验证服务暂时不可用，请稍后重试"
	}
}
