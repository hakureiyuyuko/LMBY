package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/scan"
	"github.com/hakureiyuyuko/lmby/internal/scanner"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// ---------------------------------------------------------------- 媒体库

type libraryResponse struct {
	store.Library
	Counts      map[string]int64 `json:"counts"`
	ImageCount  int64            `json:"imageCount"`
	ScanRunning bool             `json:"scanRunning"`
}

// handleListLibraries 列出全部媒体库及条目统计。
func (s *Server) handleListLibraries(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	libs, err := s.store.ListLibraries(ctx)
	if err != nil {
		s.serverError(w, "读取媒体库失败", err)
		return
	}

	out := make([]libraryResponse, 0, len(libs))
	for _, lib := range libs {
		counts, err := s.store.CountItemsByKind(ctx, lib.ID)
		if err != nil {
			s.serverError(w, "统计条目失败", err)
			return
		}
		images, err := s.store.CountImages(ctx, lib.ID)
		if err != nil {
			s.serverError(w, "统计图片失败", err)
			return
		}
		out = append(out, libraryResponse{Library: lib, Counts: counts, ImageCount: images})
	}
	for i := range out {
		if _, running := s.scans.Status(out[i].ID); running {
			out[i].ScanRunning = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

type createLibraryRequest struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	Paths []string `json:"paths"`
}

// handleCreateLibrary 新建媒体库。
func (s *Server) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var req createLibraryRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "请填写媒体库名称")
		return
	}
	if !store.ValidLibraryKind(req.Kind) {
		writeError(w, http.StatusBadRequest, "库类型只能是 movie / tv / homevideo / mixed")
		return
	}

	paths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		writeError(w, http.StatusBadRequest, "至少需要一个根路径")
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	// 先做一次可达性检查：路径不存在是最常见的配置错误，
	// 与其等到扫描时才发现，不如建库时就明确报出来。
	for _, p := range paths {
		info, err := statPath(p)
		if err != nil {
			writeError(w, http.StatusBadRequest, "根路径不可访问："+p+"（"+err.Error()+"）")
			return
		}
		if !info.IsDir() {
			writeError(w, http.StatusBadRequest, "根路径不是目录："+p)
			return
		}
	}

	lib, err := s.store.CreateLibrary(ctx, name, req.Kind, nil, paths)
	if err != nil {
		s.serverError(w, "创建媒体库失败", err)
		return
	}
	s.log.Info("已创建媒体库", "id", lib.ID, "name", lib.Name, "kind", lib.Kind, "paths", paths)
	writeJSON(w, http.StatusCreated, libraryResponse{Library: *lib, Counts: map[string]int64{}})
}

// handleGetLibrary 媒体库详情（含最近一次扫描与问题列表）。
func (s *Server) handleGetLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	lib, err := s.store.GetLibrary(ctx, id)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}
	counts, err := s.store.CountItemsByKind(ctx, lib.ID)
	if err != nil {
		s.serverError(w, "统计条目失败", err)
		return
	}
	images, _ := s.store.CountImages(ctx, lib.ID)

	var lastRun *store.ScanRun
	if run, err := s.store.LatestScanRun(ctx, lib.ID); err == nil {
		lastRun = run
	}
	issues, err := s.store.ListScanIssues(ctx, lib.ID, 100)
	if err != nil {
		s.serverError(w, "读取扫描问题失败", err)
		return
	}
	if issues == nil {
		issues = []store.ScanIssue{}
	}

	progress, running := s.scans.Status(lib.ID)

	probeProgress, err := s.store.ProbeProgressOf(ctx, lib.ID)
	if err != nil {
		s.serverError(w, "统计探测进度失败", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"library":  libraryResponse{Library: *lib, Counts: counts, ImageCount: images, ScanRunning: running},
		"lastScan": lastRun,
		"issues":   issues,
		"progress": progressOrNil(progress, running),
		"probe":    probeProgress,
	})
}

type updateLibraryRequest struct {
	Name *string `json:"name"`
}

// scanRequest 是触发扫描时的可选请求体。
type scanRequest struct {
	RefreshMetadata bool `json:"refreshMetadata"`
}

// handleUpdateLibrary 目前只支持改名。
func (s *Server) handleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateLibraryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, http.StatusBadRequest, "没有需要更新的字段")
		return
	}
	name := strings.TrimSpace(*req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "名称不能为空")
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	if err := s.store.UpdateLibrary(ctx, id, name); err != nil {
		s.notFoundOrError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteLibrary 删除媒体库（条目与文件级联删除）。
func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	// 先停掉正在跑的扫描：否则它会继续往一个已删除的库里写数据，
	// 而且 id 被新库复用时还会出现「新库显示正在扫描」这种怪现象。
	if s.scans.Cancel(id) {
		s.log.Info("删除媒体库时取消了正在运行的扫描", "libraryId", id)
	}

	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	if err := s.store.DeleteLibrary(ctx, id); err != nil {
		s.notFoundOrError(w, err)
		return
	}
	s.log.Info("已删除媒体库", "id", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- 扫描

// handleStartScan 触发一次扫描。
//
// 可选请求体：{"refreshMetadata": true} —— 文件没变也重读同目录的 nfo。
// 用户手改了 nfo（nfo 在本项目里是权威元数据）之后靠这个让它生效，
// 而不必去 touch 媒体文件（网络盘上那样会连带触发重新探测）。
func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req scanRequest
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	runID, err := s.scans.Start(id, scan.StartOptions{
		Trigger:         "manual",
		RefreshMetadata: req.RefreshMetadata,
	})
	if err == nil {
		s.log.Info("已启动扫描", "libraryId", id, "scanRunId", runID, "refreshMetadata", req.RefreshMetadata)
	}
	if errors.Is(err, scan.ErrAlreadyRunning) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "该媒体库已有扫描任务在运行", "scanRunId": runID,
		})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "媒体库不存在")
		return
	}
	if err != nil {
		s.serverError(w, "启动扫描失败", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"scanRunId": runID})
}

// handleCancelScan 取消扫描。
func (s *Server) handleCancelScan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.scans.Cancel(id) {
		writeError(w, http.StatusConflict, "该媒体库没有正在运行的扫描")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleScanStatus 查询扫描状态与历史。
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	progress, running := s.scans.Status(id)

	var lastRun *store.ScanRun
	if run, err := s.store.LatestScanRun(ctx, id); err == nil {
		lastRun = run
	}
	issues, err := s.store.ListScanIssues(ctx, id, 200)
	if err != nil {
		s.serverError(w, "读取扫描问题失败", err)
		return
	}
	if issues == nil {
		issues = []store.ScanIssue{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"running":  running,
		"progress": progressOrNil(progress, running),
		"lastScan": lastRun,
		"issues":   issues,
	})
}

// ---------------------------------------------------------------- 条目

// handleListItems 分页列出条目。
func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	kind := r.URL.Query().Get("kind")
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)

	items, err := s.store.ListItems(ctx, id, kind, limit, offset)
	if err != nil {
		s.serverError(w, "读取条目失败", err)
		return
	}
	total, err := s.store.CountItems(ctx, id, kind)
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

// ---------------------------------------------------------------- 工具

// notFoundOrError 把 store 的 ErrNotFound 翻成 404，其余当 500。
func (s *Server) notFoundOrError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "媒体库不存在")
		return
	}
	s.serverError(w, "服务器内部错误", err)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "路径参数 id 非法")
		return 0, false
	}
	return id, true
}

func queryInt(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func progressOrNil(p scanner.Progress, running bool) any {
	if !running {
		return nil
	}
	return p
}
