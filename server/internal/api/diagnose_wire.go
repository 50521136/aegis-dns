package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/diagnose"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件负责两件事：
//  1. 模型 API Key 的加密存储（secretBox）；
//  2. 诊断 worker 的生命周期（按设置重建）。
//
// Key 为什么不能明文落库：数据库会被备份、会被导出、会出现在
// 「系统备份」功能的 tar 包里。明文 Key 一旦跟着备份泄露，
// 别人就能拿它刷用户的模型额度 —— 而这类事故是不可撤销的。

// secretBox 用 AES-GCM 加解密短字符串。
type secretBox struct {
	aead cipher.AEAD
}

// newSecretBox 从 .env 里的 AEGIS_SECRET 派生密钥。
//
// 没有 AEGIS_SECRET 时自动生成一个并写回 .env：
// 让功能开箱可用，同时保证每台机器的密钥不同（而不是硬编码一个默认值，
// 那样等于没有加密）。
func newSecretBox(log interface{ Warn(string, ...any) }) (*secretBox, error) {
	secret := strings.TrimSpace(os.Getenv("AEGIS_SECRET"))
	if secret == "" {
		buf := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return nil, fmt.Errorf("生成密钥失败: %w", err)
		}
		secret = base64.RawURLEncoding.EncodeToString(buf)
		// 尽力写回 .env：失败也不影响本次运行（密钥在内存里可用），
		// 只是重启后会重新生成，之前加密的 Key 就解不开了。
		if err := appendEnvSecret(secret); err != nil && log != nil {
			log.Warn("无法把 AEGIS_SECRET 写回 .env，重启后需要重新填写模型 Key", "err", err)
		}
		os.Setenv("AEGIS_SECRET", secret)
	}
	// 用 SHA-256 归一到 32 字节，这样 AEGIS_SECRET 可以是任意长度字符串。
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

// encrypt 加密并 base64 编码。
func (b *secretBox) encrypt(plain string) (string, error) {
	if b == nil || b.aead == nil {
		return "", errors.New("加密器未初始化")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.RawStdEncoding.EncodeToString(out), nil
}

// decrypt 解密。密文损坏或密钥变了会返回错误。
func (b *secretBox) decrypt(enc string) (string, error) {
	if b == nil || b.aead == nil || enc == "" {
		return "", nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("密文长度不足")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// appendEnvSecret 把新生成的密钥追加到 .env。
func appendEnvSecret(secret string) error {
	path := configEnvPath()
	if path == "" {
		return errors.New("找不到 .env 路径")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# 由 AI 诊断功能自动生成，用于加密模型 API Key\nAEGIS_SECRET=%s\n", secret)
	return err
}

// configEnvPath 返回 .env 的位置（与配置文件同目录）。
func configEnvPath() string {
	if p := os.Getenv("AEGIS_ENV_PATH"); p != "" {
		return p
	}
	return "/opt/aegis/.env"
}

// diagnoserIface 是 api 层对诊断 worker 的最小依赖。
type diagnoserIface interface {
	Enqueue(model.Feedback)
	ShouldAutoRun() bool
	Close()
	// SetOnDone 注册「诊断完成后」的回调（自动处理反馈用）。
	SetOnDone(func(fb *model.Feedback))
}

// encryptSecret 供 handler 使用。
func (s *Server) encryptSecret(plain string) (string, error) {
	if s.secretBox == nil {
		return "", errors.New("密钥加密器未初始化")
	}
	return s.secretBox.encrypt(plain)
}

// decryptSecret 供构造 LLMConfig 使用。
func (s *Server) decryptSecret(enc string) string {
	if s.secretBox == nil {
		return ""
	}
	v, err := s.secretBox.decrypt(enc)
	if err != nil {
		s.log.Warn("模型 Key 解密失败（AEGIS_SECRET 可能已变），需要重新填写", "err", err)
		return ""
	}
	return v
}

// rebuildDiagnoser 按当前设置重建 worker。
//
// 改名不重建的是 store/log —— 它们是进程级依赖；改的是 endpoint / key /
// model / 开关，所以整体替换最省心，也避免旧配置的 worker 残留。
func (s *Server) rebuildDiagnoser(set *model.GlobalSettings) {
	if s.diagnoser != nil {
		s.diagnoser.Close()
		s.diagnoser = nil
	}
	if !set.AIEnabled || set.AIBaseURL == "" || set.AIModel == "" {
		return
	}
	cfg := func() diagnose.LLMConfig {
		// 每次调用都重新读设置：管理员改了模型名或 Key，
		// 下一条诊断就该用新配置，不需要重启进程。
		cur, err := s.store.GetSettings(context.Background())
		if err != nil {
			return diagnose.LLMConfig{}
		}
		return diagnose.LLMConfig{
			BaseURL:    cur.AIBaseURL,
			APIKey:     s.decryptSecret(cur.AIKeyEncrypted),
			Model:      cur.AIModel,
			Timeout:    time.Duration(cur.AITimeoutSec) * time.Second,
			MaxPerHour: cur.AIMaxPerHour,
		}
	}
	modeFn := func() string {
		cur, err := s.store.GetSettings(context.Background())
		if err != nil {
			return "off"
		}
		return cur.AITriggerMode
	}

	s.diagnoser = diagnose.NewWorker(storeSource{srv: s}, s.store, s.log, cfg, modeFn)

	// 诊断完成后自动处理：核查无问题就回复结单、确认误拦截就放行白名单，
	// 其余情况转人工（需求 1）。
	//
	// 是否真的自动处理由设置里的 ai_auto_handle 决定 —— 关掉之后这里
	// 仍然会被调用，但 autoHandleFeedback 内部会直接返回 NeedsHuman，
	// 效果等于「只诊断不处理」，与旧行为一致。
	s.diagnoser.SetOnDone(func(fb *model.Feedback) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		cur, err := s.store.GetSettings(ctx)
		if err != nil || !cur.AIAutoHandle {
			return
		}
		if _, err := s.autoHandleFeedback(ctx, fb); err != nil {
			s.log.Warn("AI 自动处理反馈失败", "feedback", fb.ID, "err", err)
		}
	})

	s.log.Info("AI 诊断已启用", "model", set.AIModel, "base_url", set.AIBaseURL, "mode", set.AITriggerMode)
}

// nowUnix 便于测试注入。
func nowUnix() int64 { return time.Now().Unix() }

// diagnoseQueryWindowSec 是证据里「近期查询」的时间窗。
const diagnoseQueryWindowSec int64 = 3600
