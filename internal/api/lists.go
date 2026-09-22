package api

import (
	"errors"
	"net/http"
	"strconv"
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

// ------------------------------------------------------------------ 播放列表 / 合集
//
// 接口形状（与 store 的权限判定配合，越权一律 403、看不见的一律 404）：
//
//	GET    /api/v1/playlists                     我的列表 + 所有合集
//	POST   /api/v1/playlists                     新建（kind=collection 需要管理员）
//	GET    /api/v1/playlists/{id}                单个列表的元数据
//	PATCH  /api/v1/playlists/{id}                改名 / 改说明（只给要改的字段）
//	DELETE /api/v1/playlists/{id}                删除（不动媒体文件）
//	GET    /api/v1/playlists/{id}/items          列表里的条目（按列表顺序，可翻页）
//	POST   /api/v1/playlists/{id}/items          批量加入（追加到末尾，幂等）
//	PUT    /api/v1/playlists/{id}/items          按给定顺序重排
//	DELETE /api/v1/playlists/{id}/items/{itemId} 移出一个
//	GET    /api/v1/playlists/{id}/neighbors      某个条目的前后邻居（播放器的「下一项」）

// viewerOf 把当前会话折成 store 要的「谁在看」。
//
// store 是权限的唯一执行点（见 internal/store/lists.go 的说明），
// 这里只负责把身份**原样**传下去，不做任何判断。
func viewerOf(r *http.Request) store.Viewer {
	u := currentAuth(r).User
	return store.Viewer{UserID: u.ID, IsAdmin: u.IsAdmin}
}

// playlistIDFromPath 取路径里的 {id}。
func playlistIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "列表 id 非法")
		return 0, false
	}
	return id, true
}

// writePlaylistError 把 store 的错误映射成 HTTP 语义（一处映射，各处一致）。
func (s *Server) writePlaylistError(w http.ResponseWriter, action string, err error) {
	var inv *store.InvalidInputError
	switch {
	case errors.As(err, &inv):
		// 用户输入错 → 400（不是 500：否则日志里全是假警报）
		writeError(w, http.StatusBadRequest, inv.Msg)
	case errors.Is(err, store.ErrPlaylistNotFound):
		writeError(w, http.StatusNotFound, "列表不存在")
	case errors.Is(err, store.ErrPlaylistForbidden):
		writeError(w, http.StatusForbidden, "没有权限修改这个列表")
	case errors.Is(err, store.ErrPlaylistNameTaken):
		writeError(w, http.StatusConflict, "已经有同名的列表了")
	default:
		s.serverError(w, action, err)
	}
}

// handleListPlaylists 我的列表 + 所有合集。
func (s *Server) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	lists, err := s.store.ListPlaylists(ctx, viewerOf(r))
	if err != nil {
		s.serverError(w, "读取列表失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": lists})
}

// handleCreatePlaylist 新建播放列表 / 合集。
func (s *Server) handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Overview string `json:"overview"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	p, err := s.store.CreatePlaylist(ctx, viewerOf(r), body.Kind, body.Name, body.Overview)
	if err != nil {
		s.writePlaylistError(w, "新建列表失败", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"playlist": p})
}

// handleGetPlaylist 单个列表的元数据。
func (s *Server) handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	p, err := s.store.GetPlaylistFor(ctx, viewerOf(r), id)
	if err != nil {
		s.writePlaylistError(w, "读取列表失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlist": p})
}

// handleUpdatePlaylist 改名 / 改说明。
//
// 用指针接 body：**没给的字段就是「不改」**（与 store 的约定一致）——
// 界面只改名字时不该把说明抹掉。
func (s *Server) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Overview *string `json:"overview"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	p, err := s.store.UpdatePlaylist(ctx, viewerOf(r), id, body.Name, body.Overview)
	if err != nil {
		s.writePlaylistError(w, "更新列表失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlist": p})
}

// handleDeletePlaylist 删除列表（条目关系级联走，媒体文件不动）。
func (s *Server) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if err := s.store.DeletePlaylist(ctx, viewerOf(r), id); err != nil {
		s.writePlaylistError(w, "删除列表失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleListPlaylistItems 列表里的条目（按列表顺序）。
func (s *Server) handleListPlaylistItems(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	items, total, err := s.store.ListPlaylistItems(ctx, viewerOf(r), id, limit, offset)
	if err != nil {
		s.writePlaylistError(w, "读取列表条目失败", err)
		return
	}
	if items == nil {
		items = []store.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

// playlistItemsBody 是加入 / 重排共用的请求体。
type playlistItemsBody struct {
	ItemIDs []int64 `json:"itemIds"`
}

// handleAddPlaylistItems 批量加入（追加到末尾，幂等）。
func (s *Server) handleAddPlaylistItems(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	var body playlistItemsBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.ItemIDs) > 500 {
		writeError(w, http.StatusBadRequest, "一次最多加入 500 个条目")
		return
	}
	ctx, cancel := contextWithTimeout(r, 25*time.Second)
	defer cancel()

	added, err := s.store.AddPlaylistItems(ctx, viewerOf(r), id, body.ItemIDs)
	if err != nil {
		s.writePlaylistError(w, "加入列表失败", err)
		return
	}
	p, err := s.store.GetPlaylistFor(ctx, viewerOf(r), id)
	if err != nil {
		s.writePlaylistError(w, "读取列表失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "added": added, "itemCount": p.ItemCount, "playlistId": id,
	})
}

// handleReorderPlaylist 按给定顺序重排（整串写一遍，见 store 里的说明）。
func (s *Server) handleReorderPlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	var body playlistItemsBody
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx, cancel := contextWithTimeout(r, 25*time.Second)
	defer cancel()

	if err := s.store.SetPlaylistOrder(ctx, viewerOf(r), id, body.ItemIDs); err != nil {
		s.writePlaylistError(w, "重排列表失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "playlistId": id})
}

// handleRemovePlaylistItem 从列表里移出一个条目。
func (s *Server) handleRemovePlaylistItem(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	itemID, err := strconv.ParseInt(r.PathValue("itemId"), 10, 64)
	if err != nil || itemID <= 0 {
		writeError(w, http.StatusBadRequest, "条目 id 非法")
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if err := s.store.RemovePlaylistItems(ctx, viewerOf(r), id, []int64{itemID}); err != nil {
		s.writePlaylistError(w, "移出列表失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "playlistId": id, "itemId": itemID})
}

// handlePlaylistNeighbors 某个条目在列表里的前后邻居 + 位次（播放器的「下一项」）。
//
// 播放器只要这三个数，不要整个列表：列表可能有几百条，而这一步是**每次切集都要跑的**。
func (s *Server) handlePlaylistNeighbors(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistIDFromPath(w, r)
	if !ok {
		return
	}
	itemID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("itemId")), 10, 64)
	if err != nil || itemID <= 0 {
		writeError(w, http.StatusBadRequest, "缺少 itemId")
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	v := viewerOf(r)
	p, err := s.store.GetPlaylistFor(ctx, v, id)
	if err != nil {
		s.writePlaylistError(w, "读取列表失败", err)
		return
	}
	prev, next, err := s.store.PlaylistNeighbors(ctx, v, id, itemID)
	if err != nil {
		s.writePlaylistError(w, "读取列表邻居失败", err)
		return
	}
	idx, err := s.store.PlaylistItemIndex(ctx, v, id, itemID)
	if err != nil {
		s.writePlaylistError(w, "读取列表位次失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"playlistId":   id,
		"playlistName": p.Name,
		"playlistKind": p.Kind,
		"itemId":       itemID,
		"index":        idx, // 从 1 开始；0 = 这个条目不在列表里
		"total":        p.ItemCount,
		"prevId":       prev,
		"nextId":       next,
	})
}
