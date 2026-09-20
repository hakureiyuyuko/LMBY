package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProviderCacheEntry 是一条缓存的 provider 响应。
type ProviderCacheEntry struct {
	Provider  string
	Kind      string
	Key       string
	Lang      string
	Body      jsonb
	FetchedAt time.Time
	ExpiresAt time.Time
}

// jsonb 是给缓存用的别名，强调「这里存的是原样 JSON」。
type jsonb = json.RawMessage

// GetProviderCache 读取缓存。未命中或已过期返回 ErrNotFound。
func (s *Store) GetProviderCache(ctx context.Context, provider, kind, key, lang string) (*ProviderCacheEntry, error) {
	var e ProviderCacheEntry
	err := s.pool.QueryRow(ctx,
		`select provider, kind, cache_key, lang, body, fetched_at, expires_at
		 from provider_cache
		 where provider = $1 and kind = $2 and cache_key = $3 and lang = $4
		   and expires_at > now()`, provider, kind, key, lang).
		Scan(&e.Provider, &e.Kind, &e.Key, &e.Lang, &e.Body, &e.FetchedAt, &e.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取 provider 缓存失败: %w", err)
	}
	return &e, nil
}

// PutProviderCache 写入缓存。
func (s *Store) PutProviderCache(ctx context.Context, provider, kind, key, lang string, body []byte, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	_, err := s.pool.Exec(ctx,
		`insert into provider_cache (provider, kind, cache_key, lang, body, fetched_at, expires_at)
		 values ($1, $2, $3, $4, $5, now(), now() + make_interval(secs => $6))
		 on conflict (provider, kind, cache_key, lang)
		 do update set body = excluded.body,
		               fetched_at = excluded.fetched_at,
		               expires_at = excluded.expires_at`,
		provider, kind, key, lang, body, ttl.Seconds())
	if err != nil {
		return fmt.Errorf("写入 provider 缓存失败: %w", err)
	}
	return nil
}

// PurgeProviderCache 清理过期缓存，返回删除条数。
func (s *Store) PurgeProviderCache(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`delete from provider_cache where expires_at < now() - make_interval(secs => $1)`,
		7*24*3600.0)
	if err != nil {
		return 0, fmt.Errorf("清理 provider 缓存失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ClearProviderCache 清空某个 provider 的全部缓存（换语言、怀疑数据不对时用）。
func (s *Store) ClearProviderCache(ctx context.Context, provider string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `delete from provider_cache where provider = $1`, provider)
	if err != nil {
		return 0, fmt.Errorf("清空 provider 缓存失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ProviderCacheStats 是缓存规模。
type ProviderCacheStats struct {
	Entries int64 `json:"entries"`
	Expired int64 `json:"expired"`
}

// ProviderCacheStatsOf 统计缓存条目数与其中已过期数量。
func (s *Store) ProviderCacheStatsOf(ctx context.Context) (*ProviderCacheStats, error) {
	var st ProviderCacheStats
	err := s.pool.QueryRow(ctx,
		`select count(*), count(*) filter (where expires_at <= now()) from provider_cache`).
		Scan(&st.Entries, &st.Expired)
	if err != nil {
		return nil, fmt.Errorf("统计 provider 缓存失败: %w", err)
	}
	return &st, nil
}
