package store

// 审计日志：谁在什么时候做了什么。
//
// 与 internal/logbuf 的分工：那个是**最近日志**（内存、会滚掉、用于「刚才那一下为什么失败」），
// 这个是**持久记录**（按人 / 动作 / 对象查，用于「谁把那个库删了」）。
//
// 写入失败绝不能让主操作失败（调用方只 Warn）—— 宁可少一条审计，也不能让「改个设置」
// 因为审计写不进去而报错。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AuditEntry 是一条审计记录。
type AuditEntry struct {
	ID        int64          `json:"id"`
	At        time.Time      `json:"at"`
	ActorID   *int64         `json:"actorId,omitempty"`
	ActorName string         `json:"actorName"`
	Action    string         `json:"action"`
	Target    string         `json:"target"`
	Result    string         `json:"result"`
	Detail    map[string]any `json:"detail,omitempty"`
	IP        string         `json:"ip"`
}

// AuditFilter 是审计查询条件（都可选）。
type AuditFilter struct {
	// Action 精确匹配动作名（如 user.create）。
	Action string
	// Actor 匹配操作者（按 id 或名字，包含匹配）。
	Actor string
	// Query 关键字：动作 / 对象 / 操作者名。
	Query string
	// Since 只要这个时间之后的。
	Since *time.Time
	// FailedOnly 只看失败的（登录失败、越权尝试…）。
	FailedOnly bool

	Limit  int
	Offset int
}

// AppendAudit 写一条审计。
func (s *Store) AppendAudit(ctx context.Context, e AuditEntry) error {
	if e.Result == "" {
		e.Result = "ok"
	}
	// nil map 会被 pgx 写成 SQL NULL，而 detail 列是 not null —— 那样这条审计
	// 会**写不进去**（而调用方只 Warn），表现就是「少了一半记录」。
	// 空 map 才是「没有额外细节」。
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	_, err := s.pool.Exec(ctx,
		`insert into audit_logs (actor_id, actor_name, action, target, result, detail, ip)
		 values ($1, $2, $3, $4, $5, $6, $7)`,
		e.ActorID, e.ActorName, e.Action, e.Target, e.Result, e.Detail, e.IP)
	if err != nil {
		return fmt.Errorf("写入审计日志失败: %w", err)
	}
	return nil
}

// ListAuditLogs 按条件查审计（时间倒序），并返回符合条件的总数。
//
// 条件与参数同时拼：用 ph() 注册一个参数并拿到占位符 ——
// 这样「参数表的顺序」与「SQL 里的占位符」**天然一致**，
// 不会出现「参数多了一个 / 占位符跳号」那类只在真跑时才炸的问题
// （首页那次就是这么坏的，见 ROADMAP 教训 52）。
func (s *Store) ListAuditLogs(ctx context.Context, f AuditFilter) ([]AuditEntry, int, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}

	var args []any
	ph := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	var conds []string
	if f.Action != "" {
		conds = append(conds, "action = "+ph(f.Action))
	}
	if f.Actor != "" {
		byName := ph(likeArg(f.Actor))
		byID := ph(f.Actor)
		conds = append(conds, "(actor_name ilike "+byName+" or actor_id::text = "+byID+")")
	}
	if f.Query != "" {
		q1, q2, q3 := ph(likeArg(f.Query)), ph(likeArg(f.Query)), ph(likeArg(f.Query))
		conds = append(conds, "(action ilike "+q1+" or target ilike "+q2+" or actor_name ilike "+q3+")")
	}
	if f.FailedOnly {
		conds = append(conds, "result <> "+ph("ok"))
	}
	if f.Since != nil {
		conds = append(conds, "at >= "+ph(*f.Since))
	}
	where := "true"
	if len(conds) > 0 {
		where = strings.Join(conds, " and ")
	}

	var total int
	if err := s.pool.QueryRow(ctx, `select count(*) from audit_logs where `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计审计日志失败: %w", err)
	}

	lim, off := ph(f.Limit), ph(f.Offset)
	rows, err := s.pool.Query(ctx,
		`select id, at, actor_id, actor_name, action, target, result, detail, ip
		 from audit_logs where `+where+` order by at desc, id desc limit `+lim+` offset `+off,
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询审计日志失败: %w", err)
	}
	defer rows.Close()

	out := make([]AuditEntry, 0, f.Limit)
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorName, &e.Action, &e.Target,
			&e.Result, &e.Detail, &e.IP); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// PruneAuditLogs 删掉 olderThan 之前的审计记录，返回删掉多少条。
func (s *Store) PruneAuditLogs(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `delete from audit_logs where at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("清理审计日志失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

func likeArg(s string) string { return "%" + strings.TrimSpace(s) + "%" }
