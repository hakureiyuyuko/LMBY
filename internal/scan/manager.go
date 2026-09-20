// Package scan 管理扫描任务的运行与进度广播。
//
// 一个库同时只允许一个扫描在跑（重复触发直接拒绝），
// 进度通过内存广播给 SSE 订阅者，结束时把统计写回 scan_runs。
package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/scanner"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// ErrAlreadyRunning 表示该库已有扫描在跑。
var ErrAlreadyRunning = errors.New("scan: 该媒体库已有扫描任务在运行")

// running 是一次正在跑的扫描。
type running struct {
	runID   int64
	started time.Time
	cancel  context.CancelFunc
	last    scanner.Progress
}

// Manager 管理所有库的扫描任务。
type Manager struct {
	st  *store.Store
	log *slog.Logger

	mu      sync.Mutex
	current map[int64]*running

	subMu sync.Mutex
	subs  map[int]chan scanner.Progress
	next  int
}

// NewManager 创建扫描管理器。
func NewManager(st *store.Store, log *slog.Logger) *Manager {
	return &Manager{
		st:      st,
		log:     log,
		current: map[int64]*running{},
		subs:    map[int]chan scanner.Progress{},
	}
}

// Start 异步启动一次扫描，立即返回 scan_runs 的 id。
func (m *Manager) Start(libraryID int64, trigger string) (int64, error) {
	m.mu.Lock()
	if r, ok := m.current[libraryID]; ok {
		m.mu.Unlock()
		return r.runID, ErrAlreadyRunning
	}
	m.mu.Unlock()

	lib, err := m.st.GetLibrary(context.Background(), libraryID)
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	runID, err := m.st.StartScanRun(ctx, libraryID, trigger)
	if err != nil {
		cancel()
		return 0, err
	}

	m.mu.Lock()
	m.current[libraryID] = &running{runID: runID, started: time.Now(), cancel: cancel}
	m.mu.Unlock()

	go m.run(ctx, *lib, runID, trigger)
	return runID, nil
}

func (m *Manager) run(ctx context.Context, lib store.Library, runID int64, trigger string) {
	defer func() {
		m.mu.Lock()
		delete(m.current, lib.ID)
		m.mu.Unlock()
	}()
	defer func() {
		if rec := recover(); rec != nil {
			m.log.Error("扫描过程 panic", "libraryId", lib.ID, "err", rec)
			_ = m.st.FinishScanRun(context.Background(), runID, "failed", nil, fmt.Sprintf("内部错误: %v", rec))
		}
	}()

	// 新一轮扫描开始前清掉历史问题，避免新旧混在一起看不出重点
	if err := m.st.ClearScanIssues(ctx, lib.ID); err != nil {
		m.log.Warn("清理历史扫描问题失败", "err", err)
	}

	start := time.Now()
	stats, err := scanner.Scan(ctx, m.st, lib, scanner.Options{
		ScanRunID: runID,
		Trigger:   trigger,
		OnProgress: func(p scanner.Progress) {
			m.mu.Lock()
			if r, ok := m.current[lib.ID]; ok {
				r.last = p
			}
			m.mu.Unlock()
			m.broadcast(p)
		},
	})

	switch {
	case errors.Is(err, context.Canceled):
		m.log.Info("扫描已取消", "libraryId", lib.ID, "elapsed_ms", time.Since(start).Milliseconds())
		_ = m.st.FinishScanRun(context.Background(), runID, "canceled", statsMap(stats), "被用户取消")
	case err != nil:
		m.log.Error("扫描失败", "libraryId", lib.ID, "err", err)
		_ = m.st.FinishScanRun(context.Background(), runID, "failed", statsMap(stats), err.Error())
	default:
		m.log.Info("扫描完成",
			"libraryId", lib.ID,
			"videos", stats.Videos,
			"new", stats.NewFiles,
			"changed", stats.ChangedFiles,
			"moved", stats.MovedFiles,
			"deleted", stats.DeletedFiles,
			"items", stats.ItemsNew,
			"images", stats.Images,
			"issues", stats.Issues,
			"elapsed_ms", stats.ElapsedMS)
		_ = m.st.FinishScanRun(context.Background(), runID, "done", statsMap(stats), "")
	}
}

// Cancel 取消某个库正在跑的扫描。
func (m *Manager) Cancel(libraryID int64) bool {
	m.mu.Lock()
	r, ok := m.current[libraryID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	r.cancel()
	return true
}

// Status 返回某个库正在跑的扫描状态。
func (m *Manager) Status(libraryID int64) (scanner.Progress, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.current[libraryID]
	if !ok {
		return scanner.Progress{}, false
	}
	return r.last, true
}

// Running 列出正在扫描的库。
func (m *Manager) Running() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, 0, len(m.current))
	for id := range m.current {
		out = append(out, id)
	}
	return out
}

// ---------------------------------------------------------------- 广播

// Subscribe 订阅进度事件。返回的 cancel 必须被调用，否则会泄漏。
func (m *Manager) Subscribe() (<-chan scanner.Progress, func()) {
	ch := make(chan scanner.Progress, 32)

	m.subMu.Lock()
	id := m.next
	m.next++
	m.subs[id] = ch
	m.subMu.Unlock()

	cancel := func() {
		m.subMu.Lock()
		if c, ok := m.subs[id]; ok {
			delete(m.subs, id)
			close(c)
		}
		m.subMu.Unlock()
	}
	return ch, cancel
}

func (m *Manager) broadcast(p scanner.Progress) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- p:
		default:
			// 订阅者跟不上就丢事件：进度是「最新状态」语义，丢中间帧无所谓
		}
	}
}

func statsMap(s scanner.Stats) map[string]any {
	return map[string]any{
		"dirs":         s.Dirs,
		"videos":       s.Videos,
		"newFiles":     s.NewFiles,
		"changedFiles": s.ChangedFiles,
		"movedFiles":   s.MovedFiles,
		"deletedFiles": s.DeletedFiles,
		"unchanged":    s.Unchanged,
		"itemsNew":     s.ItemsNew,
		"seriesNew":    s.SeriesNew,
		"seasonsNew":   s.SeasonsNew,
		"episodesNew":  s.EpisodesNew,
		"moviesNew":    s.MoviesNew,
		"nfoRead":      s.NFORead,
		"images":       s.Images,
		"subtitles":    s.Subtitles,
		"unrecognized": s.Unrecognized,
		"issues":       s.Issues,
		"elapsedMs":    s.ElapsedMS,
	}
}
