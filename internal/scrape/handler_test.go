package scrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/match"
	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// —— 测试替身 ——
//
// 为什么要替身：刮削的判断逻辑（该不该落库、锁字段有没有生效、
// 状态怎么流转、重复刮削会不会再打 API）不该只能靠真库 + 真 TMDB 验证。
// 真实验证另有一套（见 docs/ROADMAP.md 的 M2 验收记录 + scripts/dev/match-sample.sh）。

type fakeStore struct {
	item     *store.Item
	itemErr  error
	season   int
	episodes int
	metas    []store.ItemMeta
	outcomes []store.MatchOutcome
	applyErr error
}

func (f *fakeStore) GetItem(context.Context, int64) (*store.Item, error) {
	if f.itemErr != nil {
		return nil, f.itemErr
	}
	if f.item == nil {
		return nil, store.ErrNotFound
	}
	return f.item, nil
}

func (f *fakeStore) SeriesStructure(context.Context, int64) (int, int, error) {
	return f.season, f.episodes, nil
}

func (f *fakeStore) ApplyItemMeta(_ context.Context, _ int64, m store.ItemMeta) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.metas = append(f.metas, m)
	return nil
}

func (f *fakeStore) SaveMatchOutcome(_ context.Context, _ int64, o store.MatchOutcome) error {
	f.outcomes = append(f.outcomes, o)
	return nil
}

type fakeProvider struct {
	queries      []string
	seasonCalls  []int
	movieResults []provider.SearchResult
	tvResults    []provider.SearchResult
	movie        *provider.Movie
	series       *provider.Series
	season       *provider.Season
	searchErr    error
	detailErr    error
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) SearchMovie(_ context.Context, q string, _ provider.SearchOptions) ([]provider.SearchResult, error) {
	f.queries = append(f.queries, "movie:"+q)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.movieResults, nil
}

func (f *fakeProvider) SearchSeries(_ context.Context, q string, _ provider.SearchOptions) ([]provider.SearchResult, error) {
	f.queries = append(f.queries, "tv:"+q)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.tvResults, nil
}

func (f *fakeProvider) Movie(context.Context, int, string) (*provider.Movie, error) {
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	if f.movie == nil {
		return nil, errors.New("替身没有准备电影详情")
	}
	return f.movie, nil
}

func (f *fakeProvider) Series(context.Context, int, string) (*provider.Series, error) {
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	if f.series == nil {
		return nil, errors.New("替身没有准备剧集详情")
	}
	return f.series, nil
}

func (f *fakeProvider) Season(_ context.Context, _ int, season int, _ string) (*provider.Season, error) {
	f.seasonCalls = append(f.seasonCalls, season)
	if f.season == nil {
		return &provider.Season{}, nil
	}
	return f.season, nil
}

func (f *fakeProvider) Episode(context.Context, int, int, int, string) (*provider.Episode, error) {
	return &provider.Episode{}, nil
}

func (f *fakeProvider) Images(context.Context, string, int, string) ([]provider.Image, error) {
	return nil, nil
}

func (f *fakeProvider) Credits(context.Context, string, int) (*provider.Credits, error) {
	return nil, nil
}

// —— 小工具 ——

func newTestHandler(st Store, p provider.Client) *Handler {
	return NewHandler(st, p, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func task(itemID int64, force bool) store.Task {
	payload := map[string]any{"itemId": itemID}
	if force {
		payload["force"] = true
	}
	return store.Task{ID: 1, Kind: store.TaskKindScrape, Payload: payload}
}

func item(kind, title string, year int32) *store.Item {
	it := &store.Item{ID: 7, LibraryID: 1, Kind: kind, Title: title}
	if year > 0 {
		it.Year = ptrInt32(year)
	}
	return it
}

// —— 用例 ——

// TestHandleAutoMatchMovie 真实案例：《言叶之庭》→ TMDB 198375。
func TestHandleAutoMatchMovie(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "言叶之庭", 2013)}
	p := &fakeProvider{
		movieResults: []provider.SearchResult{{
			ID: 198375, Kind: provider.KindMovie, Title: "言叶之庭",
			OriginalTitle: "言の葉の庭", Year: 2013, Popularity: 40, VoteCount: 1900,
		}},
		movie: &provider.Movie{
			ID: 198375, Title: "言叶之庭", OriginalTitle: "言の葉の庭", Year: 2013,
			Overview: "下雨天的庭园里……", Tagline: "一句台词", ReleaseDate: "2013-05-31",
			RuntimeMinutes: 46, Rating: 8.2,
			Genres:      []string{"动画", "爱情"},
			Studios:     []string{"CoMix Wave Films"},
			ProviderIDs: map[string]string{"tmdb": "198375", "imdb": "tt2176672"},
		},
	}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(st.metas) != 1 || len(st.outcomes) != 1 {
		t.Fatalf("落库次数 = %d 次元数据 / %d 次结果，期望各 1 次", len(st.metas), len(st.outcomes))
	}

	m := st.metas[0]
	if m.Title != "言叶之庭" || m.OriginalTitle != "言の葉の庭" {
		t.Errorf("标题写入不对: %+v", m)
	}
	if m.Year == nil || *m.Year != 2013 {
		t.Errorf("年份 = %v, 期望 2013", m.Year)
	}
	if m.RuntimeTicks == nil || *m.RuntimeTicks != int64(46)*60*10_000_000 {
		t.Errorf("时长 = %v ticks, 期望 %d", m.RuntimeTicks, int64(46)*60*10_000_000)
	}
	if len(m.Genres) != 2 || m.Genres[0] != "动画" {
		t.Errorf("流派 = %v", m.Genres)
	}
	if m.ProviderIDs["tmdb"] != "198375" {
		t.Errorf("provider_ids = %v", m.ProviderIDs)
	}
	if m.PremiereDate == nil || m.PremiereDate.Format("2006-01-02") != "2013-05-31" {
		t.Errorf("首映日期 = %v", m.PremiereDate)
	}

	o := st.outcomes[0]
	if o.State != store.MatchStateMatched {
		t.Errorf("状态 = %s, 期望 %s", o.State, store.MatchStateMatched)
	}
	if o.Score < 0.9 {
		t.Errorf("分数 = %.3f, 期望 ≥0.9", o.Score)
	}
	if o.Source != "fake" {
		t.Errorf("来源 = %s, 期望 fake", o.Source)
	}
	// 电影不该去请求剧集详情
	if len(p.seasonCalls) != 0 {
		t.Errorf("电影不该请求季详情，实际请求了 %v", p.seasonCalls)
	}
}

// TestHandleKeyboardLockedFields 手改过的字段重扫不能被覆盖。
func TestHandleKeyboardLockedFields(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "言叶之庭", 2013)}
	st.item.LockedFields = []string{FieldOverview, FieldGenres}
	p := &fakeProvider{
		movieResults: []provider.SearchResult{{ID: 198375, Kind: provider.KindMovie, Title: "言叶之庭", Year: 2013}},
		movie: &provider.Movie{
			ID: 198375, Title: "言叶之庭", Year: 2013,
			Overview: "TMDB 的简介", Genres: []string{"动画", "爱情"},
			ProviderIDs: map[string]string{"tmdb": "198375"},
		},
	}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	m := st.metas[0]
	if m.Overview != "" {
		t.Errorf("overview 被锁却还写了 %q", m.Overview)
	}
	if len(m.Genres) != 0 {
		t.Errorf("genres 被锁却还写了 %v", m.Genres)
	}
	// 没锁的字段照写
	if m.Title != "言叶之庭" || m.SortTitle == "" {
		t.Errorf("没锁的标题/排序标题应当写入: %+v", m)
	}
	if m.ProviderIDs["tmdb"] != "198375" {
		t.Errorf("没锁的 provider_ids 应当写入: %v", m.ProviderIDs)
	}
}

// TestHandleReviewKeepsCandidates 不够确定时进人工队列，并把候选存下来。
func TestHandleReviewKeepsCandidates(t *testing.T) {
	// 没有年份的《钢之炼金术师》：候选里有本体(2003)与 FA(2009)，
	// 标题近乎相同 → 打分器要求「领先第二名」才自动，这里差得不够 → review。
	st := &fakeStore{item: item(itemKindSeries, "钢之炼金术师", 0)}
	st.season, st.episodes = 1, 64
	p := &fakeProvider{
		tvResults: []provider.SearchResult{
			{ID: 31910, Kind: provider.KindTV, Title: "钢之炼金术师", Year: 2003, Popularity: 80},
			{ID: 31911, Kind: provider.KindTV, Title: "钢之炼金术师 FA", Year: 2009, Popularity: 120},
		},
		series: &provider.Series{
			ID: 31911, Name: "钢之炼金术师 FA", OriginalName: "鋼の錬金術師 FULLMETAL ALCHEMIST",
			Year: 2009, Overview: "…", FirstAirDate: "2009-04-05",
			Seasons: []provider.SeasonSummary{{SeasonNumber: 1, EpisodeCount: 64}},
		},
	}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(st.metas) != 0 {
		t.Errorf("review 不该写元数据，实际写了 %d 次", len(st.metas))
	}
	o := st.outcomes[0]
	if o.State != store.MatchStateReview {
		t.Errorf("状态 = %s, 期望 %s", o.State, store.MatchStateReview)
	}
	if o.Error == "" {
		t.Error("review 应当带上「为什么需要人工看」的说明")
	}
	cands, ok := o.Candidates.([]match.Verdict)
	if !ok || len(cands) != 2 {
		t.Fatalf("候选没存下来: %#v", o.Candidates)
	}
	if cands[0].Decision == "" || len(cands[0].Parts) == 0 {
		t.Errorf("候选人缺打分明细，人工界面就没法解释: %+v", cands[0])
	}
}

func TestHandleNoCandidates(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "某个不存在于 TMDB 的自制视频", 2024)}
	p := &fakeProvider{movieResults: nil}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("没搜到候选不该让任务失败重试: %v", err)
	}
	o := st.outcomes[0]
	if o.State != store.MatchStateFailed {
		t.Errorf("状态 = %s, 期望 %s", o.State, store.MatchStateFailed)
	}
	if o.Error == "" {
		t.Error("应当写明原因")
	}
	if len(st.metas) != 0 {
		t.Error("没匹配上不该写元数据")
	}
}

// TestHandleSkipsAlreadyMatched 重复刮削零 API 调用。
func TestHandleSkipsAlreadyMatched(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "言叶之庭", 2013)}
	st.item.MatchState = store.MatchStateMatched
	p := &fakeProvider{movieResults: []provider.SearchResult{{ID: 1, Kind: provider.KindMovie, Title: "言叶之庭", Year: 2013}}}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(p.queries) != 0 {
		t.Errorf("已匹配的条目不该再打 API，实际请求了 %v", p.queries)
	}
	if len(st.outcomes) != 0 {
		t.Error("已匹配的条目不该重写状态")
	}
}

func TestHandleForceRescrapes(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "言叶之庭", 2013)}
	st.item.MatchState = store.MatchStateMatched
	p := &fakeProvider{
		movieResults: []provider.SearchResult{{ID: 198375, Kind: provider.KindMovie, Title: "言叶之庭", Year: 2013}},
		movie:        &provider.Movie{ID: 198375, Title: "言叶之庭", Year: 2013},
	}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, true)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(p.queries) != 1 {
		t.Errorf("force 时应当重新搜索，实际请求 %v", p.queries)
	}
}

func TestHandleSkipsNonScrapableKinds(t *testing.T) {
	for _, kind := range []string{"episode", "season", "extra"} {
		st := &fakeStore{item: &store.Item{ID: 7, Kind: kind, Title: "第 1 集"}}
		p := &fakeProvider{}
		if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
			t.Errorf("%s: Handle 返回错误: %v", kind, err)
		}
		if len(p.queries) != 0 || len(st.outcomes) != 0 {
			t.Errorf("%s 这类条目不应当被单独刮削", kind)
		}
	}
}

func TestHandleMissingTitle(t *testing.T) {
	st := &fakeStore{item: &store.Item{ID: 7, Kind: itemKindMovie}}
	p := &fakeProvider{}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(p.queries) != 0 {
		t.Errorf("没标题不该去搜索，实际 %v", p.queries)
	}
	if st.outcomes[0].State != store.MatchStateFailed {
		t.Errorf("状态 = %s, 期望 %s", st.outcomes[0].State, store.MatchStateFailed)
	}
}

// TestHandleProviderErrorRetries 接口/网络问题要让队列退避重试（返回 error），
// 而不是把条目判成「匹配失败」。
func TestHandleProviderErrorRetries(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "言叶之庭", 2013)}
	p := &fakeProvider{searchErr: errors.New("tmdb: 503 service unavailable")}

	err := newTestHandler(st, p).Handle(context.Background(), task(7, false))
	if err == nil {
		t.Fatal("接口报错时应当返回 error 交给队列重试")
	}
	if len(st.outcomes) != 0 {
		t.Error("重试前不该落「匹配失败」的结论")
	}
}

func TestHandleItemGone(t *testing.T) {
	st := &fakeStore{} // GetItem 会返回 ErrNotFound
	p := &fakeProvider{}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("条目已删除时应当安静丢弃任务: %v", err)
	}
	if len(st.outcomes) != 0 {
		t.Error("条目没了不该写状态")
	}
}

// TestHandleSeriesUsesStructureAndNetworks 剧集路径：
// 结构信号（集数最多的那一季）要参与打分，出品方要回退到电视台。
func TestHandleSeriesUsesStructureAndNetworks(t *testing.T) {
	st := &fakeStore{item: item(itemKindSeries, "致不灭的你", 2021)}
	st.season, st.episodes = 1, 20
	p := &fakeProvider{
		tvResults: []provider.SearchResult{
			{ID: 97525, Kind: provider.KindTV, Title: "致不灭的你", OriginalTitle: "不滅のあなたへ", Year: 2021},
		},
		series: &provider.Series{
			ID: 97525, Name: "致不灭的你", OriginalName: "不滅のあなたへ",
			Year: 2021, Overview: "…", FirstAirDate: "2021-04-12", Rating: 8.4,
			Genres:      []string{"动画", "剧情"},
			Networks:    []string{"NHK"},
			Seasons:     []provider.SeasonSummary{{SeasonNumber: 1, EpisodeCount: 20}},
			ProviderIDs: map[string]string{"tmdb": "97525"},
		},
		season: &provider.Season{
			SeasonNumber: 1,
			Episodes: []provider.EpisodeSummary{
				{EpisodeNumber: 1, RuntimeMin: 24}, {EpisodeNumber: 2, RuntimeMin: 24},
				{EpisodeNumber: 3, RuntimeMin: 100}, // 离群值应当被中位数挡掉
			},
		},
	}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(st.metas) != 1 {
		t.Fatalf("应当写入一次元数据，实际 %d 次", len(st.metas))
	}
	m := st.metas[0]
	if m.Studios[0] != "NHK" {
		t.Errorf("出品方应当回退到电视台，实际 %v", m.Studios)
	}
	if st.outcomes[0].State != store.MatchStateMatched {
		t.Errorf("状态 = %s, 期望 %s", st.outcomes[0].State, store.MatchStateMatched)
	}
	// 结构信号用的是本地「集数最多的那一季」的季号
	if len(p.seasonCalls) != 1 || p.seasonCalls[0] != 1 {
		t.Errorf("请求的季 = %v, 期望 [1]", p.seasonCalls)
	}
	// 结构项参与打分后会出现在明细里
	var found bool
	if cands, ok := st.outcomes[0].Candidates.([]match.Verdict); ok && len(cands) > 0 {
		_, found = cands[0].Part("structure")
	}
	if !found {
		t.Errorf("结构信号应当参与打分（明细里应有 structure 项）")
	}
}

// TestHandleTitleConflictGoesToReview 同一个作品被扫成两个条目时：
// 改名会撞唯一索引，应当转人工确认而不是反复重试。
// （真实案例：库里《你的颜色》有两条，刮削写入时撞 media_items_movie_uniq）
func TestHandleTitleConflictGoesToReview(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "你的颜色", 0)}
	st.applyErr = fmt.Errorf("%w: duplicate key value violates unique constraint", store.ErrAlreadyExists)
	p := &fakeProvider{
		movieResults: []provider.SearchResult{{
			ID: 1327819, Kind: provider.KindMovie, Title: "你的颜色", Year: 2024,
		}},
		movie: &provider.Movie{ID: 1327819, Title: "你的颜色", Year: 2024, Overview: "…"},
	}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("撞唯一索引不该让任务重试: %v", err)
	}
	if len(st.outcomes) != 1 {
		t.Fatalf("应当落一条结论，实际 %d 条", len(st.outcomes))
	}
	o := st.outcomes[0]
	if o.State != store.MatchStateReview {
		t.Errorf("状态 = %s, 期望 %s", o.State, store.MatchStateReview)
	}
	if !strings.Contains(o.Error, "同名") {
		t.Errorf("说明里应当讲清楚是同名冲突: %q", o.Error)
	}
}

// TestHandleNFOPriority nfo 优先：有 nfo 元数据的条目默认一条 API 都不打。
// 这是用户的明确要求（nfo 是人工花大力气整理的），也是产品策略而不是实现细节，
// 所以要有单测卡着。
func TestHandleNFOPriority(t *testing.T) {
	st := &fakeStore{item: item(itemKindMovie, "言叶之庭", 2013)}
	st.item.MatchState = store.MatchStateNFO
	st.item.MetadataSource = store.MetadataSourceNFO
	p := &fakeProvider{
		movieResults: []provider.SearchResult{{ID: 198375, Kind: provider.KindMovie, Title: "言叶之庭", Year: 2013}},
		movie:        &provider.Movie{ID: 198375, Title: "言叶之庭", Year: 2013},
	}

	if err := newTestHandler(st, p).Handle(context.Background(), task(7, false)); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(p.queries) != 0 {
		t.Errorf("有 nfo 的条目不该打 API，实际请求了 %v", p.queries)
	}
	if len(st.metas) != 0 || len(st.outcomes) != 0 {
		t.Error("有 nfo 的条目不该被改写元数据或状态")
	}

	// 只有显式 force 才会去搜（人主动要求覆盖时才允许）
	st2 := &fakeStore{item: item(itemKindMovie, "言叶之庭", 2013)}
	st2.item.MatchState = store.MatchStateNFO
	st2.item.MetadataSource = store.MetadataSourceNFO
	p2 := &fakeProvider{
		movieResults: []provider.SearchResult{{ID: 198375, Kind: provider.KindMovie, Title: "言叶之庭", Year: 2013}},
		movie:        &provider.Movie{ID: 198375, Title: "言叶之庭", Year: 2013},
	}
	if err := newTestHandler(st2, p2).Handle(context.Background(), task(7, true)); err != nil {
		t.Fatalf("force 时 Handle 返回错误: %v", err)
	}
	if len(p2.queries) != 1 {
		t.Errorf("force 时应当重新搜索，实际请求 %v", p2.queries)
	}
}

func TestSortTitleAndTicks(t *testing.T) {
	if got := sortTitle("The Matrix"); got != "matrix" {
		t.Errorf("sortTitle(The Matrix) = %q, 期望 matrix", got)
	}
	if got := sortTitle("  言叶之庭  "); got != "言叶之庭" {
		t.Errorf("sortTitle(言叶之庭) = %q", got)
	}
	if got := runtimeTicks(46); got != 27_600_000_000 {
		t.Errorf("runtimeTicks(46) = %d", got)
	}
}
