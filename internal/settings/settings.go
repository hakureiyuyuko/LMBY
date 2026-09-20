// Package settings 管理「运行期可以改、并且要持久化」的设置。
//
// 目前唯一的用途是 TMDB 凭据：以前只能改 config.toml 再重启服务，现在能在设置页里填、
// 存进数据库、**立刻生效**（下一次刮削/图片回源就用新凭据）。
//
// 三层来源，优先级从高到低：
//
//  1. 数据库（设置页写入的；两个密钥字段是密文，见 internal/secrets）
//  2. 配置文件 / 环境变量（初始值，也是数据库里没有时的兜底）
//  3. 空（那就没有元数据源，扫描与浏览照常）
//
// 「数据库优先」这件事必须让用户看得见：设置页会显示当前值来自哪里，
// 并且提供「恢复为配置文件的值」把数据库里那条删掉。
package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/hakureiyuyuko/lmby/internal/secrets"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// DefaultLanguage 是没指定语言时的默认值（与 config 的默认一致）。
const DefaultLanguage = "zh-CN"

// TMDB 是 TMDB 元数据源的设置（明文形态，只在内存与 HTTP 响应里出现）。
type TMDB struct {
	ReadToken string `json:"readToken,omitempty"`
	APIKey    string `json:"apiKey,omitempty"`
	Language  string `json:"language,omitempty"`
	// FromDB 表示这份值来自数据库（设置页写入的），而不是配置文件兜底。
	FromDB bool `json:"fromDb"`
}

// Configured 表示凭据是否够用（够用才谈得上刮削）。
func (t TMDB) Configured() bool {
	return strings.TrimSpace(t.ReadToken) != "" || strings.TrimSpace(t.APIKey) != ""
}

// Patch 是设置页提交的补丁：字段为 nil 表示「不改」，指向空串表示「清掉」。
//
// 为什么需要这种区分：密钥类字段不可能回显给前端（否则等于把秘密摊在页面上），
// 所以界面提交时「没填」与「要清空」必须是两种不同的意图。
type Patch struct {
	ReadToken *string
	APIKey    *string
	Language  *string
}

// Service 持有当前设置，并负责与数据库同步。
type Service struct {
	st     *store.Store
	cipher *secrets.Cipher
	log    *slog.Logger

	// fallback 是配置文件里的值（数据库里没有设置时用它）
	fallback TMDB

	mu      sync.RWMutex
	current TMDB

	// onChange 在设置变化后调用（main 用它把新凭据推给 TMDB 客户端）
	onChange func(TMDB)
}

// New 构造服务。cipher 可以为 nil —— 那就退化成明文存储（会打一条警告）。
func New(st *store.Store, cipher *secrets.Cipher, fallback TMDB, log *slog.Logger) *Service {
	if fallback.Language == "" {
		fallback.Language = DefaultLanguage
	}
	return &Service{
		st:       st,
		cipher:   cipher,
		log:      log,
		fallback: fallback,
		current:  fallback,
	}
}

// SetOnChange 注册变化回调。
func (s *Service) SetOnChange(fn func(TMDB)) { s.onChange = fn }

// Load 从数据库装载设置（启动时调一次）。数据库里没有就用配置兜底。
func (s *Service) Load(ctx context.Context) (TMDB, error) {
	creds, found, err := s.st.GetTMDBCredentials(ctx)
	if err != nil {
		return s.TMDB(), err
	}
	if !found {
		s.mu.Lock()
		s.current = s.fallback
		s.mu.Unlock()
		return s.TMDB(), nil
	}

	tmdb, err := s.decrypt(creds)
	if err != nil {
		// 解不开时**不静默回落到配置**：那会让用户以为设置在生效，
		// 而实际用的是另一套凭据（更难查）。留着错误让上层决定报不报。
		return s.TMDB(), err
	}
	tmdb.FromDB = true
	s.mu.Lock()
	s.current = tmdb
	s.mu.Unlock()
	s.log.Info("已从数据库装载 TMDB 设置", "language", tmdb.Language,
		"auth", authMode(tmdb), "encrypted", s.cipher != nil)
	return s.TMDB(), nil
}

// TMDB 返回当前设置（明文）。
func (s *Service) TMDB() TMDB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Configured 表示当前是否有可用的 TMDB 凭据。
func (s *Service) Configured() bool { return s.TMDB().Configured() }

// Fallback 返回配置文件里的值（设置页用来显示「配置文件里的值是什么」）。
func (s *Service) Fallback() TMDB { return s.fallback }

// Encrypted 表示密钥字段是否会被加密存储。
func (s *Service) Encrypted() bool { return s.cipher != nil }

// Apply 应用一次补丁：更新内存、加密后写库、通知回调。
//
// 返回的新值里带 FromDB=true —— 只要写进过数据库，数据库就接管了优先级。
func (s *Service) Apply(ctx context.Context, patch Patch) (TMDB, bool, error) {
	next := s.TMDB()
	if patch.ReadToken != nil {
		next.ReadToken = strings.TrimSpace(*patch.ReadToken)
	}
	if patch.APIKey != nil {
		next.APIKey = strings.TrimSpace(*patch.APIKey)
	}
	if patch.Language != nil {
		lang := strings.TrimSpace(*patch.Language)
		if lang == "" {
			lang = s.fallback.Language
		}
		next.Language = lang
	}
	if next.Language == "" {
		next.Language = DefaultLanguage
	}
	next.FromDB = true

	langChanged := next.Language != s.TMDB().Language

	sealed, err := s.encrypt(next)
	if err != nil {
		return s.TMDB(), false, err
	}
	if err := s.st.SaveTMDBCredentials(ctx, sealed); err != nil {
		return s.TMDB(), false, err
	}

	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange(next)
	}
	s.log.Info("已更新 TMDB 设置", "language", next.Language, "auth", authMode(next), "fromDb", true)
	return next, langChanged, nil
}

// Reset 删掉数据库里的设置，回落到配置文件的值（设置页的「恢复为配置文件的值」）。
func (s *Service) Reset(ctx context.Context) (TMDB, error) {
	if _, err := s.st.DeleteSetting(ctx, store.SettingKeyTMDBCredentials); err != nil {
		return s.TMDB(), err
	}
	s.mu.Lock()
	s.current = s.fallback
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange(s.fallback)
	}
	s.log.Info("已删除数据库里的 TMDB 设置，回落为配置文件的值",
		"language", s.fallback.Language, "auth", authMode(s.fallback))
	return s.TMDB(), nil
}

// ---------------------------------------------------------------- 加解密

// encrypt 把密钥字段加密（两边都空时也照样写一条记录 —— 那是「用户显式设成了空」，
// 与「数据库里没有」是两件事）。
func (s *Service) encrypt(in TMDB) (store.TMDBCredentials, error) {
	out := store.TMDBCredentials{Language: in.Language}
	if s.cipher == nil {
		out.ReadToken, out.APIKey = in.ReadToken, in.APIKey
		return out, nil
	}
	var err error
	if out.ReadToken, err = s.cipher.Seal(in.ReadToken); err != nil {
		return out, fmt.Errorf("加密 TMDB read token 失败: %w", err)
	}
	if out.APIKey, err = s.cipher.Seal(in.APIKey); err != nil {
		return out, fmt.Errorf("加密 TMDB api key 失败: %w", err)
	}
	return out, nil
}

func (s *Service) decrypt(in *store.TMDBCredentials) (TMDB, error) {
	out := TMDB{Language: in.Language}
	if s.cipher == nil {
		out.ReadToken, out.APIKey = in.ReadToken, in.APIKey
		return out, nil
	}
	var err error
	if out.ReadToken, err = s.cipher.Open(in.ReadToken); err != nil {
		return out, fmt.Errorf("解密 TMDB read token 失败: %w", err)
	}
	if out.APIKey, err = s.cipher.Open(in.APIKey); err != nil {
		return out, fmt.Errorf("解密 TMDB api key 失败: %w", err)
	}
	return out, nil
}

// authMode 把凭据形态描述成人话（与 main.go 里的同名函数一致）。
func authMode(t TMDB) string {
	switch {
	case strings.TrimSpace(t.ReadToken) != "":
		return "v4 read access token"
	case strings.TrimSpace(t.APIKey) != "":
		return "v3 api key"
	default:
		return "未配置"
	}
}

// ErrNoCredentials 表示没有任何凭据（不能做需要元数据源的事）。
var ErrNoCredentials = errors.New("settings: 未配置 TMDB 凭据")
