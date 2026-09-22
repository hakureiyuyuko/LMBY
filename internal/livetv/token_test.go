package livetv

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

// fakeSigner 是单测用的假签名器（真实现是 secrets.Cipher）。
type fakeSigner struct{ key string }

func (f fakeSigner) Sign(domain string, msg []byte) []byte {
	m := hmac.New(sha256.New, []byte(f.key))
	m.Write([]byte(domain))
	m.Write([]byte{0})
	m.Write(msg)
	return m.Sum(nil)
}

func TestPlayTokenRoundTrip(t *testing.T) {
	s := fakeSigner{key: "k1"}
	now := time.Unix(1_700_000_000, 0)
	tok := SignPlayToken(s, 42, now.Add(time.Hour))

	id, err := ParsePlayToken(s, tok, now)
	if err != nil {
		t.Fatalf("校验失败: %v（token=%s）", err, tok)
	}
	if id != 42 {
		t.Errorf("频道 id = %d，期望 42", id)
	}
	// token 必须能直接塞进 URL 路径
	if strings.ContainsAny(tok, "/?# ") {
		t.Errorf("token 里有路径不安全字符: %s", tok)
	}
}

func TestPlayTokenRejectsTamper(t *testing.T) {
	s := fakeSigner{key: "k1"}
	now := time.Unix(1_700_000_000, 0)
	tok := SignPlayToken(s, 42, now.Add(time.Hour))
	parts := strings.Split(tok, "-")

	cases := map[string]string{
		"改频道 id":  "43-" + parts[1] + "-" + parts[2],
		"延长过期时间":  parts[0] + "-" + "9999999999" + "-" + parts[2],
		"改签名":     parts[0] + "-" + parts[1] + "-" + strings.Repeat("a", len(parts[2])),
		"去掉签名":    parts[0] + "-" + parts[1],
		"多一段":     tok + "-x",
		"空 token": "",
	}
	for name, bad := range cases {
		if _, err := ParsePlayToken(s, bad, now); err != ErrTokenInvalid {
			t.Errorf("%s：期望 ErrTokenInvalid，实际 %v", name, err)
		}
	}
}

func TestPlayTokenExpiry(t *testing.T) {
	s := fakeSigner{key: "k1"}
	now := time.Unix(1_700_000_000, 0)

	tok := SignPlayToken(s, 7, now.Add(-time.Second))
	if _, err := ParsePlayToken(s, tok, now); err != ErrTokenExpired {
		t.Errorf("过期 token：期望 ErrTokenExpired，实际 %v", err)
	}

	// 边界：恰好到期的瞬间还算有效（now.Unix() == exp）
	tok = SignPlayToken(s, 7, now)
	if _, err := ParsePlayToken(s, tok, now); err != nil {
		t.Errorf("恰好到期应当仍然有效，实际 %v", err)
	}
}

func TestPlayTokenWrongKeyRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := SignPlayToken(fakeSigner{key: "k1"}, 5, now.Add(time.Hour))
	if _, err := ParsePlayToken(fakeSigner{key: "k2"}, tok, now); err != ErrTokenInvalid {
		t.Errorf("换密钥后旧 token 必须失效，实际 %v", err)
	}
}
