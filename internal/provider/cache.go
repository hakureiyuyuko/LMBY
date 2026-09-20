package provider

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// CacheStore 是缓存装饰器需要的最小存储能力（收窄接口便于测试）。
type CacheStore interface {
	GetProviderCache(ctx context.Context, provider, kind, key, lang string) (*store.ProviderCacheEntry, error)
	PutProviderCache(ctx context.Context, provider, kind, key, lang string, body []byte, ttl time.Duration) error
}

// TTLOptions 是按类型区分的缓存有效期。
//
// 搜索结果的时效性更强（新片上映），详情页几乎不变，所以分开配置。
type TTLOptions struct {
	Search  time.Duration
	Details time.Duration
	Season  time.Duration
	Episode time.Duration
	Images  time.Duration
	Credits time.Duration
}

// DefaultTTLs 返回默认缓存策略。
func DefaultTTLs() TTLOptions {
	return TTLOptions{
		Search:  24 * time.Hour,
		Details: 7 * 24 * time.Hour,
		Season:  7 * 24 * time.Hour,
		Episode: 7 * 24 * time.Hour,
		Images:  7 * 24 * time.Hour,
		Credits: 30 * 24 * time.Hour,
	}
}

// Cached 给任意 Client 套一层 PostgreSQL 缓存。
//
// 为什么强调「套一层」：缓存是横切关注点，放在装饰器里，
// provider 实现（tmdb 包）就完全不用知道缓存的存在，也便于单测。
type Cached struct {
	inner  Client
	st     CacheStore
	ttl    TTLOptions
	hits   atomic.Int64
	misses atomic.Int64
}

// NewCached 构造带缓存的 provider。
func NewCached(inner Client, st CacheStore, ttl TTLOptions) *Cached {
	return &Cached{inner: inner, st: st, ttl: ttl}
}

// Name 实现 Client。
func (c *Cached) Name() string { return c.inner.Name() }

// ImageURL 实现 Client：直通底层（图片 URL 不是数据，没什么可缓存的）。
func (c *Cached) ImageURL(path, size string) string { return c.inner.ImageURL(path, size) }

// Stats 返回缓存命中/未命中计数（便于观察与调优）。
func (c *Cached) Stats() (hits, misses int64) {
	return c.hits.Load(), c.misses.Load()
}

// Inner 返回被装饰的原始 client（诊断用）。
func (c *Cached) Inner() Client { return c.inner }

// fetchCached 是通用的「先查缓存、未命中再取并写入」流程。
func fetchCached[T any](
	c *Cached, ctx context.Context,
	kind, key, lang string, ttl time.Duration,
	fetch func() (T, error),
) (T, error) {
	var zero T

	if e, err := c.st.GetProviderCache(ctx, c.inner.Name(), kind, key, lang); err == nil {
		var v T
		if json.Unmarshal(e.Body, &v) == nil {
			c.hits.Add(1)
			return v, nil
		}
		// 缓存里是坏数据：当成未命中，重新取一次覆盖掉
	}

	c.misses.Add(1)
	v, err := fetch()
	if err != nil {
		return zero, err
	}
	if body, mErr := json.Marshal(v); mErr == nil {
		// 写缓存失败不影响主流程：本次结果照样返回，下次再试
		_ = c.st.PutProviderCache(ctx, c.inner.Name(), kind, key, lang, body, ttl)
	}
	return v, nil
}

// SearchMovie 实现 Client。
func (c *Cached) SearchMovie(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	key := "q=" + normKey(query) + "|y=" + strconv.Itoa(opts.Year)
	return fetchCached(c, ctx, "search-movie", key, opts.Lang, c.ttl.Search, func() ([]SearchResult, error) {
		return c.inner.SearchMovie(ctx, query, opts)
	})
}

// SearchSeries 实现 Client。
func (c *Cached) SearchSeries(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	key := "q=" + normKey(query) + "|y=" + strconv.Itoa(opts.Year)
	return fetchCached(c, ctx, "search-tv", key, opts.Lang, c.ttl.Search, func() ([]SearchResult, error) {
		return c.inner.SearchSeries(ctx, query, opts)
	})
}

// Movie 实现 Client。
func (c *Cached) Movie(ctx context.Context, id int, lang string) (*Movie, error) {
	key := strconv.Itoa(id)
	return fetchCached(c, ctx, "movie", key, lang, c.ttl.Details, func() (*Movie, error) {
		return c.inner.Movie(ctx, id, lang)
	})
}

// Series 实现 Client。
func (c *Cached) Series(ctx context.Context, id int, lang string) (*Series, error) {
	key := strconv.Itoa(id)
	return fetchCached(c, ctx, "tv", key, lang, c.ttl.Details, func() (*Series, error) {
		return c.inner.Series(ctx, id, lang)
	})
}

// Season 实现 Client。
func (c *Cached) Season(ctx context.Context, seriesID int, season int, lang string) (*Season, error) {
	key := strconv.Itoa(seriesID) + ":" + strconv.Itoa(season)
	return fetchCached(c, ctx, "season", key, lang, c.ttl.Season, func() (*Season, error) {
		return c.inner.Season(ctx, seriesID, season, lang)
	})
}

// Episode 实现 Client。
func (c *Cached) Episode(ctx context.Context, seriesID, season, episode int, lang string) (*Episode, error) {
	key := strconv.Itoa(seriesID) + ":" + strconv.Itoa(season) + ":" + strconv.Itoa(episode)
	return fetchCached(c, ctx, "episode", key, lang, c.ttl.Episode, func() (*Episode, error) {
		return c.inner.Episode(ctx, seriesID, season, episode, lang)
	})
}

// Images 实现 Client。
func (c *Cached) Images(ctx context.Context, kind string, id int, lang string) ([]Image, error) {
	key := kind + ":" + strconv.Itoa(id)
	return fetchCached(c, ctx, "images", key, lang, c.ttl.Images, func() ([]Image, error) {
		return c.inner.Images(ctx, kind, id, lang)
	})
}

// Credits 实现 Client。
func (c *Cached) Credits(ctx context.Context, kind string, id int) (*Credits, error) {
	key := kind + ":" + strconv.Itoa(id)
	return fetchCached(c, ctx, "credits", key, "", c.ttl.Credits, func() (*Credits, error) {
		return c.inner.Credits(ctx, kind, id)
	})
}

// normKey 归一化查询串，让「大小写 / 多余空格不同」的搜索共用缓存。
func normKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
