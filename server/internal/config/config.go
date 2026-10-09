// Package config 负责加载 config.yaml。
//
// 设计约束（文档 P2 运行期零 IO）：dnsd 只在启动时读一次配置，
// 之后所有可变参数都来自快照文件。因此这里的配置项刻意保持精简，
// 只有「进程级、不常变」的内容（监听地址、数据目录、密钥）。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DNS 是 dnsd 的监听与证书配置。
type DNS struct {
	ListenUDP string `yaml:"listen_udp"`
	ListenTCP string `yaml:"listen_tcp"`
	ListenDOT string `yaml:"listen_dot"`
	// ListenDOH 是 DoH 的 HTTPS 监听地址。dnsd 自己做 TLS 终止，
	// 不依赖任何反代（文档约束 C4）。
	ListenDOH string `yaml:"listen_doh"`
	// ListenDOHPlain 是明文 HTTP 的 DoH 监听（默认关闭）。
	// 仅用于反代终止 TLS 的部署形态，或本机 curl 调试。
	ListenDOHPlain string `yaml:"listen_doh_plain"`
	CertFile       string `yaml:"cert_file"`
	KeyFile        string `yaml:"key_file"`
	// DevTLS 为 true 时，若无证书则自签一张内存证书，方便内网测试。
	DevTLS bool `yaml:"dev_tls"`
	ACME   ACME `yaml:"acme"`
	// QueryLogRing 是内存查询环形缓冲的容量（dnsd 侧，未落库前的暂存）。
	QueryLogRing int `yaml:"query_log_ring"`
	// StatsFlushSeconds 是统计批量落库的间隔，默认 60 秒。
	//
	// 为什么可配：60 秒意味着「刚发生的查询要等一分钟才能在看板上看到」。
	// 排障时把管理面板上的数字调到 5 秒会方便很多；而小规模部署把它调到
	// 300 秒能显著减少 SQLite 的写入次数。落库本身在后台协程里做，
	// 不占用查询路径，所以调小不会影响解析延迟。
	StatsFlushSeconds int `yaml:"stats_flush_seconds"`
}

// ACME 是内置证书签发配置（DNS-01）。
type ACME struct {
	Enabled   bool   `yaml:"enabled"`
	Provider  string `yaml:"provider"` // cloudflare | alidns | dnspod
	TokenEnv  string `yaml:"token_env"`
	Email     string `yaml:"email"`
	Directory string `yaml:"directory"` // 留空用 Let's Encrypt 生产环境
	// ExtraEnv 允许把 provider 需要的额外参数（如阿里云的 AccessKeySecret）
	// 以 环境变量名 -> 值来源 的形式传进来。
	ExtraEnv map[string]string `yaml:"extra_env"`
}

// API 是 apid 的监听与管理员配置。
type API struct {
	Listen string `yaml:"listen"`
	// TLSListen 为可选。因为 443 被 dnsd 的 DoH 占用，面板走独立端口
	// （默认 :8443）并由 apid 自己做 TLS 终止，同样不需要反代。
	TLSListen string `yaml:"tls_listen"`
	CertFile  string `yaml:"cert_file"`
	KeyFile   string `yaml:"key_file"`
	JWTSecret string `yaml:"jwt_secret"`
	Admin     Admin  `yaml:"admin"`
	// TrustedProxies 为 CIDR 列表，用于从 X-Forwarded-For 取真实客户端 IP。
	TrustedProxies []string `yaml:"trusted_proxies"`
	// CORSOrigins 为空表示同源（默认，最安全）；"*" 表示放开。
	CORSOrigins []string `yaml:"cors_origins"`
}

// Admin 是首次启动时创建的管理员账号。
type Admin struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Email    string `yaml:"email"`
}

// Update 控制在线更新来源。
type Update struct {
	Repo    string `yaml:"repo"`
	Channel string `yaml:"channel"`
}

// Log 是日志配置。
type Log struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // text|json
}

// Config 是完整配置。
type Config struct {
	Domain    string   `yaml:"domain"`
	DataDir   string   `yaml:"data_dir"`
	DNS       DNS      `yaml:"dns"`
	API       API      `yaml:"api"`
	Upstreams []string `yaml:"upstreams"`
	Update    Update   `yaml:"update"`
	Log       Log      `yaml:"log"`
	// Path 记录配置文件自身的路径（不来自 YAML）。
	Path string `yaml:"-"`
}

// Default 返回一份可直接运行的默认配置。
func Default() *Config {
	return &Config{
		Domain:  "",
		DataDir: "./data",
		DNS: DNS{
			ListenUDP:    ":53",
			ListenTCP:    ":53",
			ListenDOT:    ":853",
			ListenDOH:    ":443",
			QueryLogRing: 1000,
		},
		API: API{
			Listen: ":8080",
		},
		Upstreams: []string{"1.1.1.1", "8.8.8.8", "https://dns.google/dns-query"},
		Update:    Update{Channel: "stable"},
		Log:       Log{Level: "info", Format: "text"},
	}
}

// envRe 匹配 ${VAR} 与 $VAR 两种写法。
var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// expandEnv 把 ${VAR} 替换为环境变量值。
//
// 与 os.ExpandEnv 的区别：未设置的变量保留原文而不是替换成空串。
// 这样「配置里写了 ${JWT_SECRET} 但忘了导出」会在启动校验阶段被明确报错，
// 而不是静默拿到一个空密钥（那会导致 JWT 可被任意伪造）。
func expandEnv(s string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
		name = strings.TrimPrefix(name, "$")
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
}

// Load 读取并校验配置文件。
func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		path = "config.yaml"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置路径失败: %w", err)
	}
	cfg.Path = abs

	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", abs, err)
	}
	// 先做环境变量展开再解析 YAML：这样 ${VAR} 里即使含冒号、井号也不会被
	// YAML 语法吃掉（放在引号里解析后再展开也能用，但先展开更宽容）。
	expanded := expandEnv(string(raw))

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", abs, err)
	}

	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalize 补默认值并做基本校验。
func (c *Config) normalize() error {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return fmt.Errorf("解析 data_dir 失败: %w", err)
	}
	c.DataDir = abs

	if c.DNS.ListenUDP == "" {
		c.DNS.ListenUDP = ":53"
	}
	if c.DNS.ListenTCP == "" {
		c.DNS.ListenTCP = ":53"
	}
	if c.DNS.ListenDOT == "" {
		c.DNS.ListenDOT = ":853"
	}
	if c.DNS.ListenDOH == "" {
		c.DNS.ListenDOH = ":443"
	}
	if c.DNS.QueryLogRing <= 0 {
		c.DNS.QueryLogRing = 1000
	}
	if c.DNS.StatsFlushSeconds <= 0 {
		c.DNS.StatsFlushSeconds = 60
	}
	// 下限 1 秒：再小就变成每条查询一次事务，会把 WAL 写爆。
	if c.DNS.StatsFlushSeconds < 1 {
		c.DNS.StatsFlushSeconds = 1
	}
	if c.API.Listen == "" {
		c.API.Listen = ":8080"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	if len(c.Upstreams) == 0 {
		c.Upstreams = []string{"1.1.1.1", "8.8.8.8"}
	}
	c.Domain = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(c.Domain)), ".")
	return nil
}

// 数据目录下的固定相对路径。
const (
	dbName       = "aegis.db"
	runtimeDir   = "runtime"
	snapshotName = "config.json"
	certsDir     = "certs"
)

// DBPath 返回 SQLite 文件路径。
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, dbName) }

// SnapshotPath 返回配置快照路径（apid 写、dnsd 读）。
func (c *Config) SnapshotPath() string {
	return filepath.Join(c.DataDir, runtimeDir, snapshotName)
}

// RuntimeDir 返回快照所在目录（fsnotify 监听它而不是文件本身，
// 因为原子 rename 替换 inode 会让直接监听文件的 watch 失效）。
func (c *Config) RuntimeDir() string { return filepath.Join(c.DataDir, runtimeDir) }

// CertsDir 返回证书目录。
func (c *Config) CertsDir() string { return filepath.Join(c.DataDir, certsDir) }

// EnsureDirs 创建数据目录结构。
func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.RuntimeDir(), c.CertsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", d, err)
		}
	}
	return nil
}

// ValidateForDNS 校验 dnsd 启动所需的配置。
func (c *Config) ValidateForDNS() error {
	if c.Domain == "" {
		return fmt.Errorf("配置项 domain 为空：必须填写主域名（例如 dns.example.com），否则无法派生用户子域名")
	}
	return nil
}

// ValidateForAPI 校验 apid 启动所需的配置。
func (c *Config) ValidateForAPI() error {
	if c.API.JWTSecret == "" {
		return fmt.Errorf("api.jwt_secret 为空：请在 .env 中导出 JWT_SECRET 并在配置里写 ${JWT_SECRET}")
	}
	if strings.Contains(c.API.JWTSecret, "${") {
		return fmt.Errorf("api.jwt_secret 仍是未展开的占位符 %q：对应环境变量没有导出", c.API.JWTSecret)
	}
	if len(c.API.JWTSecret) < 32 {
		return fmt.Errorf("api.jwt_secret 长度仅 %d：JWT 签名密钥至少需要 32 字节随机值（可用 openssl rand -hex 32 生成）", len(c.API.JWTSecret))
	}
	if c.API.Admin.Username == "" {
		return fmt.Errorf("api.admin.username 为空：需要一个初始管理员账号名")
	}
	if c.API.Admin.Password == "" || strings.Contains(c.API.Admin.Password, "${") {
		return fmt.Errorf("api.admin.password 为空或未展开：请在 .env 中导出 ADMIN_PASSWORD（首次启动后即可从配置中删除，密码已入库）")
	}
	return nil
}

// ResolveCert 返回实际使用的证书路径。
//
// 优先使用配置里显式指定的路径；为空时回落到 data/certs/ 下的约定文件名
// （内置 ACME 写入的位置）。返回的两个路径都可能不存在，调用方需自行判断。
func (c *Config) ResolveCert() (certFile, keyFile string) {
	certFile, keyFile = c.DNS.CertFile, c.DNS.KeyFile
	if certFile == "" {
		certFile = filepath.Join(c.CertsDir(), "fullchain.pem")
	}
	if keyFile == "" {
		keyFile = filepath.Join(c.CertsDir(), "privkey.pem")
	}
	return certFile, keyFile
}
