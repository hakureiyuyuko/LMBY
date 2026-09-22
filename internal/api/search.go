package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 本文件是搜索（M2 起，M6 补齐「即时联想 + 结果分面」）。
//
// 四个接口各管一件事，前端按需并发拿：
//   - GET /api/v1/search         结果列表（分页）
//   - GET /api/v1/search/facets  结果分面（类型 / 媒体库 / 流派 / 人 的命中数）
//   - GET /api/v1/search/people  「人」这一档的结果列表（分页）
//   - GET /api/v1/search/suggest 即时联想（每组几条，只为下拉，不翻页）
//
// 为什么分面不塞进结果响应：分面只在「词或筛选变了」时需要重算，
// 翻页时重算是白算（4 个额外查询）；而且分面的统计规则（每一维要关掉
// 自己的筛选）与结果列表不是一回事，混在一个响应里会让两边互相牵制。

// searchFilter 是四个接口共用的筛选条件解析结果。
type searchFilter struct {
	query store.SearchQuery
	text  string
}

// parseSearchFilter 解析公共筛选参数：q / libraryId / kind / genre / personId。
//
// 返回 false 表示已经写过错误响应（参数非法或缺少实质性条件）。
// 关于「查询词可以为空」的规则：**只有当还给了别的筛选维度时才允许**。
// 空词 + 无筛选 = 「返回全部条目」，那是列表接口的活，这里宁可报 400
// 也不给一个会被误当搜索结果的响应。
func (s *Server) parseSearchFilter(w http.ResponseWriter, r *http.Request) (searchFilter, bool) {
	var f searchFilter
	q := r.URL.Query()

	f.text = strings.TrimSpace(q.Get("q"))

	kind := strings.TrimSpace(q.Get("kind"))
	if kind != "" && !store.ValidItemKind(kind) {
		writeError(w, http.StatusBadRequest, "kind 只能是 movie / series / season / episode / extra")
		return f, false
	}
	f.query.Kind = kind
	f.query.Genre = strings.TrimSpace(q.Get("genre"))

	if raw := strings.TrimSpace(q.Get("libraryId")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "libraryId 非法")
			return f, false
		}
		f.query.LibraryID = &id
	}
	if raw := strings.TrimSpace(q.Get("personId")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "personId 非法")
			return f, false
		}
		f.query.PersonID = &id
	}

	// 权限：把「这个人能看哪些库」塞进查询 —— 列表、总数、分面、联想全都会带上它。
	// 放在这里而不是每个 handler 各写一遍：搜索这一块的所有入口都过这个函数。
	v, err := s.viewerFor(r.Context(), r)
	if err != nil {
		s.serverError(w, "读取用户权限失败", err)
		return f, false
	}
	f.query.LibraryIDs = v.SQLArgs()

	if f.text == "" && !f.query.HasFilter() {
		writeError(w, http.StatusBadRequest, "缺少查询词 q（或者给一个筛选条件：libraryId / kind / genre / personId）")
		return f, false
	}
	f.query.Text = f.text
	return f, true
}

// searchFilterEcho 把生效的筛选条件回显给界面（界面要画「已筛选」的标签，
// 验收脚本也要能读回来确认参数真的生效了）。
func searchFilterEcho(f store.SearchQuery) map[string]any {
	return map[string]any{
		"kind":      f.Kind,
		"genre":     f.Genre,
		"libraryId": f.LibraryID,
		"personId":  f.PersonID,
	}
}

// handleSearch 搜索条目（标题 / 原始标题，中文二元组 + trigram 错字容忍）。
//
// 参数：q（可空，见 parseSearchFilter）、libraryId、kind、genre、personId、limit、offset。
// 取舍：limit 上限 100 —— 搜索结果是给人翻的，不是用来导数据的。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	f, ok := s.parseSearchFilter(w, r)
	if !ok {
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

	f.query.Limit = limit
	f.query.Offset = offset

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	hits, total, err := s.store.SearchItems(ctx, f.query)
	if err != nil {
		s.serverError(w, "搜索失败", err)
		return
	}
	if hits == nil {
		hits = []store.SearchHit{}
	}
	s.log.Debug("搜索", "q", f.text, "kind", f.query.Kind, "hits", total, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   f.text,
		"filters": searchFilterEcho(f.query),
		"items":   hits,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// handleSearchFacets 返回结果分面（类型 / 媒体库 / 流派 / 人）。
//
// 参数与 handleSearch 完全一样（分面必须与结果用同一组筛选，否则数字对不上），
// 只是不分页。响应里的 facets.total 就是同一组筛选下条目的命中总数。
func (s *Server) handleSearchFacets(w http.ResponseWriter, r *http.Request) {
	f, ok := s.parseSearchFilter(w, r)
	if !ok {
		return
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	facets, err := s.store.SearchFacets(ctx, f.query)
	if err != nil {
		s.serverError(w, "统计搜索分面失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   f.text,
		"filters": searchFilterEcho(f.query),
		"facets":  facets,
	})
}

// handleSearchPeople 「人」这一档的结果列表。
//
// 人的命中只能由查询词决定（人没有库与流派归属），所以这里**不接受**库/流派筛选，
// 只认 q + 分页；kind 之类的参数给了也不看（免得界面以为筛了其实没筛）。
func (s *Server) handleSearchPeople(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	if text == "" {
		writeError(w, http.StatusBadRequest, "缺少查询词 q")
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

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	people, total, err := s.store.SearchPeople(ctx, text, limit, offset)
	if err != nil {
		s.serverError(w, "搜索演职员失败", err)
		return
	}
	if people == nil {
		people = []store.PersonHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":  text,
		"people": people,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// handleSearchSuggest 即时联想：给查询词返回几个最像的作品与人。
//
// 联想**不看当前筛选**（永远给全局最像的几个）：用户还在敲字的时候，
// 拿库/流派筛选去套只会让下拉越来越空；筛选是敲完回车之后的事。
// 这也是为什么它不接收 libraryId 之类的参数。
func (s *Server) handleSearchSuggest(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	if text == "" {
		writeError(w, http.StatusBadRequest, "缺少查询词 q")
		return
	}

	limit := queryInt(r, "limit", 8)
	if limit < 1 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	// 联想也要按可见库过滤（不然敲两个字就能探出私密库里的片名）
	v, verr := s.viewerFor(ctx, r)
	if verr != nil {
		s.serverError(w, "读取用户权限失败", verr)
		return
	}
	sug, err := s.store.SearchSuggest(ctx, text, limit, v.SQLArgs())
	if err != nil {
		s.serverError(w, "联想失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":  text,
		"items":  sug.Items,
		"people": sug.People,
		"limit":  limit,
	})
}
