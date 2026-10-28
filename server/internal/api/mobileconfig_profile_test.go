package api

import (
	"strings"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这组测试锁住「iOS / macOS 描述文件」的开关语义。
//
// 规则只有一条，但写错的代价极高：描述文件的载荷是全局协议的**下级**。
// 一旦允许「全局 DoH 关掉、描述文件里还能生成 HTTPS 载荷」，用户下到的文件
// 装上去 iOS 显示「已安装」，解析全挂 —— 线上没有任何报错可查，
// 用户也看不出是自己配错了还是服务坏了。
//
// 另一半是「管理员关掉下发后不能只靠前端隐藏按钮」：
// 知道 /me/mobileconfig 这个 URL 的人可以绕开界面直接拿文件。

func settingsWith(doh, dot, profile, profileDoH, profileDoT bool) *model.GlobalSettings {
	return &model.GlobalSettings{
		EnableDoH:      doh,
		EnableDoT:      dot,
		EnableProfile:  profile,
		ProfileDoH:     profileDoH,
		ProfileDoT:     profileDoT,
	}
}

func TestProfileGateIsSubordinateToGlobalProtocols(t *testing.T) {
	// 全局 DoH 关掉，描述文件的 HTTPS 开关还开着 → 生效值必须是 false。
	ps := profileGate(
		protoSet{DoH: false, DoT: true, Plain: true},
		settingsWith(false, true, true, true, true),
	)
	if ps.ProfileDoH {
		t.Error("全局 DoH 关闭时，描述文件不允许下发 HTTPS 载荷")
	}
	if !ps.ProfileDoT {
		t.Error("全局 DoT 开着、描述文件 TLS 也开着时，TLS 载荷应可用")
	}

	// 反过来：全局 DoT 关掉，描述文件 TLS 开关开着 → 同样是 false。
	ps = profileGate(
		protoSet{DoH: true, DoT: false, Plain: true},
		settingsWith(true, false, true, true, true),
	)
	if ps.ProfileDoT {
		t.Error("全局 DoT 关闭时，描述文件不允许下发 TLS 载荷")
	}
	if !ps.ProfileDoH {
		t.Error("全局 DoH 开着、描述文件 HTTPS 也开着时，HTTPS 载荷应可用")
	}

	// 描述文件开关自己关掉时，即使全局协议开着也不可用。
	ps = profileGate(
		protoSet{DoH: true, DoT: true, Plain: true},
		settingsWith(true, true, true, false, false),
	)
	if ps.ProfileDoH || ps.ProfileDoT {
		t.Error("描述文件里两种载荷都关掉时，两个生效值都必须是 false")
	}

	// 总开关关掉：路径整体关闭，载荷开关怎么设都不影响（但也不该被误读成可用）。
	ps = profileGate(
		protoSet{DoH: true, DoT: true, Plain: true},
		settingsWith(true, true, false, true, true),
	)
	if ps.Profile {
		t.Error("enable_ios_profile 关掉时 Profile 必须是 false")
	}
}

func TestProfileGateKeepsDefaultsWithoutSettings(t *testing.T) {
	// 读不到设置时 protos() 会走全开的兜底，profileGate 不能把它改成全关，
	// 否则一次数据库抖动就让所有 iOS 用户看不到下载按钮。
	ps := profileGate(protoSet{DoH: true, DoT: true, Plain: true, Profile: true, ProfileDoH: true, ProfileDoT: true}, nil)
	if !ps.Profile || !ps.ProfileDoH || !ps.ProfileDoT {
		t.Error("settings 为 nil 时应保留传入的兜底值")
	}
}

func TestDNSConfigCarriesProfileFlags(t *testing.T) {
	// 用户端拿到的 dns_config 必须带这三个字段，前端据此决定按钮显隐。
	protos := protoSet{DoH: true, DoT: true, Plain: true, Profile: true, ProfileDoH: true, ProfileDoT: false}
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 443, DoT: 853}, true, false, protos)
	if !cfg.EnableProfile {
		t.Error("应下发 enable_ios_profile=true")
	}
	if !cfg.ProfileDoH {
		t.Error("应下发 profile_doh=true")
	}
	if cfg.ProfileDoT {
		t.Error("应下发 profile_dot=false")
	}
}

func TestPlatformGuidesDropProfileHintWhenDisabled(t *testing.T) {
	// 关掉下发后，指引里不能再写「或安装描述文件」：
	// 用户会照着找一圈按钮然后找不到，最后来报障。
	protos := protoSet{DoH: true, DoT: true, Plain: true, Profile: false}
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 443, DoT: 853}, true, false, protos)

	ios := guideFor(t, cfg.PlatformGuides, "ios")
	for _, step := range ios.Steps {
		if strings.Contains(step, "描述文件") {
			t.Fatalf("描述文件未开启时 iOS 指引不该提描述文件：%q", step)
		}
	}

	// 开启后必须提一句，否则用户根本不知道有这条路。
	// 注意载荷开关也要一起打开：Profile 只是「这条路径存在」，
	// 真正决定文案的是 ProfileDoH（生效值）。
	protos.Profile = true
	protos.ProfileDoH = true
	cfg = buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 443, DoT: 853}, true, false, protos)
	ios = guideFor(t, cfg.PlatformGuides, "ios")
	joined := strings.Join(ios.Steps, "\n")
	if !strings.Contains(joined, "描述文件") {
		t.Error("描述文件开启后 iOS 指引应提到安装描述文件")
	}
	if !strings.Contains(joined, "DoH 描述文件") {
		t.Errorf("只开放 HTTPS 载荷时应明说是 DoH 描述文件：%q", joined)
	}
}

// 只开放 TLS 载荷时，iOS 指引不能提 DoH 描述文件 ——
// 提了等于让用户去找一个界面上根本不存在的按钮。
func TestPlatformGuidesOnlyMentionAllowedPayload(t *testing.T) {
	protos := protoSet{DoH: true, DoT: true, Plain: true, Profile: true, ProfileDoH: false, ProfileDoT: true}
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 443, DoT: 853}, true, false, protos)

	ios := strings.Join(guideFor(t, cfg.PlatformGuides, "ios").Steps, "\n")
	if strings.Contains(ios, "DoH 描述文件") {
		t.Errorf("HTTPS 载荷关闭时不该提 DoH 描述文件：%q", ios)
	}
	// 主路径是 DoH（全局开着），所以指引里仍是手填 DoH 地址，不该出现 DoT 描述文件。
	if !strings.Contains(ios, "DoH 地址") {
		t.Errorf("全局 DoH 开着时 iOS 主路径应是手填 DoH 地址：%q", ios)
	}
	mac := strings.Join(guideFor(t, cfg.PlatformGuides, "macos").Steps, "\n")
	if strings.Contains(mac, "安装描述文件") {
		t.Errorf("HTTPS 载荷关闭时 macOS 不该让用户装描述文件：%q", mac)
	}
}

func guideFor(t *testing.T, guides []platformGuide, platform string) platformGuide {
	t.Helper()
	for _, g := range guides {
		if g.Platform == platform {
			return g
		}
	}
	t.Fatalf("缺少 %s 平台指引", platform)
	return platformGuide{}
}

// 保存校验：描述文件开着，但两种载荷都不可用 → 拒绝保存。
// 这一条挡的是「用户页能下载、下下来装了不生效」的组合。
func TestValidateSettingsRejectsDeadProfile(t *testing.T) {
	base := func() *model.GlobalSettings {
		s := model.DefaultSettings()
		return &s
	}

	// 描述文件开、全局 DoH/DoT 都开、但描述文件里两个载荷都关 → 不可用，应拒绝
	s := base()
	s.EnableProfile = true
	s.ProfileDoH = false
	s.ProfileDoT = false
	if err := validateSettings(s); err == nil {
		t.Fatal("描述文件开着但两种载荷都关，应拒绝保存")
	}

	// 只留 DoT 一种载荷 → 允许
	s = base()
	s.EnableProfile = true
	s.ProfileDoH = false
	s.ProfileDoT = true
	s.EnableDoH = false // 全局 DoH 关掉，DoT 撑着
	if err := validateSettings(s); err != nil {
		t.Fatalf("只剩 DoT 载荷时应允许保存，实际 %v", err)
	}

	// 直接关掉描述文件下发 → 无论载荷怎么设都允许
	s = base()
	s.EnableProfile = false
	s.ProfileDoH = false
	s.ProfileDoT = false
	if err := validateSettings(s); err != nil {
		t.Fatalf("关掉描述文件下发后不该再校验载荷，实际 %v", err)
	}

	// DoH / DoT 全关、只留明文 53：合法操作，不能因为描述文件没载荷而拦下来。
	// 这时描述文件本来就没有可用载荷，用户页也不会给下载入口。
	s = base()
	s.EnableProfile = true
	s.EnableDoH = false
	s.EnableDoT = false
	s.EnablePlainDNS = true
	s.ProfileDoH = true
	s.ProfileDoT = true
	if err := validateSettings(s); err != nil {
		t.Fatalf("只留明文 53 时不该拦保存，实际 %v", err)
	}
}
