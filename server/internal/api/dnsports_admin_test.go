package api

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/config"
)

// 这个文件锁住端口相关的纯逻辑。
//
// 改 DNS 端口是「改错就全网断网」的操作，凡是能算的都必须能测 ——
// 尤其是「撞车判定」和「只改一行不碰其它配置」这两条。

// TestParseListenPort 断言各种监听地址写法都能取到端口。
func TestParseListenPort(t *testing.T) {
	cases := map[string]int{
		":443":            443,
		"0.0.0.0:8443":    8443,
		"127.0.0.1:14443": 14443,
		"[::]:53":         53,
		"":                0,
		"443":             0, // 没有冒号，不是合法监听地址
		":0":              0,
		":99999":          0, // 超范围
		":abc":            0,
	}
	for in, want := range cases {
		if got := parseListenPort(in); got != want {
			t.Errorf("parseListenPort(%q) = %d，期望 %d", in, got, want)
		}
	}
}

// TestFormatListenKeepsHost 断言换端口时保留原绑定地址。
//
// 有人把 dnsd 绑在 127.0.0.1 后面挂反代，一律改成 ":port"
// 会把那种部署改坏（对外全不通）。
func TestFormatListenKeepsHost(t *testing.T) {
	cases := []struct {
		old  string
		port int
		want string
	}{
		{":443", 8443, ":8443"},
		{"0.0.0.0:443", 8443, "0.0.0.0:8443"},
		{"127.0.0.1:443", 8443, "127.0.0.1:8443"},
		{"[::]:443", 8443, "[::]:8443"},
		{"", 8443, ":8443"},
	}
	for _, c := range cases {
		if got := formatListen(c.old, c.port); got != c.want {
			t.Errorf("formatListen(%q,%d) = %q，期望 %q", c.old, c.port, got, c.want)
		}
	}
}

// TestReplaceListenDOOnlyTouchesOneLine 断言只改 listen_doh 一行。
//
// 关键点：注释、字段顺序、相邻行都必须原样保留。
// 用 YAML marshal 重写整个文件会把注释全丢掉，用户的配置就毁了。
func TestReplaceListenDOOnlyTouchesOneLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := `# 这是注释，必须保留
dns:
  listen_udp: ":53"
  listen_tcp: ":53"
  listen_dot: ":853"     # 行尾注释也要保留
  listen_doh: ":443"
  cert_file: ""
api:
  listen: ":3000"
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceListenDO([]byte(orig), path, ":8443"); err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, `listen_doh: ":8443"`) {
		t.Fatalf("listen_doh 没改成 8443：\n%s", s)
	}
	// 其它行和注释必须一字不差
	for _, must := range []string{
		"# 这是注释，必须保留",
		`listen_udp: ":53"`,
		`listen_dot: ":853"     # 行尾注释也要保留`,
		`listen: ":3000"`,
	} {
		if !strings.Contains(s, must) {
			t.Errorf("原有内容被破坏，缺少：%q", must)
		}
	}
}

// TestReplaceListenDOAtomic 断言不会留下 .tmp 残余。
func TestReplaceListenDOAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := "dns:\n  listen_doh: \":443\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceListenDO([]byte(orig), path, ":9443"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("不该残留 .tmp 文件")
	}
}

// TestReplaceListenDOMissing 断言配置里没有 listen_doh 时明确报错。
//
// 静默追加一行是不可接受的：不知道插在哪一层，插错了 YAML 就废了。
func TestReplaceListenDOMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := "dns:\n  listen_udp: \":53\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	err := replaceListenDO([]byte(orig), path, ":8443")
	if err == nil {
		t.Fatal("缺 listen_doh 时必须报错")
	}
	if !strings.Contains(err.Error(), "listen_doh") {
		t.Fatalf("错误信息应指明缺哪个字段，实际 %q", err.Error())
	}
	// 文件不能被改动
	got, _ := os.ReadFile(path)
	if string(got) != orig {
		t.Fatal("失败时不该改动配置文件")
	}
}

// TestProbePortRejectsInvalid 断言越界端口被拒。
func TestProbePortRejectsInvalid(t *testing.T) {
	for _, p := range []int{0, -1, 65536, 100000} {
		res := probePort(p, nil)
		if res.OK {
			t.Errorf("端口 %d 应被判为不可用", p)
		}
		if res.Reason == "" {
			t.Errorf("端口 %d 应给出原因", p)
		}
	}
}

// TestProbePortSelfPortIsNotConflict 断言「本服务自己占着的端口」不算冲突。
//
// 这条最容易写错：保存时旧 dnsd 还占着端口，如果一律判成冲突，
// 每次保存都会报「端口已被占用」，功能等于不可用。
func TestProbePortSelfPortIsNotConflict(t *testing.T) {
	// 用一个几乎不可能被占用的高位端口做「自己的端口」
	const p = 59999
	res := probePort(p, map[int]bool{p: true})
	if !res.OK {
		t.Fatalf("自己的端口不该判冲突，实际 %q", res.Reason)
	}
	if !res.Current {
		t.Fatal("应标记为 current")
	}
}

// TestProbePortDetectsOccupied 断言真被占用时能报出来。
func TestProbePortDetectsOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("无法绑定测试端口:", err)
	}
	defer ln.Close()
	p := ln.Addr().(*net.TCPAddr).Port
	res := probePort(p, nil)
	if res.OK {
		t.Fatalf("端口 %d 正在被占用，不该判为可用", p)
	}
	if !strings.Contains(res.Reason, "占用") {
		t.Fatalf("原因应提到占用，实际 %q", res.Reason)
	}
}

// TestValidateDoHPortCollidesWithOtherDNS 断言与其它协议端口撞车会被拒。
//
// dnsd 是单进程绑全部端口，两个协议用同一个端口必然有一个绑不上，
// 结果就是 dnsd 起不来 —— 这正是「冲突导致停机」的典型形态。
func TestValidateDoHPortCollidesWithOtherDNS(t *testing.T) {
	s := newTestServerCfg()
	s.cfg.DNS.ListenUDP = ":53"
	s.cfg.DNS.ListenTCP = ":53"
	s.cfg.DNS.ListenDOT = ":853"
	s.cfg.DNS.ListenDOH = ":443"
	s.cfg.API.Listen = ":3000"
	s.cfg.API.TLSListen = ":8443"

	for _, c := range []struct {
		port int
		want string
	}{
		{853, "DoT"},
		{53, "明文"},
	} {
		_, err := s.validateDoHPort(c.port)
		if err == nil {
			t.Errorf("端口 %d 与 %s 撞车，应被拒绝", c.port, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("错误信息应指明与哪个协议撞车，实际 %q", err.Error())
		}
	}
}

// TestValidateDoHPortRejectsPanelPort 断言不能占用面板自己的端口。
//
// 占了面板端口，保存之后管理界面就打不开了 —— 只能上服务器改回来。
func TestValidateDoHPortRejectsPanelPort(t *testing.T) {
	s := newTestServerCfg()
	s.cfg.DNS.ListenUDP = ":53"
	s.cfg.DNS.ListenTCP = ":53"
	s.cfg.DNS.ListenDOT = ":853"
	s.cfg.DNS.ListenDOH = ":443"
	s.cfg.API.Listen = ":3000"
	s.cfg.API.TLSListen = ":8443"

	_, err := s.validateDoHPort(3000)
	if err == nil {
		t.Fatal("3000 是面板端口，应被拒绝")
	}
	if !strings.Contains(err.Error(), "面板") {
		t.Fatalf("错误信息应提到面板端口，实际 %q", err.Error())
	}
}

// TestDNSConfigReflectsNewPort 断言改了 listen_doh 后下发的地址跟着变。
//
// 这是「面板能改端口」这件事的最后一环：改完地址必须是新的，
// 否则用户拿到旧地址照样连不上。
func TestDNSConfigReflectsNewPort(t *testing.T) {
	s := newTestServerCfg()
	s.cfg.DNS.ListenDOH = ":8443"
	s.cfg.DNS.ListenDOT = ":853"

	ports := s.dnsPorts()
	if ports.DoH != 8443 {
		t.Fatalf("dnsPorts().DoH 应读 listen_doh，实际 %d", ports.DoH)
	}
	cfg := buildDNSConfig("example.com", "abc123", "", ports, true, false, allProtos)
	want := "https://abc123.example.com:8443/dns-query"
	if cfg.DoHURL != want {
		t.Fatalf("期望 %q，实际 %q", want, cfg.DoHURL)
	}
}

var _ = fmt.Sprintf

// newTestServerCfg 造一个带最小配置的 Server，避免 cfg 为 nil。
func newTestServerCfg() *Server {
	return &Server{cfg: &config.Config{}}
}
