// Package scrape 实现刮削任务：把本地条目对上 provider（TMDB）的条目，
// 把元数据写进 PostgreSQL。
//
// 几条设计约束：
//
//  1. **只写 PG**。不生成、不导出任何 XML/nfo（见 docs/REQUIREMENTS.md 的决策表），
//     媒体同目录的 nfo 只作为扫描的输入。
//  2. **候选判定交给 `internal/match`**，这里只负责取数、落库、状态流转。
//  3. **失败要能区分**：接口/网络问题返回 error 让队列退避重试；
//     「就是没找到」「候选都不像」是业务结论，落库并返回 nil，不该重试。
package scrape

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/match"
	"github.com/hakureiyuyuko/lmby/internal/overlay"
	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/worker"
)

// 编译期断言：Handler 必须实现 worker.Handler。
var _ worker.Handler = (*Handler)(nil)

// 库里的条目类型（media_items.kind）。
//
// 注意与 provider 那边的词汇不同：库里剧集叫 "series"，TMDB 那边叫 "tv"。
// 这个差异必须在取候选之前抹平 —— 打分器会按类型过滤候选，
// 拿 "series" 去比 "tv" 会把所有候选都筛掉（这个坑踩过一次）。
const (
	itemKindMovie   = "movie"
	itemKindSeries  = "series"
	itemKindSeason  = "season"
	itemKindEpisode = "episode"
)

// providerKind 把库里的条目类型映射成 provider 的条目类型。
func providerKind(itemKind string) string {
	if itemKind == itemKindMovie {
		return provider.KindMovie
	}
	return provider.KindTV
}

// 可被人工锁定的字段名（写进 media_items.locked_fields）。
//
// 这一组名字同时也是将来「条目编辑界面」用的字段名，所以定成常量，
// 免得界面写 "providers" 而刮削里判断 "providerIds" 这种对不上的事。
const (
	FieldTitle          = "title"
	FieldOriginalTitle  = "originalTitle"
	FieldYear           = "year"
	FieldOverview       = "overview"
	FieldTagline        = "tagline"
	FieldRuntime        = "runtime"
	FieldRating         = "rating"
	FieldOfficialRating = "officialRating"
	FieldGenres         = "genres"
	FieldStudios        = "studios"
	FieldProviderIDs    = "providerIds"
	FieldPremiereDate   = "premiereDate"
)

// Payload 是 scrape 任务的载荷。
type Payload struct {
	ItemID int64 `json:"itemId"`
	// Force 为真时即使已经匹配过也重刮（人工想覆盖自动结果时用）。
	Force bool `json:"force,omitempty"`
}

// Store 是刮削处理器需要的那部分 store 能力。
//
// 刻意定义成窄接口而不是直接吃 *store.Store：匹配判定、字段锁、状态流转
// 这些逻辑不该只能靠真库验证 —— 用测试替身能把「锁住的字段没被覆盖」
// 这类断言写成单测，而不是每次都起一个 PostgreSQL。
type Store interface {
	GetItem(ctx context.Context, id int64) (*store.Item, error)
	SeriesStructure(ctx context.Context, seriesID int64) (season, episodes int, err error)
	ApplyItemMeta(ctx context.Context, itemID int64, m store.ItemMeta) error
	SaveMatchOutcome(ctx context.Context, itemID int64, o store.MatchOutcome) error
	// EnqueueSeriesScrapes 在剧集匹配上之后，把它的季与集排上队
	// （它们要靠剧集的 provider id 才能定位）。
	EnqueueSeriesScrapes(ctx context.Context, seriesID int64) (int64, error)
}

// Handler 处理 scrape 任务。
type Handler struct {
	st     Store
	client provider.Client
	scorer match.Scorer
	log    *slog.Logger
	topN   int
	// overlay 是只读媒体库的写入层：刮削结果落库后，再写一份元数据快照
	// 到该库的叠加层（媒体目录一个字节也不碰）。可为 nil。
	overlay *overlay.Service
}

// NewHandler 构造刮削处理器。client 为 nil 时任务会明确报错（而不是静默跳过）。
func NewHandler(st Store, client provider.Client, log *slog.Logger) *Handler {
	return &Handler{
		st:     st,
		client: client,
		scorer: match.DefaultScorer(),
		log:    log,
		topN:   5,
	}
}

// SetOverlay 接入只读库的叠加层。
func (h *Handler) SetOverlay(o *overlay.Service) { h.overlay = o }

// overlaySnapshot 是写进叠加层的元数据快照（字段名与 API 的 camelCase 一致）。
//
// 它只对只读库写：那种库上的刮削成果只存在数据库里，用户想「把这个库的元数据
// 拿去备份 / 搬到别处」时没有落点；写成 JSON 之后，叠加层就是这个库的成果包。
type overlaySnapshot struct {
	ItemID         int64             `json:"itemId"`
	Title          string            `json:"title,omitempty"`
	SortTitle      string            `json:"sortTitle,omitempty"`
	OriginalTitle  string            `json:"originalTitle,omitempty"`
	Year           *int32            `json:"year,omitempty"`
	PremiereDate   *time.Time        `json:"premiereDate,omitempty"`
	Overview       string            `json:"overview,omitempty"`
	Tagline        string            `json:"tagline,omitempty"`
	Rating         *float64          `json:"communityRating,omitempty"`
	OfficialRating string            `json:"officialRating,omitempty"`
	Genres         []string          `json:"genres,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Studios        []string          `json:"studios,omitempty"`
	ProviderIDs    map[string]string `json:"providerIds,omitempty"`
	MetadataSource string            `json:"metadataSource,omitempty"`
	MatchState     string            `json:"matchState,omitempty"`
	ScrapedAt      time.Time         `json:"scrapedAt"`
}

// applyMeta 落库 +（只读库）把这次刮削到的值写成一份叠加层快照。
//
// 落库失败照旧原样返回（调用方要区分 ErrAlreadyExists 这种业务结论）；
// 写快照失败只记警告：叠加层是「收获」，不该反过来把刮削判成失败。
func (h *Handler) applyMeta(ctx context.Context, itemID int64, m store.ItemMeta) error {
	if err := h.st.ApplyItemMeta(ctx, itemID, m); err != nil {
		return err
	}
	if h.overlay == nil {
		return nil
	}
	snap := overlaySnapshot{
		ItemID:         itemID,
		Title:          m.Title,
		SortTitle:      m.SortTitle,
		OriginalTitle:  m.OriginalTitle,
		Year:           m.Year,
		PremiereDate:   m.PremiereDate,
		Overview:       m.Overview,
		Tagline:        m.Tagline,
		Rating:         m.Rating,
		OfficialRating: m.OfficialRating,
		Genres:         m.Genres,
		Tags:           m.Tags,
		Studios:        m.Studios,
		ProviderIDs:    m.ProviderIDs,
		MetadataSource: m.MetadataSource,
		MatchState:     m.MatchState,
		ScrapedAt:      time.Now().UTC(),
	}
	if p, err := h.overlay.WriteItemMeta(ctx, itemID, snap); err != nil {
		h.log.Warn("只读库：写叠加层元数据快照失败（不影响刮削结果）",
			"itemId", itemID, "err", err.Error())
	} else if p != "" {
		h.log.Info("只读库：元数据快照已进叠加层", "itemId", itemID, "path", p)
	}
	return nil
}

// Kind 实现 worker.Handler。
func (h *Handler) Kind() string { return store.TaskKindScrape }

// Handle 实现 worker.Handler。
func (h *Handler) Handle(ctx context.Context, t store.Task) error {
	var p Payload
	if err := t.Decode(&p); err != nil {
		h.log.Error("刮削任务载荷非法，丢弃任务", "taskId", t.ID, "err", err)
		return nil
	}
	if p.ItemID == 0 {
		h.log.Error("刮削任务缺少 itemId，丢弃任务", "taskId", t.ID)
		return nil
	}
	if h.client == nil {
		// 配置问题，不该悄悄放过：让任务失败并在日志里说明原因
		return errors.New("未配置元数据源（config.toml 的 [tmdb] 段或 LMBY_TMDB_READ_TOKEN）")
	}

	it, err := h.st.GetItem(ctx, p.ItemID)
	if errors.Is(err, store.ErrNotFound) {
		h.log.Debug("条目已不存在，跳过刮削", "itemId", p.ItemID)
		return nil
	}
	if err != nil {
		return err
	}

	if it.Kind != itemKindMovie && it.Kind != itemKindSeries &&
		it.Kind != itemKindSeason && it.Kind != itemKindEpisode {
		// 花絮不单独刮：它没有 provider 侧的对应物，元数据从属于父条目。
		h.log.Debug("这类条目不单独刮削", "itemId", it.ID, "kind", it.Kind)
		return nil
	}

	// 已经刮好的不再重复请求 —— 「重复刮削零 API 调用」最直接的保证。
	//
	// **nfo 与人工锁定的更不该碰**：nfo 是人工整理的元数据（用户明确要求
	// 「优先 nfo、没 nfo 才刮」），人工锁定的字段同理。
	// 只有显式 force 才会覆盖它们，而且会先打一条警告。
	if !p.Force {
		switch it.MatchState {
		case store.MatchStateNFO, store.MatchStateMatched, store.MatchStateManual:
			h.log.Debug("条目已有权威元数据，跳过", "itemId", it.ID, "state", it.MatchState,
				"source", it.MetadataSource)
			return nil
		}
	} else if it.MatchState == store.MatchStateNFO || it.MetadataSource == store.MetadataSourceNFO {
		h.log.Warn("force 刮削将覆盖 nfo 导入的元数据",
			"itemId", it.ID, "title", it.Title, "source", it.MetadataSource)
	}

	// 季与集不用搜索：按 (剧集 provider id, 季号, 集号) 直接对应。
	switch it.Kind {
	case itemKindSeason:
		return h.scrapeSeason(ctx, it)
	case itemKindEpisode:
		return h.scrapeEpisode(ctx, it)
	}

	local := h.localFacts(ctx, it)

	query := strings.TrimSpace(it.Title)
	if query == "" {
		query = strings.TrimSpace(it.OriginalTitle)
	}
	if query == "" {
		return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State: store.MatchStateFailed,
			Error: "条目没有标题，无法搜索",
		})
	}

	results, err := h.search(ctx, it.Kind, query)
	if err != nil {
		return err
	}
	// 本地标题搜不到时用原始标题再试一次：本地命名的中文译名与 TMDB 主标题
	// 不一致的情况不少，多这一次往往能把候选捞回来（命中缓存时零成本）。
	if len(results) == 0 && it.OriginalTitle != "" && it.OriginalTitle != query {
		h.log.Debug("主标题搜不到候选，改用原始标题重试",
			"itemId", it.ID, "originalTitle", it.OriginalTitle)
		results, err = h.search(ctx, it.Kind, it.OriginalTitle)
		if err != nil {
			return err
		}
	}

	cands := make([]match.Candidate, 0, len(results))
	for _, r := range results {
		cands = append(cands, match.FromSearch(r))
	}
	if h.topN > 0 && len(cands) > h.topN {
		cands = cands[:h.topN]
	}
	if len(cands) == 0 {
		return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State: store.MatchStateFailed,
			Error: "没有搜到任何候选（可人工匹配）",
		})
	}

	details := Enrich(ctx, h.client, it.Kind, cands, local.SeasonNumber, h.log)
	ranked := h.scorer.Rank(local, cands)
	// 候选要存进库给人看，顺手补上海报地址（人工界面靠它区分同名条目）
	DecoratePosters(h.client, ranked, details)
	top := ranked[0]

	switch top.Decision {
	case match.DecisionAuto:
		d, ok := details[top.CandidateID]
		if !ok {
			// 详情没取到（网络失败）：不能拿空元数据把条目标成「已匹配」，
			// 返回错误让队列重试一次。
			return fmt.Errorf("取候选 %d（%s）的详情失败，稍后重试", top.CandidateID, top.Title)
		}
		meta := itemMeta(it, d)
		if err := h.applyMeta(ctx, it.ID, meta); err != nil {
			// 同一个作品被扫成了两个条目时，刮削改名会撞上唯一索引：
			// 这是「需要人工确认是否重复」的业务结论，不是可以重试的环境问题
			// （否则只会拿同一条 SQL 重试到上限）。
			if errors.Is(err, store.ErrAlreadyExists) {
				h.log.Warn("刮削改名后与库里已有条目同名，转人工确认",
					"itemId", it.ID, "title", meta.Title, "err", err.Error())
				return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
					State:      store.MatchStateReview,
					Score:      ptrFloat(top.Score),
					Candidates: ranked,
					Error: fmt.Sprintf("库里已有同名同年条目（《%s》），改名会撞唯一索引 —— "+
						"可能是同一个作品被扫成了两个条目，需人工确认", meta.Title),
				})
			}
			return err
		}
		h.log.Info("自动匹配成功",
			"itemId", it.ID, "kind", it.Kind, "title", it.Title,
			"provider", top.CandidateID, "candidate", top.Title,
			"score", top.Score, "margin", top.Margin)
		if err := h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State:      store.MatchStateMatched,
			Score:      ptrFloat(top.Score),
			Source:     h.client.Name(),
			Candidates: ranked,
		}); err != nil {
			return err
		}
		if it.Kind == itemKindSeries {
			// 剧集匹配上了，它下面的季与集才有得刮（要靠这个 provider id 定位）
			h.enqueueSeriesChildren(ctx, it.ID)
		}
		return nil

	case match.DecisionReview:
		h.log.Info("候选需要人工确认",
			"itemId", it.ID, "title", it.Title,
			"provider", top.CandidateID, "candidate", top.Title,
			"score", top.Score, "margin", top.Margin)
		return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State: store.MatchStateReview,
			Score: ptrFloat(top.Score),
			// 存下候选：人工界面直接展示，不用让用户重新搜一遍
			Candidates: ranked,
			Error: fmt.Sprintf("最高分 %.2f、领先第二名 %.2f，需要人工确认（候选：《%s》）",
				top.Score, top.Margin, top.Title),
		})

	default:
		h.log.Info("候选都不像，记为失败",
			"itemId", it.ID, "title", it.Title, "score", top.Score, "candidate", top.Title)
		return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State:      store.MatchStateFailed,
			Score:      ptrFloat(top.Score),
			Candidates: ranked,
			Error:      fmt.Sprintf("候选都不像（最高分 %.2f：《%s》）", top.Score, top.Title),
		})
	}
}

// localFacts 把条目整理成打分器的输入。
//
// 剧集额外带一个结构信号：集数最多的那一季的季号与集数。
// 同名重制版（钢炼 2003 / FA 2009）与「剧场版 vs TV 版」只能靠这类信号分辨。
func (h *Handler) localFacts(ctx context.Context, it *store.Item) match.Local {
	local := match.Local{
		Kind:          providerKind(it.Kind),
		Title:         it.Title,
		OriginalTitle: it.OriginalTitle,
	}
	if it.Year != nil {
		local.Year = int(*it.Year)
	}
	if it.Kind != itemKindSeries {
		return local
	}
	season, episodes, err := h.st.SeriesStructure(ctx, it.ID)
	if err != nil {
		// 结构信号拿不到不影响刮削，只是判得更粗一点
		h.log.Warn("读取剧集结构失败，本次不带结构信号", "itemId", it.ID, "err", err)
		return local
	}
	local.SeasonNumber, local.EpisodeCount = season, episodes
	return local
}

// search 按类型搜索候选。
//
// 刻意**不传年份**：TMDB 的 `year` / `first_air_date_year` 是硬过滤，
// 而本地年份可能来自某一季、或者干脆解析错了（见 M2 验收记录），
// 硬过滤会把正确答案直接筛掉。年份交给打分器判 —— 差得多的直接 0 分。
func (h *Handler) search(ctx context.Context, itemKind, query string) ([]provider.SearchResult, error) {
	opts := provider.SearchOptions{}
	if itemKind == itemKindMovie {
		return h.client.SearchMovie(ctx, query, opts)
	}
	return h.client.SearchSeries(ctx, query, opts)
}

// itemMeta 把 provider 的详情映射成要落库的元数据，并跳过被人工锁定的字段。
//
// 锁的实现方式：把锁定字段留空，ApplyItemMeta 的 SQL 用
// `coalesce(nullif($n,”), 原值)` 语义，空值就等于「不动」。
// 这样「空值不覆盖」这条规则只在一个地方实现，nfo 导入与刮削共用。
func itemMeta(it *store.Item, d Detail) store.ItemMeta {
	locked := func(name string) bool { return store.FieldLocked(it.LockedFields, name) }

	var m store.ItemMeta

	switch {
	case d.Movie != nil:
		mv := d.Movie
		if !locked(FieldTitle) {
			m.Title = mv.Title
			m.SortTitle = store.SortTitle(mv.Title)
		}
		if !locked(FieldOriginalTitle) {
			m.OriginalTitle = mv.OriginalTitle
		}
		if !locked(FieldYear) && mv.Year > 0 {
			m.Year = ptrInt32(int32(mv.Year))
		}
		if !locked(FieldOverview) {
			m.Overview = mv.Overview
		}
		if !locked(FieldTagline) {
			m.Tagline = mv.Tagline
		}
		if !locked(FieldRuntime) && mv.RuntimeMinutes > 0 {
			m.RuntimeTicks = ptrInt64(runtimeTicks(mv.RuntimeMinutes))
		}
		if !locked(FieldRating) && mv.Rating > 0 {
			m.Rating = ptrFloat(mv.Rating)
		}
		if !locked(FieldOfficialRating) {
			m.OfficialRating = mv.OfficialRating
		}
		if !locked(FieldGenres) {
			m.Genres = mv.Genres
		}
		if !locked(FieldStudios) {
			m.Studios = mv.Studios
		}
		if !locked(FieldProviderIDs) {
			m.ProviderIDs = mv.ProviderIDs
		}
		if !locked(FieldPremiereDate) {
			m.PremiereDate = parseDate(mv.ReleaseDate)
		}

	case d.Series != nil:
		sv := d.Series
		if !locked(FieldTitle) {
			m.Title = sv.Name
			m.SortTitle = store.SortTitle(sv.Name)
		}
		if !locked(FieldOriginalTitle) {
			m.OriginalTitle = sv.OriginalName
		}
		if !locked(FieldYear) && sv.Year > 0 {
			m.Year = ptrInt32(int32(sv.Year))
		}
		if !locked(FieldOverview) {
			m.Overview = sv.Overview
		}
		if !locked(FieldRating) && sv.Rating > 0 {
			m.Rating = ptrFloat(sv.Rating)
		}
		if !locked(FieldOfficialRating) {
			m.OfficialRating = sv.OfficialRating
		}
		if !locked(FieldGenres) {
			m.Genres = sv.Genres
		}
		if !locked(FieldStudios) {
			m.Studios = sv.Studios
			if len(m.Studios) == 0 {
				// 剧集的出品方在 TMDB 里叫 networks（电视台），电影才叫 studios
				m.Studios = sv.Networks
			}
		}
		if !locked(FieldProviderIDs) {
			m.ProviderIDs = sv.ProviderIDs
		}
		if !locked(FieldPremiereDate) {
			m.PremiereDate = parseDate(sv.FirstAirDate)
		}
	}

	return m
}

// ---------------------------------------------------------------- 季与集
//
// 季与集不用搜索：它们与 provider 上的对象是**按位置直接对应**的
// （剧集的 tmdb id + 季号 + 集号），没有候选可比、也没有相似度分数。
// 所以它们走单独一条路：定位不到就等剧集匹配好之后再入队（见 EnqueueSeriesScrapes）。

// enqueueSeriesChildren 把剧集下的季与集排上队。
func (h *Handler) enqueueSeriesChildren(ctx context.Context, seriesID int64) {
	n, err := h.st.EnqueueSeriesScrapes(ctx, seriesID)
	if err != nil {
		h.log.Warn("入队剧集下的季/集失败", "seriesId", seriesID, "err", err)
		return
	}
	if n > 0 {
		h.log.Info("已入队剧集下的季/集", "seriesId", seriesID, "count", n)
	}
}

// scrapeSeason 刮一季。
func (h *Handler) scrapeSeason(ctx context.Context, it *store.Item) error {
	series, tmdbID, err := h.seriesOf(ctx, it)
	if err != nil {
		return err
	}
	if series == nil {
		h.log.Debug("季没有所属剧集，跳过", "itemId", it.ID)
		return nil
	}
	if tmdbID == 0 {
		h.log.Debug("剧集还没在 provider 上定位，季的刮削留待剧集匹配之后",
			"itemId", it.ID, "seriesId", series.ID)
		return nil
	}

	season := int(numOr(it.SeasonNum, 0))
	det, err := h.client.Season(ctx, tmdbID, season, "")
	if errors.Is(err, provider.ErrNotFound) {
		return h.providerMissing(ctx, it, fmt.Sprintf("provider 上没有第 %d 季", season))
	}
	if err != nil {
		return err
	}

	if err := h.applyMeta(ctx, it.ID, seasonMeta(det)); err != nil {
		return err
	}
	h.log.Info("已刮削季",
		"itemId", it.ID, "seriesId", series.ID, "season", season, "name", det.Name)
	return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
		State:  store.MatchStateMatched,
		Source: h.client.Name(),
	})
}

// scrapeEpisode 刮一集。
func (h *Handler) scrapeEpisode(ctx context.Context, it *store.Item) error {
	series, tmdbID, err := h.seriesOf(ctx, it)
	if err != nil {
		return err
	}
	if series == nil {
		h.log.Debug("集没有所属剧集，跳过", "itemId", it.ID)
		return nil
	}
	if tmdbID == 0 {
		h.log.Debug("剧集还没在 provider 上定位，集的刮削留待剧集匹配之后",
			"itemId", it.ID, "seriesId", series.ID)
		return nil
	}

	episode := int(numOr(it.EpisodeNum, 0))
	if episode <= 0 {
		return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State: store.MatchStateFailed,
			Error: "这一集没有集号，无法在 provider 上定位",
		})
	}
	// 与扫描器口径一致：没标季号的按第 1 季
	season := int(numOr(it.SeasonNum, 1))
	if season <= 0 {
		season = 1
	}

	det, err := h.client.Episode(ctx, tmdbID, season, episode, "")
	if errors.Is(err, provider.ErrNotFound) {
		return h.providerMissing(ctx, it, fmt.Sprintf("provider 上没有 S%02dE%02d", season, episode))
	}
	if err != nil {
		return err
	}

	if err := h.applyMeta(ctx, it.ID, episodeMeta(det)); err != nil {
		return err
	}
	h.log.Info("已刮削集",
		"itemId", it.ID, "seriesId", series.ID, "season", season, "episode", episode,
		"name", det.Name)
	return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
		State:  store.MatchStateMatched,
		Source: h.client.Name(),
	})
}

// seriesOf 取条目所属的剧集条目，以及它在 provider 上的 id（0 表示还没定位）。
func (h *Handler) seriesOf(ctx context.Context, it *store.Item) (*store.Item, int, error) {
	if it.SeriesID == nil || *it.SeriesID == 0 {
		return nil, 0, nil
	}
	series, err := h.st.GetItem(ctx, *it.SeriesID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	id, err := h.locateSeries(ctx, series)
	if err != nil {
		return series, 0, err
	}
	return series, id, nil
}

// locateSeries 尽力找到剧集在 provider 上的 id。
//
// 先看元数据里带的外部 id（nfo 里的 <tmdbid> / <uniqueid type="tmdb"> 会进 provider_ids），
// 没有就按标题搜一次。
//
// **搜索只用于定位，不写剧集自己的元数据** —— 剧集的元数据是 nfo 说了算的。
// 这是「本地优先」在剧集层面的延伸：本地有 nfo 就用 nfo，
// 但为了把季/集元数据拉回来，得先知道这部剧在 provider 上是哪一个。
func (h *Handler) locateSeries(ctx context.Context, series *store.Item) (int, error) {
	if id, err := strconv.Atoi(strings.TrimSpace(series.ProviderIDs["tmdb"])); err == nil && id > 0 {
		return id, nil
	}

	title := strings.TrimSpace(series.Title)
	if title == "" {
		title = strings.TrimSpace(series.OriginalTitle)
	}
	if title == "" {
		return 0, nil
	}

	results, err := h.client.SearchSeries(ctx, title, provider.SearchOptions{})
	if err != nil {
		return 0, err
	}
	cands := make([]match.Candidate, 0, len(results))
	for _, r := range results {
		cands = append(cands, match.FromSearch(r))
	}
	if h.topN > 0 && len(cands) > h.topN {
		cands = cands[:h.topN]
	}
	if len(cands) == 0 {
		return 0, nil
	}

	local := match.Local{
		Kind:          provider.KindTV,
		Title:         series.Title,
		OriginalTitle: series.OriginalTitle,
	}
	if series.Year != nil {
		local.Year = int(*series.Year)
	}
	if season, episodes, err := h.st.SeriesStructure(ctx, series.ID); err == nil {
		local.SeasonNumber, local.EpisodeCount = season, episodes
	}

	top, ok := h.scorer.Best(local, cands)
	if !ok || top.Decision != match.DecisionAuto {
		h.log.Warn("剧集没有 provider id，按标题也没能确定，季/集的元数据只能跳过",
			"seriesId", series.ID, "title", title)
		return 0, nil
	}
	h.log.Info("剧集没有 provider id，按标题定位（仅用于取季/集元数据，不写剧集自己的元数据）",
		"seriesId", series.ID, "provider", top.CandidateID, "candidate", top.Title, "score", top.Score)
	return top.CandidateID, nil
}

// providerMissing 把「provider 上就没有这个东西」落成业务失败（不重试）。
func (h *Handler) providerMissing(ctx context.Context, it *store.Item, reason string) error {
	h.log.Info("provider 上没有这个条目，记为失败",
		"itemId", it.ID, "kind", it.Kind, "title", it.Title, "reason", reason)
	return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
		State: store.MatchStateFailed,
		Error: reason,
	})
}

// seasonMeta 把季详情映射成要落库的元数据。
//
// 刻意**不写标题**：季标题是本地按季号生成的（「第 1 季」），
// 比 TMDB 的译名稳（TMDB 有时只给英文的 "Season 1"，反而是退化）。
func seasonMeta(s *provider.Season) store.ItemMeta {
	meta := store.ItemMeta{
		Overview:     s.Overview,
		PremiereDate: parseDate(s.AirDate),
	}
	if s.ID > 0 {
		meta.ProviderIDs = map[string]string{"tmdb": strconv.Itoa(s.ID)}
	}
	return meta
}

// episodeMeta 把单集详情映射成要落库的元数据。
func episodeMeta(e *provider.Episode) store.ItemMeta {
	meta := store.ItemMeta{
		Title:        e.Name,
		Overview:     e.Overview,
		PremiereDate: parseDate(e.AirDate),
	}
	if e.RuntimeMin > 0 {
		meta.RuntimeTicks = ptrInt64(runtimeTicks(e.RuntimeMin))
	}
	if e.Rating > 0 {
		meta.Rating = ptrFloat(e.Rating)
	}
	if e.ID > 0 {
		meta.ProviderIDs = map[string]string{"tmdb": strconv.Itoa(e.ID)}
	}
	return meta
}

// numOr 取可空整数的值，为空时返回兜底值。
func numOr(v *int32, def int32) int32 {
	if v == nil {
		return def
	}
	return *v
}

// parseDate 解析 TMDB 的 YYYY-MM-DD；拿不到就返回 nil（空值不覆盖已有的首播日期）。
func parseDate(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &t
}

// runtimeTicks 把分钟换算成 .NET 风格的 tick（1 tick = 100ns），
// 与 ffprobe 探测出来的 runtime_ticks 用同一套单位。
func runtimeTicks(minutes int) int64 {
	return int64(minutes) * 60 * 10_000_000
}

func ptrInt32(v int32) *int32     { return &v }
func ptrInt64(v int64) *int64     { return &v }
func ptrFloat(v float64) *float64 { return &v }
