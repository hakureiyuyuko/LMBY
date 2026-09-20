package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	c, err := LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	for _, plain := range []string{"abc123", "带中文的口令-!@#$%^&*()", strings.Repeat("x", 500)} {
		sealed, err := c.Seal(plain)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plain, err)
		}
		if !IsSealed(sealed) {
			t.Fatalf("密文应当带前缀 %q：%q", Prefix, sealed)
		}
		if strings.Contains(sealed, plain) && plain != "" {
			t.Fatalf("密文里不该出现明文")
		}
		got, err := c.Open(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != plain {
			t.Fatalf("Open 回来不一致：%q != %q", got, plain)
		}
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	c, _ := LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	a, _ := c.Seal("same")
	b, _ := c.Seal("same")
	if a == b {
		t.Error("同一个明文两次加密不该得到相同密文（nonce 每次都要新）")
	}
}

func TestSealEmptyStaysEmpty(t *testing.T) {
	c, _ := LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	got, err := c.Seal("")
	if err != nil || got != "" {
		t.Fatalf("空串应当原样返回，得到 %q err=%v", got, err)
	}
}

func TestOpenPlaintextPassthrough(t *testing.T) {
	// 手工往库里塞的明文凭据必须还能用（否则老部署会突然连不上）
	c, _ := LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	got, err := c.Open("a-plain-token-from-before")
	if err != nil || got != "a-plain-token-from-before" {
		t.Fatalf("明文应当原样返回，得到 %q err=%v", got, err)
	}
}

func TestOpenDetectsTamperAndWrongKey(t *testing.T) {
	dir := t.TempDir()
	c1, _ := LoadOrCreate(filepath.Join(dir, "k1.key"))
	sealed, _ := c1.Seal("secret-value")

	// 改一个字节
	raw := []byte(sealed)
	raw[len(raw)-2] = raw[len(raw)-2] ^ 0x01
	if _, err := c1.Open(string(raw)); err == nil {
		t.Error("密文被改过还解开了")
	}

	// 换密钥文件
	c2, _ := LoadOrCreate(filepath.Join(dir, "k2.key"))
	if _, err := c2.Open(sealed); err == nil {
		t.Error("用另一个密钥解开了")
	}
}

func TestKeyFileIsCreatedOnceWith0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secret.key")
	c1, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("密钥文件没建出来: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("密钥文件权限 = %o，期望 600", perm)
	}

	// 再读一次必须是同一把钥匙（不能重新生成，否则已存的密文全废）
	c2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("第二次 LoadOrCreate: %v", err)
	}
	sealed, _ := c1.Seal("hello")
	if got, err := c2.Open(sealed); err != nil || got != "hello" {
		t.Fatalf("同一密钥文件应当能互相解密：got=%q err=%v", got, err)
	}
}

func TestBadKeyFile(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("not-hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(bad); err == nil {
		t.Error("非十六进制的密钥文件应当报错")
	}

	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte("00112233\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(short); err == nil {
		t.Error("长度不对的密钥文件应当报错")
	}
}
