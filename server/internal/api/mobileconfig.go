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
func buildDNSProfile(clientID, dohURL, fingerprint string) string {
	profileID := stableUUID("profile:" + clientID)
	dnsID := stableUUID("dns:" + clientID)

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
                <key>DNSProtocol</key>
                <string>HTTPS</string>
                <key>ServerURL</key>
                <string>`)
	b.WriteString(xmlEscape(dohURL))
	b.WriteString(`</string>
            </dict>
            <key>PayloadDescription</key>
            <string>启用 DNSForge 加密 DNS（DoH），系统级生效</string>
            <key>PayloadDisplayName</key>
            <string>DNSForge 加密 DNS</string>
            <key>PayloadIdentifier</key>
            <string>com.dnsforge.doh.`)
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
    <string>把本设备的加密 DNS 指向 DNSForge。`)
	if fingerprint != "" {
		// 指纹写进描述里，方便用户事后核对装的是哪张证书。
		b.WriteString(`证书 SHA-256 指纹：`)
		b.WriteString(xmlEscape(fingerprint))
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
func (s *Server) handleMobileConfig(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	cfg := buildDNSConfig(s.domain(r), u.ClientID, s.fingerprint(), s.dotPort(), s.coversSubdomains(r))

	body := buildDNSProfile(u.ClientID, cfg.DoHURL, cfg.CertFingerprintSHA256)

	// x-apple-aspen-config 是 iOS/macOS 识别描述文件的 MIME 类型，
	// 用别的类型 Safari 只会当普通文本打开。
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="dnsforge-%s.mobileconfig"`, u.ClientID))
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
