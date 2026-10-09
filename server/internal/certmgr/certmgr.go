// Package certmgr 负责 TLS 证书的加载、热重载与指纹计算。
//
// dnsd 与 apid 共用同一张通配证书，因此把这块逻辑抽出来独立成包：
//   - dnsd 用它给 DoT/DoH 提供 tls.Config；
//   - apid 用它给自己的 HTTPS 监听提供证书，并给 /me/dns-config 返回指纹。
//
// 热重载走 fsnotify + GetCertificate 回调，证书换新后无需重启进程
// （文档 3.4 的「无需重启」要求）。
package certmgr

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Manager 管理一张（对）证书文件。
type Manager struct {
	certFile string
	keyFile  string
	devTLS   bool
	log      *slog.Logger

	cert atomic.Pointer[tls.Certificate]
	// fingerprint 是叶子证书的 SHA-256，格式 AA:BB:...（供客户端校验用）。
	fingerprint atomic.Pointer[string]
	// notAfter 记录证书到期时间，用于到期告警与 /admin/system 展示。
	notAfter atomic.Int64

	mu       sync.Mutex
	watcher  *fsnotify.Watcher
	lastLoad time.Time
}

// New 创建证书管理器。
//
// devTLS 为 true 且文件不存在时，会生成一张内存自签证书，
// 让内网/测试环境也能起 DoT/DoH（客户端需加 -k 或忽略校验）。
func New(certFile, keyFile string, devTLS bool, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{certFile: certFile, keyFile: keyFile, devTLS: devTLS, log: log}
}

// Load 从磁盘加载证书。失败时保留上一次成功的证书（P4 优雅降级）。
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

func (m *Manager) loadLocked() error {
	certPEM, err := os.ReadFile(m.certFile)
	if err != nil {
		return fmt.Errorf("读取证书 %s 失败: %w", m.certFile, err)
	}
	keyPEM, err := os.ReadFile(m.keyFile)
	if err != nil {
		return fmt.Errorf("读取私钥 %s 失败: %w", m.keyFile, err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("证书与私钥不匹配或格式错误: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return fmt.Errorf("证书文件里没有有效证书")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("解析证书失败: %w", err)
	}
	cert.Leaf = leaf

	fp := sha256.Sum256(cert.Certificate[0])
	hexFP := strings.ToUpper(hex.EncodeToString(fp[:]))
	// 转成 AA:BB:CC... 便于人眼比对与客户端粘贴。
	var parts []string
	for i := 0; i < len(hexFP); i += 2 {
		parts = append(parts, hexFP[i:i+2])
	}
	formatted := strings.Join(parts, ":")

	m.cert.Store(&cert)
	m.fingerprint.Store(&formatted)
	m.notAfter.Store(leaf.NotAfter.Unix())
	m.lastLoad = time.Now()
	return nil
}

// Ensure 保证有一张可用证书：优先加载文件，失败且开了 devTLS 就自签一张。
func (m *Manager) Ensure() error {
	if err := m.Load(); err == nil {
		return nil
	} else if !m.devTLS {
		return err
	} else {
		m.log.Warn("证书加载失败，dev_tls 已开启，改用自签证书", "err", err)
	}
	return m.generateSelfSigned()
}

// generateSelfSigned 生成一张内存自签证书（仅测试用）。
func (m *Manager) generateSelfSigned() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成自签密钥失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("生成序列号失败: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "aegis-dns dev"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"*", "localhost"},
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("签发自签证书失败: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("编码自签私钥失败: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("加载自签证书失败: %w", err)
	}
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	cert.Leaf = leaf

	fp := sha256.Sum256(cert.Certificate[0])
	m.cert.Store(&cert)
	s := strings.ToUpper(hex.EncodeToString(fp[:]))
	m.fingerprint.Store(&s)
	if leaf != nil {
		m.notAfter.Store(leaf.NotAfter.Unix())
	}
	return nil
}

// Available 判断当前是否已有可用证书。
func (m *Manager) Available() bool { return m.cert.Load() != nil }

// Fingerprint 返回叶子证书的 SHA-256 指纹（AA:BB:... 格式）。
func (m *Manager) Fingerprint() string {
	if p := m.fingerprint.Load(); p != nil {
		return *p
	}
	return ""
}

// NotAfter 返回证书到期时间（Unix 秒，0 表示未知）。
func (m *Manager) NotAfter() int64 { return m.notAfter.Load() }

// GetCertificate 是 tls.Config 的回调，每次握手时取当前证书。
//
// 用回调而不是在 tls.Config 里塞静态证书，是热重载的关键：
// 换证书只需替换 atomic 指针，已建立的监听无需重启。
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := m.cert.Load()
	if c == nil {
		return nil, fmt.Errorf("当前没有可用证书")
	}
	return c, nil
}

// TLSConfig 返回一份配置好的 tls.Config。
//
// NextProtos 同时提供 h2 与 http/1.1：DoH 客户端（浏览器、curl）默认走 h2。
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: m.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1", "dot"},
	}
}

// Watch 监听证书文件变化并自动重载。
//
// 监听父目录而不是文件本身：很多证书工具（certbot、acme.sh）是用
// rename 替换文件的，直接 watch 文件会在第一次替换后失效。
func (m *Manager) Watch(stop <-chan struct{}) {
	dir := filepath.Dir(m.certFile)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		m.log.Warn("证书监听不可用，将退化为定时重载", "err", err)
		m.pollLoop(stop)
		return
	}
	if err := w.Add(dir); err != nil {
		m.log.Warn("无法监听证书目录，将退化为定时重载", "dir", dir, "err", err)
		w.Close()
		m.pollLoop(stop)
		return
	}
	m.mu.Lock()
	m.watcher = w
	m.mu.Unlock()

	// 兜底轮询：inotify 在容器 volume / NFS 下可能不触发。
	go m.pollLoop(stop)

	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	pending := false

	for {
		select {
		case <-stop:
			w.Close()
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// 只关心证书/私钥相关的事件。
			base := filepath.Base(ev.Name)
			if base != filepath.Base(m.certFile) && base != filepath.Base(m.keyFile) {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			// 去抖：certbot 会连续写多个文件，合并成一次重载。
			if !pending {
				pending = true
				debounce.Reset(500 * time.Millisecond)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			m.log.Warn("证书监听出错", "err", err)
		case <-debounce.C:
			pending = false
			if err := m.Load(); err != nil {
				m.log.Error("证书热重载失败，继续使用旧证书", "err", err)
			} else {
				m.log.Info("证书已热重载", "not_after", time.Unix(m.NotAfter(), 0).Format(time.RFC3339))
			}
		}
	}
}

// pollLoop 每 10 分钟无条件尝试重载一次，作为 inotify 失效时的兜底。
func (m *Manager) pollLoop(stop <-chan struct{}) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			// 只在不影响当前证书的前提下替换：Load 失败会保留旧值。
			if err := m.Load(); err == nil {
				m.log.Debug("证书定时重载完成")
			}
		}
	}
}
