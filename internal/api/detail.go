package api

import (
	"net/http"
	"strconv"
	"time"
)

// 本文件是**详情页**（M6）要用的两个只读接口：演职员与相关推荐。
//
// 为什么不塞进 GET /api/v1/items/{id}：
//   - 演职员动辄几十上百条（一部剧的每一集都是同一批人），而条目详情是
//     编辑页在用的，把大厅数据塞进去会让那一页也跟着变重；
//   - 相关推荐是一个「猜你可能还想看」的查询，与条目自身的元数据无关。
// 两者各自一个接口，前端三路并发拿即可。

// handleItemPeople 返回条目的演职员。
func (s *Server) handleItemPeople(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	people, err := s.store.ListItemPeople(ctx, item.ID)
	if err != nil {
		s.serverError(w, "读取演职员失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"itemId": item.ID,
		"people": people,
		// 数据来源目前只有 nfo：界面要能如实告诉用户「为什么这里空着」
		"source": "nfo",
	})
}

// handleItemRelated 返回相关推荐（同库 + 同类型 + 共同流派）。
func (s *Server) handleItemRelated(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	limit := 12
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	items, err := s.store.ListRelatedItems(ctx, item, limit)
	if err != nil {
		s.serverError(w, "读取相关推荐失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}
