package secrets

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSignIsDeterministicAndDomainSeparated(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	msg := []byte("42-1700000000")

	a := c.Sign("domain-a", msg)
	b := c.Sign("domain-a", msg)
	if !bytes.Equal(a, b) {
		t.Fatal("同一 domain 与消息的签名必须稳定（否则发出去的 token 下一秒就失效）")
	}
	if len(a) != 32 {
		t.Errorf("签名长度 = %d，期望 32（HMAC-SHA256）", len(a))
	}
	if bytes.Equal(a, c.Sign("domain-b", msg)) {
		t.Error("不同 domain 必须签出不同的值（用途隔离）")
	}
	if bytes.Equal(a, c.Sign("domain-a", []byte("42-1700000001"))) {
		t.Error("消息改了签名必须跟着变")
	}
}

func TestSignDiffersAcrossKeys(t *testing.T) {
	c1, _ := LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	c2, _ := LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	if bytes.Equal(c1.Sign("d", []byte("m")), c2.Sign("d", []byte("m"))) {
		t.Error("换密钥后签名必须不同（否则外链 token 在新密钥下仍然有效）")
	}
}
