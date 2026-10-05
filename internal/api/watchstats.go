package api

// 观看统计（设置页 → 观看统计，只给管理员）。
//
// 两个接口：
//   GET /api/v1/stats/watch           总览 + 两个排行（热门影片 / 活跃用户）
//   GET /api/v1/stats/watch/records   谁看了什么的明细（可按用户筛、分页）
//
// 数据全部来自 playback_progress，口径见 store/watchstats.go 的注释。

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	watchTopDefault     = 10
	watchTopMax         = 50
	watchRecordsDefault = 50
	watchRecordsMax     = 500
)

// handleWatchStats 返回总览数字与两个排行。
func (s *Server) handleWatchStats(w http.ResponseWriter, r *http.Request) {
	top := watchTopDefault
	if v := strings.TrimSpace(r.URL.Query().Get("top")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			top = min(max(n, 1), watchTopMax)
		}
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	totals, err := s.store.WatchTotals(ctx)
	if err != nil {
		s.serverError(w, "统计观看总览失败", err)
		return
	}
	topItems, err := s.store.TopWatchedItems(ctx, top)
	if err != nil {
		s.serverError(w, "统计热门影片失败", err)
		return
	}
	topUsers, err := s.store.TopWatchers(ctx, top)
	if err != nil {
		s.serverError(w, "统计活跃用户失败", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totals":   totals,
		"topItems": topItems,
		"topUsers": topUsers,
	})
}

// handleWatchRecords 返回「谁看了什么」明细。
func (s *Server) handleWatchRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := watchRecordsDefault
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = min(max(n, 1), watchRecordsMax)
		}
	}
	offset := 0
	if v := strings.TrimSpace(q.Get("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}

	var userID int64
	if v := strings.TrimSpace(q.Get("userId")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			userID = n
		}
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	records, total, err := s.store.ListWatchRecords(ctx, userID, limit, offset)
	if err != nil {
		s.serverError(w, "读取观看记录失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"records": records,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}
