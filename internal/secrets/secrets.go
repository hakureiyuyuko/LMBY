// Package secrets 保护「存在数据库里的凭据」（目前是 TMDB Key）。
//
// 威胁模型很具体：**数据库的备份/导出被单独泄漏**（pg_dump 传给了别人、备份盘丢了），
// 而数据目录没跟着泄漏。配置在 config.toml 里的凭据本来就有这个风险，
// 所以数据库里的凭据至少要做到同级或更好：用一个只存在数据目录里、权限 0600 的密钥加密。
//
// 刻意不做的：
//   - 不防「同时拿到数据库和数据目录」的攻击者（那需要一个不在机器上的主密钥，
//     部署复杂度会上一个台阶）；
//   - 不假装是密钥管理系统（没有轮换、没有 KMS 集成）。
//
// 密文格式：`enc:v1:<base64(nonce||ciphertext)>`。带前缀才认，不带前缀按**明文**读 ——
// 这样手工往库里塞过明文凭据的老部署不会突然连不上，也便于肉眼分辨。
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Prefix 是密文的前缀（版本化：以后换算法能区分开）。
const Prefix = "enc:v1:"

// keySize 是 AES-256 的密钥长度。
const keySize = 32

// Cipher 是加解密器。
type Cipher struct {
	aead cipher.AEAD
	// path 是密钥文件位置，只用于报错信息。
	path string
}

// LoadOrCreate 读取密钥文件；不存在就生成一个（目录 0700、文件 0600）。
//
// 幂等：文件存在时读它，不会重新生成（重新生成会让已存的密文全部解不开）。
func LoadOrCreate(path string) (*Cipher, error) {
	key, err := readKeyFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key, err = createKeyFile(path)
	}
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化加密器失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化加密器失败: %w", err)
	}
	return &Cipher{aead: aead, path: path}, nil
}

// Path 返回密钥文件位置。
func (c *Cipher) Path() string { return c.path }

// Seal 加密。空串原样返回（「没设置」不该变成一段密文）。
func (c *Cipher) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, []byte(plain), nil)

	// nonce 与密文拼在一起（解密时前 NonceSize 字节就是 nonce）
	blob := make([]byte, 0, len(nonce)+len(ct))
	blob = append(blob, nonce...)
	blob = append(blob, ct...)
	return Prefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Open 解密。不带前缀的值按明文返回（手工写进库里的老数据）。
func (c *Cipher) Open(value string) (string, error) {
	if value == "" || !IsSealed(value) {
		return value, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, Prefix))
	if err != nil {
		return "", fmt.Errorf("密文格式非法: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足")
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		// 最常见的两种原因：换了密钥文件，或数据被人改过
		return "", fmt.Errorf("解密失败（密钥文件是不是换了？%s）: %w", c.path, err)
	}
	return string(plain), nil
}

// IsSealed 判断一个值是否已经加密。
func IsSealed(value string) bool { return strings.HasPrefix(value, Prefix) }

// ---------------------------------------------------------------- 密钥文件

func readKeyFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("密钥文件 %s 不是十六进制: %w", path, err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("密钥文件 %s 长度不对（%d 字节，需要 %d）", path, len(key), keySize)
	}
	return key, nil
}

func createKeyFile(path string) ([]byte, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建密钥目录失败: %w", err)
		}
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成密钥失败: %w", err)
	}
	// O_EXCL：万一两个进程同时启动，后到的那个不会覆盖先写入的密钥
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readKeyFile(path)
		}
		return nil, fmt.Errorf("写入密钥文件失败: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		return nil, fmt.Errorf("写入密钥文件失败: %w", err)
	}
	return key, nil
}
