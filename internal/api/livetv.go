package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/livetv"
	"github.com/hakureiyuyuko/lmby/internal/livetvsync"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 本文件是直播电视（M5）的接口：源管理、频道列表与管理、导入/刷新、导出。
//
// 分工：
//   - 播放相关的接口在 livetv_play.go（S2 的直播会话）；
//   - 解析播放列表在 internal/livetv，落库与增量更新在 internal/store/livetv.go。

// ---------------------------------------------------------------- 视图

// tvChannelView 是频道的对外形状。
//
// 两条刻意的取舍：
//   - **不回显 headers 内容**，只说「有没有」。订阅源里常常塞 User-Agent/Referer，
//     有的甚至带 token，界面上没必要看见（与 TMDB 密钥的处理一致）；
//   - url 照常回显：它就是频道本身的地址，用户要复制到外部播放器里用。
type tvChannelView struct {
	ID         int64  `json:"id"`
	SourceID   *int64 `json:"sourceId,omitempty"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Kind       string `json:"kind"`
	Group      string `json:"group"`
	Logo       string `json:"logo"`
	TvgID      string `json:"tvgId,omitempty"`
	HasHeaders bool   `json:"hasHeaders"`
	SortOrder  int    `json:"sortOrder"`
	Disabled   bool   `json:"disabled"`
	Favorite   bool   `json:"favorite"`
	Probe      string `json:"probe,omitempty"`
	ProbeOK    *bool  `json:"probeOk,omitempty"`
	ProbeAt    string `json:"probeAt,omitempty"`
	// VideoCodec/VideoHeight：探测记下的源视频信息（空/0 = 未知）。
	// 界面/脚本靠它判断这一路会不会转码（浏览器解不开 HEVC 时服务端会转码）。
	VideoCodec  string `json:"videoCodec,omitempty"`
	VideoHeight int    `json:"videoHeight,omitempty"`
}

func viewTVChannel(c store.TVChannel) tvChannelView {
	v := tvChannelView{
		ID:          c.ID,
		SourceID:    c.SourceID,
		Name:        c.Name,
		URL:         c.URL,
		Kind:        livetv.KindLabel(c.URL),
		Group:       c.Group,
		Logo:        c.Logo,
		TvgID:       c.TvgID,
		HasHeaders:  strings.TrimSpace(c.Headers) != "",
		SortOrder:   c.SortOrder,
		Disabled:    c.Disabled,
		Favorite:    c.Favorite,
		Probe:       c.Probe,
		ProbeOK:     c.ProbeOK,
		VideoCodec:  c.VideoCodec,
		VideoHeight: c.VideoHeight,
	}
	if c.ProbeAt != nil {
		v.ProbeAt = c.ProbeAt.Format(time.RFC3339)
	}
	return v
}

// tvSourceView 是直播源的对外形状。
type tvSourceView struct {
	ID                     int64  `json:"id"`
	Name                   string `json:"name"`
	Kind                   string `json:"kind"`
	URL                    string `json:"url,omitempty"`
	Enabled                bool   `json:"enabled"`
	RefreshIntervalMinutes int    `json:"refreshIntervalMinutes"`
	LastRefreshAt          string `json:"lastRefreshAt,omitempty"`
	LastStatus             string `json:"lastStatus,omitempty"`
	LastChannelCount       int    `json:"lastChannelCount"`
	ChannelCount           int    `json:"channelCount"` // 当前挂在这个源下的频道数
}

func viewTVSource(src store.TVSource, channelCount int) tvSourceView {
	v := tvSourceView{
		ID:                     src.ID,
		Name:                   src.Name,
		Kind:                   src.Kind,
		URL:                    src.URL,
		Enabled:                src.Enabled,
		RefreshIntervalMinutes: src.RefreshIntervalMinutes,
		LastStatus:             src.LastStatus,
		LastChannelCount:       src.LastChannelCount,
		ChannelCount:           channelCount,
	}
	if src.LastRefreshAt != nil {
		v.LastRefreshAt = src.LastRefreshAt.Format(time.RFC3339)
	}
	return v
}

// ---------------------------------------------------------------- 频道列表

// handleListTVChannels 列出频道（支持按分组/名字过滤、只看启用/只看收藏）。
func (s *Server) handleListTVChannels(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	// 权限：被限制为「不允许直播」的账号连列表都不给（前端也会把入口藏起来，
	// 这里只是兜底 —— 界面上不该出现点了就 403 的东西，但接口不能靠界面自觉）
	v, verr := s.viewerFor(r.Context(), r)
	if verr != nil {
		s.serverError(w, "读取用户权限失败", verr)
		return
	}
	if !v.AllowLiveTV {
		writeError(w, http.StatusForbidden, "你的账号被限制为不允许观看直播")
		return
	}

	q := r.URL.Query()
	query := store.TVChannelQuery{
		UserID:        a.User.ID,
		Search:        strings.TrimSpace(q.Get("q")),
		Group:         strings.TrimSpace(q.Get("group")),
		OnlyEnabled:   q.Get("enabled") == "1" || q.Get("enabled") == "true",
		OnlyFavorites: q.Get("favorites") == "1" || q.Get("favorites") == "true",
		// probe=pending|ok|failed：按探测结果筛（失效源标记的入口）
		Probe: strings.TrimSpace(q.Get("probe")),
		// hide_failed=1：前台只留「能用能看的」—— 探测过且不通的不出现
		HideFailed: q.Get("hide_failed") == "1" || q.Get("hide_failed") == "true",
	}
	channels, err := s.store.ListTVChannels(ctx, query)
	if err != nil {
		s.serverError(w, "读取频道列表失败", err)
		return
	}
	groups, err := s.store.ListTVGroups(ctx)
	if err != nil {
		s.serverError(w, "读取直播分组失败", err)
		return
	}
	// 统计与列表用同一套过滤（否则「共 N 台」和列出来的条数会对不上）
	total, enabled, err := s.store.CountTVChannels(ctx, query)
	if err != nil {
		s.serverError(w, "统计频道数失败", err)
		return
	}

	out := make([]tvChannelView, 0, len(channels))
	for _, c := range channels {
		out = append(out, viewTVChannel(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": out,
		"groups":   groups,
		"total":    total,
		"enabled":  enabled,
	})
}

// handleGetTVChannel 取单个频道。
//
// 界面（改一条、看一条的探测结果）与验收脚本都要用；顺带修掉一个旧问题：
// 验收脚本还原收藏状态时 GET 这个地址拿到的一直是 404，于是它每次都
// 多切一次收藏 —— 一个「验收脚本把用户现场搞脏」的缺口。
func (s *Server) handleGetTVChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	c, err := s.store.GetTVChannel(ctx, a.User.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "频道不存在")
		return
	}
	if err != nil {
		s.serverError(w, "读取频道失败", err)
		return
	}
	writeJSON(w, http.StatusOK, viewTVChannel(*c))
}

// tvChannelPatchRequest 是频道手动编辑的请求体（nil = 不改）。
type tvChannelPatchRequest struct {
	Name      *string `json:"name"`
	Group     *string `json:"group"`
	Logo      *string `json:"logo"`
	SortOrder *int    `json:"sortOrder"`
	Disabled  *bool   `json:"disabled"`
}

// handleUpdateTVChannel 手动编辑频道（改名/分组/logo/排序/启用状态）。
func (s *Server) handleUpdateTVChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req tvChannelPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	err := s.store.UpdateTVChannel(ctx, id, store.TVChannelPatch{
		Name:      req.Name,
		Group:     req.Group,
		Logo:      req.Logo,
		SortOrder: req.SortOrder,
		Disabled:  req.Disabled,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "频道不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	a := currentAuth(r)
	c, err := s.store.GetTVChannel(ctx, a.User.ID, id)
	if err != nil {
		s.serverError(w, "读取频道失败", err)
		return
	}
	writeJSON(w, http.StatusOK, viewTVChannel(*c))
}

// handleToggleTVFavorite 切换收藏。
func (s *Server) handleToggleTVFavorite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	// 先确认频道存在：收藏一个不存在的 id 会留下悬空行（外键能挡住，但报错不够清楚）
	if _, err := s.store.GetTVChannel(ctx, a.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "频道不存在")
			return
		}
		s.serverError(w, "读取频道失败", err)
		return
	}
	fav, err := s.store.ToggleTVFavorite(ctx, a.User.ID, id)
	if err != nil {
		s.serverError(w, "更新收藏失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "favorite": fav})
}

// ---------------------------------------------------------------- 源：新建/刷新/删除

// handleListTVSources 列出直播源（附带每个源当前挂着的频道数）。
func (s *Server) handleListTVSources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	sources, err := s.store.ListTVSources(ctx)
	if err != nil {
		s.serverError(w, "读取直播源失败", err)
		return
	}
	counts, err := s.store.CountTVChannelsBySource(ctx)
	if err != nil {
		s.serverError(w, "统计直播源频道数失败", err)
		return
	}
	out := make([]tvSourceView, 0, len(sources))
	for _, src := range sources {
		out = append(out, viewTVSource(src, counts[src.ID]))
	}
	total, enabled, err := s.store.CountTVChannels(ctx, store.TVChannelQuery{})
	if err != nil {
		s.serverError(w, "统计频道数失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sources": out,
		"total":   total,
		"enabled": enabled,
	})
}

// createTVSourceRequest 是新建直播源/导入播放列表的请求体。
//
// kind 决定走哪条路：
//   - paste：content 是粘贴的播放列表文本；
//   - file：content 是上传文件的文本内容（前端读成文本再发上来，省一套 multipart）；
//   - url：url 是订阅地址，服务端去拉。
type createTVSourceRequest struct {
	Name                   string `json:"name"`
	Kind                   string `json:"kind"`
	URL                    string `json:"url"`
	Content                string `json:"content"`
	RefreshIntervalMinutes *int   `json:"refreshIntervalMinutes"`
	Enabled                *bool  `json:"enabled"`
}

// handleCreateTVSource 新建直播源并立刻导入一次。
func (s *Server) handleCreateTVSource(w http.ResponseWriter, r *http.Request) {
	var req createTVSourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Kind = strings.TrimSpace(strings.ToLower(req.Kind))
	switch req.Kind {
	case "paste", "file", "url":
	default:
		writeError(w, http.StatusBadRequest, "kind 只能是 paste / file / url")
		return
	}
	if req.Kind == "url" && strings.TrimSpace(req.URL) == "" {
		writeError(w, http.StatusBadRequest, "订阅源必须填地址")
		return
	}
	if req.Kind != "url" && strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "没有可导入的内容")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		if req.Kind == "url" {
			name = strings.TrimSpace(req.URL)
		} else {
			name = "手动导入"
		}
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	// 先拉取/解析，成功后才建源：内容坏了就不该在源列表里留一个没频道的空壳
	entries, err := s.live.Resolve(ctx, req.URL, req.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	src, err := s.store.CreateTVSource(ctx, store.TVSourceInput{
		Name:                   name,
		Kind:                   req.Kind,
		URL:                    req.URL,
		Enabled:                req.Enabled,
		RefreshIntervalMinutes: req.RefreshIntervalMinutes,
	})
	if err != nil {
		s.serverError(w, "新建直播源失败", err)
		return
	}

	res, err := s.live.Import(ctx, src, entries)
	if err != nil {
		// 源已经建好了（用户能看到失败原因），这里把导入失败如实回给他
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, enabled, _ := s.store.CountTVChannels(ctx, store.TVChannelQuery{})
	s.log.Info("导入直播源", "source", src.ID, "name", src.Name, "kind", src.Kind,
		"added", res.Added, "updated", res.Updated, "removed", res.Removed,
		"total", res.Total, "username", usernameOf(r))
	writeJSON(w, http.StatusCreated, map[string]any{
		"source":  viewTVSource(*src, res.Added+res.Updated+res.Kept),
		"import":  res,
		"total":   total,
		"enabled": enabled,
	})
}

// handleRefreshTVSource 重新拉取订阅源并导入（粘贴/上传的源没有可重拉的内容）。
func (s *Server) handleRefreshTVSource(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()

	src, err := s.store.GetTVSource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "直播源不存在")
		return
	}
	if err != nil {
		s.serverError(w, "读取直播源失败", err)
		return
	}
	if src.Kind != "url" || strings.TrimSpace(src.URL) == "" {
		writeError(w, http.StatusBadRequest, livetvsync.ErrNoSourceURL.Error())
		return
	}

	res, err := s.live.Refresh(ctx, src, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	updated, err := s.store.GetTVSource(ctx, id)
	if err != nil {
		s.serverError(w, "读取直播源失败", err)
		return
	}
	counts, _ := s.store.CountTVChannelsBySource(ctx)
	total, enabled, _ := s.store.CountTVChannels(ctx, store.TVChannelQuery{})
	s.log.Info("刷新直播源", "source", id, "added", res.Added, "updated", res.Updated,
		"removed", res.Removed, "total", res.Total, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"source":  viewTVSource(*updated, counts[id]),
		"import":  res,
		"total":   total,
		"enabled": enabled,
	})
}

// handleUpdateTVSource 改直播源的名字/订阅地址/自动刷新间隔/启用状态。
func (s *Server) handleUpdateTVSource(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name                   *string `json:"name"`
		URL                    *string `json:"url"`
		Enabled                *bool   `json:"enabled"`
		RefreshIntervalMinutes *int    `json:"refreshIntervalMinutes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	in := store.TVSourceInput{Enabled: req.Enabled, RefreshIntervalMinutes: req.RefreshIntervalMinutes}
	if req.Name != nil {
		in.Name = *req.Name
	}
	if req.URL != nil {
		in.URL = *req.URL
	}
	src, err := s.store.UpdateTVSource(ctx, id, in)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "直播源不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	counts, _ := s.store.CountTVChannelsBySource(ctx)
	writeJSON(w, http.StatusOK, viewTVSource(*src, counts[id]))
}

// handleDeleteTVSource 删除直播源（频道保留，只是不再归它管）。
func (s *Server) handleDeleteTVSource(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	if err := s.store.DeleteTVSource(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "直播源不存在")
			return
		}
		s.serverError(w, "删除直播源失败", err)
		return
	}
	s.log.Info("删除直播源", "source", id, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleExportTVM3U 把启用的频道导出成 m3u（可以拿去喂外部播放器）。
func (s *Server) handleExportTVM3U(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	includeDisabled := r.URL.Query().Get("all") == "1"
	entries, err := s.store.ExportTVEntries(ctx, includeDisabled)
	if err != nil {
		s.serverError(w, "导出频道失败", err)
		return
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lmby-livetv.m3u"`)
	_, _ = w.Write([]byte(livetv.Render(entries)))
}
