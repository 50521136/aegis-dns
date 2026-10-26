// Package id 生成带前缀的、时间有序的唯一标识符。
//
// 使用 UUID v7 布局（48 位毫秒时间戳 + 版本/变体位 + 随机位），
// 好处是同一毫秒内的 ID 仍然随机，但整体按时间单调递增，
// 对 SQLite 的 B-Tree 索引友好（新记录总是追加，不产生页分裂）。
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 领域前缀，与文档中的 ID 形态保持一致。
const (
	PrefixUser = "usr_"
	PrefixRule = "rul_"
	PrefixSub  = "sub_"
	// PrefixFeedback 用于用户反馈。
	PrefixFeedback = "fb_"
	// PrefixFeedbackMsg 用于反馈时间线上的单条消息。
	PrefixFeedbackMsg = "fbm_"
	// PrefixNotification 用于站内通知。
	PrefixNotification = "ntf_"
)

var (
	mu       sync.Mutex
	lastMS   int64
	sequence uint16
)

// New 生成一个带前缀的 UUID v7 字符串，例如 usr_01926f4a-....
//
// 返回值形如 <prefix><8-4-4-4-12>，前缀必须由调用方给出（含下划线）。
func New(prefix string) string {
	return prefix + UUIDv7()
}

// UUIDv7 生成一个标准格式的 UUID v7 字符串。
func UUIDv7() string {
	ms := time.Now().UnixMilli()

	mu.Lock()
	if ms <= lastMS {
		// 同一毫秒内（或时钟回拨）用序列号保证单调递增。
		sequence++
		if sequence > 0x0fff {
			// 序列号耗尽：借用下一毫秒，宁可略微超前也不重复。
			lastMS++
			sequence = 0
			ms = lastMS
		}
	} else {
		lastMS = ms
		sequence = 0
	}
	seq := sequence
	cur := lastMS
	mu.Unlock()

	var b [16]byte
	// 前 48 位：毫秒时间戳（大端）。
	b[0] = byte(cur >> 40)
	b[1] = byte(cur >> 32)
	b[2] = byte(cur >> 24)
	b[3] = byte(cur >> 16)
	b[4] = byte(cur >> 8)
	b[5] = byte(cur)
	// 版本 7 + 高 12 位序列号。
	b[6] = 0x70 | byte(seq>>8)&0x0f
	b[7] = byte(seq)
	// 变体位 0b10 + 62 位随机数。
	if _, err := rand.Read(b[8:]); err != nil {
		// crypto/rand 失败在现代内核上不可能发生；真发生就退化为时间纳秒填充，
		// 保证函数不 panic（ID 只要求唯一，不要求密码学强度）。
		n := time.Now().UnixNano()
		for i := 8; i < 16; i++ {
			b[i] = byte(n >> (uint(i-8) * 8))
		}
	}
	b[8] = (b[8] & 0x3f) | 0x80

	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// 客户端 ID 字符集：base32 的小写形式（RFC 4648 §6），天然排除 0/1/8/9，
// 避免在日志、截图、口述中与 O/I/B 混淆。
// clientIDAlphabet 是**生成** client_id 用的字符集。
//
// 刻意去掉 0/1/8/9：这些字符和 o/l 在不少字体里几乎分不清，用户手抄会抄错。
const clientIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// clientIDAccepted 是**校验**时接受的字符集，比生成用的宽。
//
// 放宽的原因：用户自定义 client_id 时不该被「不能打 0 和 1」这种内部约定绑住，
// 想叫 shenzhen01 就该能叫。生成侧仍然用窄字符集，所以系统发的 ID 不会有歧义。
// 注意这里只加 0-9，不加连字符和点：client_id 是 DNS 标签的首段，
// 允许 "-" 会让 "a--b"、"-abc" 这类边界形态混进来，收益不值这个复杂度。
const clientIDAccepted = "abcdefghijklmnopqrstuvwxyz0123456789"

// CustomClientIDMinLen 是用户自定义 client_id 的最小长度。
//
// 比生成的 10 位短，但不能太短：client_id 是子域名的首段，太短既容易撞车，
// 也更容易被扫描猜到别人的租户。
const CustomClientIDMinLen = 6

// ClientIDLen 是 client_id 的长度，也是它的子域名标签长度。
const ClientIDLen = 10

// NewClientID 生成一个 10 位 base32 小写 client_id（约 50 bit 熵）。
//
// 使用拒绝采样消除取模偏差：256 % 32 == 0，所以这里其实无偏，
// 但保留通用写法以防将来调整字符集长度。
func NewClientID() string {
	const n = len(clientIDAlphabet) // 32
	// 32 整除 256，直接映射即可，无偏差；写成通用形式以便将来调整字符集。
	max := 256 - (256 % n)
	buf := make([]byte, ClientIDLen)
	out := make([]byte, 0, ClientIDLen)
	for len(out) < ClientIDLen {
		if _, err := rand.Read(buf); err != nil {
			nano := time.Now().UnixNano()
			for i := range buf {
				buf[i] = byte(nano >> (uint(i) * 8))
			}
		}
		for _, c := range buf {
			if int(c) >= max {
				continue // 拒绝采样
			}
			out = append(out, clientIDAlphabet[int(c)%n])
			if len(out) == ClientIDLen {
				break
			}
		}
	}
	return string(out)
}

// ValidClientID 校验 client_id 是否满足 ^[a-z2-7]{10}$。
//
// 文档允许长度 4-32 以兼容手工设置，但本实现只生成 10 位；
// 校验放宽到 4-32 是为了让管理员能通过 API 覆盖成自定义值。
func ValidClientID(s string) bool {
	if len(s) < 4 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune(clientIDAccepted, c) {
			return false
		}
	}
	return true
}

// CheckCustomClientID 校验用户自定义的 client_id，返回人类可读的拒绝原因。
//
// 和 ValidClientID 分开是因为这里的规则是给用户看的：
// 长度下限更高（6 位），而且错误信息要说清楚到底哪一条不满足 ——
// 只回一个 false，前端就只能写「格式不正确」，用户根本不知道该改什么。
func CheckCustomClientID(s string) error {
	if s == "" {
		return fmt.Errorf("请填写接入 ID")
	}
	if len(s) < CustomClientIDMinLen {
		return fmt.Errorf("接入 ID 至少 %d 位，当前 %d 位", CustomClientIDMinLen, len(s))
	}
	if len(s) > 32 {
		return fmt.Errorf("接入 ID 最多 32 位，当前 %d 位", len(s))
	}
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			return fmt.Errorf("只能用小写字母和数字，不支持大写")
		}
		if !strings.ContainsRune(clientIDAccepted, c) {
			return fmt.Errorf("只能用小写字母和数字，不能包含 %q", string(c))
		}
	}
	return nil
}

// NewPAT 生成一个长期 API Token（PAT）。
//
// 形态：agx_ + 32 字节随机数的 base32 小写（无填充）。
// 服务端只保存它的 SHA-256，原文仅在创建时返回一次。
func NewPAT() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		nano := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(nano >> (uint(i%8) * 8))
		}
	}
	var sb strings.Builder
	sb.WriteString("agx_")
	// 每 5 位映射到一个 base32 字符。
	var acc uint32
	var bits uint
	for _, b := range buf {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(clientIDAlphabet[(acc>>bits)&0x1f])
		}
	}
	if bits > 0 {
		sb.WriteByte(clientIDAlphabet[(acc<<(5-bits))&0x1f])
	}
	return sb.String()
}
