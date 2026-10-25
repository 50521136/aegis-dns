package api

import (
	"strings"
	"testing"
)

// 这组测试锁住「DoQ / DoH3 的地址下发」这几条容易静默出错的规则。
//
// 它们都不会报错，只会让用户照着一个连不上的地址去配：
//
//   - DoQ 没配监听却下发地址 → 客户端连上去没有任何响应（dnsd 根本没监听 UDP 端口）；
//   - DoQ 在证书无通配符时给了子域名 → QUIC 没有路径段，SNI 主机名校验直接失败，
//     服务端只看到握手失败；
//   - DoH3 另造一个 h3:// 地址 → 除了 dnsproxy 没有任何客户端认这种写法，
//     H3 与 DoH 本来就是同一个 URL；
//   - DoT 关掉后 DoQ 仍下发地址 → 用户配好了却被 REFUSED。

func TestDoQAddrFollowsDoTPort(t *testing.T) {
	// DoQ 与 DoT 共用 853 端口号，但各自由自己的配置项决定。
	protos := protoSet{DoH: true, DoT: true, Plain: true, DoQ: true}
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853, DoQ: 853}, true, false, protos)

	if !cfg.EnableDoQ {
		t.Fatal("DoQ 开启时应置 enable_doq")
	}
	if cfg.DoQAddr != "abc123.example.com:853" {
		t.Fatalf("DoQ 地址应带 SNI 子域名，实际 %q", cfg.DoQAddr)
	}
	if cfg.DoQPort != 853 {
		t.Fatalf("DoQ 端口应为 853，实际 %d", cfg.DoQPort)
	}

	// 非默认端口必须如实下发，不能写死 853。
	cfg = buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853, DoQ: 8853}, true, false, protos)
	if cfg.DoQAddr != "abc123.example.com:8853" {
		t.Fatalf("DoQ 换了端口后地址必须跟着走，实际 %q", cfg.DoQAddr)
	}
}

func TestDoQUsesBareDomainWithoutWildcard(t *testing.T) {
	// QUIC 没有路径段可放 client_id，租户只能靠 SNI 首段。
	// 证书无通配符时给子域名 = 客户端主机名校验失败，握手在客户端就被毙掉，
	// 服务端日志只有一条握手失败，极难定位。此时必须与 DoT 一样退回裸域名。
	protos := protoSet{DoH: true, DoT: true, Plain: true, DoQ: true}
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853, DoQ: 853}, false, false, protos)

	if cfg.DoQAddr != "example.com:853" {
		t.Fatalf("无通配符时 DoQ 只能用裸域名，实际 %q", cfg.DoQAddr)
	}
	if strings.Contains(cfg.DoQAddr, "abc123") {
		t.Fatal("无通配符时 DoQ 地址里不能出现 client_id 子域名")
	}
	// 与 DoT 的主机名策略必须一致，否则两个 853 协议给出的身份识别方式不同，
	// 用户会以为是服务端随机行为。
	if cfg.DoTAddr != cfg.DoQAddr {
		t.Fatalf("DoT 与 DoQ 的主机名策略应一致：dot=%q doq=%q", cfg.DoTAddr, cfg.DoQAddr)
	}
}

func TestDoQNotAdvertisedWhenDisabled(t *testing.T) {
	// DoQ 跟随 enable_dot：DoT 关了 DoQ 也必须是空，否则用户配好了被 REFUSED。
	protos := protoSet{DoH: true, DoT: false, Plain: true, DoQ: false}
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853, DoQ: 853}, true, false, protos)

	if cfg.EnableDoQ {
		t.Fatal("DoQ 未开启时不能置 enable_doq")
	}
	if cfg.DoQAddr != "" {
		t.Fatalf("DoQ 未开启时不能下发地址，实际 %q", cfg.DoQAddr)
	}
}

func TestDoH3SharesDoHURLAndNeedsDoH(t *testing.T) {
	// H3 与 DoH 是同一个 URL，只是传输不同：只置标志，不造新地址。
	protos := protoSet{DoH: true, DoT: true, Plain: true, DoH3: true}
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853, DoH3: 443}, true, false, protos)

	if !cfg.DoH3Supported || !cfg.EnableDoH3 {
		t.Fatal("H3 开启时应置 doh3_supported / enable_doh3")
	}
	if cfg.DoHURL == "" {
		t.Fatal("H3 依赖 DoH 的 URL，此时 DoHURL 不能为空")
	}

	// DoH 关掉时 H3 不能单独存在：客户端拿到的地址就是 DoH 的地址，
	// 「DoH 关了但 H3 还开着」在客户端看来就是随机故障。
	protos = protoSet{DoH: false, DoT: true, Plain: true, DoH3: true}
	cfg = buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853, DoH3: 443}, true, false, protos)
	if cfg.DoH3Supported || cfg.EnableDoH3 {
		t.Fatal("DoH 关闭时不能宣称支持 H3")
	}
}

func TestPlatformGuidesMentionDoQ(t *testing.T) {
	protos := protoSet{DoH: true, DoT: true, Plain: true, DoQ: true}
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853, DoQ: 853}, true, false, protos)

	var doqGuide *platformGuide
	for i := range cfg.PlatformGuides {
		if cfg.PlatformGuides[i].Platform == "doq" {
			doqGuide = &cfg.PlatformGuides[i]
		}
	}
	if doqGuide == nil {
		t.Fatal("DoQ 开启时应给出 doq 平台指引")
	}
	if !doqGuide.Supported {
		t.Fatal("DoQ 指引应为 supported")
	}
	joined := strings.Join(doqGuide.Steps, " ")
	if !strings.Contains(joined, cfg.DoQAddr) {
		t.Fatalf("DoQ 指引里必须出现真实地址 %q，实际：%s", cfg.DoQAddr, joined)
	}
	// 手机平台不支持 DoQ 这件事必须写在指引里，否则用户会去手机设置里找。
	if !strings.Contains(joined, "Android") {
		t.Fatalf("DoQ 指引应说明 Android 不支持，实际：%s", joined)
	}

	// 关掉 DoQ 时不该再出现这一页。
	protos.DoQ = false
	cfg = buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 443, DoT: 853}, true, false, protos)
	for _, g := range cfg.PlatformGuides {
		if g.Platform == "doq" {
			t.Fatal("DoQ 关闭时不该出现 doq 指引")
		}
	}
}
