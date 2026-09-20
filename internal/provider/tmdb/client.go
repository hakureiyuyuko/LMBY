// Package tmdb 实现 The Movie Database 的元数据提供方。
//
// 认证：优先用 v4 的 Read Access Token（Bearer），没有时退回 v3 的 api_key 参数。
// 限流：TMDB 的限制约为每秒 50 次，这里用「并发上限 + 最小请求间隔」双重约束，
// 并且对 429/5xx 做退避重试（尊重 Retry-After）。
//
// 请求效率上有个关键做法：详情接口用 append_to_response 一次带回
// credits / images / external_ids / alternative_titles / release_dates，
// 把「一个条目要打 5 次 API」压成 1 次。
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/provider"
)

// DefaultBaseURL 是 API 根地址。
const DefaultBaseURL = "https://api.themoviedb.org/3"

// DefaultImageBaseURL 是图片根地址。
const DefaultImageBaseURL = "https://image.tmdb.org/t/p"

// ErrNotFound 表示 TMDB 明确回复「没有这个东西」（404）。
//
// 哨兵定义在 provider 包里，因为「刮削器要区分不可重试的 404 与可重试的网络错误」
// 是所有元数据源共有的需求，不该让上层为了判断它去 import 具体实现。
var ErrNotFound = provider.ErrNotFound

// Config 是客户端配置。
type Config struct {
	ReadToken    string // v4 Bearer token（推荐）
	APIKey       string // v3 api_key（兜底）
	Language     string // 默认 zh-CN
	BaseURL      string
	ImageBaseURL string
	// MaxConcurrent 是同时在飞的请求数上限。
	MaxConcurrent int
	// MinInterval 是两次请求之间的最小间隔。
	MinInterval time.Duration
	HTTPClient  *http.Client
}

// Client 是 TMDB 客户端。
type Client struct {
	cfg     Config
	hc      *http.Client
	sem     chan struct{}
	mu      sync.Mutex
	nextRun time.Time
}

// New 构造客户端。
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.ImageBaseURL == "" {
		cfg.ImageBaseURL = DefaultImageBaseURL
	}
	if cfg.Language == "" {
		cfg.Language = "zh-CN"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = 40 * time.Millisecond // ≈25 req/s
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 25 * time.Second}
	}
	return &Client{
		cfg: cfg,
		hc:  hc,
		sem: make(chan struct{}, cfg.MaxConcurrent),
	}
}

// Name 实现 provider.Client。
func (c *Client) Name() string { return "tmdb" }

// ImageURL 拼接图片地址。size 例如 w500 / original。
func (c *Client) ImageURL(path, size string) string {
	if path == "" {
		return ""
	}
	if size == "" {
		size = "original"
	}
	return fmt.Sprintf("%s/%s%s", c.cfg.ImageBaseURL, size, path)
}

// Configured 表示凭据是否齐备。
func (c *Client) Configured() bool {
	return c.cfg.ReadToken != "" || c.cfg.APIKey != ""
}

// ---------------------------------------------------------------- HTTP 基础设施

func (c *Client) newRequest(ctx context.Context, path string, params url.Values) (*http.Request, error) {
	if params == nil {
		params = url.Values{}
	}
	if c.cfg.ReadToken == "" && c.cfg.APIKey != "" {
		params.Set("api_key", c.cfg.APIKey)
	}
	u := c.cfg.BaseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.ReadToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.ReadToken)
	}
	return req, nil
}

// waitTurn 保证请求之间至少间隔 MinInterval。
func (c *Client) waitTurn(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if c.nextRun.After(now) {
		wait = c.nextRun.Sub(now)
	}
	c.nextRun = now.Add(wait).Add(c.cfg.MinInterval)
	c.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get 发起一次带重试的请求并解析 JSON。
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.waitTurn(ctx); err != nil {
			return err
		}

		req, err := c.newRequest(ctx, path, params)
		if err != nil {
			return err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			if !sleepCtx(ctx, backoff(attempt)) {
				return ctx.Err()
			}
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("解析 TMDB 响应失败（%s）: %w", path, err)
			}
			return nil
		case resp.StatusCode == http.StatusNotFound:
			return ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized:
			return errors.New("TMDB 鉴权失败：Read Access Token / API Key 无效")
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("TMDB 返回 %d: %s", resp.StatusCode, truncate(string(body), 160))
			wait := backoff(attempt)
			if s := resp.Header.Get("Retry-After"); s != "" {
				if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
					wait = time.Duration(secs) * time.Second
				}
			}
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
			continue
		default:
			return fmt.Errorf("TMDB 返回 %d: %s", resp.StatusCode, truncate(string(body), 160))
		}
	}
	return fmt.Errorf("TMDB 请求多次失败: %w", lastErr)
}

// ---------------------------------------------------------------- 搜索

type rawSearchResult struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"` // movie
	Name          string  `json:"name"`  // tv
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	PosterPath    string  `json:"poster_path"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
	Popularity    float64 `json:"popularity"`
}

type rawSearchResponse struct {
	Results []rawSearchResult `json:"results"`
}

// SearchMovie 实现 provider.Client。
func (c *Client) SearchMovie(ctx context.Context, query string, opts provider.SearchOptions) ([]provider.SearchResult, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("include_adult", "false")
	params.Set("language", orDefault(opts.Lang, c.cfg.Language))
	if opts.Year > 0 {
		params.Set("year", strconv.Itoa(opts.Year))
	}
	var raw rawSearchResponse
	if err := c.get(ctx, "/search/movie", params, &raw); err != nil {
		return nil, err
	}
	out := make([]provider.SearchResult, 0, len(raw.Results))
	for _, r := range raw.Results {
		out = append(out, provider.SearchResult{
			ID:            r.ID,
			Kind:          provider.KindMovie,
			Title:         r.Title,
			OriginalTitle: r.OriginalTitle,
			Year:          yearOf(r.ReleaseDate),
			ReleaseDate:   r.ReleaseDate,
			Overview:      r.Overview,
			PosterPath:    r.PosterPath,
			Rating:        r.VoteAverage,
			VoteCount:     r.VoteCount,
			Popularity:    r.Popularity,
		})
	}
	return out, nil
}

// SearchSeries 实现 provider.Client。
func (c *Client) SearchSeries(ctx context.Context, query string, opts provider.SearchOptions) ([]provider.SearchResult, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("include_adult", "false")
	params.Set("language", orDefault(opts.Lang, c.cfg.Language))
	if opts.Year > 0 {
		params.Set("first_air_date_year", strconv.Itoa(opts.Year))
	}
	var raw rawSearchResponse
	if err := c.get(ctx, "/search/tv", params, &raw); err != nil {
		return nil, err
	}
	out := make([]provider.SearchResult, 0, len(raw.Results))
	for _, r := range raw.Results {
		out = append(out, provider.SearchResult{
			ID:            r.ID,
			Kind:          provider.KindTV,
			Title:         r.Name,
			OriginalTitle: r.OriginalName,
			Year:          yearOf(r.FirstAirDate),
			ReleaseDate:   r.FirstAirDate,
			Overview:      r.Overview,
			PosterPath:    r.PosterPath,
			Rating:        r.VoteAverage,
			VoteCount:     r.VoteCount,
			Popularity:    r.Popularity,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------- 详情

// 详情接口一次带回的附加数据。
const appendMovie = "credits,images,external_ids,alternative_titles,release_dates"
const appendTV = "credits,images,external_ids,alternative_titles,content_ratings"

type rawExternalIDs struct {
	IMDBID string `json:"imdb_id"`
	TVDBID int    `json:"tvdb_id"`
}

type rawNamed struct {
	Name string `json:"name"`
}

type rawImage struct {
	FilePath    string  `json:"file_path"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Language    string  `json:"language"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
	AspectRatio float64 `json:"aspect_ratio"`
}

type rawImages struct {
	Posters   []rawImage `json:"posters"`
	Backdrops []rawImage `json:"backdrops"`
	Logos     []rawImage `json:"logos"`
	Stills    []rawImage `json:"stills"`
}

type rawCast struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	Order       int    `json:"order"`
	ProfilePath string `json:"profile_path"`
}

type rawCrew struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Job         string `json:"job"`
	Department  string `json:"department"`
	ProfilePath string `json:"profile_path"`
}

type rawCredits struct {
	Cast []rawCast `json:"cast"`
	Crew []rawCrew `json:"crew"`
}

type rawAltTitle struct {
	ISO31661 string `json:"iso_3166_1"`
	Title    string `json:"title"`
	Type     string `json:"type"`
}

type rawReleaseDates struct {
	Results []struct {
		ISO31661     string `json:"iso_3166_1"`
		ReleaseDates []struct {
			Certification string `json:"certification"`
			Type          int    `json:"type"`
		} `json:"release_dates"`
	} `json:"results"`
}

type rawContentRatings struct {
	Results []struct {
		ISO31661 string `json:"iso_3166_1"`
		Rating   string `json:"rating"`
	} `json:"results"`
}

type rawMovie struct {
	ID                  int            `json:"id"`
	IMDBID              string         `json:"imdb_id"`
	Title               string         `json:"title"`
	OriginalTitle       string         `json:"original_title"`
	Overview            string         `json:"overview"`
	Tagline             string         `json:"tagline"`
	ReleaseDate         string         `json:"release_date"`
	Runtime             int            `json:"runtime"`
	VoteAverage         float64        `json:"vote_average"`
	VoteCount           int            `json:"vote_count"`
	PosterPath          string         `json:"poster_path"`
	BackdropPath        string         `json:"backdrop_path"`
	Homepage            string         `json:"homepage"`
	Status              string         `json:"status"`
	Genres              []rawNamed     `json:"genres"`
	ProductionCompanies []rawNamed     `json:"production_companies"`
	ProductionCountries []rawNamed     `json:"production_countries"`
	SpokenLanguages     []rawNamed     `json:"spoken_languages"`
	ExternalIDs         rawExternalIDs `json:"external_ids"`
	Credits             rawCredits     `json:"credits"`
	Images              rawImages      `json:"images"`
	AlternativeTitles   struct {
		Titles []rawAltTitle `json:"titles"`
	} `json:"alternative_titles"`
	ReleaseDates rawReleaseDates `json:"release_dates"`
}

// Movie 实现 provider.Client。
func (c *Client) Movie(ctx context.Context, id int, lang string) (*provider.Movie, error) {
	params := url.Values{}
	params.Set("language", orDefault(lang, c.cfg.Language))
	params.Set("append_to_response", appendMovie)

	var raw rawMovie
	if err := c.get(ctx, "/movie/"+strconv.Itoa(id), params, &raw); err != nil {
		return nil, err
	}

	m := &provider.Movie{
		ID:             raw.ID,
		Title:          raw.Title,
		OriginalTitle:  raw.OriginalTitle,
		Overview:       raw.Overview,
		Tagline:        raw.Tagline,
		ReleaseDate:    raw.ReleaseDate,
		Year:           yearOf(raw.ReleaseDate),
		RuntimeMinutes: raw.Runtime,
		Rating:         raw.VoteAverage,
		VoteCount:      raw.VoteCount,
		Homepage:       raw.Homepage,
		Status:         raw.Status,
		PosterPath:     raw.PosterPath,
		BackdropPath:   raw.BackdropPath,
		ProviderIDs:    providerIDs(raw.ID, raw.ExternalIDs),
		OfficialRating: pickCertification(raw.ReleaseDates, c.cfg.Language),
	}
	for _, g := range raw.Genres {
		m.Genres = append(m.Genres, g.Name)
	}
	for _, s := range raw.ProductionCompanies {
		m.Studios = append(m.Studios, s.Name)
	}
	for _, s := range raw.ProductionCountries {
		m.Countries = append(m.Countries, s.Name)
	}
	for _, t := range raw.AlternativeTitles.Titles {
		if t.Title != "" {
			m.AlternativeTitles = append(m.AlternativeTitles, t.Title)
		}
	}
	m.Images = imagesFrom(raw.Images)
	m.Credits = creditsFrom(raw.Credits)
	return m, nil
}

type rawSeasonSummary struct {
	ID           int    `json:"id"`
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	AirDate      string `json:"air_date"`
	PosterPath   string `json:"poster_path"`
	EpisodeCount int    `json:"episode_count"`
}

type rawTV struct {
	ID                  int                `json:"id"`
	Name                string             `json:"name"`
	OriginalName        string             `json:"original_name"`
	Overview            string             `json:"overview"`
	FirstAirDate        string             `json:"first_air_date"`
	LastAirDate         string             `json:"last_air_date"`
	Status              string             `json:"status"`
	VoteAverage         float64            `json:"vote_average"`
	VoteCount           int                `json:"vote_count"`
	PosterPath          string             `json:"poster_path"`
	BackdropPath        string             `json:"backdrop_path"`
	Genres              []rawNamed         `json:"genres"`
	Networks            []rawNamed         `json:"networks"`
	ProductionCompanies []rawNamed         `json:"production_companies"`
	Seasons             []rawSeasonSummary `json:"seasons"`
	ExternalIDs         rawExternalIDs     `json:"external_ids"`
	Credits             rawCredits         `json:"credits"`
	Images              rawImages          `json:"images"`
	AlternativeTitles   struct {
		// 注意：剧集的别名在 results 里，电影在 titles 里 —— TMDB 的不一致，别踩。
		Results []rawAltTitle `json:"results"`
	} `json:"alternative_titles"`
	ContentRatings rawContentRatings `json:"content_ratings"`
}

// Series 实现 provider.Client。
func (c *Client) Series(ctx context.Context, id int, lang string) (*provider.Series, error) {
	params := url.Values{}
	params.Set("language", orDefault(lang, c.cfg.Language))
	params.Set("append_to_response", appendTV)

	var raw rawTV
	if err := c.get(ctx, "/tv/"+strconv.Itoa(id), params, &raw); err != nil {
		return nil, err
	}

	s := &provider.Series{
		ID:             raw.ID,
		Name:           raw.Name,
		OriginalName:   raw.OriginalName,
		Overview:       raw.Overview,
		FirstAirDate:   raw.FirstAirDate,
		LastAirDate:    raw.LastAirDate,
		Year:           yearOf(raw.FirstAirDate),
		Rating:         raw.VoteAverage,
		VoteCount:      raw.VoteCount,
		Status:         raw.Status,
		PosterPath:     raw.PosterPath,
		BackdropPath:   raw.BackdropPath,
		ProviderIDs:    providerIDs(raw.ID, raw.ExternalIDs),
		OfficialRating: pickContentRating(raw.ContentRatings, c.cfg.Language),
	}
	for _, g := range raw.Genres {
		s.Genres = append(s.Genres, g.Name)
	}
	for _, n := range raw.Networks {
		s.Networks = append(s.Networks, n.Name)
	}
	for _, n := range raw.ProductionCompanies {
		s.Studios = append(s.Studios, n.Name)
	}
	for _, t := range raw.AlternativeTitles.Results {
		if t.Title != "" {
			s.AlternativeTitles = append(s.AlternativeTitles, t.Title)
		}
	}
	for _, season := range raw.Seasons {
		s.Seasons = append(s.Seasons, provider.SeasonSummary{
			ID:           season.ID,
			SeasonNumber: season.SeasonNumber,
			Name:         season.Name,
			Overview:     season.Overview,
			AirDate:      season.AirDate,
			PosterPath:   season.PosterPath,
			EpisodeCount: season.EpisodeCount,
		})
	}
	s.Images = imagesFrom(raw.Images)
	s.Credits = creditsFrom(raw.Credits)
	return s, nil
}

type rawEpisode struct {
	ID            int     `json:"id"`
	SeasonNumber  int     `json:"season_number"`
	EpisodeNumber int     `json:"episode_number"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview"`
	AirDate       string  `json:"air_date"`
	Runtime       int     `json:"runtime"`
	VoteAverage   float64 `json:"vote_average"`
	StillPath     string  `json:"still_path"`
}

type rawSeason struct {
	ID           int          `json:"id"`
	SeasonNumber int          `json:"season_number"`
	Name         string       `json:"name"`
	Overview     string       `json:"overview"`
	AirDate      string       `json:"air_date"`
	PosterPath   string       `json:"poster_path"`
	Episodes     []rawEpisode `json:"episodes"`
	Images       rawImages    `json:"images"`
}

// Season 实现 provider.Client（一次拿到整季，避免逐集请求）。
func (c *Client) Season(ctx context.Context, seriesID int, season int, lang string) (*provider.Season, error) {
	params := url.Values{}
	params.Set("language", orDefault(lang, c.cfg.Language))
	params.Set("append_to_response", "images")

	var raw rawSeason
	path := fmt.Sprintf("/tv/%d/season/%d", seriesID, season)
	if err := c.get(ctx, path, params, &raw); err != nil {
		return nil, err
	}

	out := &provider.Season{
		ID:           raw.ID,
		SeasonNumber: raw.SeasonNumber,
		Name:         raw.Name,
		Overview:     raw.Overview,
		AirDate:      raw.AirDate,
		PosterPath:   raw.PosterPath,
		Images:       imagesFrom(raw.Images),
	}
	for _, e := range raw.Episodes {
		out.Episodes = append(out.Episodes, provider.EpisodeSummary{
			ID:            e.ID,
			SeasonNumber:  e.SeasonNumber,
			EpisodeNumber: e.EpisodeNumber,
			Name:          e.Name,
			Overview:      e.Overview,
			AirDate:       e.AirDate,
			RuntimeMin:    e.Runtime,
			Rating:        e.VoteAverage,
			StillPath:     e.StillPath,
		})
	}
	return out, nil
}

type rawEpisodeDetails struct {
	rawEpisode
	ExternalIDs rawExternalIDs `json:"external_ids"`
	Images      rawImages      `json:"images"`
	Credits     rawCredits     `json:"credits"`
}

// Episode 实现 provider.Client。
func (c *Client) Episode(ctx context.Context, seriesID, season, episode int, lang string) (*provider.Episode, error) {
	params := url.Values{}
	params.Set("language", orDefault(lang, c.cfg.Language))
	params.Set("append_to_response", "credits,images,external_ids")

	var raw rawEpisodeDetails
	path := fmt.Sprintf("/tv/%d/season/%d/episode/%d", seriesID, season, episode)
	if err := c.get(ctx, path, params, &raw); err != nil {
		return nil, err
	}

	return &provider.Episode{
		ID:            raw.ID,
		SeasonNumber:  raw.SeasonNumber,
		EpisodeNumber: raw.EpisodeNumber,
		Name:          raw.Name,
		Overview:      raw.Overview,
		AirDate:       raw.AirDate,
		RuntimeMin:    raw.Runtime,
		Rating:        raw.VoteAverage,
		StillPath:     raw.StillPath,
		ProviderIDs:   providerIDs(raw.ID, raw.ExternalIDs),
		Images:        imagesFrom(raw.Images),
		Credits:       creditsFrom(raw.Credits),
	}, nil
}

// ---------------------------------------------------------------- 图片与演职员

// Images 实现 provider.Client。
func (c *Client) Images(ctx context.Context, kind string, id int, lang string) ([]provider.Image, error) {
	params := url.Values{}
	// 图片列表不传 language，一次拿全语言再本地过滤，避免多次请求
	params.Set("include_image_language", orDefault(lang, "zh")+",en,null")

	var (
		raw rawImages
		err error
	)
	switch kind {
	case provider.KindTV:
		err = c.get(ctx, fmt.Sprintf("/tv/%d/images", id), params, &raw)
	default:
		err = c.get(ctx, fmt.Sprintf("/movie/%d/images", id), params, &raw)
	}
	if err != nil {
		return nil, err
	}
	return imagesFrom(raw), nil
}

// Credits 实现 provider.Client。
func (c *Client) Credits(ctx context.Context, kind string, id int) (*provider.Credits, error) {
	var raw rawCredits
	path := fmt.Sprintf("/movie/%d/credits", id)
	if kind == provider.KindTV {
		path = fmt.Sprintf("/tv/%d/credits", id)
	}
	if err := c.get(ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	return creditsFrom(raw), nil
}

// ---------------------------------------------------------------- 映射工具

func imagesFrom(raw rawImages) []provider.Image {
	var out []provider.Image
	add := func(kind string, list []rawImage) {
		for _, i := range list {
			out = append(out, provider.Image{
				Kind:        kind,
				Path:        i.FilePath,
				Width:       i.Width,
				Height:      i.Height,
				Language:    i.Language,
				VoteAverage: i.VoteAverage,
				VoteCount:   i.VoteCount,
				AspectRatio: i.AspectRatio,
			})
		}
	}
	add("poster", raw.Posters)
	add("backdrop", raw.Backdrops)
	add("logo", raw.Logos)
	add("still", raw.Stills)
	return out
}

func creditsFrom(raw rawCredits) *provider.Credits {
	if len(raw.Cast) == 0 && len(raw.Crew) == 0 {
		return nil
	}
	out := &provider.Credits{}
	for _, p := range raw.Cast {
		out.Cast = append(out.Cast, provider.Person{
			Name: p.Name, Character: p.Character, Order: p.Order,
			ProfilePath: p.ProfilePath, TMDBID: p.ID,
		})
	}
	for _, p := range raw.Crew {
		out.Crew = append(out.Crew, provider.Person{
			Name: p.Name, Job: p.Job, Department: p.Department,
			ProfilePath: p.ProfilePath, TMDBID: p.ID,
		})
	}
	return out
}

func providerIDs(id int, ext rawExternalIDs) map[string]string {
	ids := map[string]string{"tmdb": strconv.Itoa(id)}
	if ext.IMDBID != "" {
		ids["imdb"] = ext.IMDBID
	}
	if ext.TVDBID > 0 {
		ids["tvdb"] = strconv.Itoa(ext.TVDBID)
	}
	return ids
}

// pickCertification 从 release_dates 里挑一个分级。
// 优先匹配请求语言所在地区，其次 US，最后任意非空值。
func pickCertification(rd rawReleaseDates, lang string) string {
	region := regionOf(lang)
	if v := certificationFor(rd, region); v != "" {
		return v
	}
	if v := certificationFor(rd, "US"); v != "" {
		return v
	}
	for _, r := range rd.Results {
		for _, d := range r.ReleaseDates {
			if d.Certification != "" {
				return d.Certification
			}
		}
	}
	return ""
}

func certificationFor(rd rawReleaseDates, region string) string {
	for _, r := range rd.Results {
		if !strings.EqualFold(r.ISO31661, region) {
			continue
		}
		for _, d := range r.ReleaseDates {
			if d.Certification != "" {
				return d.Certification
			}
		}
	}
	return ""
}

func pickContentRating(cr rawContentRatings, lang string) string {
	region := regionOf(lang)
	for _, r := range cr.Results {
		if strings.EqualFold(r.ISO31661, region) && r.Rating != "" {
			return r.Rating
		}
	}
	for _, r := range cr.Results {
		if strings.EqualFold(r.ISO31661, "US") && r.Rating != "" {
			return r.Rating
		}
	}
	for _, r := range cr.Results {
		if r.Rating != "" {
			return r.Rating
		}
	}
	return ""
}

// regionOf 从 BCP-47 语言标签里取地区，如 zh-CN → CN。
func regionOf(lang string) string {
	if i := strings.IndexByte(lang, '-'); i >= 0 {
		return strings.ToUpper(lang[i+1:])
	}
	return ""
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second // 1s, 2s, 4s
	if d > 15*time.Second {
		d = 15 * time.Second
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
