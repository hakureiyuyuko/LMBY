package api

import (
	"net/http"
	"time"
)

// scrapeRequest 是触发刮削的请求体。
type scrapeRequest struct {
	// Force 为真时连已匹配过的条目也重刮。
	Force bool `json:"force"`
	// Kind 限定 movie / series，空表示两者都要。
	Kind string `json:"kind"`
}

// handleEnqueueScrapes 把某个库里需要刮削的条目批量入队。
//
// 与探测接口同样的定位：扫描不会自动刮（刮削要花 API 配额，得由人决定何时开跑），
// 这个接口就是那个「一键刮削」的入口。任务由后台 worker 执行，
// 进度可以在 GET /api/v1/tasks 与 scrape 状态里看到。
func (s *Server) handleEnqueueScrapes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.scrapeConfigured() {
		writeError(w, http.StatusConflict, "未配置元数据源（config.toml 的 [tmdb] 段或 LMBY_TMDB_READ_TOKEN）")
		return
	}

	var req scrapeRequest
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	if req.Kind != "" && req.Kind != "movie" && req.Kind != "series" {
		writeError(w, http.StatusBadRequest, "kind 只能是 movie 或 series")
		return
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	if _, err := s.store.GetLibrary(ctx, id); err != nil {
		s.notFoundOrError(w, err)
		return
	}

	enqueued, err := s.store.EnqueueScrapesForLibrary(ctx, id, req.Kind, req.Force)
	if err != nil {
		s.serverError(w, "入队刮削任务失败", err)
		return
	}
	progress, err := s.store.ScrapeProgressOf(ctx, id)
	if err != nil {
		s.serverError(w, "统计刮削进度失败", err)
		return
	}

	s.log.Info("已批量入队刮削任务", "libraryId", id, "enqueued", enqueued, "force", req.Force)
	writeJSON(w, http.StatusOK, map[string]any{
		"enqueued": enqueued,
		"scrape":   progress,
	})
}

// handleScrapeStatus 返回某个库的刮削进度。
func (s *Server) handleScrapeStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if _, err := s.store.GetLibrary(ctx, id); err != nil {
		s.notFoundOrError(w, err)
		return
	}
	progress, err := s.store.ScrapeProgressOf(ctx, id)
	if err != nil {
		s.serverError(w, "统计刮削进度失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": s.scrapeConfigured(),
		"scrape":     progress,
	})
}

// handleResetFailedScrapes 把刮削失败的条目重置为待刮并入队。
//
// 典型场景：TMDB 短暂不可用或凭据配错，导致一批条目判失败，修好后一键重来。
func (s *Server) handleResetFailedScrapes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	if _, err := s.store.GetLibrary(ctx, id); err != nil {
		s.notFoundOrError(w, err)
		return
	}

	reset, err := s.store.ResetFailedScrapes(ctx, id)
	if err != nil {
		s.serverError(w, "重置失败的刮削失败", err)
		return
	}
	enqueued, err := s.store.EnqueueScrapesForLibrary(ctx, id, "", false)
	if err != nil {
		s.serverError(w, "入队刮削任务失败", err)
		return
	}
	progress, err := s.store.ScrapeProgressOf(ctx, id)
	if err != nil {
		s.serverError(w, "统计刮削进度失败", err)
		return
	}

	s.log.Info("已重置失败的刮削", "libraryId", id, "reset", reset, "enqueued", enqueued)
	writeJSON(w, http.StatusOK, map[string]any{
		"reset":    reset,
		"enqueued": enqueued,
		"scrape":   progress,
	})
}

// scrapeConfigured 判断是否配了可用的元数据源（没配就明确回 409，而不是让任务白排队）。
func (s *Server) scrapeConfigured() bool {
	return s.cfg.TMDB.ReadToken != "" || s.cfg.TMDB.APIKey != ""
}
