package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
)

// buildDNSProfile 生成 iOS / macOS 的加密 DNS 描述文件（.mobileconfig）。
//
// 载荷类型 com.apple.dnsSettings.managed 是 iOS 14 / macOS 11 起系统原生支持的
// 「加密 DNS」配置，装上之后系统级生效，不用逐台手填地址 —— 这正是 iOS 指引里
// 「或安装描述文件批量下发」那句话承诺的东西。
//
// ServerAddresses 故意不给：DNSProtocol=HTTPS 时它只是回退用的明文地址，
// 而本系统不提供明文 DNS；省略后系统直接走 DoH。
func buildDNSProfile(clientID string, p profileTarget) string {
	profileID := stableUUID("profile:" + clientID)
	dnsID := stableUUID("dns:" + clientID)

	// 载荷体按协议不同：HTTPS 用 ServerURL，TLS 用 ServerName。
	// 两者都不能为空 —— 空地址的描述文件装上之后 iOS 会显示「已安装」但解析全挂，
	// 比不生成更糟（用户以为配好了）。
	var settings strings.Builder
	switch p.Protocol {
	case "TLS":
		settings.WriteString(`                <key>DNSProtocol</key>
                <string>TLS</string>
                <key>ServerName</key>
                <string>`)
		settings.WriteString(xmlEscape(p.ServerName))
		settings.WriteString(`</string>
`)
	default:
		settings.WriteString(`                <key>DNSProtocol</key>
                <string>HTTPS</string>
                <key>ServerURL</key>
                <string>`)
		settings.WriteString(xmlEscape(p.ServerURL))
		settings.WriteString(`</string>
`)
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>PayloadContent</key>
    <array>
        <dict>
            <key>DNSSettings</key>
            <dict>
`)
	b.WriteString(settings.String())
	b.WriteString(`            </dict>
            <key>PayloadDescription</key>
            <string>`)
	b.WriteString(xmlEscape(p.Describe()))
	b.WriteString(`</string>
            <key>PayloadDisplayName</key>
            <string>DNSForge 加密 DNS</string>
            <key>PayloadIdentifier</key>
            <string>com.dnsforge.dns.`)
	b.WriteString(xmlEscape(clientID))
	b.WriteString(`</string>
            <key>PayloadType</key>
            <string>com.apple.dnsSettings.managed</string>
            <key>PayloadUUID</key>
            <string>`)
	b.WriteString(dnsID)
	b.WriteString(`</string>
            <key>PayloadVersion</key>
            <integer>1</integer>
        </dict>
    </array>
    <key>PayloadDisplayName</key>
    <string>DNSForge · `)
	b.WriteString(xmlEscape(clientID))
	b.WriteString(`</string>
    <key>PayloadDescription</key>
    <string>把本设备的加密 DNS 指向 DNSForge（`)
	b.WriteString(xmlEscape(p.Protocol))
	b.WriteString(`）。`)
	if p.Fingerprint != "" {
		// 指纹写进描述里，方便用户事后核对装的是哪张证书。
		b.WriteString(`证书 SHA-256 指纹：`)
		b.WriteString(xmlEscape(p.Fingerprint))
	}
	b.WriteString(`</string>
    <key>PayloadIdentifier</key>
    <string>com.dnsforge.profile.`)
	b.WriteString(xmlEscape(clientID))
	b.WriteString(`</string>
    <key>PayloadRemovalDisallowed</key>
    <false/>
    <key>PayloadType</key>
    <string>Configuration</string>
    <key>PayloadUUID</key>
    <string>`)
	b.WriteString(profileID)
	b.WriteString(`</string>
    <key>PayloadVersion</key>
    <integer>1</integer>
</dict>
</plist>
`)
	return b.String()
}

// profileTarget 是描述文件要指向的目标。
//
// Protocol 只能是 HTTPS / TLS：明文（Cleartext）本系统不对外提供地址，
// 生成一份「没有地址的明文配置」没有意义。
type profileTarget struct {
	Protocol    string // HTTPS | TLS
	ServerURL   string // Protocol=HTTPS 时使用
	ServerName  string // Protocol=TLS 时使用
	Fingerprint string
}

// Describe 生成载荷里那句人话说明。
func (p profileTarget) Describe() string {
	if p.Protocol == "TLS" {
		return "启用 DNSForge 加密 DNS（DoT），系统级生效"
	}
	return "启用 DNSForge 加密 DNS（DoH），系统级生效"
}

// Usable 判断这个目标是否可用：地址为空就是不可用。
func (p profileTarget) Usable() bool {
	if p.Protocol == "TLS" {
		return p.ServerName != ""
	}
	return p.ServerURL != ""
}

// stableUUID 由种子确定性地生成一个 UUID v4 形态的字符串。
//
// 必须稳定：同一个 client_id 每次下载都得到相同的 UUID，否则重复安装会在
// 「VPN 与设备管理」里堆出一串同名描述文件。
func stableUUID(seed string) string {
	sum := sha256.Sum256([]byte("aegis-dns/" + seed))
	// 按 RFC 4122 设置版本（4）与变体位
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// xmlEscape 转义 plist 里会出现的特殊字符。
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// handleMobileConfig 下发当前用户的 iOS/macOS 加密 DNS 描述文件。
//
// 优先 DoH（描述文件形态最稳，Apple 原生支持），DoH 关掉时退到 DoT。
// 两个都关就返回 409 —— 生成一份地址为空的描述文件比不生成更糟：
// 装上之后系统显示「已安装」，但解析全挂，用户完全看不出哪里错了。
func (s *Server) handleMobileConfig(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	protos := s.protos(r.Context())
	cfg := buildDNSConfig(s.domain(r), u.ClientID, s.fingerprint(), s.dnsPorts(), s.coversSubdomains(r), protos)

	// proto 让用户自己选生成哪种描述文件：
	//   doh → DNSProtocol=HTTPS（默认，Apple 支持最稳）
	//   dot → DNSProtocol=TLS
	// 不传就按「DoH 优先、其次 DoT」自动挑，兼容老页面。
	want := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("proto")))
	target := profileTarget{Fingerprint: cfg.CertFingerprintSHA256}
	switch want {
	case "dot", "tls":
		if !protos.DoT || cfg.DoTHost == "" {
			writeError(w, http.StatusConflict, CodeFeatureDisabled, "DoT 未开启，无法生成 DoT 描述文件", nil)
			return
		}
		target.Protocol = "TLS"
		target.ServerName = cfg.DoTHost
	case "doh", "https":
		if !protos.DoH || cfg.DoHURL == "" {
			writeError(w, http.StatusConflict, CodeFeatureDisabled, "DoH 未开启，无法生成 DoH 描述文件", nil)
			return
		}
		target.Protocol = "HTTPS"
		target.ServerURL = cfg.DoHURL
	default:
		switch {
		case protos.DoH && cfg.DoHURL != "":
			target.Protocol = "HTTPS"
			target.ServerURL = cfg.DoHURL
		case protos.DoT && cfg.DoTHost != "":
			target.Protocol = "TLS"
			target.ServerName = cfg.DoTHost
		}
	}
	if !target.Usable() {
		writeError(w, http.StatusConflict, CodeFeatureDisabled,
			"当前没有可用的加密 DNS 协议（DoH / DoT 均已关闭），无法生成描述文件", nil)
		return
	}

	body := buildDNSProfile(u.ClientID, target)

	// x-apple-aspen-config 是 iOS/macOS 识别描述文件的 MIME 类型，
	// 用别的类型 Safari 只会当普通文本打开。
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="dnsforge-%s-%s.mobileconfig"`, u.ClientID, strings.ToLower(target.Protocol)))
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
