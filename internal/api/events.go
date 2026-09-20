package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleEvents 是一个最小可用的 SSE 端点。
//
// 为什么用 SSE 而不是 WebSocket：这里只需要「服务端单向推进度」，
// SSE 是纯 HTTP、自带断线重连语义（浏览器的 EventSource），
// 不需要引入额外的协议与依赖。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前连接不支持流式响应")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// 反代（Nginx）下防止缓冲导致事件被积压
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	events, unsubscribe := s.scans.Subscribe()
	defer unsubscribe()

	fmt.Fprint(w, "event: hello\ndata: {\"ok\":true}\n\n")
	flusher.Flush()

	// 心跳：有些代理会掐掉长时间静默的连接
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case p, open := <-events:
			if !open {
				return
			}
			payload, err := json.Marshal(p)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: scan\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
