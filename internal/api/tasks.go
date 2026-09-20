package api

import (
	"net/http"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// handleTaskStats 返回后台队列水位与最近任务，供界面展示与排查。
func (s *Server) handleTaskStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	stats, err := s.store.TaskStatsOf(ctx)
	if err != nil {
		s.serverError(w, "统计任务队列失败", err)
		return
	}
	byKind, err := s.store.TaskStatsByKind(ctx)
	if err != nil {
		s.serverError(w, "统计任务类型失败", err)
		return
	}

	state := r.URL.Query().Get("state")
	recent, err := s.store.ListTasks(ctx, state, 30)
	if err != nil {
		s.serverError(w, "读取任务列表失败", err)
		return
	}
	if recent == nil {
		recent = []store.Task{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stats":  stats,
		"byKind": byKind,
		"recent": recent,
	})
}

// handleEnqueueProbes 把某个库里还没探测的文件批量入队。
//
// 平时扫描结束会自动入队；这个接口是给「探测失败修好后重跑」
// 「旧版本升级上来补探测」这类情况用的。
func (s *Server) handleEnqueueProbes(w http.ResponseWriter, r *http.Request) {
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

	enqueued, err := s.store.EnqueueProbesForLibrary(ctx, id)
	if err != nil {
		s.serverError(w, "入队探测任务失败", err)
		return
	}
	progress, err := s.store.ProbeProgressOf(ctx, id)
	if err != nil {
		s.serverError(w, "统计探测进度失败", err)
		return
	}

	s.log.Info("已批量入队探测任务", "libraryId", id, "enqueued", enqueued)
	writeJSON(w, http.StatusOK, map[string]any{
		"enqueued": enqueued,
		"probe":    progress,
	})
}

// handleResetFailedProbes 把探测失败的文件重置为待探测并入队。
//
// 典型场景：网盘掉线导致一批文件探测失败，恢复后一键重试。
func (s *Server) handleResetFailedProbes(w http.ResponseWriter, r *http.Request) {
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

	reset, err := s.store.ResetFailedProbes(ctx, id)
	if err != nil {
		s.serverError(w, "重置失败的探测失败", err)
		return
	}
	enqueued, err := s.store.EnqueueProbesForLibrary(ctx, id)
	if err != nil {
		s.serverError(w, "入队探测任务失败", err)
		return
	}
	progress, err := s.store.ProbeProgressOf(ctx, id)
	if err != nil {
		s.serverError(w, "统计探测进度失败", err)
		return
	}

	s.log.Info("已重置失败的探测", "libraryId", id, "reset", reset, "enqueued", enqueued)
	writeJSON(w, http.StatusOK, map[string]any{
		"reset":    reset,
		"enqueued": enqueued,
		"probe":    progress,
	})
}
