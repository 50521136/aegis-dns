package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/50521136/aegis-dns/server/internal/bjtime"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"

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
	// today 是北京时间的「今天 0 点起」，不是滚动的 24 小时。
	// 每日请求量额度按北京自然日计，所以统计也得以 0 点为界，两边口径一致。
	if s == "today" {
		return time.Unix(bjtime.StartOfDay(bjtime.Today()), 0)
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
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	ClientID string `json:"client_id"`
	Enabled  bool   `json:"enabled"`
	// UseGlobalRules / IDResetCount / RulesEnabled / SubsEnabled 前端要用：
	// 前者控制设置页的全局层开关，后三者控制后台用户列表里的显示与开关状态。
	UseGlobalRules bool `json:"use_global_rules"`
	IDResetCount   int  `json:"id_reset_count"`
	RulesEnabled   bool `json:"rules_enabled"`
	SubsEnabled    bool `json:"subs_enabled"`
	CustomIDUsed   bool `json:"custom_id_used"`
	// DailyReport 是用户自己的每日邮件日报订阅开关。
	DailyReport bool `json:"daily_report"`
	// ReportTime 是日报推送时间（北京时间 HH:MM），由用户自己设。
	ReportTime string `json:"report_time"`
	CreatedAt  string `json:"created_at"`

	// 会员信息。种类与有效期在库里，但「此刻是什么档」按当前时间实时算，
	// 前端不需要自己判断 member_until 有没有过期。
	MemberUntil   int64  `json:"member_until"`
	MemberDays    int    `json:"member_days"`
	MemberForever bool   `json:"member_forever"`
	Tier          string `json:"tier"`
	// TierID 是库里记的会员种类（空串 = 会员期内走默认会员档）。
	// 界面上要显示具体种类时用它，别拿 Tier 猜。
	TierID    string `json:"tier_id"`
	InvitedBy string `json:"invited_by,omitempty"`
}

// toUserJSON 直接吃 *model.User。
//
// 之前是七个人参的位置参数，加字段就得改所有调用点，而且漏改一个就会出现
// 「某个接口返回的用户少了字段」这种只在特定页面暴露的问题。
// toUserJSON 需要档位表才能给出「生效的会员种类」。
//
// 之前这里调 model.TierOf（只知道 free/vip），于是 /me 和登录响应里的
// tier 永远是 vip，而用户实际是「超级会员」—— 前端拿它显示就会串档。
func toUserJSON(u *model.User, tiers map[string]model.TierDef) userJSON {
	now := time.Now()
	mem := model.MemberStateOf(u, tiers, now)
	return userJSON{
		ID: u.ID, Username: u.Username, Email: u.Email, Role: u.Role,
		ClientID: u.ClientID, Enabled: u.Enabled,
		UseGlobalRules: u.UseGlobalRules, IDResetCount: u.IDResetCount,
		RulesEnabled: u.RulesEnabled, SubsEnabled: u.SubsEnabled,
		CustomIDUsed:  u.CustomIDUsed,
		DailyReport:   u.DailyReport,
		ReportTime:    u.ReportTime,
		CreatedAt:     time.Unix(u.CreatedAt, 0).UTC().Format(time.RFC3339),
		MemberUntil:   mem.Until,
		MemberDays:    mem.Days,
		MemberForever: mem.Forever,
		Tier:          mem.Tier,
		TierID:        mem.AssignedTier,
		InvitedBy:     u.InvitedBy,
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
	// DoQAddr 是 DoQ（DNS-over-QUIC，UDP）的接入地址，形态与 DoT 相同：
	// QUIC 没有路径段可放 client_id，租户只能靠 SNI 首段识别，
	// 所以证书没有通配符时只能给裸域名（与 DoT 同一个限制）。
	DoQAddr string `json:"doq_addr"`
	DoQHost string `json:"doq_host"`
	DoQPort int    `json:"doq_port"`
	// DoH3Supported 表示同一份 DoH 地址也支持 HTTP/3。
	// 不另给地址：H3 与 DoH 是**同一个 URL**，只是传输从 TCP 换成 UDP，
	// 客户端靠 Alt-Svc + ALPN 自己切。给一个"h3://"地址反而没有客户端认。
	DoH3Supported bool `json:"doh3_supported"`
	// PlainDNS 为 null：本系统不对外提供明文 DNS（避免被当作开放解析器滥用）。
	PlainDNS *string `json:"plain_dns"`
	// CertFingerprintSHA256 供客户端固定证书用（自签场景尤其重要）。
	CertFingerprintSHA256 string `json:"cert_fingerprint_sha256"`
	// 协议开关状态：前端据此决定显示哪些地址、哪些平台标「暂不支持」
	EnableDoH      bool `json:"enable_doh"`
	EnableDoT      bool `json:"enable_dot"`
	EnablePlainDNS bool `json:"enable_plain_dns"`
	// EnableDoQ / EnableDoH3 跟随 DoT / DoH 开关，另加「config 里配了监听」这个前提。
	EnableDoQ      bool            `json:"enable_doq"`
	EnableDoH3     bool            `json:"enable_doh3"`
	PlatformGuides []platformGuide `json:"platform_guides"`
}

type platformGuide struct {
	Platform string `json:"platform"`
	// Supported=false 表示该平台需要的协议都被关掉了，
	// 前端显示「暂不支持」而不是给一段照做也用不了的步骤。
	Supported bool     `json:"supported"`
	Steps     []string `json:"steps"`
}

// buildDNSConfig 构造某个 client_id 的接入配置。
// protoSet 是当前开启的接入协议集合。
type protoSet struct {
	DoH   bool
	DoT   bool
	Plain bool
	// DoQ / DoH3 在开关之外还要求 config.yaml 里配了对应监听：
	// 没配监听就下发地址，用户照填必然连不上。
	DoQ  bool
	DoH3 bool
}

// dnsPorts 是下发给客户端的实际端口。
//
// 单独打包而不是传两个 int：DoH 端口必须跟着 config.yaml 的 listen_doh 走，
// 否则管理员把 DoH 换到 8443 后，面板还在发 https://host/dns-query（隐含 443），
// 用户照填连不上，而服务端日志只有握手失败 —— 这种错最难自查。
type dnsPorts struct {
	DoH int
	DoT int
	DoQ int
	// DoH3 与 DoH 同端口号（传输层不同），单独存一份便于文案里显示。
	DoH3 int
}

// urlHost 按端口拼「主机[:端口]」：标准端口省略端口号，
// 非标准端口必须显式带上，否则客户端会回落到默认端口。
func (p dnsPorts) urlHost(host string) string {
	if p.DoH == 443 || p.DoH <= 0 {
		return host
	}
	return fmt.Sprintf("%s:%d", host, p.DoH)
}

func buildDNSConfig(domain, clientID, fingerprint string, ports dnsPorts, wildcard, rootReserved bool, protos protoSet) dnsConfigJSON {
	subHost := clientID + "." + domain
	if clientID == "" {
		subHost = domain
	}

	// 证书没有通配符时，<client_id>.domain 这种形态一律不可用：客户端做主机名
	// 校验会直接拒绝握手，而服务端只会看到「握手超时/EOF」，看不出是证书问题。
	// 所以这时主推裸域名 + 路径段带 client_id 的形态 —— 它只依赖单域名证书，
	// 身份照样能从路径里拿到，规则不受影响。
	dohURL := "https://" + ports.urlHost(subHost) + "/dns-query"
	dohURLPath := "https://" + ports.urlHost(domain) + "/dns-query/" + clientID
	dothost := subHost
	if !wildcard && clientID != "" {
		dohURL = dohURLPath
		// DoT 没有路径段可放 client_id，只能用裸域名；
		// 此时租户靠来源 IP 识别，未登记 IP 的客户端会走兜底策略。
		dothost = domain
	} else if rootReserved {
		// 主域名被拿去当网站了，路径式地址指向的是网站而不是 DNS 服务，
		// 发出去只会让用户白试。有通配符时子域名形态本来就能用，
		// 所以这里直接不发 —— 前端见到空值就不渲染那一栏。
		dohURLPath = ""
	}

	cfg := dnsConfigJSON{
		ClientID:              clientID,
		Domain:                domain,
		CertFingerprintSHA256: fingerprint,
		EnableDoH:             protos.DoH,
		EnableDoT:             protos.DoT,
		EnablePlainDNS:        protos.Plain,
	}
	// 关掉的协议一律不下发地址：页面上没地址，用户就没法照着填一个连不上的东西
	if protos.DoH {
		cfg.DoHURL = dohURL
		cfg.DoHURLPath = dohURLPath
	}
	// DoT 端口先算好：平台指引里也要用（Android 换端口会连不上，得告知）
	dotPort := ports.DoT
	if dotPort <= 0 {
		dotPort = 853
	}
	if protos.DoT {
		cfg.DoTHost = dothost
		cfg.DoTPort = dotPort
		cfg.DoTAddr = fmt.Sprintf("%s:%d", dothost, dotPort)
	}
	// DoQ 与 DoT 用同一个主机名策略：QUIC 没有路径段，租户只能靠 SNI，
	// 所以证书无通配符时同样只能给裸域名（此时租户靠来源 IP 识别）。
	if protos.DoQ {
		doqPort := ports.DoQ
		if doqPort <= 0 {
			doqPort = 853
		}
		cfg.DoQHost = dothost
		cfg.DoQPort = doqPort
		cfg.DoQAddr = fmt.Sprintf("%s:%d", dothost, doqPort)
		cfg.EnableDoQ = true
	}
	// DoH3 与 DoH 同地址、同 URL，只是传输换成 QUIC，所以只置一个标志，
	// 由前端在 DoH 那一栏加「支持 HTTP/3」的说明。
	if protos.DoH3 && protos.DoH {
		cfg.DoH3Supported = true
		cfg.EnableDoH3 = true
	}
	cfg.PlatformGuides = platformGuides(clientID, domain, dohURL, dothost, wildcard, protos, dotPort, cfg.DoQAddr)
	return cfg
}

// platformGuides 返回各平台的配置步骤。
//
// 文案跟随真实系统菜单路径：这几段文字是用户唯一会照着做的地方，
// 写得含糊等于没写。
// platformGuides 返回各平台的配置步骤。
//
// dohURL / dothost 由 buildDNSConfig 按证书能力算好后传进来 —— 文案是用户唯一
// 会照着做的地方，写一个连不上的地址等于没写。
func platformGuides(clientID, domain, dohURL, dothost string, wildcard bool, protos protoSet, dotPort int, doqAddr string) []platformGuide {
	host := dothost
	dotNote := ""
	if !wildcard && clientID != "" {
		dotNote = "（当前证书不含通配符，DoT 只能用主域名；如需按用户区分请改用 DoH）"
	}
	// DoQ 的租户识别只能靠 SNI（QUIC 没有路径段），所以它的限制与 DoT 完全一样。
	doqNote := ""
	if !wildcard && clientID != "" {
		doqNote = "（当前证书不含通配符，DoQ 只能用主域名，且租户靠来源 IP 识别）"
	}

	// unsupported 是「这个平台需要的协议都没开」时的统一文案。
	// 不给步骤：给了也照做不成，只会让用户以为是自己配错了。
	unsupported := func(platform, need string) platformGuide {
		return platformGuide{
			Platform:  platform,
			Supported: false,
			Steps:     []string{need + " 当前未开放，请联系管理员开启后再按指引配置。"},
		}
	}

	out := make([]platformGuide, 0, 5)

	// iOS / macOS：系统级加密 DNS 走 DoH（描述文件）或 DoT
	if protos.DoH {
		out = append(out,
			platformGuide{
				Platform:  "ios",
				Supported: true,
				Steps: []string{
					"打开「设置」→「通用」→「VPN 与设备管理」→「DNS」",
					"选择「加密 DNS」，填入 DoH 地址：" + dohURL,
					"或安装描述文件（.mobileconfig）批量下发，适合多设备",
				},
			},
			platformGuide{
				Platform:  "macos",
				Supported: true,
				Steps: []string{
					"打开「系统设置」→「网络」→ 选中当前连接 →「详细信息」",
					"切到「DNS」标签页，添加 DNS 服务器",
					"如需加密 DNS，安装描述文件并填入：" + dohURL,
				},
			},
		)
	} else if protos.DoT {
		out = append(out,
			platformGuide{
				Platform:  "ios",
				Supported: true,
				Steps: []string{
					"打开「设置」→「通用」→「VPN 与设备管理」→「DNS」",
					"选择「加密 DNS」→「手动」，主机名填：" + host + dotNote,
					"或安装描述文件（.mobileconfig）批量下发",
				},
			},
			platformGuide{
				Platform:  "macos",
				Supported: true,
				Steps: []string{
					"打开「系统设置」→「网络」→ 选中当前连接 →「详细信息」",
					"切到「DNS」标签页，添加 DNS 服务器",
					"加密 DNS 主机名填：" + host + dotNote,
				},
			},
		)
	} else {
		out = append(out,
			unsupported("ios", "DoH / DoT"),
			unsupported("macos", "DoH / DoT"),
		)
	}

	// Android 的「私人 DNS」只支持 DoT，且端口写死 853 —— 系统不给改。
	// 所以 listen_dot 一旦离开 853，Android 这条路就断了，必须如实告知，
	// 不能让用户照着填一个连不上的主机名。
	if protos.DoT {
		androidHint := "（Android 使用 DoT，端口固定 853）" + dotNote
		if dotPort != 853 {
			androidHint = fmt.Sprintf("（注意：服务端 DoT 已改到 %d 端口，Android「私人 DNS」固定用 853，"+
				"无法连接；此设备请改用 DoH）", dotPort) + dotNote
		}
		out = append(out, platformGuide{
			Platform:  "android",
			Supported: true,
			Steps: []string{
				"打开「设置」→「网络和互联网」→「私人 DNS」",
				"选择「私人 DNS 提供商主机名」",
				"填入：" + host + androidHint,
			},
		})
	} else {
		out = append(out, unsupported("android", "DoT（Android 私人 DNS 只支持 DoT）"))
	}

	// Windows 11 原生 DoH；没有 DoH 时退到手动填 IP（需要明文 DNS 或本地代理）
	if protos.DoH {
		out = append(out, platformGuide{
			Platform:  "windows",
			Supported: true,
			Steps: []string{
				"打开「设置」→「网络和 Internet」→「以太网 / Wi-Fi」",
				"编辑 DNS 服务器分配，改为手动",
				"Windows 11 原生支持 DoH：填入解析地址后在「DNS over HTTPS」下拉里选择「开（自动模板）」",
				"或使用支持 DoH 的浏览器 / 客户端填入：" + dohURL,
			},
		})
	} else if protos.Plain {
		out = append(out, platformGuide{
			Platform:  "windows",
			Supported: true,
			Steps: []string{
				"打开「设置」→「网络和 Internet」→「以太网 / Wi-Fi」",
				"编辑 DNS 服务器分配，改为手动，填入本服务的公网 IP",
				"明文 DNS 不做加密，建议只在可信网络下使用",
			},
		})
	} else {
		out = append(out, unsupported("windows", "DoH / 普通 DNS"))
	}

	// 路由器：优先 DoT/DoH，只支持明文的路由器退到明文
	switch {
	case protos.DoT:
		out = append(out, platformGuide{
			Platform:  "router",
			Supported: true,
			Steps: []string{
				"在路由器 DHCP 设置里把 DNS 指向本服务",
				"若路由器支持 DoT/DoH，直接填入：" + host + dotNote,
			},
		})
	case protos.DoH:
		out = append(out, platformGuide{
			Platform:  "router",
			Supported: true,
			Steps: []string{
				"在路由器 DHCP 设置里把 DNS 指向本服务",
				"若路由器支持 DoH，填入：" + dohURL,
			},
		})
	case protos.Plain:
		out = append(out, platformGuide{
			Platform:  "router",
			Supported: true,
			Steps: []string{
				"在路由器 DHCP 设置里把 DNS 指向本服务的公网 IP",
				"若只支持明文 DNS，请先把出口 IP 加入「来源 IP 归属」",
			},
		})
	default:
		out = append(out, unsupported("router", "DoT / DoH / 普通 DNS"))
	}

	// AdGuard Home / dnsproxy / mosdns 这类客户端是 DoQ 的主要使用者
	// （Android 私人 DNS 和多数系统解析器都不支持 DoQ，所以不放进手机平台段）。
	if protos.DoQ && doqAddr != "" {
		out = append(out, platformGuide{
			Platform:  "doq",
			Supported: true,
			Steps: []string{
				"适用于支持 DoQ 的客户端：AdGuard Home、dnsproxy、mosdns、部分路由器固件",
				"DoQ 地址（走 UDP，与 DoT 同端口号、传输层不同）：" + doqAddr + doqNote,
				"Android「私人 DNS」与 iOS 描述文件都不支持 DoQ，手机请用 DoH/DoT",
			},
		})
	}

	return out
}

// protos 返回当前开启的接入协议。
//
// 读不到设置或三个全关时按「全开」处理：这个函数决定用户能不能拿到地址，
// 因为一次读库失败就返回「全部未开放」，用户会以为服务挂了。
// rootDomainReserved 返回「主域名是否已被用作网站/前端域名」。
//
// 读不到设置时按 false 处理：宁可多发一个可能用不了的地址，
// 也不要在读库失败时把唯一可用的接入地址藏起来。
func (s *Server) rootDomainReserved(ctx context.Context) bool {
	set, err := s.store.GetSettings(ctx)
	if err != nil || set == nil {
		return false
	}
	return set.RootDomainReserved
}

func (s *Server) protos(ctx context.Context) protoSet {
	set, err := s.store.GetSettings(ctx)
	if err != nil || set == nil {
		return protoSet{DoH: true, DoT: true, Plain: true, DoQ: s.cfg.DNS.ListenDOQ != "", DoH3: s.cfg.DNS.ListenDOH3 != ""}
	}
	ps := protoSet{DoH: set.EnableDoH, DoT: set.EnableDoT, Plain: set.EnablePlainDNS}
	if !ps.DoH && !ps.DoT && !ps.Plain {
		return protoSet{DoH: true, DoT: true, Plain: true, DoQ: s.cfg.DNS.ListenDOQ != "", DoH3: s.cfg.DNS.ListenDOH3 != ""}
	}
	// DoQ 跟随 DoT、DoH3 跟随 DoH（与 dnsd 侧 protoEnabled 的判定一致），
	// 另外要求 config.yaml 里真的配了监听 —— 没起监听却下发地址，
	// 用户照着填一定连不上，而服务端连日志都不会有。
	ps.DoQ = ps.DoT && s.cfg.DNS.ListenDOQ != ""
	ps.DoH3 = ps.DoH && s.cfg.DNS.ListenDOH3 != ""
	return ps
}
