package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// 本文件是 settings 表（全局设置）的读写。
//
// 表结构只有 (key, value jsonb)，刻意做成「谁用谁定义自己的形状」——
// 加一项设置不需要迁移。加解密与校验都在上层（internal/settings），
// 这一层只管 JSON 进 JSON 出。

// SettingKeyTMDBCredentials 是 TMDB 凭据在 settings 表里的键。
const SettingKeyTMDBCredentials = "tmdb.credentials"

// TMDBCredentials 是存在数据库里的 TMDB 凭据。
//
// 两个密钥字段存的是**密文**（见 internal/secrets 的 Seal/Open）；
// Language 是明文 —— 它是偏好不是秘密。
type TMDBCredentials struct {
	ReadToken string `json:"readToken,omitempty"`
	APIKey    string `json:"apiKey,omitempty"`
	Language  string `json:"language,omitempty"`
}

// GetSetting 读一项设置。found=false 表示没存过（与「存了个空值」区分开）。
func (s *Store) GetSetting(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `select value from settings where key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取设置 %s 失败: %w", key, err)
	}
	return json.RawMessage(raw), true, nil
}

// SetSetting 写一项设置（upsert）。
func (s *Store) SetSetting(ctx context.Context, key string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("序列化设置 %s 失败: %w", key, err)
	}
	_, err = s.pool.Exec(ctx,
		`insert into settings (key, value, updated_at) values ($1, $2, now())
		 on conflict (key) do update set value = excluded.value, updated_at = now()`,
		key, body)
	if err != nil {
		return fmt.Errorf("写入设置 %s 失败: %w", key, err)
	}
	return nil
}

// DeleteSetting 删一项设置（返回是否删掉了什么）。
func (s *Store) DeleteSetting(ctx context.Context, key string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `delete from settings where key = $1`, key)
	if err != nil {
		return false, fmt.Errorf("删除设置 %s 失败: %w", key, err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetTMDBCredentials 读 TMDB 凭据。found=false 表示数据库里还没有（用配置兜底）。
func (s *Store) GetTMDBCredentials(ctx context.Context) (*TMDBCredentials, bool, error) {
	raw, found, err := s.GetSetting(ctx, SettingKeyTMDBCredentials)
	if err != nil || !found {
		return nil, false, err
	}
	var creds TMDBCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, false, fmt.Errorf("解析数据库里的 TMDB 凭据失败（手工改过这个值？）: %w", err)
	}
	return &creds, true, nil
}

// SaveTMDBCredentials 写 TMDB 凭据。
func (s *Store) SaveTMDBCredentials(ctx context.Context, creds TMDBCredentials) error {
	return s.SetSetting(ctx, SettingKeyTMDBCredentials, creds)
}

// SettingKeyMetadataSharing 是「加入元数据共享改进计划」开关在 settings 表里的键。
const SettingKeyMetadataSharing = "sharing.metadata"

// MetadataSharing 是共享开关的值。
//
// 目前**只有开关本身**：真正的上传功能在**另一个项目**里开发中，这里先把
// 用户的意愿记下来（将来那边来读这个值），不发送任何数据。
type MetadataSharing struct {
	Enabled bool `json:"enabled"`
}

// GetMetadataSharing 读共享开关（没存过 = 没加入，返回 false）。
func (s *Store) GetMetadataSharing(ctx context.Context) (MetadataSharing, error) {
	raw, found, err := s.GetSetting(ctx, SettingKeyMetadataSharing)
	if err != nil || !found {
		return MetadataSharing{}, err
	}
	var v MetadataSharing
	if err := json.Unmarshal(raw, &v); err != nil {
		return MetadataSharing{}, fmt.Errorf("解析元数据共享开关失败: %w", err)
	}
	return v, nil
}

// SetMetadataSharing 写共享开关。
func (s *Store) SetMetadataSharing(ctx context.Context, v MetadataSharing) error {
	return s.SetSetting(ctx, SettingKeyMetadataSharing, v)
}

// SettingKeyKeqDB 是贡献目标站（KeqDB）连接配置在 settings 表里的键。
const SettingKeyKeqDB = "sharing.keqdb"

// KeqDBConfig 是共享目标站的连接配置。
//
// Token 存的是**密文**（见 internal/secrets 的 Seal/Open）—— 与 TMDB 凭据同一套规矩：
// 明文只在内存里、绝不回显；BaseURL 是明文（它不是秘密）。
type KeqDBConfig struct {
	BaseURL string `json:"baseUrl,omitempty"`
	Token   string `json:"token,omitempty"`
}

// GetKeqDBConfig 读 KeqDB 连接配置（没存过 = 空）。
func (s *Store) GetKeqDBConfig(ctx context.Context) (KeqDBConfig, error) {
	raw, found, err := s.GetSetting(ctx, SettingKeyKeqDB)
	if err != nil || !found {
		return KeqDBConfig{}, err
	}
	var v KeqDBConfig
	if err := json.Unmarshal(raw, &v); err != nil {
		return KeqDBConfig{}, fmt.Errorf("解析 KeqDB 配置失败: %w", err)
	}
	return v, nil
}

// SetKeqDBConfig 写 KeqDB 连接配置。
func (s *Store) SetKeqDBConfig(ctx context.Context, v KeqDBConfig) error {
	return s.SetSetting(ctx, SettingKeyKeqDB, v)
}
