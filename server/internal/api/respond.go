package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/store"
)

// 统一错误码（文档 9.1）。
const (
	CodeInvalidRequest   = "INVALID_REQUEST"
	CodeInvalidRule      = "INVALID_RULE_SYNTAX"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeTokenExpired     = "TOKEN_EXPIRED"
	CodeForbidden        = "FORBIDDEN"
	CodeNotFound         = "NOT_FOUND"
	CodeConflict         = "CONFLICT"
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeRateLimited      = "RATE_LIMITED"
	CodeInternal         = "INTERNAL_ERROR"
)

// APIError 是统一错误响应体里的 error 对象。
type APIError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// errorEnvelope 是统一错误响应外壳。
type errorEnvelope struct {
	Error APIError `json:"error"`
}

// writeJSON 写出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	// 不转义 HTML：响应里会包含用户填写的域名与规则原文，
	// 转义成 \u003c 之类只是让人更难读，安全性由前端的 React 转义负责。
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		slog.Default().Error("写 JSON 响应失败", "err", err)
	}
}

// writeError 写出统一格式的错误响应。
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, errorEnvelope{Error: APIError{Code: code, Message: message, Details: details}})
}

// writeStoreError 把数据层的错误映射成合适的 HTTP 状态码。
//
// 集中在一处映射，避免每个 handler 各写一遍 errors.Is 判断
// （那种写法迟早会漏掉一个分支，把「不存在」当成 500 返回）。
func writeStoreError(w http.ResponseWriter, err error, notFoundMsg string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, notFoundMsg, nil)
	case errors.Is(err, store.ErrUsernameTaken):
		writeError(w, http.StatusConflict, CodeConflict, "用户名已存在", nil)
	case errors.Is(err, store.ErrSubExists):
		writeError(w, http.StatusConflict, CodeConflict, "该订阅地址已经添加过", nil)
	default:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
	}
}

// decodeJSON 解析请求体。
//
// 限制 4MB：规则批量导入可能较大，但 4MB 已能装下十几万条规则文本，
// 再大就该走「上传文件」而不是塞进 JSON body。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法的 JSON: "+err.Error(), nil)
		return false
	}
	return true
}

// pagination 解析分页参数。
func pagination(r *http.Request, defSize int) (page, size, offset int) {
	page = atoiDefault(r.URL.Query().Get("page"), 1)
	size = atoiDefault(r.URL.Query().Get("page_size"), defSize)
	if page < 1 {
		page = 1
	}
	// 上限 200：再大就不是分页而是导出了，应该走专用接口。
	if size < 1 {
		size = defSize
	}
	if size > 200 {
		size = 200
	}
	return page, size, (page - 1) * size
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// parseBoolParam 解析可选的布尔查询参数，未提供时返回 nil。
func parseBoolParam(s string) *bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil
	}
	v := s == "1" || s == "true" || s == "yes"
	return &v
}

// timeRange 把 range 参数转成起始时间戳。
//
// 支持 1h / 24h / 7d / 30d / 90d，默认 24h。
func timeRange(s string, now time.Time) time.Time {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		s = "24h"
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return now.AddDate(0, 0, -n)
		}
	}
	if strings.HasSuffix(s, "h") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "h")); err == nil {
			return now.Add(-time.Duration(n) * time.Hour)
		}
	}
	if strings.HasSuffix(s, "m") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "m")); err == nil {
			return now.Add(-time.Duration(n) * time.Minute)
		}
	}
	return now.Add(-24 * time.Hour)
}

// intervalSeconds 把 interval 参数转成秒。
func intervalSeconds(s string, fallback int64) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return fallback
	}
	unit := s[len(s)-1]
	num, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || num <= 0 {
		return fallback
	}
	switch unit {
	case 'm':
		return int64(num) * 60
	case 'h':
		return int64(num) * 3600
	case 'd':
		return int64(num) * 86400
	}
	return fallback
}

// userJSON 是用户对象的对外形态。
//
// 单独定义而不是直接返回 model.User：后者带 PasswordHash 与 Unix 时间戳，
// 前者只暴露 API 契约里承诺的字段。用显式结构体而不是给 model 加 json tag
// 能保证「新增内部字段」永远不会意外泄漏到响应里。
type userJSON struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ClientID  string `json:"client_id"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

func toUserJSON(id, username, email, role, clientID string, enabled bool, createdAt int64) userJSON {
	return userJSON{
		ID: id, Username: username, Email: email, Role: role,
		ClientID: clientID, Enabled: enabled,
		CreatedAt: time.Unix(createdAt, 0).UTC().Format(time.RFC3339),
	}
}

// dnsConfigJSON 是 DNS 接入配置（文档 9.3）。
type dnsConfigJSON struct {
	ClientID string `json:"client_id"`
	Domain   string `json:"domain"`
	// DoHURL 是子域名形态，需要通配证书（推荐）。
	DoHURL string `json:"doh_url"`
	// DoHURLPath 是路径形态，兼容不能自定义 Host 的客户端。
	DoHURLPath string `json:"doh_url_path"`
	DoTHost    string `json:"dot_host"`
	DoTPort    int    `json:"dot_port"`
	DoTAddr    string `json:"dot_addr"`
	// PlainDNS 为 null：本系统不对外提供明文 DNS（避免被当作开放解析器滥用）。
	PlainDNS *string `json:"plain_dns"`
	// CertFingerprintSHA256 供客户端固定证书用（自签场景尤其重要）。
	CertFingerprintSHA256 string `json:"cert_fingerprint_sha256"`
	PlatformGuides        []platformGuide `json:"platform_guides"`
}

type platformGuide struct {
	Platform string   `json:"platform"`
	Steps    []string `json:"steps"`
}

// buildDNSConfig 构造某个 client_id 的接入配置。
func buildDNSConfig(domain, clientID, fingerprint string, dotPort int) dnsConfigJSON {
	host := clientID + "." + domain
	if clientID == "" {
		host = domain
	}
	return dnsConfigJSON{
		ClientID:              clientID,
		Domain:                domain,
		DoHURL:                "https://" + host + "/dns-query",
		DoHURLPath:            "https://" + domain + "/dns-query/" + clientID,
		DoTHost:               host,
		DoTPort:               dotPort,
		DoTAddr:               fmt.Sprintf("%s:%d", host, dotPort),
		PlainDNS:              nil,
		CertFingerprintSHA256: fingerprint,
		PlatformGuides:        platformGuides(clientID, domain),
	}
}

// platformGuides 返回各平台的配置步骤。
//
// 文案跟随真实系统菜单路径：这几段文字是用户唯一会照着做的地方，
// 写得含糊等于没写。
func platformGuides(clientID, domain string) []platformGuide {
	host := clientID + "." + domain
	return []platformGuide{
		{
			Platform: "ios",
			Steps: []string{
				"打开「设置」→「通用」→「VPN 与设备管理」→「DNS」",
				"选择「加密 DNS」，填入 DoH 地址：" + "https://" + host + "/dns-query",
				"或安装描述文件（.mobileconfig）批量下发，适合多设备",
			},
		},
		{
			Platform: "android",
			Steps: []string{
				"打开「设置」→「网络和互联网」→「私人 DNS」",
				"选择「私人 DNS 提供商主机名」",
				"填入：" + host + "（Android 使用 DoT，端口固定 853）",
			},
		},
		{
			Platform: "windows",
			Steps: []string{
				"打开「设置」→「网络和 Internet」→「以太网 / Wi-Fi」",
				"编辑 DNS 服务器分配，改为手动",
				"首选 DNS 填写本机可访问的 DoH 代理地址，或使用支持 DoH 的浏览器内置解析",
				"Windows 11 原生支持 DoH：填写 IP 后在「DNS over HTTPS」下拉里选择「开（自动模板）」",
			},
		},
		{
			Platform: "macos",
			Steps: []string{
				"打开「系统设置」→「网络」→ 选中当前连接 →「详细信息」",
				"切到「DNS」标签页，添加 DNS 服务器",
				"如需加密 DNS，安装描述文件并填入：" + "https://" + host + "/dns-query",
			},
		},
		{
			Platform: "router",
			Steps: []string{
				"在路由器 DHCP 设置里把 DNS 指向本服务",
				"若路由器支持 DoT/DoH，直接填入：" + host,
				"若只支持明文 DNS，请先把本机 IP 加入「来源 IP 归属」，再填本服务的公网 IP",
			},
		},
	}
}
