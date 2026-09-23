package settings

// 管理 API 密钥：给外部程序（bot / 脚本 / 自动化）用的**长期凭据**。
//
// 为什么不能直接用登录会话：会话是「人」在用 —— cookie 跟浏览器、会过期、
// 还会一改口令就被吊销。机器需要的是「放在 Header 里的长期凭据」。
//
// 三条刻意的设计：
//
//  1. **只存哈希**（SHA-256）：密钥不需要被读回来（生成时给管理员看一次就够），
//     存哈希意味着数据库泄露也拿不到能用的密钥 —— 比「可解密的加密存储」更安全；
//  2. 明文只在生成的那一刻返回一次，界面上之后只显示前缀（认得出是哪一个就行）；
//  3. 权限另说（在 api 层）：这把钥匙**只能开用户管理那几个接口**，
//     泄露时最坏情况是「乱建/删账号」，而不是「改设置、删媒体库、读别人的库」。

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SettingKeyBotAPIKey 是管理 API 密钥在 settings 表里的键。
const SettingKeyBotAPIKey = "bot.api_key"

// BotKeyInfo 是管理密钥的元信息（**不含明文**）。
type BotKeyInfo struct {
	// Hash 是密钥的 SHA-256（十六进制）。
	Hash string `json:"hash"`
	// Prefix 是密钥前 8 个字符：界面上用来辨认「当前是哪一个密钥」。
	Prefix string `json:"prefix"`
	// CreatedAt 是生成时间。
	CreatedAt time.Time `json:"createdAt"`
}

// ErrNoBotKey 表示还没配置过管理密钥。
var ErrNoBotKey = errors.New("还没有配置管理密钥")

// BotKey 读当前管理密钥的元信息。
func (s *Service) BotKey(ctx context.Context) (*BotKeyInfo, error) {
	raw, found, err := s.st.GetSetting(ctx, SettingKeyBotAPIKey)
	if err != nil {
		return nil, fmt.Errorf("读取管理密钥失败: %w", err)
	}
	if !found {
		return nil, ErrNoBotKey
	}
	var info BotKeyInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("管理密钥记录损坏: %w", err)
	}
	return &info, nil
}

// SetBotKey 记录一把新的管理密钥（只存哈希与前缀）。
func (s *Service) SetBotKey(ctx context.Context, key string) error {
	info := BotKeyInfo{
		Hash:      hashKey(key),
		Prefix:    prefixOf(key),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.SetSetting(ctx, SettingKeyBotAPIKey, info); err != nil {
		return fmt.Errorf("保存管理密钥失败: %w", err)
	}
	return nil
}

// ClearBotKey 撤销管理密钥（返回之前有没有）。
func (s *Service) ClearBotKey(ctx context.Context) (bool, error) {
	existed, err := s.st.DeleteSetting(ctx, SettingKeyBotAPIKey)
	if err != nil {
		return false, fmt.Errorf("撤销管理密钥失败: %w", err)
	}
	return existed, nil
}

// VerifyBotKey 校验一把密钥。
//
// 用 constant-time 比较：密钥比较是密码学意义上的「秘密比较」，
// 别让时序差异泄漏「前几个字符猜对了」。
func (s *Service) VerifyBotKey(ctx context.Context, key string) (bool, error) {
	info, err := s.BotKey(ctx)
	if err != nil {
		if errors.Is(err, ErrNoBotKey) {
			return false, nil
		}
		return false, err
	}
	want := info.Hash
	got := hashKey(key)
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1, nil
}

// hashKey 是密钥的哈希（十六进制 SHA-256）。
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// prefixOf 取前 8 个字符（短于此就整个返回）。
func prefixOf(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:8]
}
