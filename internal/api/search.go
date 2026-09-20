package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// handleSearch 搜索条目（标题 / 原始标题，中文二元组 + trigram 错字容忍）。
//
// 参数：q（必填）、libraryId、kind、limit、offset。
// 取舍：空词直接 400（不做「空词返回全部」，那是列表接口的活）；
// limit 上限 100 —— 搜索结果是给人翻的，不是用来导数据的。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "缺少查询词 q")
		return
	}

	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && !store.ValidItemKind(kind) {
		writeError(w, http.StatusBadRequest, "kind 只能是 movie / series / season / episode / extra")
		return
	}

	limit := queryInt(r, "limit", 24)
	if limit < 1 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	q := store.SearchQuery{Text: query, Kind: kind, Limit: limit, Offset: offset}
	if raw := strings.TrimSpace(r.URL.Query().Get("libraryId")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "libraryId 非法")
			return
		}
		q.LibraryID = &id
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	hits, total, err := s.store.SearchItems(ctx, q)
	if err != nil {
		s.serverError(w, "搜索失败", err)
		return
	}
	if hits == nil {
		hits = []store.SearchHit{}
	}
	s.log.Debug("搜索", "q", query, "kind", kind, "hits", total, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"query":  query,
		"items":  hits,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}
