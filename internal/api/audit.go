package api

// 审计日志：谁在什么时候做了什么。
//
// 与「日志」页（internal/logbuf）的分工：
//   - 日志页 = **最近的服务端日志**（内存环形缓冲、排查用、会滚掉）；
//   - 审计   = **持久的问责记录**（按人 / 动作 / 对象翻旧账：谁把那个库删了）。
//
// 两条硬规矩：
//  1. **审计写不进去，绝不能让主操作失败** —— 宁可少一条记录，也不能让「改个设置」
//     因为审计表有问题而报错。所以下面两个 helper 失败只 Warn。
//  2. **口令 / token / 密钥不落审计**：detail 里只写「重置了某人的口令」这种事实。

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

const (
	auditDefaultLimit = 100
	auditMaxLimit     = 600
)

// audit 记一条**成功**的审计，自动带上当前操作者与来源 IP。
//
// 失败那条（result=failed）走 auditNamed；这里的结果恒为 ok，所以不设 result 形参。
func (s *Server) audit(ctx context.Context, r *http.Request, action, target string, detail map[string]any) {
	e := store.AuditEntry{Action: action, Target: target, Result: "ok", Detail: detail, IP: clientIP(r)}
	if auth := currentAuth(r); auth != nil && auth.User != nil {
		id := auth.User.ID
		e.ActorID = &id
		e.ActorName = auth.User.Username
	}
	s.writeAudit(ctx, e)
}

// auditNamed 用于「还不知道是谁」的场合（典型：登录失败）—— 只记下尝试用的用户名。
func (s *Server) auditNamed(ctx context.Context, r *http.Request, actorName, action, target, result string, detail map[string]any) {
	s.writeAudit(ctx, store.AuditEntry{
		ActorName: actorName, Action: action, Target: target, Result: result,
		Detail: detail, IP: clientIP(r),
	})
}

func (s *Server) writeAudit(ctx context.Context, e store.AuditEntry) {
	if s.store == nil {
		return
	}
	if err := s.store.AppendAudit(ctx, e); err != nil {
		// 只 Warn：审计是「旁路」，不该拖累主操作（这条注释是故意的）
		s.log.Warn("写入审计日志失败（这次操作照常成功）", "action", e.Action, "err", err)
	}
}

// handleListAudit 返回审计记录（时间倒序）。
//
// 查询参数：limit / offset（分页）、action（精确动作名）、actor（操作者）、
// q（关键字：动作 / 对象 / 操作者）、failed=1（只看失败的）、since（RFC3339）。
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := auditDefaultLimit
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = min(max(n, 1), auditMaxLimit)
		}
	}
	offset := 0
	if v := strings.TrimSpace(q.Get("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}

	f := store.AuditFilter{
		Action:     strings.TrimSpace(q.Get("action")),
		Actor:      strings.TrimSpace(q.Get("actor")),
		Query:      strings.TrimSpace(q.Get("q")),
		FailedOnly: q.Get("failed") == "1",
		Limit:      limit,
		Offset:     offset,
	}
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = &t
		}
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	entries, total, err := s.store.ListAuditLogs(ctx, f)
	if err != nil {
		s.serverError(w, "读取审计日志失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}
