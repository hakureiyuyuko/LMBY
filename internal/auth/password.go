// Package auth 提供口令哈希与会话令牌的底层工具（纯函数，不碰数据库）。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Params 是 Argon2id 的代价参数。
type Params struct {
	Memory      uint32 // 内存开销，单位 KiB
	Iterations  uint32 // 迭代次数
	Parallelism uint8  // 并行度
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams 约 64 MiB / 3 轮，OWASP 推荐量级。
// 单次校验几十毫秒，足以让离线爆破变得昂贵，又不至于影响登录体验。
var DefaultParams = Params{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

// ErrInvalidHash 表示哈希字符串不是本程序能识别的格式。
var ErrInvalidHash = errors.New("auth: 口令哈希格式非法")

// HashPassword 生成 PHC 字符串形式的 argon2id 哈希。
func HashPassword(password string, p Params) (string, error) {
	if password == "" {
		return "", errors.New("auth: 口令不能为空")
	}
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: 生成随机盐失败: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword 校验口令。
//
// 参数从哈希串本身解析，因此以后调高 DefaultParams 也不会让旧口令失效。
// needsRehash 为 true 表示该哈希的代价参数已弱于当前默认值，登录成功后应静默重算。
func VerifyPassword(password, encoded string) (ok bool, needsRehash bool, err error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	needsRehash = p.Memory < DefaultParams.Memory ||
		p.Iterations < DefaultParams.Iterations ||
		p.Parallelism < DefaultParams.Parallelism
	return true, needsRehash, nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// DummyVerify 在「用户名不存在」时也消耗一次等价的哈希计算，
// 让攻击者无法用响应时间区分「用户不存在」和「口令错误」。
func DummyVerify(password string) {
	dummyOnce.Do(func() {
		h, err := HashPassword("lmby-timing-equalizer", DefaultParams)
		if err == nil {
			dummyHash = h
		}
	})
	if dummyHash != "" {
		_, _, _ = VerifyPassword(password, dummyHash)
	}
}

func decodeHash(encoded string) (Params, []byte, []byte, error) {
	var p Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	return p, salt, key, nil
}
