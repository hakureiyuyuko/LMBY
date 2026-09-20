// Package provider 定义元数据提供方（TMDB 等）的抽象与共用模型。
//
// 为什么要有这层抽象：LMBY 不打算只支持 TMDB —— TVDB、Bangumi、豆瓣、MusicBrainz
// 的接入方式差异很大，但「按标题+年份搜索」→「按 id 取详情」→「补季集/图片/演职员」
// 这套流程是一样的。把这套流程固定下来，具体站点只负责翻译成自己的 API 形状。
package provider

import "context"

// Kind 取值。
const (
	KindMovie = "movie"
	KindTV    = "tv"
)

// Client 是元数据提供方要实现的全部能力。
//
// 约定：lang 用 BCP-47 形式（zh-CN / en-US），空串表示用提供方默认语言。
type Client interface {
	// Name 是提供方标识，用于缓存键与日志（如 "tmdb"）。
	Name() string

	SearchMovie(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
	SearchSeries(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)

	Movie(ctx context.Context, id int, lang string) (*Movie, error)
	Series(ctx context.Context, id int, lang string) (*Series, error)
	Season(ctx context.Context, seriesID int, season int, lang string) (*Season, error)
	Episode(ctx context.Context, seriesID, season, episode int, lang string) (*Episode, error)

	Images(ctx context.Context, kind string, id int, lang string) ([]Image, error)
	Credits(ctx context.Context, kind string, id int) (*Credits, error)
}

// SearchOptions 是搜索条件。
type SearchOptions struct {
	Year    int // 0 表示不限
	Lang    string
	Primary bool // 只要主要结果（减少无用请求）
}

// SearchResult 是一条搜索结果（候选）。
type SearchResult struct {
	ID            int     `json:"id"`
	Kind          string  `json:"kind"`
	Title         string  `json:"title"`         // 本地化标题
	OriginalTitle string  `json:"originalTitle"` // 原名
	Year          int     `json:"year"`
	ReleaseDate   string  `json:"releaseDate"`
	Overview      string  `json:"overview"`
	PosterPath    string  `json:"posterPath"`
	Rating        float64 `json:"rating"`
	VoteCount     int     `json:"voteCount"`
	Popularity    float64 `json:"popularity"`
}

// Image 是一张远程图片。
type Image struct {
	Kind        string  `json:"kind"` // poster | backdrop | logo | still | profile
	Path        string  `json:"path"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Language    string  `json:"language"`
	VoteAverage float64 `json:"voteAverage"`
	VoteCount   int     `json:"voteCount"`
	AspectRatio float64 `json:"aspectRatio"`
}

// Person 是演职员。
type Person struct {
	Name        string `json:"name"`
	Character   string `json:"character,omitempty"`
	Job         string `json:"job,omitempty"`
	Department  string `json:"department,omitempty"`
	Order       int    `json:"order,omitempty"`
	ProfilePath string `json:"profilePath,omitempty"`
	TMDBID      int    `json:"tmdbId,omitempty"`
}

// Credits 是演职员集合。
type Credits struct {
	Cast []Person `json:"cast"`
	Crew []Person `json:"crew"`
}

// Movie 是电影详情。
type Movie struct {
	ID                int               `json:"id"`
	Title             string            `json:"title"`
	OriginalTitle     string            `json:"originalTitle"`
	Overview          string            `json:"overview"`
	Tagline           string            `json:"tagline"`
	ReleaseDate       string            `json:"releaseDate"`
	Year              int               `json:"year"`
	RuntimeMinutes    int               `json:"runtimeMinutes"`
	Rating            float64           `json:"rating"`
	VoteCount         int               `json:"voteCount"`
	Genres            []string          `json:"genres"`
	Studios           []string          `json:"studios"`
	Countries         []string          `json:"countries"`
	OfficialRating    string            `json:"officialRating,omitempty"`
	Homepage          string            `json:"homepage,omitempty"`
	Status            string            `json:"status,omitempty"`
	PosterPath        string            `json:"posterPath"`
	BackdropPath      string            `json:"backdropPath"`
	ProviderIDs       map[string]string `json:"providerIds"`
	AlternativeTitles []string          `json:"alternativeTitles"`
	Images            []Image           `json:"images,omitempty"`
	Credits           *Credits          `json:"credits,omitempty"`
}

// SeasonSummary 是剧集详情里的季摘要。
type SeasonSummary struct {
	ID           int    `json:"id"`
	SeasonNumber int    `json:"seasonNumber"`
	Name         string `json:"name"`
	Overview     string `json:"overview,omitempty"`
	AirDate      string `json:"airDate,omitempty"`
	PosterPath   string `json:"posterPath,omitempty"`
	EpisodeCount int    `json:"episodeCount"`
}

// Series 是剧集详情。
type Series struct {
	ID                int               `json:"id"`
	Name              string            `json:"name"`
	OriginalName      string            `json:"originalName"`
	Overview          string            `json:"overview"`
	FirstAirDate      string            `json:"firstAirDate"`
	LastAirDate       string            `json:"lastAirDate,omitempty"`
	Year              int               `json:"year"`
	Rating            float64           `json:"rating"`
	VoteCount         int               `json:"voteCount"`
	Status            string            `json:"status,omitempty"`
	Genres            []string          `json:"genres"`
	Networks          []string          `json:"networks"`
	Studios           []string          `json:"studios,omitempty"`
	Countries         []string          `json:"countries,omitempty"`
	OfficialRating    string            `json:"officialRating,omitempty"`
	PosterPath        string            `json:"posterPath"`
	BackdropPath      string            `json:"backdropPath"`
	ProviderIDs       map[string]string `json:"providerIds"`
	AlternativeTitles []string          `json:"alternativeTitles"`
	Seasons           []SeasonSummary   `json:"seasons"`
	Images            []Image           `json:"images,omitempty"`
	Credits           *Credits          `json:"credits,omitempty"`
}

// EpisodeSummary 是季详情里的一集。
type EpisodeSummary struct {
	ID            int     `json:"id"`
	SeasonNumber  int     `json:"seasonNumber"`
	EpisodeNumber int     `json:"episodeNumber"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview,omitempty"`
	AirDate       string  `json:"airDate,omitempty"`
	RuntimeMin    int     `json:"runtimeMinutes,omitempty"`
	Rating        float64 `json:"rating,omitempty"`
	StillPath     string  `json:"stillPath,omitempty"`
}

// Season 是季详情（含全部集）。
type Season struct {
	ID           int              `json:"id"`
	SeasonNumber int              `json:"seasonNumber"`
	Name         string           `json:"name"`
	Overview     string           `json:"overview"`
	AirDate      string           `json:"airDate"`
	PosterPath   string           `json:"posterPath"`
	Episodes     []EpisodeSummary `json:"episodes"`
	Images       []Image          `json:"images,omitempty"`
}

// Episode 是单集详情。
type Episode struct {
	ID            int               `json:"id"`
	SeasonNumber  int               `json:"seasonNumber"`
	EpisodeNumber int               `json:"episodeNumber"`
	Name          string            `json:"name"`
	Overview      string            `json:"overview"`
	AirDate       string            `json:"airDate"`
	RuntimeMin    int               `json:"runtimeMinutes"`
	Rating        float64           `json:"rating"`
	StillPath     string            `json:"stillPath"`
	ProviderIDs   map[string]string `json:"providerIds,omitempty"`
	Images        []Image           `json:"images,omitempty"`
	Credits       *Credits          `json:"credits,omitempty"`
}
