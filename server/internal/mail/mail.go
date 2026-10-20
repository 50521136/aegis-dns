// Package mail 发送系统邮件（验证码、找回密码）。
//
// 只实现最小可用的 SMTP 客户端：连接 → （可选）STARTTLS 或隐式 TLS →
// AUTH PLAIN → MAIL/RCPT/DATA。不引第三方库是因为需要的功能就这几步，
// 而多一个依赖就多一份升级与供应链负担。
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Config 是 SMTP 配置。
type Config struct {
	Host     string `json:"smtp_host"`
	Port     int    `json:"smtp_port"`
	User     string `json:"smtp_user"`
	Pass     string `json:"smtp_pass"`
	From     string `json:"smtp_from"`
	FromName string `json:"smtp_from_name"`
	// TLS 模式：none（明文/本地中继）| starttls | tls（隐式 TLS，常见于 465）
	TLS string `json:"smtp_tls"`
	// Enabled 是总开关：关掉后所有发信动作直接报「未启用」，
	// 而不是静默失败让用户以为验证码发出去了。
	Enabled bool `json:"smtp_enabled"`
}

// 支持的 TLS 模式。
const (
	TLSNone     = "none"
	TLSStartTLS = "starttls"
	TLSImplicit = "tls"
)

// Validate 校验配置是否完整。
func (c Config) Validate() error {
	if !c.Enabled {
		return errors.New("邮件服务未启用")
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("SMTP 服务器地址未填写")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("SMTP 端口不合法")
	}
	if strings.TrimSpace(c.From) == "" {
		return errors.New("发件人地址未填写")
	}
	if !strings.Contains(c.From, "@") {
		return errors.New("发件人地址格式不正确")
	}
	switch c.TLS {
	case TLSNone, TLSStartTLS, TLSImplicit:
	case "":
		return errors.New("TLS 模式未设置")
	default:
		return fmt.Errorf("不支持的 TLS 模式 %q", c.TLS)
	}
	return nil
}

// Message 是一封待发送的邮件。
type Message struct {
	To      string
	Subject string
	Body    string // 纯文本；多行用 \n
}

// Send 发送一封邮件。
//
// 超时统一 20 秒：SMTP 服务器卡住时不能让 HTTP 请求跟着一起挂，
// 验证码这类请求的用户体验是「点了要立刻有反馈」。
func Send(cfg Config, msg Message) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if !strings.Contains(msg.To, "@") {
		return errors.New("收件人地址格式不正确")
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	var conn net.Conn
	var err error
	if cfg.TLS == TLSImplicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName: cfg.Host,
			MinVersion: tls.VersionTLS12,
		})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	defer conn.Close()

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("建立 SMTP 会话失败: %w", err)
	}
	defer c.Close()

	if cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			// 服务器不支持 STARTTLS 却配置成 starttls：宁可报错也不要
			// 悄悄降级成明文把密码发出去。
			return errors.New("服务器不支持 STARTTLS，请改用 none 或 tls 模式")
		}
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("STARTTLS 握手失败: %w", err)
		}
	}

	if cfg.User != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
				return fmt.Errorf("SMTP 认证失败: %w", err)
			}
		}
	}

	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("设置发件人失败: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return fmt.Errorf("设置收件人失败: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("开始写入邮件失败: %w", err)
	}
	if _, err := wc.Write(buildMIME(cfg, msg)); err != nil {
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("提交邮件失败: %w", err)
	}
	return c.Quit()
}

// buildMIME 组装 RFC 5322 报文。
//
// 主题与正文都用 base64 编码：中文主题不编码会在部分邮件客户端显示成乱码，
// 而这是「验证码邮件」——乱码等于用户拿不到码。
func buildMIME(cfg Config, msg Message) []byte {
	var b strings.Builder
	from := cfg.From
	if cfg.FromName != "" {
		from = encodeHeader(cfg.FromName) + " <" + cfg.From + ">"
	}
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	b.WriteString("Subject: " + encodeHeader(msg.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	// 正文按 76 列折行（base64 的规范要求）
	enc := base64Wrap(msg.Body)
	b.WriteString(enc)
	b.WriteString("\r\n")
	return []byte(b.String())
}

// encodeHeader 用 RFC 2047 编码头部里的非 ASCII 内容。
func encodeHeader(s string) string {
	ascii := true
	for _, r := range s {
		if r > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	return "=?UTF-8?B?" + base64Raw([]byte(s)) + "?="
}
