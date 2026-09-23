// Package logbuf 是「最近日志」的内存环形缓冲：给管理界面看服务日志用。
//
// 为什么不用 journalctl：服务可能跑在容器里（往往没有 journalctl），也可能跑在
// Windows / macOS（根本没有 journald）。应用自己留最近若干条，到哪都能看。
// 目标读者是「出问题时想在界面上看一眼」的管理员，**不是长期归档** ——
// 归档该由 journald / 日志文件承担（轮转是另一件事）。
//
// 用法：包一层现有的 slog.Handler（下游照常写 stderr/文件），同时把它们留在内存里：
//
//	log, buf := newServeLogger(cfg.LogLevel)   // buf 是 *logbuf.Buffer
//	srv.SetLogBuffer(buf)
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultCapacity 是默认保留的条数。
//
// 2000 条：按正常流量大约几十分钟到几小时，足够回答「刚才那一下为什么失败」；
// 再大就开始明显占内存了（每条还带若干字段）。
const DefaultCapacity = 2000

// Entry 是一条日志（界面上按时间倒序展示）。
type Entry struct {
	Time  time.Time      `json:"time"`
	Level string         `json:"level"` // DEBUG / INFO / WARN / ERROR
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Stats 是缓冲自身的情况：容量、当前条数、因容量被覆盖掉的条数。
//
// Dropped 要如实告诉界面 —— 不然「看不到更早的日志」会被当成「没发生过」。
type Stats struct {
	Capacity int   `json:"capacity"`
	Count    int   `json:"count"`
	Dropped  int64 `json:"dropped"`
}

type ring struct {
	cap     int
	buf     []Entry
	next    int   // 下一个写入位置
	count   int   // 已保留的条数（≤ cap）
	dropped int64 // 被覆盖掉的条数
	mu      sync.RWMutex
}

// Buffer 既是 slog.Handler（包了下游），也是一份最近日志的环形缓冲。
//
// 注意 WithAttrs / WithGroup 返回的新 handler **共享同一个环**，而且要把
// 「With 带的字段」自己累积起来 —— 下游（JSON handler）会自己记住它们，
// 但我们这条记录路径必须也记住，否则 `logger.With("module", "livetv")` 之后
// 的日志进环时就丢字段了（真踩到过）。
type Buffer struct {
	down slog.Handler
	r    *ring
	// base 是 With 累积下来的字段；groups 是 WithGroup 累积下来的组名（显示时做前缀）。
	base   []slog.Attr
	groups []string
}

// New 包装下游 handler；capacity <= 0 时用 DefaultCapacity。
func New(downstream slog.Handler, capacity int) *Buffer {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Buffer{
		down: downstream,
		r: &ring{
			cap: capacity,
			buf: make([]Entry, capacity),
		},
	}
}

func (b *Buffer) Enabled(ctx context.Context, level slog.Level) bool {
	return b.down.Enabled(ctx, level)
}

func (b *Buffer) Handle(ctx context.Context, rec slog.Record) error {
	b.r.add(rec, b.base, b.groups)
	return b.down.Handle(ctx, rec)
}

func (b *Buffer) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return b
	}
	merged := make([]slog.Attr, 0, len(b.base)+len(attrs))
	merged = append(merged, b.base...)
	merged = append(merged, attrs...)
	return &Buffer{down: b.down.WithAttrs(attrs), r: b.r, base: merged, groups: b.groups}
}

func (b *Buffer) WithGroup(name string) slog.Handler {
	if name == "" {
		return b
	}
	groups := make([]string, 0, len(b.groups)+1)
	groups = append(groups, b.groups...)
	groups = append(groups, name)
	return &Buffer{down: b.down.WithGroup(name), r: b.r, base: b.base, groups: groups}
}

func (r *ring) add(rec slog.Record, base []slog.Attr, groups []string) {
	prefix := strings.Join(groups, ".")
	e := Entry{Time: rec.Time, Level: levelName(rec.Level), Msg: rec.Message}
	if len(base) > 0 || rec.NumAttrs() > 0 {
		e.Attrs = make(map[string]any, len(base)+rec.NumAttrs())
		for _, a := range base {
			e.Attrs[withPrefix(prefix, a.Key)] = attrValue(a.Value)
		}
		rec.Attrs(func(a slog.Attr) bool {
			e.Attrs[withPrefix(prefix, a.Key)] = attrValue(a.Value)
			return true
		})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % r.cap
	if r.count < r.cap {
		r.count++
	} else {
		r.dropped++ // 覆盖了最旧的一条
	}
}

func withPrefix(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// Snapshot 取最近 limit 条（**时间倒序**，新的在前），可按最低级别与关键字过滤。
//
// minLevel 传 logbuf.ParseLevel("") （也就是 noLevelFilter）表示不按级别过滤。
func (b *Buffer) Snapshot(limit int, minLevel slog.Level, query string) ([]Entry, Stats) {
	if b == nil || b.r == nil {
		return []Entry{}, Stats{}
	}
	if limit <= 0 {
		limit = 200
	}
	q := strings.ToLower(strings.TrimSpace(query))
	r := b.r
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Entry, 0, min(limit, r.count))
	for i := 0; i < r.count && len(out) < limit; i++ {
		idx := ((r.next-1-i)%r.cap + r.cap) % r.cap
		e := r.buf[idx]
		if minLevel != noLevelFilter && levelOf(e.Level) < minLevel {
			continue
		}
		if q != "" && !entryMatches(e, q) {
			continue
		}
		out = append(out, e)
	}
	return out, Stats{Capacity: r.cap, Count: r.count, Dropped: r.dropped}
}

// noLevelFilter 是「不按级别过滤」的哨兵（比 DEBUG 还低）。
//
// 注意别用 slog.LevelInfo(0) 当哨兵：那正好是 INFO，会把 DEBUG 过滤掉，
// 而调用方想表达的往往是「不过滤」。
const noLevelFilter = slog.Level(-1000)

// ParseLevel 把界面传来的 level 参数解析成 slog.Level；
// 空串或认不出来时返回 noLevelFilter（= 不过滤，而不是「什么都不返回」）。
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return noLevelFilter
	}
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

func levelOf(name string) slog.Level {
	return ParseLevel(name)
}

func entryMatches(e Entry, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(e.Msg), lowerQuery) {
		return true
	}
	for k, v := range e.Attrs {
		if strings.Contains(strings.ToLower(k), lowerQuery) {
			return true
		}
		if strings.Contains(strings.ToLower(fmt.Sprint(v)), lowerQuery) {
			return true
		}
	}
	return false
}

// attrValue 把 slog 的值折成 JSON 友好的形态（界面上直接显示）。
func attrValue(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindGroup:
		m := map[string]any{}
		for _, a := range v.Group() {
			m[a.Key] = attrValue(a.Value)
		}
		return m
	default:
		return fmt.Sprint(v.Any())
	}
}
