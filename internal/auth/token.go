package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// sessionTokenBytes 是会话令牌的随机字节数（256 bit）。
const sessionTokenBytes = 32

// NewSessionToken 生成会话令牌。
//
// 返回值分两部分：plain 只交给浏览器的 Cookie，hash 存进数据库。
// 这样即使数据库泄露也无法直接冒用会话。
func NewSessionToken() (plain, hash string, err error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: 生成会话令牌失败: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashSessionToken(plain), nil
}

// HashSessionToken 计算会话令牌的存储形式（sha256 十六进制）。
func HashSessionToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
