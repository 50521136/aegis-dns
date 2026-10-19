package certmgr

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Status 是证书的对外状态，供管理面板展示。
type Status struct {
	// Available 为假表示没有可用证书（DoT/DoH 会被跳过）。
	Available bool `json:"available"`
	// SelfSigned 为真表示当前用的是 dev_tls 生成的自签证书。
	SelfSigned bool `json:"self_signed"`
	// Subject / Issuer 是证书主体与签发者。
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`
	// DNSNames 是证书覆盖的域名（含通配符）。
	DNSNames []string `json:"dns_names"`
	// NotBefore / NotAfter 是 Unix 秒。
	NotBefore int64 `json:"not_before"`
	NotAfter  int64 `json:"not_after"`
	// DaysLeft 是剩余天数，负数表示已过期。
	DaysLeft int `json:"days_left"`
	// Fingerprint 是叶子证书 SHA-256，AA:BB:... 形式。
	Fingerprint string `json:"fingerprint"`
	// CertFile / KeyFile 是文件路径，便于运维定位。
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// Paths 返回证书与私钥的文件路径。
func (m *Manager) Paths() (certFile, keyFile string) {
	return m.certFile, m.keyFile
}

// Status 汇总当前证书状态。
//
// 没有可用证书时返回零值 + Available=false，而不是报错：
// 「还没配证书」是正常的初始状态，不该让管理页面报红。
func (m *Manager) Status() Status {
	st := Status{CertFile: m.certFile, KeyFile: m.keyFile}
	c := m.cert.Load()
	if c == nil || c.Leaf == nil {
		return st
	}
	st.Available = true
	st.Subject = c.Leaf.Subject.String()
	st.Issuer = c.Leaf.Issuer.String()
	st.DNSNames = append([]string{}, c.Leaf.DNSNames...)
	st.NotBefore = c.Leaf.NotBefore.Unix()
	st.NotAfter = c.Leaf.NotAfter.Unix()
	st.DaysLeft = int(time.Until(c.Leaf.NotAfter).Hours() / 24)
	if fp := m.fingerprint.Load(); fp != nil {
		st.Fingerprint = *fp
	}
	// 自签的判据：签发者等于主体。够用且不需要额外状态位。
	st.SelfSigned = c.Leaf.Issuer.String() == c.Leaf.Subject.String()
	return st
}

// Replace 用新上传的 PEM 替换证书并热加载。
//
// 顺序很重要：**先校验再落盘**。写坏文件之后 Load 失败会让 DoT/DoH 直接失去
// 证书，而 dnsd 那边还会继续用旧证书 —— 两个进程状态不一致，很难查。
// 所以这里先把新证书解析一遍，确认 cert/key 成对、能解析，才允许覆盖。
func (m *Manager) Replace(certPEM, keyPEM []byte) error {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return fmt.Errorf("证书与私钥都不能为空")
	}
	// 必须至少包含一个 CERTIFICATE 块，否则多半是把私钥贴进了证书框。
	if blk, _ := pem.Decode(certPEM); blk == nil || blk.Type != "CERTIFICATE" {
		return fmt.Errorf("证书内容不是 PEM 格式（缺少 CERTIFICATE 块）")
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("证书与私钥不匹配或格式错误: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return fmt.Errorf("证书文件里没有有效证书")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("解析证书失败: %w", err)
	}
	if time.Now().After(leaf.NotAfter) {
		return fmt.Errorf("证书已过期（到期时间 %s），请上传新证书",
			leaf.NotAfter.Format("2006-01-02 15:04:05"))
	}

	if err := writeFileAtomic(m.certFile, certPEM, 0o644); err != nil {
		return fmt.Errorf("写入证书失败: %w", err)
	}
	if err := writeFileAtomic(m.keyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("写入私钥失败: %w", err)
	}

	// 落盘成功后立刻热加载，让 DoT/DoH 用上新证书而不需要重启进程。
	if err := m.Load(); err != nil {
		return fmt.Errorf("证书已写入但加载失败: %w", err)
	}
	m.log.Info("证书已更新",
		"subject", leaf.Subject.String(),
		"not_after", leaf.NotAfter.Format(time.RFC3339),
		"dns_names", strings.Join(leaf.DNSNames, ","))
	return nil
}

// writeFileAtomic 先写临时文件再 rename，避免写到一半被读到半个文件。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后这里是空操作

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
