package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 本文件是**收藏**（M6，迁移 0012）。
//
// 三个接口各管一件事：
//   - GET  /api/v1/favorites                 我的收藏（列表，可翻页、可按类型过滤）
//   - GET  /api/v1/items/{id}/favorite       单条的状态（我收藏了没 + 全站收藏数）
//   - POST /api/v1/items/{id}/favorite       收藏 / 取消（幂等）
//
// 为什么不把「我收藏了没」塞进 `GET /api/v1/items/{id}`：那个接口是**编辑页**在用的，
// 它不该因为界面多了一个爱心按钮就多查一张表；而且收藏状态是「随账号变的」，
// 混进条目本体后，将来做缓存会很难受（同一份条目 JSON 对不同账号含义不同）。

// handleListFavorites 我的收藏。
func (s *Server) handleListFavorites(w http.ResponseWriter, r *http.Request) {
	authCtx := currentAuth(r)
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	// 类型取值复用条目那边的校验（只有一处定义，不会两边跑偏）
	if kind != "" && !store.ValidItemKind(kind) {
		writeError(w, http.StatusBadRequest, "kind 只能是 movie / series / season / episode / extra")
		return
	}
	limit := queryInt(r, "limit", 50)
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	items, total, err := s.store.ListFavorites(ctx, authCtx.User.ID, kind, limit, offset)
	if err != nil {
		s.serverError(w, "读取收藏失败", err)
		return
	}
	if items == nil {
		items = []store.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		// 连「有几个类型」也不猜：界面要分面就再查一次，别在这里塞聚合
		"limit":  limit,
		"offset": offset,
	})
}

// handleGetFavorite 单条条目的收藏状态。
func (s *Server) handleGetFavorite(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	authCtx := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	mine, total, err := s.store.FavoriteState(ctx, authCtx.User.ID, item.ID)
	if err != nil {
		s.serverError(w, "读取收藏状态失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"itemId":   item.ID,
		"favorite": mine,
		"count":    total,
	})
}

// handleSetFavorite 收藏 / 取消收藏（幂等：重复收藏同一个条目也是 200）。
//
// 副作用刻意做小：响应里回**新状态与新的总数**，界面可以直接用它更新按钮与计数，
// 不必再查一次（少一次往返，也少一次「按钮和数字不同步」的机会）。
func (s *Server) handleSetFavorite(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		Favorite *bool `json:"favorite"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Favorite == nil {
		writeError(w, http.StatusBadRequest, "缺少 favorite 字段（true = 收藏，false = 取消）")
		return
	}
	authCtx := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if err := s.store.SetFavorite(ctx, authCtx.User.ID, item.ID, *body.Favorite); err != nil {
		s.serverError(w, "更新收藏失败", err)
		return
	}
	mine, total, err := s.store.FavoriteState(ctx, authCtx.User.ID, item.ID)
	if err != nil {
		s.serverError(w, "读取收藏状态失败", err)
		return
	}
	s.log.Debug("收藏状态变更", "item", item.ID, "favorite", *body.Favorite, "user", authCtx.User.Username)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"itemId":   item.ID,
		"favorite": mine,
		"count":    total,
	})
}
