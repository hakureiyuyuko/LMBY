package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 本文件是「海报墙 / 剧集视图」与「批量操作」的接口。

// handleBrowseLibrary 海报墙：列出一个库的**顶层**条目（电影、剧集），可排序分页。
//
// 与 /items 的分工（两个都留着）：
//   - GET /libraries/{id}/items 是「原始条目表」，平铺所有类型（含季与集），排查问题用；
//   - GET /libraries/{id}/browse 是「给人看的」，只要顶层、带海报、可按年份/最近添加排序。
func (s *Server) handleBrowseLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	if _, err := s.store.GetLibrary(ctx, id); err != nil {
		s.notFoundOrError(w, err)
		return
	}

	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != "movie" && kind != "series" {
		writeError(w, http.StatusBadRequest, "kind 只能是 movie 或 series（海报墙只看顶层条目）")
		return
	}
	sort := strings.TrimSpace(r.URL.Query().Get("sort"))
	switch sort {
	case "", "title", "year", "added":
	default:
		writeError(w, http.StatusBadRequest, "sort 只能是 title / year / added")
		return
	}
	limit := queryInt(r, "limit", 60)
	if limit < 1 {
		limit = 60
	}
	if limit > 200 {
		limit = 200
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	filter := store.ItemFilter{Kind: kind, TopLevel: true, Sort: sort}
	items, err := s.store.ListItems(ctx, id, filter, limit, offset)
	if err != nil {
		s.serverError(w, "读取条目失败", err)
		return
	}
	total, err := s.store.CountItems(ctx, id, filter)
	if err != nil {
		s.serverError(w, "统计条目失败", err)
		return
	}
	if items == nil {
		items = []store.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// handleItemChildren 子项列表：剧集 → 季、季 → 集。
//
// counts 是「每个子项自己的子项数」，界面上用来显示「第 1 季 · 12 集」——
// 与列表一次返回，免得为每一季再发一次请求。
func (s *Server) handleItemChildren(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	summary, err := s.store.ChildrenOf(ctx, item.ID)
	if err != nil {
		s.serverError(w, "读取子项失败", err)
		return
	}
	if summary.Items == nil {
		summary.Items = []store.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item":   itemBrief(item),
		"items":  summary.Items,
		"counts": summary.Counts,
	})
}

// itemBatchRequest 是批量操作的请求体。
type itemBatchRequest struct {
	// Action 只支持 scrape（排刮削任务）与 skip（标记不需要匹配）。
	Action string `json:"action"`
	// ItemIDs 是选中的条目。
	ItemIDs []int64 `json:"itemIds"`
	// Force 仅对 scrape 有意义：连已有元数据的条目也重刮。
	Force bool `json:"force"`
	// Reason 仅对 skip 有意义。
	Reason string `json:"reason"`
}

// handleBatchItems 对一批条目做同一个动作（人工匹配页的多选操作）。
//
// 为什么做成一个接口而不是让界面循环调用单条接口：
//   - 几百条时会发几百个请求，而这是用户点一下就要看到反馈的操作；
//   - 单条接口逐个失败时，界面很难说清「哪几条成功了」。
func (s *Server) handleBatchItems(w http.ResponseWriter, r *http.Request) {
	var req itemBatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.ItemIDs) == 0 {
		writeError(w, http.StatusBadRequest, "itemIds 不能为空")
		return
	}
	// 上限在 store 里也有一道（防手写请求）；这里先拦是为了给出 400 而不是 500。
	if len(req.ItemIDs) > 500 {
		writeError(w, http.StatusBadRequest, "一次最多处理 500 条")
		return
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	var res store.BatchResult
	var err error
	switch req.Action {
	case "scrape":
		if !s.scrapeConfigured() {
			writeError(w, http.StatusConflict, "未配置元数据源（在设置页里填 TMDB 凭据，或写进 config.toml）")
			return
		}
		res, err = s.store.EnqueueItemScrapes(ctx, req.ItemIDs, req.Force)
	case "skip":
		res, err = s.store.MarkItemsUnmatched(ctx, req.ItemIDs, req.Reason)
	default:
		writeError(w, http.StatusBadRequest, "action 只能是 scrape 或 skip")
		return
	}
	if err != nil {
		s.serverError(w, "批量操作失败", err)
		return
	}

	s.log.Info("批量操作", "action", req.Action, "requested", len(req.ItemIDs),
		"applied", res.Applied, "skipped", res.Skipped, "force", req.Force, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"action":  req.Action,
		"applied": res.Applied,
		"skipped": res.Skipped,
	})
}

// handleTestProvider 用当前凭据打一次真实的 provider 请求（设置页的「测试连接」）。
//
// 为什么值得做：TMDB 只在**第一次真请求**时才暴露问题（Key 写错、被停用、
// 网络不通、语言码非法），而用户填完凭据后最想知道的就是「它到底能不能用」。
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	if s.meta == nil {
		writeError(w, http.StatusConflict, "元数据源未初始化")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		query = "Matrix"
	}
	if !s.scrapeConfigured() {
		writeError(w, http.StatusConflict, "未配置 TMDB 凭据")
		return
	}

	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	started := time.Now()
	results, err := s.meta.SearchMovie(ctx, query, provider.SearchOptions{Primary: true})
	if err != nil {
		s.log.Warn("provider 测试失败", "query", query, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "query": query, "error": err.Error(),
		})
		return
	}
	samples := make([]map[string]any, 0, 3)
	for i, res := range results {
		if i >= 3 {
			break
		}
		samples = append(samples, map[string]any{
			"id": res.ID, "title": res.Title, "year": res.Year,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"query":   query,
		"count":   len(results),
		"samples": samples,
		"elapsed": time.Since(started).Milliseconds(),
	})
}
