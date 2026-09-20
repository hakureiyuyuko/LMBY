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
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/match"
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
	itemKindMovie  = "movie"
	itemKindSeries = "series"
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
}

// Handler 处理 scrape 任务。
type Handler struct {
	st     Store
	client provider.Client
	scorer match.Scorer
	log    *slog.Logger
	topN   int
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

	if it.Kind != itemKindMovie && it.Kind != itemKindSeries {
		// 季/集/花絮不单独刮：拿「第 3 集」去搜标题没有意义，
		// 它们的元数据应当由所属剧集带下来。
		h.log.Debug("这类条目不单独刮削", "itemId", it.ID, "kind", it.Kind)
		return nil
	}

	// 已经刮好的不再重复请求 —— 「重复刮削零 API 调用」最直接的保证。
	// 人工锁定的更不该碰（人工结果优先于自动结果）。
	if !p.Force {
		switch it.MatchState {
		case store.MatchStateMatched, store.MatchStateManual:
			h.log.Debug("条目已是最终状态，跳过", "itemId", it.ID, "state", it.MatchState)
			return nil
		}
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
	top := ranked[0]

	switch top.Decision {
	case match.DecisionAuto:
		meta := itemMeta(it, details[top.CandidateID])
		if err := h.st.ApplyItemMeta(ctx, it.ID, meta); err != nil {
			// 同一个作品被扫成了两个条目时，刮削改名会撞上唯一索引：
			// 这是「需要人工确认是否重复」的业务结论，不是可以重试的环境问题
			// （否则只会拿同一条 SQL 重试到上限）。
			if errors.Is(err, store.ErrAlreadyExists) {
				h.log.Warn("刮削改名后与库里已有条目同名，转人工确认",
					"itemId", it.ID, "title", meta.Title, "err", err.Error())
				return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
					State:      store.MatchStateReview,
					Score:      top.Score,
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
		return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State:      store.MatchStateMatched,
			Score:      top.Score,
			Source:     h.client.Name(),
			Candidates: ranked,
		})

	case match.DecisionReview:
		h.log.Info("候选需要人工确认",
			"itemId", it.ID, "title", it.Title,
			"provider", top.CandidateID, "candidate", top.Title,
			"score", top.Score, "margin", top.Margin)
		return h.st.SaveMatchOutcome(ctx, it.ID, store.MatchOutcome{
			State: store.MatchStateReview,
			Score: top.Score,
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
			Score:      top.Score,
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
	locked := func(name string) bool {
		for _, f := range it.LockedFields {
			if f == name {
				return true
			}
		}
		return false
	}

	var m store.ItemMeta

	switch {
	case d.Movie != nil:
		mv := d.Movie
		if !locked(FieldTitle) {
			m.Title = mv.Title
			m.SortTitle = sortTitle(mv.Title)
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
			m.SortTitle = sortTitle(sv.Name)
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

// sortTitle 生成排序标题：去掉开头的冠词，让《The Matrix》排在 M 而不是 T。
func sortTitle(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, article) {
			t = t[len(article):]
			break
		}
	}
	return strings.TrimSpace(strings.TrimLeft(t, " ._-·、|"))
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
