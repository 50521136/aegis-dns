package api

import (
	"strings"
	"testing"
)

// 这个文件测模型 API Key 的加密存储。
//
// 为什么单独测：Key 一旦以明文落库，泄漏是永久且不可撤销的
// （别人拿去刷额度）。加密逻辑必须能证明「同明文两次加密密文不同」
// 「换密钥解不开」这两条，否则等于没加密。

func mustBox(t *testing.T) *secretBox {
	t.Helper()
	t.Setenv("AEGIS_SECRET", "unit-test-secret-value")
	b, err := newSecretBox(nil)
	if err != nil {
		t.Fatalf("初始化密钥盒失败: %v", err)
	}
	return b
}

// TestSecretBoxRoundTrip 断言加密后可还原。
func TestSecretBoxRoundTrip(t *testing.T) {
	b := mustBox(t)
	plain := "sk-1234567890abcdefghijklmn"
	enc, err := b.encrypt(plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if enc == plain {
		t.Fatal("密文不能等于明文")
	}
	if strings.Contains(enc, plain) {
		t.Fatal("密文里不能包含明文片段")
	}
	got, err := b.decrypt(enc)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != plain {
		t.Fatalf("还原不一致: %q != %q", got, plain)
	}
}

// TestSecretBoxNonceRandomized 断言同一明文两次加密结果不同。
//
// 相同密文说明 nonce 被复用了 —— 那是 GCM 的致命错误（可导致密钥恢复）。
func TestSecretBoxNonceRandomized(t *testing.T) {
	b := mustBox(t)
	a, err := b.encrypt("same-plaintext")
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.encrypt("same-plaintext")
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("同一明文两次加密必须不同（nonce 不能复用）")
	}
}

// TestSecretBoxWrongKey 断言换密钥后解不开。
func TestSecretBoxWrongKey(t *testing.T) {
	t.Setenv("AEGIS_SECRET", "key-one")
	b1, err := newSecretBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := b1.encrypt("secret-value")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("AEGIS_SECRET", "key-two")
	b2, err := newSecretBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b2.decrypt(enc); err == nil {
		t.Fatal("换密钥后必须解不开")
	}
}

// TestSecretBoxEmpty 断言空密文返回空串而不报错（未配置 Key 的正常情况）。
func TestSecretBoxEmpty(t *testing.T) {
	b := mustBox(t)
	got, err := b.decrypt("")
	if err != nil {
		t.Fatalf("空密文不该报错: %v", err)
	}
	if got != "" {
		t.Fatalf("空密文应返回空串，实际 %q", got)
	}
}

// TestSecretBoxCorrupted 断言密文损坏时报错而不是返回垃圾。
func TestSecretBoxCorrupted(t *testing.T) {
	b := mustBox(t)
	if _, err := b.decrypt("not-a-valid-base64!!!"); err == nil {
		t.Fatal("非法 base64 应报错")
	}
	if _, err := b.decrypt("YWJj"); err == nil {
		t.Fatal("长度不足的密文应报错")
	}
}

// TestSplitCommaList 断言采纳阶段的域名列表拆分。
func TestSplitCommaList(t *testing.T) {
	got := splitCommaList(" a.com , b.com ,, c.com ")
	want := []string{"a.com", "b.com", "c.com"}
	if len(got) != len(want) {
		t.Fatalf("期望 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("期望 %v，实际 %v", want, got)
		}
	}
	if got := splitCommaList("   "); got != nil {
		t.Fatalf("空白应返回 nil，实际 %v", got)
	}
}
