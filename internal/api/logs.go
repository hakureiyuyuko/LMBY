package api

// 管理界面的「日志」页：读最近日志（内存环形缓冲，见 internal/logbuf）。
//
// 只给管理员：日志里带用户名、媒体路径与错误详情，不是给普通用户看的东西。
//
// 为什么不直接读 journald / 日志文件：服务可能跑在容器里（往往没有 journalctl），
// 也可能跑在 Windows / macOS。应用自己留一份最近日志，到哪都能看；
// 长期归档由 journald / 日志文件承担（那是另一件事）。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/logbuf"
)

const (
	// logsDefaultLimit 是不传 limit 时给的条数。
	logsDefaultLimit = 200
	// logsMaxLimit 是一次最多给多少条 —— 界面要渲染，再多就该用关键字缩小范围了。
	logsMaxLimit = 1000
)

// handleLogs 返回最近的日志（时间倒序）。
//
// 查询参数：
//
//	limit  条数（默认 200，1~1000）
//	level  最低级别：debug / info / warn / error（空 = 不过滤）
//	q      关键字：匹配消息、字段名或字段值（忽略大小写）
//
// 响应里的 dropped 表示「因为容量被覆盖掉的条数」—— 要如实告诉界面，
// 不然「看不到更早的日志」会被当成「没发生过」。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := logsDefaultLimit
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = min(max(n, 1), logsMaxLimit)
		}
	}

	entries, stats := s.logBuf.Snapshot(limit, logbuf.ParseLevel(q.Get("level")), q.Get("q"))
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":  entries,
		"total":    stats.Count,
		"capacity": stats.Capacity,
		"dropped":  stats.Dropped,
	})
}
