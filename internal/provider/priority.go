package provider

// Priority：把「主源优先、未命中回退备用源」做成一个 Client 装饰器。
//
// 用途：KeqDB（LMBY 的配套社区元数据库，接口与 TMDB v3 同形）**启用后在取数上
// 优先于 TMDB** —— 先在 KeqDB 里找，它没有（ErrNotFound / 空结果）再问 TMDB。
//
// 开关是**运行期可变**的（设置页的「加入元数据共享改进计划」），所以这里用回调
// 而不是布尔值；关掉时一次都不问主源。
//
// 注意：它**不**再套一层 Cached —— 两个源各自在里层已经带缓存，且缓存键按源名
// 分开（provider_cache.provider = "keqdb" / "tmdb"），这正是对接文档 §5.4 提醒过的
// 「别把两边的缓存键混在一起」。

import (
	"context"
	"log/slog"
	"regexp"
)

// Priority 见包注释。
type Priority struct {
	primary Client
	backup  Client
	enabled func() bool
	log     *slog.Logger
}

// NewPriority 构造。primary 可以为 nil（等于永远只用 backup）。
func NewPriority(primary, backup Client, enabled func() bool, log *slog.Logger) *Priority {
	return &Priority{primary: primary, backup: backup, enabled: enabled, log: log}
}

var _ Client = (*Priority)(nil)

// Name 只用于日志：真正的缓存键由里层各自的 Cached 决定。
func (p *Priority) Name() string { return p.backup.Name() + "+keqdb" }

func (p *Priority) usePrimary() bool {
	return p.primary != nil && p.enabled != nil && p.enabled()
}

// warn 记一条「主源不行、退到备用源」的日志（未命中不算错，只有真报错才打）。
func (p *Priority) warn(op string, err error) {
	if p.log != nil && err != nil {
		p.log.Info("KeqDB 取数失败，回退 TMDB", "op", op, "err", err)
	}
}

func (p *Priority) SearchMovie(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if !p.usePrimary() {
		return p.backup.SearchMovie(ctx, query, opts)
	}
	res, err := p.primary.SearchMovie(ctx, query, opts)
	if err == nil && len(res) > 0 {
		return res, nil
	}
	p.warn("SearchMovie", err)
	return p.backup.SearchMovie(ctx, query, opts)
}

func (p *Priority) SearchSeries(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if !p.usePrimary() {
		return p.backup.SearchSeries(ctx, query, opts)
	}
	res, err := p.primary.SearchSeries(ctx, query, opts)
	if err == nil && len(res) > 0 {
		return res, nil
	}
	p.warn("SearchSeries", err)
	return p.backup.SearchSeries(ctx, query, opts)
}

func (p *Priority) Movie(ctx context.Context, id int, lang string) (*Movie, error) {
	if !p.usePrimary() {
		return p.backup.Movie(ctx, id, lang)
	}
	m, err := p.primary.Movie(ctx, id, lang)
	if err == nil {
		return m, nil
	}
	p.warn("Movie", err)
	return p.backup.Movie(ctx, id, lang)
}

func (p *Priority) Series(ctx context.Context, id int, lang string) (*Series, error) {
	if !p.usePrimary() {
		return p.backup.Series(ctx, id, lang)
	}
	s, err := p.primary.Series(ctx, id, lang)
	if err == nil {
		return s, nil
	}
	p.warn("Series", err)
	return p.backup.Series(ctx, id, lang)
}

func (p *Priority) Season(ctx context.Context, seriesID int, season int, lang string) (*Season, error) {
	if !p.usePrimary() {
		return p.backup.Season(ctx, seriesID, season, lang)
	}
	s, err := p.primary.Season(ctx, seriesID, season, lang)
	if err == nil {
		return s, nil
	}
	p.warn("Season", err)
	return p.backup.Season(ctx, seriesID, season, lang)
}

func (p *Priority) Episode(ctx context.Context, seriesID, season, episode int, lang string) (*Episode, error) {
	if !p.usePrimary() {
		return p.backup.Episode(ctx, seriesID, season, episode, lang)
	}
	e, err := p.primary.Episode(ctx, seriesID, season, episode, lang)
	if err == nil {
		return e, nil
	}
	p.warn("Episode", err)
	return p.backup.Episode(ctx, seriesID, season, episode, lang)
}

func (p *Priority) Images(ctx context.Context, kind string, id int, lang string) ([]Image, error) {
	if !p.usePrimary() {
		return p.backup.Images(ctx, kind, id, lang)
	}
	imgs, err := p.primary.Images(ctx, kind, id, lang)
	if err == nil && len(imgs) > 0 {
		return imgs, nil
	}
	p.warn("Images", err)
	return p.backup.Images(ctx, kind, id, lang)
}

func (p *Priority) Credits(ctx context.Context, kind string, id int) (*Credits, error) {
	if !p.usePrimary() {
		return p.backup.Credits(ctx, kind, id)
	}
	c, err := p.primary.Credits(ctx, kind, id)
	if err == nil {
		return c, nil
	}
	p.warn("Credits", err)
	return p.backup.Credits(ctx, kind, id)
}

// keqDBImagePath 匹配 KeqDB 的图片路径：它是**内容寻址**的
// `/<64 位 hex>.webp`；TMDB 是 `/abc.jpg` 那种。
var keqDBImagePath = regexp.MustCompile(`^/[0-9a-fA-F]{64}\.webp$`)

// ImageURL 按**路径的形状**选域名，而不是按开关状态。
//
// 原因：库里的图片 path 是「抓的时候」那个源给的 —— 开关开了关关了开，
// 老 path 不会跟着变。KeqDB 的内容寻址 .webp 只能用它自己的域名取，
// TMDB 的 /abc.jpg 只能用 image.tmdb.org；按开关切会有一半图 404。
func (p *Priority) ImageURL(path, size string) string {
	if keqDBImagePath.MatchString(path) && p.primary != nil {
		return p.primary.ImageURL(path, size)
	}
	return p.backup.ImageURL(path, size)
}
