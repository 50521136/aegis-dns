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
const clientIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

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
		if !strings.ContainsRune(clientIDAlphabet, c) {
			return false
		}
	}
	return true
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
