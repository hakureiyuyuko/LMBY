package api

import (
	"net/http"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/ffmpeg"
	"github.com/hakureiyuyuko/lmby/internal/version"
)

// healthComponent 描述单个依赖的健康状态。
type healthComponent struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latencyMs,omitempty"`
	Error     string `json:"error,omitempty"`
}

// healthResponse 是 /healthz 的响应体。
type healthResponse struct {
	Status    string          `json:"status"` // ok | degraded | error
	Version   string          `json:"version"`
	UptimeSec int64           `json:"uptimeSeconds"`
	Database  healthComponent `json:"database"`
	FFmpeg    ffmpeg.Info     `json:"ffmpeg"`
}

// handleHealthz 报告服务自身与依赖的状态。
//
// 数据库不可用返回 503；ffmpeg 缺失只降级（仍可浏览元数据），返回 200 + degraded。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()

	resp := healthResponse{
		Version:   version.Version,
		UptimeSec: int64(time.Since(s.started).Seconds()),
		FFmpeg:    s.ffmpeg,
	}

	start := time.Now()
	err := s.store.Ping(ctx)
	resp.Database.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		resp.Database.OK = false
		resp.Database.Error = err.Error()
	} else {
		resp.Database.OK = true
	}

	switch {
	case !resp.Database.OK:
		resp.Status = "error"
		writeJSON(w, http.StatusServiceUnavailable, resp)
	case !s.ffmpeg.Available:
		resp.Status = "degraded"
		writeJSON(w, http.StatusOK, resp)
	default:
		resp.Status = "ok"
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleMeta 返回前端启动时需要的少量公开信息。
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()

	n, err := s.store.CountUsers(ctx)
	if err != nil {
		s.serverError(w, "查询账号数量失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       version.Version,
		"versionFull":   version.String(),
		"setupRequired": n == 0,
		"ffmpeg":        s.ffmpeg.Available,
	})
}
