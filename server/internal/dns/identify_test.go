package dns

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
)

// 这个文件锁住一个曾经真实存在的缺陷：
//
//	DoH 的路径式地址 https://<域名>/dns-query/<client_id> 完全失效。
//
// 原因是 firstLabel 不校验形状 —— 裸域名 Host（"dns.example.com"）的首段
// 会被当成 client_id 认领下来，handleDoH 于是认为「Host 已经给出线索」，
// 把路径段整段跳过。之后 hint 查不到租户，回退到来源 IP。
//
// 危害有两层：
//  1. 文档承诺的路径式 DoH 用不了；
//  2. 若来源 IP 恰好登记在别的租户名下，请求会套用**别人的规则**。
//
// 所以下面既测纯函数，也测完整的线索解析顺序。
//
// 注意测试里用的 client_id 都是 [a-z2-7]{10} 形状的合法值。
// 设计文档里的示例 "k7m2p9xq4a" 含字符 '9'，与它自己的字符集规则矛盾
// （字符集排除 0/1/8/9），照抄那个示例会让测试对不上实现 —— 以字符集为准。

func TestFirstLabelOnlyAcceptsClientIDShape(t *testing.T) {
	cases := []struct {
		host string
		want string
		why  string
	}{
		{"k7m2pqx4ab.dns.example.com", "k7m2pqx4ab", "合法 client_id 作为子域首段"},
		{"K7M2PQX4AB.dns.example.com", "k7m2pqx4ab", "大小写不敏感"},
		{"k7m2pqx4ab.dns.example.com.", "k7m2pqx4ab", "去掉结尾的点"},
		{"k7m2pqx4ab", "k7m2pqx4ab", "裸 client_id"},
		{"dns.example.com", "", "裸域名：首段 'dns' 太短，不是 client_id，必须放空"},
		{"www.example.com", "", "常见子域不是 client_id"},
		{"520342.xyz", "", "数字域名：含字符集外的 '0'"},
		{"127.0.0.1", "", "IPv4 字面量"},
		{"127.0.0.1:443", "", "带端口"},
		{"[::1]:443", "", "IPv6 字面量"},
		{"", "", "空 Host"},
		{"ab", "", "长度不足 4"},
		{"k7m2p9xq4a.dns.example.com", "", "文档示例含 '9'，按字符集规则应判为非法"},
	}
	for _, c := range cases {
		if got := firstLabel(c.host); got != c.want {
			t.Errorf("firstLabel(%q) = %q，期望 %q（%s）", c.host, got, c.want, c.why)
		}
	}
}

func TestDoHPathClientID(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/dns-query/k7m2pqx4ab", "k7m2pqx4ab"},
		{"/dns-query/k7m2pqx4ab/", "k7m2pqx4ab"},
		{"/dns-query/K7M2PQX4AB", "k7m2pqx4ab"},
		{"/dns-query/k7m2pqx4ab/extra", "k7m2pqx4ab"},
		{"/dns-query/", ""},
		{"/dns-query", ""},
		{"/dns-query/ab", ""},     // 太短
		{"/dns-query/520342", ""}, // 含字符集外的 '0'
		{"/other/k7m2pqx4ab", ""}, // 不是 DoH 路径，绝不能把 "other" 当租户
		{"/", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := dohPathClientID(c.path); got != c.want {
			t.Errorf("dohPathClientID(%q) = %q，期望 %q", c.path, got, c.want)
		}
	}
}

// 构造一个双租户的 registry：租户 1 持有来源 IP 127.0.0.1，租户 2 什么都没有。
// 这正是路径式 DoH 出问题的场景 —— 请求从租户 1 的 IP 发出，却在 URL 里
// 指定了租户 2。
func buildTwoTenantRegistry(t *testing.T) *Registry {
	t.Helper()
	snap := &model.Snapshot{
		Version: 1,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			MaxInflight:  300,
			QueryTimeout: 10000,
			BlockingMode: model.BlockingNullIP,
			Upstreams:    []string{"1.1.1.1"},
		},
		Users: []model.UserSnap{
			{
				UserID:   "usr_t1",
				ClientID: "aaaaaaaaaa",
				Enabled:  true,
				IPs:      []string{"127.0.0.1/32"},
			},
			{
				UserID:   "usr_t2",
				ClientID: "bbbbbbbbbb",
				Enabled:  true,
			},
		},
	}
	reg := NewRegistry(resolver.NewCache())
	if err := reg.Build(snap); err != nil {
		t.Fatalf("构建 registry 失败: %v", err)
	}
	return reg
}

func newTestHandler(t *testing.T) (*Handler, *registrySnapshot) {
	t.Helper()
	reg := buildTwoTenantRegistry(t)
	snap := reg.Load()
	if snap == nil {
		t.Fatal("registry 快照为空")
	}
	h := NewHandler(reg, NewCache(16, 60, 3600), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h, snap
}

func TestIdentifyPrefersExplicitClientIDOverSourceIP(t *testing.T) {
	h, snap := newTestHandler(t)

	req := new(dns.Msg)
	req.SetQuestion("verify-block.test.", dns.TypeA)

	// 模拟 DoH：Host 是裸域名（解析不出 client_id），路径段给出租户 2，
	// 但连接来自租户 1 登记过的 127.0.0.1。
	// 期望：租户 2 胜出 —— 显式指定的租户必须压过环境推断的来源 IP。
	w := &captureWriter{remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}}
	got := h.identify(snap, w, req, &IdentifyHint{
		ClientID:    "", // Host 首段不是合法 client_id
		Source:      "",
		AltClientID: "bbbbbbbbbb",
		AltSource:   "path",
	})
	if got == nil {
		t.Fatal("应当识别出租户 2，实际为 nil（路径段线索被丢弃）")
	}
	if got.UserID != "usr_t2" {
		t.Errorf("应识别出租户 2（usr_t2），实际 %q —— 路径段线索没有压过来源 IP", got.UserID)
	}

	// Host 直接给出租户 1 时应命中租户 1
	got = h.identify(snap, w, req, &IdentifyHint{ClientID: "aaaaaaaaaa", Source: "host"})
	if got == nil || got.UserID != "usr_t1" {
		t.Errorf("Host 给出租户 1 时应命中 usr_t1，实际 %v", got)
	}

	// Host 段与路径段都有效时，Host 优先（它更显式）
	got = h.identify(snap, w, req, &IdentifyHint{
		ClientID: "aaaaaaaaaa", Source: "host",
		AltClientID: "bbbbbbbbbb", AltSource: "path",
	})
	if got == nil || got.UserID != "usr_t1" {
		t.Errorf("Host 段应优先于路径段，实际 %v", got)
	}

	// 两条线索都不合法时，才回退到来源 IP
	got = h.identify(snap, w, req, &IdentifyHint{ClientID: "dns", Source: "host", AltClientID: "ab", AltSource: "path"})
	if got == nil || got.UserID != "usr_t1" {
		t.Errorf("无线索时应回退来源 IP 命中 usr_t1，实际 %v", got)
	}

	// 路径段指向不存在的租户：不应猜测，应回退来源 IP
	got = h.identify(snap, w, req, &IdentifyHint{AltClientID: "zzzzzzzzzz", AltSource: "path"})
	if got == nil || got.UserID != "usr_t1" {
		t.Errorf("路径段查不到租户时应回退来源 IP，实际 %v", got)
	}
}

// 确保 Handle 在拿到 hint 时不会 panic（DoH 走的就是这条路）。
func TestHandleWithHintDoesNotPanic(t *testing.T) {
	h, _ := newTestHandler(t)

	req := new(dns.Msg)
	req.SetQuestion("verify-block.test.", dns.TypeA)
	w := &captureWriter{remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}}

	h.Handle(w, req, &IdentifyHint{AltClientID: "bbbbbbbbbb", AltSource: "path"})
	if w.msg == nil {
		t.Fatal("Handle 应当写出应答")
	}
}
