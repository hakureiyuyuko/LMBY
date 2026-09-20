package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/match"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// matchRequest 是「应用某个候选」的请求体。
type matchRequest struct {
	// ProviderID 是选中的候选在 provider 上的 id（界面从候选列表里拿）。
	ProviderID int `json:"providerId"`
}

// matchSearchRequest 是「换个词再搜」的请求体。
type matchSearchRequest struct {
	Query string `json:"query"`
}

// matchSkipRequest 是「标记不需要匹配」的请求体。
type matchSkipRequest struct {
	Reason string `json:"reason"`
}

// handleGetItemMatch 返回条目的匹配状态与候选。
//
// 候选是刮削时存下来的（含每个候选的打分明细），所以界面**不需要重新搜**；
// 没有候选时前端会给「换个词再搜」的入口。
func (s *Server) handleGetItemMatch(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	candidates := json.RawMessage(`[]`)
	if raw, err := s.store.MatchCandidates(ctx, item.ID); err == nil && len(raw) > 0 {
		candidates = raw
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "读取匹配候选失败", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"item":       itemBrief(item),
		"candidates": candidates,
	})
}

// handleApplyItemMatch 应用人工选定的候选。
func (s *Server) handleApplyItemMatch(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	if s.scraper == nil {
		writeError(w, http.StatusConflict, "未配置元数据源（TMDB），无法人工匹配")
		return
	}

	var req matchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ProviderID <= 0 {
		writeError(w, http.StatusBadRequest, "缺少 providerId")
		return
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	if err := s.scraper.ApplyCandidate(ctx, item, req.ProviderID); err != nil {
		s.log.Warn("人工匹配失败", "itemId", item.ID, "provider", req.ProviderID, "err", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("人工匹配已生效", "itemId", item.ID, "provider", req.ProviderID, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "matchState": store.MatchStateManual})
}

// handleSearchItemMatch 按给定词重新搜索候选（不落库）。
func (s *Server) handleSearchItemMatch(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	if s.scraper == nil {
		writeError(w, http.StatusConflict, "未配置元数据源（TMDB），无法搜索")
		return
	}

	var req matchSearchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		// 没给词就用条目标题（相当于「重新搜一次」）
		query = strings.TrimSpace(item.Title)
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	ranked, err := s.scraper.SearchCandidates(ctx, item, query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if ranked == nil {
		ranked = []match.Verdict{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":      query,
		"candidates": ranked,
	})
}

// handleSkipItemMatch 记下「这条不需要自动匹配」。
func (s *Server) handleSkipItemMatch(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	if s.scraper == nil {
		writeError(w, http.StatusConflict, "未配置元数据源")
		return
	}

	var req matchSkipRequest
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if err := s.scraper.MarkUnmatched(ctx, item, req.Reason); err != nil {
		s.serverError(w, "标记失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "matchState": store.MatchStateManual})
}

// itemBrief 是人工匹配界面要的条目摘要（不含内部字段）。
func itemBrief(item *store.Item) map[string]any {
	brief := map[string]any{
		"id":             item.ID,
		"libraryId":      item.LibraryID,
		"kind":           item.Kind,
		"title":          item.Title,
		"originalTitle":  item.OriginalTitle,
		"matchState":     item.MatchState,
		"metadataSource": item.MetadataSource,
		"scrapeError":    item.ScrapeError,
		"providerIds":    item.ProviderIDs,
		"lockedFields":   item.LockedFields,
	}
	if item.Year != nil {
		brief["year"] = *item.Year
	}
	if item.MatchScore != nil {
		brief["matchScore"] = *item.MatchScore
	}
	if item.Overview != "" {
		brief["overview"] = item.Overview
	}
	return brief
}

// matchVerdictAlias 只是为了让 nil 切片能序列化成 []（而不是 null）。
func usernameOf(r *http.Request) string {
	if a := currentAuth(r); a != nil && a.User != nil {
		return a.User.Username
	}
	return ""
}
