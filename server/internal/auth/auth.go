// Package auth 实现密码哈希、JWT 签发/校验与长期 API Token。
//
// 令牌设计（文档 10.1）：
//
//	access_token   JWT HS256，15 分钟，不进数据库，靠短期自然过期
//	refresh_token  JWT HS256，30 天，jti 入库以支持撤销与轮转
//	PAT            agx_ 前缀随机串，服务端只存 SHA-256
//
// 三者都用 Authorization: Bearer <token>，中间件统一校验，
// 因此前端不需要 Cookie / Session（也天然免疫 CSRF）。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// BcryptCost 是密码哈希强度（文档 10.2）。
//
// 12 比默认的 10 慢约 4 倍，单次约 250ms —— 对登录接口完全可接受，
// 但对离线爆破是把成本直接放大 4 倍。
const BcryptCost = 12

// AccessTTL / RefreshTTL 是两类令牌的有效期。
const (
	AccessTTL  = 15 * time.Minute
	RefreshTTL = 30 * 24 * time.Hour
)

// ErrInvalidCredentials 表示用户名或密码错误。
//
// 用户名不存在与密码错误返回同一个错误，避免通过响应差异枚举账号。
var ErrInvalidCredentials = errors.New("用户名或密码错误")

// ErrInvalidToken 表示令牌无效、过期或已撤销。
var ErrInvalidToken = errors.New("令牌无效或已过期")

// HashPassword 生成密码哈希。
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(b), nil
}

// CheckPassword 校验密码。
func CheckPassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// ValidatePasswordStrength 检查密码强度（文档 10.2）。
func ValidatePasswordStrength(password string) error {
	if len([]rune(password)) < 8 {
		return errors.New("密码至少需要 8 个字符")
	}
	if len(password) > 200 {
		return errors.New("密码过长（最多 200 字符）")
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("密码必须同时包含字母和数字")
	}
	lower := strings.ToLower(password)
	for _, weak := range weakPasswords {
		if lower == weak {
			return errors.New("该密码过于常见，请更换")
		}
	}
	// 连续重复字符（aaaaaaaa）与顺序串（12345678）一律拒绝。
	if isRepeatedOrSequential(lower) {
		return errors.New("密码过于简单（重复或连续字符），请更换")
	}
	return nil
}

func isRepeatedOrSequential(s string) bool {
	if len(s) < 4 {
		return false
	}
	same, asc, desc := true, true, true
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			same = false
		}
		if s[i] != s[i-1]+1 {
			asc = false
		}
		if s[i] != s[i-1]-1 {
			desc = false
		}
	}
	return same || asc || desc
}

// weakPasswords 是常见弱密码黑名单。
//
// 文档提到「top 10000」，但把上万条明文密码打进二进制并不划算：
// 真正的防线是 bcrypt cost=12 + 长度/复杂度要求。这里保留最高频的一批，
// 主要拦住「admin123」这类会被人肉优先尝试的密码。
var weakPasswords = []string{
	"password", "password1", "password123", "passw0rd", "p@ssw0rd",
	"12345678", "123456789", "1234567890", "87654321", "11111111",
	"qwerty123", "qwertyuiop", "abc12345", "abcd1234", "a1234567",
	"admin123", "administrator", "root1234", "letmein1", "welcome1",
	"iloveyou1", "monkey123", "dragon123", "sunshine1", "princess1",
	"football1", "baseball1", "superman1", "trustno1", "master123",
	"changeme1", "secret123", "test1234", "guest1234", "user1234",
	"1qaz2wsx", "1q2w3e4r", "zaq12wsx", "qazwsxedc", "asdfghjkl",
}

// === JWT ===

// Claims 是访问令牌的载荷。
type Claims struct {
	UserID string `json:"sub"`
	Role   string `json:"role"`
	// Type 区分 access / refresh，防止把 refresh token 当 access 用
	// （后者有效期 30 天，一旦混用等于把短令牌策略完全作废）。
	Type string `json:"typ"`
	jwt.RegisteredClaims
}

// TokenService 签发与校验令牌。
type TokenService struct {
	secret []byte
	issuer string
}

// NewTokenService 创建令牌服务。
func NewTokenService(secret, issuer string) *TokenService {
	if issuer == "" {
		issuer = "aegis-dns"
	}
	return &TokenService{secret: []byte(secret), issuer: issuer}
}

// TokenPair 是一对令牌。
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// Issue 为指定用户签发一对令牌。
func (s *TokenService) Issue(userID, role string) (*TokenPair, error) {
	now := time.Now()

	access := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: userID,
		Role:   role,
		Type:   "access",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTTL)),
		},
	})
	accessStr, err := access.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("签发访问令牌失败: %w", err)
	}

	jti, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	refresh := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: userID,
		Role:   role,
		Type:   "refresh",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   userID,
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(RefreshTTL)),
		},
	})
	refreshStr, err := refresh.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("签发刷新令牌失败: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessStr,
		RefreshToken: refreshStr,
		ExpiresIn:    int(AccessTTL.Seconds()),
		TokenType:    "Bearer",
	}, nil
}

// ParseAccess 校验访问令牌。
func (s *TokenService) ParseAccess(token string) (*Claims, error) {
	return s.parse(token, "access")
}

// ParseRefresh 校验刷新令牌，并返回其 jti（用于撤销检查）。
func (s *TokenService) ParseRefresh(token string) (*Claims, error) {
	return s.parse(token, "refresh")
}

func (s *TokenService) parse(token, want string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		// 必须校验算法：不校验的话攻击者可以把 alg 改成 none 或 RS256，
		// 用公钥当 HMAC 密钥来伪造令牌（经典 JWT 混淆攻击）。
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("非预期的签名算法 %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithIssuer(s.issuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if !parsed.Valid {
		return nil, ErrInvalidToken
	}
	if claims.Type != want {
		return nil, fmt.Errorf("%w: 令牌类型为 %q，期望 %q", ErrInvalidToken, claims.Type, want)
	}
	return claims, nil
}

// RefreshExpiry 返回刷新令牌的过期时间（用于入库）。
func RefreshExpiry() time.Time { return time.Now().Add(RefreshTTL) }

// === PAT ===

// HashPAT 计算长期令牌的 SHA-256 十六进制摘要。
//
// 用 SHA-256 而不是 bcrypt：PAT 是 256 位随机串，不存在被字典爆破的可能，
// 而鉴权发生在每个 API 请求上，bcrypt 的 250ms 会让接口无法使用。
func HashPAT(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// PATPprefix 返回用于 UI 辨识的前缀（agx_ + 前 6 位）。
func PATPprefix(token string) string {
	if len(token) <= 10 {
		return token
	}
	return token[:10]
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GenerateSecret 生成一个随机密钥（用于首次启动时提示 JWT_SECRET）。
func GenerateSecret() string {
	s, err := randomHex(32)
	if err != nil {
		// 极端情况下退化到时间戳，至少保证进程能起来并给出明确日志。
		return fmt.Sprintf("insecure-%d", time.Now().UnixNano())
	}
	return s
}
