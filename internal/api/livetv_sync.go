package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/livetvsync"
)

// 本文件是直播电视（M5）**运行部分**的接口：
//
//	订阅源定时刷新（真正的调度在 cmd/lmby 的协程里，这里只暴露一次刷新的入口）
//	频道探测（失效源标记）：POST 起一次，GET 查进度
//
// 刷新与探测的实现都在 internal/livetvsync（那里才能同时用 store 与 livetv）。

// RefreshLiveSources 刷新订阅源（main 里的定时任务调用）。
//
// onlyDue=true 时只刷到点的（间隔由每个源自己的 refresh_interval_minutes 决定），
// false 时刷全部启用的订阅源。单个源失败不会中断其它源：
// 结果里逐个源都带 Err，返回的 error 只表示「连该刷哪些源都查不出来」。
func (s *Server) RefreshLiveSources(ctx context.Context, onlyDue bool) (livetvsync.RefreshSummary, error) {
	return s.live.RefreshSources(ctx, onlyDue)
}

// ---------------------------------------------------------------- 频道探测

// tvProbeSelection 是「这一次探哪些频道」的选择，同时也是它的对外形状。
//
// 三个条件都是可选的：不带就是全探（启用中的频道）。
type tvProbeSelection struct {
	OnlyUnknown bool    `json:"onlyUnknown"`
	Group       string  `json:"group,omitempty"`
	ChannelIDs  []int64 `json:"channelIds,omitempty"`
}

func (sel tvProbeSelection) toOptions() livetvsync.ProbeOptions {
	return livetvsync.ProbeOptions{
		OnlyUnknown: sel.OnlyUnknown,
		Group:       sel.Group,
		ChannelIDs:  sel.ChannelIDs,
	}
}

// tvProbeState 是一次频道探测的运行状态。
//
// 只有「在不在跑」是内存态：进程重启后不会再显示在跑（那一次确实中断了），
// 但**进度不会丢** —— 探过的结果已经写进库，没探过的 probe_at 还是空，
// 再点一次探测就接着探剩下的。
type tvProbeState struct {
	mu        sync.Mutex
	running   bool
	startedAt time.Time
	sel       tvProbeSelection
	progress  livetvsync.ProbeProgress
}

// tvProbeStatus 是探测状态的对外形状。
//
// progress 是**这一次**的进度（内存），stats 是**库里**的总账
// （总/未探/通/不通）—— 两个都给出，是因为「这次探了 20 条」与
// 「库里还有 129 条没探过」是两件事。
type tvProbeStatus struct {
	Running        bool                     `json:"running"`
	StartedAt      string                   `json:"startedAt,omitempty"`
	ElapsedSeconds int                      `json:"elapsedSeconds"`
	Selection      tvProbeSelection         `json:"selection"`
	Progress       livetvsync.ProbeProgress `json:"progress"`
	Stats          tvProbeStatsView         `json:"stats"`
}

type tvProbeStatsView struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	OK      int `json:"ok"`
	Failed  int `json:"failed"`
}

// handleTVProbeStatus 报告探测状态（这一次的进度 + 库里的总账）。
func (s *Server) handleTVProbeStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	stats, err := s.live.ProbeStats(ctx)
	if err != nil {
		s.serverError(w, "读取探测进度失败", err)
		return
	}

	s.tvProbe.mu.Lock()
	st := tvProbeStatus{
		Running:   s.tvProbe.running,
		Selection: s.tvProbe.sel,
		Progress:  s.tvProbe.progress,
	}
	if s.tvProbe.running && !s.tvProbe.startedAt.IsZero() {
		st.StartedAt = s.tvProbe.startedAt.Format(time.RFC3339)
		st.ElapsedSeconds = int(time.Since(s.tvProbe.startedAt).Seconds())
	}
	s.tvProbe.mu.Unlock()

	st.Stats = tvProbeStatsView{
		Total:   stats.Total,
		Pending: stats.Pending,
		OK:      stats.OK,
		Failed:  stats.Failed,
	}
	writeJSON(w, http.StatusOK, st)
}

// handleStartTVProbe 起一次频道探测（管理员）。
//
// 请求体（都可选）：{"onlyUnknown":true} / {"group":"央视"} / {"channelIds":[1,2]}。
// 都不给就是全探启用中的频道。
//
// 探测要真连源站，是最多几分钟的后台活儿，所以这里**不**同步等结果：
// 立刻回 202，前端拿 GET 查进度。
func (s *Server) handleStartTVProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OnlyUnknown *bool   `json:"onlyUnknown"`
		Group       *string `json:"group"`
		ChannelIDs  []int64 `json:"channelIds"`
	}
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	sel := tvProbeSelection{ChannelIDs: req.ChannelIDs}
	if req.OnlyUnknown != nil {
		sel.OnlyUnknown = *req.OnlyUnknown
	}
	if req.Group != nil {
		sel.Group = strings.TrimSpace(*req.Group)
	}

	// 先确认 ffprobe 在不在：起了一趟「全都失败」的探测会把用户的频道
	// 全标成失效，这里的预检比事后解释便宜得多。
	if err := s.live.CheckProbeTool(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	s.tvProbe.mu.Lock()
	if s.tvProbe.running {
		s.tvProbe.mu.Unlock()
		writeError(w, http.StatusConflict, "已经有一次探测在进行中")
		return
	}
	s.tvProbe.running = true
	s.tvProbe.startedAt = time.Now()
	s.tvProbe.sel = sel
	s.tvProbe.progress = livetvsync.ProbeProgress{}
	s.tvProbe.mu.Unlock()

	s.log.Info("开始探测直播频道（后台）",
		"onlyUnknown", sel.OnlyUnknown, "group", sel.Group, "channels", len(sel.ChannelIDs),
		"username", usernameOf(r))

	go func() {
		defer func() {
			s.tvProbe.mu.Lock()
			s.tvProbe.running = false
			s.tvProbe.mu.Unlock()
		}()
		// 用服务的生命周期 context：请求早就返回了，探测还得继续跑完
		run, err := s.live.ProbeChannels(s.base(), sel.toOptions(), func(p livetvsync.ProbeProgress) {
			s.tvProbe.mu.Lock()
			s.tvProbe.progress = p
			s.tvProbe.mu.Unlock()
		})
		if err != nil {
			s.log.Warn("探测直播频道失败", "err", err)
			return
		}
		s.log.Info("探测直播频道完成", "total", run.Total, "ok", run.OK,
			"failed", run.Failed, "canceled", run.Canceled)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"running": true, "selection": sel})
}
