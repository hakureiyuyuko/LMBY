package scrape

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/match"
	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 本文件是「人工匹配」需要的那几个动作。
//
// 它们刻意复用自动刮削的同一套落库逻辑（字段锁、ApplyItemMeta 的合并语义、
// SaveMatchOutcome 的状态流转），差别只在「候选是谁定的」：
// 人工选定的结果标成 manual —— 以后重扫不会再覆盖它，
// 这正是 docs/REQUIREMENTS.md 决策表里「人工结果优先于自动结果」那条。

// ApplyCandidate 把人工选定的 provider 条目写进库。
func (h *Handler) ApplyCandidate(ctx context.Context, item *store.Item, providerID int) error {
	if h.client == nil {
		return errors.New("未配置元数据源")
	}
	if providerID <= 0 {
		return errors.New("候选 id 非法")
	}
	switch item.Kind {
	case itemKindMovie, itemKindSeries:
	default:
		return fmt.Errorf("这类条目不支持人工指定候选（%s）", item.Kind)
	}

	// 取一次详情：结构信号（集数/时长）与要落库的元数据都从这里来，
	// 与自动匹配走的是同一条路，保证写进去的东西一致。
	cands := []match.Candidate{{ID: providerID, Kind: providerKind(item.Kind)}}
	details := Enrich(ctx, h.client, item.Kind, cands, int(numOr(item.SeasonNum, 0)), h.log)
	d, ok := details[providerID]
	if !ok {
		return fmt.Errorf("取候选 %d 的详情失败，稍后重试", providerID)
	}

	meta := itemMeta(item, d)
	if err := h.applyMeta(ctx, item.ID, meta); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return fmt.Errorf("库里已有同名同年条目（《%s》），改名会撞唯一索引 —— "+
				"可能是同一个作品被扫成了两个条目", meta.Title)
		}
		return err
	}

	h.log.Info("人工匹配已应用",
		"itemId", item.ID, "kind", item.Kind, "provider", providerID, "title", meta.Title)

	// 状态标 manual：人工结果优先，自动刮削不再覆盖它。
	// 候选列表不覆盖（传 nil），界面上还能看到当初的候选。
	if err := h.st.SaveMatchOutcome(ctx, item.ID, store.MatchOutcome{
		State:  store.MatchStateManual,
		Source: h.client.Name(),
	}); err != nil {
		return err
	}
	if item.Kind == itemKindSeries {
		h.enqueueSeriesChildren(ctx, item.ID)
	}
	return nil
}

// DecoratePosters 给候选补上海报地址（界面用）。
//
// 打分器不关心图片，所以海报只在界面层意义上重要；但要拿它得先有详情，
// 而详情只有这里（Enrich）取过，于是就在这一步顺手补上，不另开一轮请求。
func DecoratePosters(client provider.Client, ranked []match.Verdict, details map[int]Detail) {
	if client == nil {
		return
	}
	for i := range ranked {
		d := details[ranked[i].CandidateID]
		var path string
		switch {
		case d.Movie != nil:
			path = d.Movie.PosterPath
		case d.Series != nil:
			path = d.Series.PosterPath
		}
		if path != "" {
			ranked[i].PosterURL = client.ImageURL(path, "w185")
		}
	}
}

// SearchCandidates 按给定查询词搜一遍并打分（界面上「换个词再搜」用）。
func (h *Handler) SearchCandidates(ctx context.Context, item *store.Item, query string) ([]match.Verdict, error) {
	if h.client == nil {
		return nil, errors.New("未配置元数据源")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("搜索词不能为空")
	}
	switch item.Kind {
	case itemKindMovie, itemKindSeries:
	default:
		// 季/集是按位置对应的，没有「换个词搜」这回事
		return nil, fmt.Errorf("这类条目按位置对应，不支持搜索（%s）", item.Kind)
	}

	results, err := h.search(ctx, item.Kind, query)
	if err != nil {
		return nil, err
	}
	cands := make([]match.Candidate, 0, len(results))
	for _, r := range results {
		cands = append(cands, match.FromSearch(r))
	}
	if h.topN > 0 && len(cands) > h.topN {
		cands = cands[:h.topN]
	}
	if len(cands) == 0 {
		return nil, nil
	}

	local := h.localFacts(ctx, item)
	details := Enrich(ctx, h.client, item.Kind, cands, local.SeasonNumber, h.log)
	ranked := h.scorer.Rank(local, cands)
	DecoratePosters(h.client, ranked, details)
	return ranked, nil
}

// MarkUnmatched 记下「人工确认过：这条不需要自动匹配」。
//
// 用场：库里的自制视频、码流测试片、花絮合集 —— 它们永远不会匹配上，
// 但不该一直挂在待处理列表里。
func (h *Handler) MarkUnmatched(ctx context.Context, item *store.Item, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "人工标记：不需要自动匹配"
	}
	h.log.Info("人工标记为不需要匹配", "itemId", item.ID, "title", item.Title, "reason", reason)
	return h.st.SaveMatchOutcome(ctx, item.ID, store.MatchOutcome{
		State: store.MatchStateManual,
		Error: reason,
	})
}
