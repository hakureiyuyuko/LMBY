package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/overlay"
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

	// 权限：只列这个人看得见的库（看不见的库不下发，界面上也不该有它的名字）
	v, err := s.viewerFor(ctx, r)
	if err != nil {
		s.serverError(w, "读取用户权限失败", err)
		return
	}
	libs, err := s.store.ListLibraries(ctx, v.SQLArgs())
	if err != nil {
		s.serverError(w, "读取媒体库失败", err)
		return
	}
	// 补上「上次 / 下次扫描」两个派生字段（扫描计划页要用）。
	s.fillScanSchedule(ctx, libs)

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
	s.fillScanScheduleOne(ctx, lib)

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

	// 只读库：顺带把叠加层的占用报给界面（它就是这个库「刮削产物的落地岛」）
	overlayStats := overlay.Stats{}
	if s.overlay != nil {
		overlayStats, _ = s.overlay.Stats(lib.ID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"library":  libraryResponse{Library: *lib, Counts: counts, ImageCount: images, ScanRunning: running},
		"lastScan": lastRun,
		"issues":   issues,
		"overlay":  overlayStats,
		"progress": progressOrNil(progress, running),
		"probe":    probeProgress,
	})
}

type updateLibraryRequest struct {
	Name *string `json:"name"`
	// Kind：库类型（movie | tv | homevideo | mixed）。改了只影响以后的扫描判定，
	// 已入库的条目不会因此改变自己已定死的 kind。
	Kind *string `json:"kind"`
	// Paths：整份替换根路径（不是增量）。
	Paths []string `json:"paths"`
	// ReadOnly：网盘 / 只读挂载的库打开它 —— LMBY 就不再往库目录里写，
	// 刮削产物（元数据快照 + 图片）落进数据目录的 overlay 层。
	ReadOnly *bool `json:"readonly"`
	// ScanIntervalMinutes：自动扫描间隔（分钟；0 = 不自动扫）。上限一周。
	ScanIntervalMinutes *int `json:"scanIntervalMinutes"`
}

// scanRequest 是触发扫描时的可选请求体。
type scanRequest struct {
	RefreshMetadata bool `json:"refreshMetadata"`
}

// handleUpdateLibrary 支持改名、改库类型、替换根路径与「只读」开关（可以一起改）。
//
// 一个 PATCH 干完所有编辑：界面上就是一张「编辑媒体库」表，多个字段一起提交时
// 不应该出现「改了一半」的状态（每项各自一个事务，但顺序固定、失败即返回）。
func (s *Server) handleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateLibraryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil && req.Kind == nil && req.ReadOnly == nil && req.Paths == nil &&
		req.ScanIntervalMinutes == nil {
		writeError(w, http.StatusBadRequest, "没有需要更新的字段")
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "名称不能为空")
			return
		}
		if err := s.store.UpdateLibrary(ctx, id, name); err != nil {
			s.notFoundOrError(w, err)
			return
		}
	}
	if req.Kind != nil {
		kind := strings.TrimSpace(*req.Kind)
		if !store.ValidLibraryKind(kind) {
			writeError(w, http.StatusBadRequest, "库类型只能是 movie / tv / homevideo / mixed")
			return
		}
		if err := s.store.UpdateLibraryKind(ctx, id, kind); err != nil {
			s.notFoundOrError(w, err)
			return
		}
	}
	if req.Paths != nil {
		paths := make([]string, 0, len(req.Paths))
		for _, p := range req.Paths {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			writeError(w, http.StatusBadRequest, "至少需要一个根路径（想清空请删库）")
			return
		}
		for _, p := range paths {
			if !filepath.IsAbs(p) {
				writeError(w, http.StatusBadRequest, "根路径要写绝对路径: "+p)
				return
			}
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				writeError(w, http.StatusBadRequest, "根路径不存在或不是目录: "+p)
				return
			}
		}
		if err := s.store.ReplaceLibraryPaths(ctx, id, paths); err != nil {
			s.notFoundOrError(w, err)
			return
		}
		s.log.Info("媒体库根路径已更新", "libraryId", id, "paths", len(paths))
	}
	if req.ReadOnly != nil {
		if err := s.store.SetLibraryReadOnly(ctx, id, *req.ReadOnly); err != nil {
			s.notFoundOrError(w, err)
			return
		}
		s.log.Info("媒体库只读开关已更新", "libraryId", id, "readonly", *req.ReadOnly)
	}
	if req.ScanIntervalMinutes != nil {
		m := *req.ScanIntervalMinutes
		// 范围检查在这里做（而不是只靠 store 里的那一层）：错了要回 400 加人话，
		// 而不是一个 500。
		if m < 0 || m > 10080 {
			writeError(w, http.StatusBadRequest, "扫描间隔要在 0 到 10080 分钟之间（0 = 不自动扫）")
			return
		}
		if err := s.store.SetLibraryScanInterval(ctx, id, m); err != nil {
			s.notFoundOrError(w, err)
			return
		}
		s.log.Info("媒体库扫描计划已更新", "libraryId", id, "intervalMinutes", m)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// fillScanTimes 按「最近扫描时间表」给一个库补上上次 / 下次（都是派生字段，不进库）。
//
// 下次 = 上次 + 间隔；从没扫过的库从**建库时间**起算 —— 刚配好计划应该很快跑第一次。
func fillScanTimes(lib *store.Library, last map[int64]time.Time) {
	if at, ok := last[lib.ID]; ok {
		t := at
		lib.LastScanAt = &t
	}
	if lib.ScanIntervalMinutes > 0 {
		base := lib.CreatedAt
		if lib.LastScanAt != nil {
			base = *lib.LastScanAt
		}
		next := base.Add(time.Duration(lib.ScanIntervalMinutes) * time.Minute)
		lib.NextScanAt = &next
	}
}

// fillScanSchedule 给一批库补上派生字段（只查一次库，不是每个库查一次）。
//
// 拿不到扫描记录时**不影响主流程**：界面少显示两列，比整个列表 500 好。
func (s *Server) fillScanSchedule(ctx context.Context, libs []store.Library) {
	last, err := s.store.LastScanTimes(ctx)
	if err != nil {
		s.log.Warn("读取最近扫描时间失败（界面上少显示上次/下次）", "err", err)
		return
	}
	for i := range libs {
		fillScanTimes(&libs[i], last)
	}
}

// fillScanScheduleOne 是单个库的版本（库详情用）。
func (s *Server) fillScanScheduleOne(ctx context.Context, lib *store.Library) {
	last, err := s.store.LastScanTimes(ctx)
	if err != nil {
		return
	}
	fillScanTimes(lib, last)
}

// handleClearLibraryOverlay 清空某个只读库的叠加层（刮削产物）。
//
// 只删数据目录里那个库的目录树，媒体目录与数据库一个字都不动；
// 清完之后下次取图/刮削会重新往里面写。
func (s *Server) handleClearLibraryOverlay(w http.ResponseWriter, r *http.Request) {
	if s.overlay == nil {
		s.serverError(w, "未接入叠加层", errors.New("overlay 未初始化"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	files, bytes, err := s.overlay.Clear(ctx, id)
	if err != nil {
		s.serverError(w, "清空叠加层失败", err)
		return
	}
	s.log.Info("叠加层已清空", "libraryId", id, "files", files, "bytes", bytes)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": files, "bytes": bytes})
}

// handleOverlayStats 汇总各库的叠加层占用（设置页的看板用）。
func (s *Server) handleOverlayStats(w http.ResponseWriter, r *http.Request) {
	if s.overlay == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"root": "", "libraries": []overlay.LibraryStats{}, "total": overlay.Stats{},
		})
		return
	}
	stats, err := s.overlay.AllStats()
	if err != nil {
		s.serverError(w, "统计叠加层失败", err)
		return
	}
	total := overlay.Stats{}
	for _, st := range stats {
		total.Files += st.Files
		total.Bytes += st.Bytes
	}

	// 把库名带上，看板上就不用再查一次库列表（库可能刚被删，那时只显示 id）。
	names := map[int64]string{}
	nameCtx, cancelNames := contextWithTimeout(r, 5*time.Second)
	defer cancelNames()
	if libs, err := s.store.ListLibraries(nameCtx, nil); err == nil {
		for _, l := range libs {
			names[l.ID] = l.Name
		}
	}
	type row struct {
		overlay.LibraryStats
		Name string `json:"name"`
	}
	out := make([]row, 0, len(stats))
	for _, st := range stats {
		out = append(out, row{LibraryStats: st, Name: names[st.LibraryID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root":      s.overlay.Root(),
		"libraries": out,
		"total":     total,
	})
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

	filter := store.ItemFilter{Kind: kind}
	// matchState 支持逗号分隔（人工匹配界面要同时看 review 与 failed）
	for _, st := range strings.Split(r.URL.Query().Get("matchState"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			filter.MatchState = append(filter.MatchState, st)
		}
	}

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
