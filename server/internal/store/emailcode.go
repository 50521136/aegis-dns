package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// 验证码用途。
const (
	EmailPurposeRegister = "register"
	EmailPurposeReset    = "reset"
)

// 验证码策略。
const (
	// EmailCodeTTL 是验证码有效期。10 分钟足够用户切换邮箱/等收信，
	// 又不至于让一个被截获的码长期可用。
	EmailCodeTTL = 10 * time.Minute
	// EmailCodeCooldown 是同一邮箱两次发送之间的最小间隔。
	EmailCodeCooldown = 60 * time.Second
	// EmailCodeHourlyLimit 是同一邮箱每小时最多发送次数。
	// 不限制的话这里就是一个免费的短信/邮件轰炸接口。
	EmailCodeHourlyLimit = 5
	// EmailCodeMaxAttempts 是同一验证码允许的校验次数。
	// 6 位数字只有 100 万种，不限次数用脚本几分钟就能撞开。
	EmailCodeMaxAttempts = 5
)

// IssueEmailCode 生成并保存一个验证码，返回明文码。
//
// 会先检查冷却与小时上限，超限返回错误 —— 这类检查必须放在发信之前，
// 否则「限流了但邮件已经发出去」等于没限流。
func (s *Store) IssueEmailCode(ctx context.Context, email, purpose string) (string, error) {
	now := time.Now()
	// 冷却
	var last int64
	err := s.read.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(created_at), 0) FROM email_codes WHERE email = ?`, email).Scan(&last)
	if err != nil {
		return "", fmt.Errorf("查询验证码失败: %w", err)
	}
	if last > 0 && now.Unix()-last < int64(EmailCodeCooldown.Seconds()) {
		wait := int64(EmailCodeCooldown.Seconds()) - (now.Unix() - last)
		return "", fmt.Errorf("请 %d 秒后再试", wait)
	}
	// 小时上限
	var hourly int
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM email_codes WHERE email = ? AND created_at > ?`,
		email, now.Add(-time.Hour).Unix()).Scan(&hourly); err != nil {
		return "", fmt.Errorf("查询验证码失败: %w", err)
	}
	if hourly >= EmailCodeHourlyLimit {
		return "", fmt.Errorf("该邮箱一小时内发送次数过多，请稍后再试")
	}

	code, err := randomDigits(6)
	if err != nil {
		return "", err
	}
	if _, err := s.write.ExecContext(ctx,
		`INSERT INTO email_codes (email, code, purpose, expires_at, used, attempts, created_at)
		 VALUES (?,?,?,?,0,0,?)`,
		email, code, purpose, now.Add(EmailCodeTTL).Unix(), now.Unix()); err != nil {
		return "", fmt.Errorf("保存验证码失败: %w", err)
	}
	// 顺手清掉过期很久的记录，避免表无限增长
	_, _ = s.write.ExecContext(ctx, `DELETE FROM email_codes WHERE expires_at < ?`, now.Add(-24*time.Hour).Unix())
	return code, nil
}

// VerifyEmailCode 校验并消费验证码。
//
// 成功即置为已用：验证码是一次性的，能重复使用就失去了「证明你收到了邮件」的意义。
func (s *Store) VerifyEmailCode(ctx context.Context, email, purpose, code string) error {
	var id, expiresAt, attempts int64
	var used int
	err := s.read.QueryRowContext(ctx,
		`SELECT id, expires_at, used, attempts FROM email_codes
		 WHERE email = ? AND purpose = ? ORDER BY created_at DESC LIMIT 1`,
		email, purpose).Scan(&id, &expiresAt, &used, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("验证码不存在或已失效，请重新获取")
	}
	if err != nil {
		return fmt.Errorf("查询验证码失败: %w", err)
	}
	if used == 1 {
		return errors.New("验证码已使用，请重新获取")
	}
	if time.Now().Unix() > expiresAt {
		return errors.New("验证码已过期，请重新获取")
	}
	if attempts >= EmailCodeMaxAttempts {
		return errors.New("验证码错误次数过多，请重新获取")
	}
	// 先记一次尝试再比对：否则错误次数永远不会增长
	if _, err := s.write.ExecContext(ctx, `UPDATE email_codes SET attempts = attempts + 1 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("更新验证码状态失败: %w", err)
	}
	var stored string
	if err := s.read.QueryRowContext(ctx, `SELECT code FROM email_codes WHERE id = ?`, id).Scan(&stored); err != nil {
		return fmt.Errorf("查询验证码失败: %w", err)
	}
	if stored != code {
		return errors.New("验证码不正确")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE email_codes SET used = 1 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("标记验证码已用失败: %w", err)
	}
	return nil
}

// randomDigits 生成 n 位数字验证码。
//
// 用 crypto/rand 而不是 math/rand：后者在并发下可预测，
// 而验证码的全部安全性就建立在「猜不到」上。
func randomDigits(n int) (string, error) {
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", fmt.Errorf("生成验证码失败: %w", err)
		}
		out[i] = byte('0' + v.Int64())
	}
	return string(out), nil
}
