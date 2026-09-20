package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Task 是后台任务队列里的一条记录。
//
// 队列刻意用 PostgreSQL 实现而不是引入 Redis/MQ：任务本来就是业务数据的一部分，
// 和媒体库放在同一个事务里才能保证「扫描入库 + 排队探测」不会只成功一半。
type Task struct {
	ID          int64          `json:"id"`
	Kind        string         `json:"kind"`
	Payload     map[string]any `json:"payload"`
	State       string         `json:"state"`
	Priority    int            `json:"priority"`
	Attempts    int            `json:"attempts"`
	MaxAttempts int            `json:"maxAttempts"`
	LastError   string         `json:"lastError,omitempty"`
	RunAt       time.Time      `json:"runAt"`
	LockedBy    string         `json:"lockedBy,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	StartedAt   *time.Time     `json:"startedAt,omitempty"`
	FinishedAt  *time.Time     `json:"finishedAt,omitempty"`
}

// EnqueueOptions 控制入队行为。
type EnqueueOptions struct {
	// Priority 越大越先执行。
	Priority int
	// Delay 延迟多久才可被领取（用于退避重试）。
	Delay time.Duration
	// MaxAttempts 最大尝试次数，<=0 时用 3。
	MaxAttempts int
	// DedupeKey 非空时保证「同一 kind 下同一 key 只有一个待处理任务」。
	DedupeKey string
}

const taskColumns = `id, kind, payload, state, priority, attempts, max_attempts,
	last_error, run_at, locked_by, created_at, started_at, finished_at`

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Kind, &t.Payload, &t.State, &t.Priority, &t.Attempts,
		&t.MaxAttempts, &t.LastError, &t.RunAt, &t.LockedBy, &t.CreatedAt,
		&t.StartedAt, &t.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Decode 把 payload 解成具体类型。
func (t *Task) Decode(dst any) error {
	raw, err := json.Marshal(t.Payload)
	if err != nil {
		return fmt.Errorf("序列化任务载荷失败: %w", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("解析任务载荷失败: %w", err)
	}
	return nil
}

// Enqueue 入队一个任务。返回 created=false 表示被幂等规则挡下（已有同样的待处理任务）。
func (s *Store) Enqueue(ctx context.Context, kind string, payload any, opts EnqueueOptions) (id int64, created bool, err error) {
	if kind == "" {
		return 0, false, errors.New("任务 kind 不能为空")
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, false, fmt.Errorf("序列化任务载荷失败: %w", err)
	}

	err = s.pool.QueryRow(ctx,
		`insert into tasks (kind, payload, priority, run_at, max_attempts, dedupe_key)
		 values ($1, $2, $3, now() + make_interval(secs => $4), $5, $6)
		 on conflict do nothing
		 returning id`,
		kind, body, opts.Priority, opts.Delay.Seconds(), opts.MaxAttempts, opts.DedupeKey).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("入队 %s 任务失败: %w", kind, err)
	}

	// 走到了这里说明命中了幂等约束：把已存在的那条找出来返回。
	if opts.DedupeKey == "" {
		return 0, false, errors.New("入队失败：唯一约束冲突但未设置 dedupe key")
	}
	err = s.pool.QueryRow(ctx,
		`select id from tasks
		 where kind = $1 and dedupe_key = $2 and state in ('pending', 'running')
		 limit 1`, kind, opts.DedupeKey).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("查询已存在的 %s 任务失败: %w", kind, err)
	}
	return id, false, nil
}

// ClaimTask 领取一个待执行任务；没有可领的任务时返回 (nil, nil)。
//
// 用 `for update skip locked` 抢占：多个 worker 并发时不会互相等待，
// 这是 PostgreSQL 上做任务队列最省事的正确姿势。
func (s *Store) ClaimTask(ctx context.Context, workerID string, kinds []string) (*Task, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	t, err := scanTask(s.pool.QueryRow(ctx,
		`update tasks set state = 'running',
		                  started_at = now(),
		                  locked_by = $1,
		                  attempts = attempts + 1
		 where id = (
		     select id from tasks
		     where state = 'pending' and run_at <= now() and kind = any($2)
		     order by priority desc, run_at, id
		     for update skip locked
		     limit 1
		 )
		 returning `+taskColumns, workerID, kinds))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("领取任务失败: %w", err)
	}
	return t, nil
}

// CompleteTask 标记任务成功。
func (s *Store) CompleteTask(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`update tasks set state = 'done', finished_at = now(), locked_by = '', last_error = ''
		 where id = $1`, id)
	if err != nil {
		return fmt.Errorf("标记任务完成失败: %w", err)
	}
	return nil
}

// FailTask 记录一次失败。未超过最大尝试次数时回到 pending 并延迟重试。
// 返回值 retried 表示是否还会重试。
func (s *Store) FailTask(ctx context.Context, id int64, errMsg string, retryDelay time.Duration) (bool, error) {
	var (
		state    string
		attempts int
		max      int
	)
	err := s.pool.QueryRow(ctx, `select state, attempts, max_attempts from tasks where id = $1`, id).
		Scan(&state, &attempts, &max)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取任务状态失败: %w", err)
	}

	if attempts < max {
		_, err = s.pool.Exec(ctx,
			`update tasks set state = 'pending', locked_by = '',
			                  run_at = now() + make_interval(secs => $2), last_error = $3
			 where id = $1`, id, retryDelay.Seconds(), errMsg)
		if err != nil {
			return false, fmt.Errorf("重置任务失败: %w", err)
		}
		return true, nil
	}

	_, err = s.pool.Exec(ctx,
		`update tasks set state = 'failed', finished_at = now(), locked_by = '', last_error = $2
		 where id = $1`, id, errMsg)
	if err != nil {
		return false, fmt.Errorf("标记任务失败: %w", err)
	}
	return false, nil
}

// RecoverStaleTasks 把「卡在 running 超过指定时长」的任务放回队列。
//
// 进程被 kill、容器被重启都会留下这种任务；没有它，任务会永久卡死。
func (s *Store) RecoverStaleTasks(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`update tasks set state = 'pending', locked_by = '',
		                  last_error = coalesce(nullif(last_error, ''), 'worker 中断，任务被回收')
		 where state = 'running' and started_at < now() - make_interval(secs => $1)`,
		olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("回收卡住的任务失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TaskStats 是队列水位。
type TaskStats struct {
	Pending              int64 `json:"pending"`
	Running              int64 `json:"running"`
	Done                 int64 `json:"done"`
	Failed               int64 `json:"failed"`
	Canceled             int64 `json:"canceled"`
	OldestPendingSeconds int64 `json:"oldestPendingSeconds"`
}

// TaskStatsOf 返回队列水位。
func (s *Store) TaskStatsOf(ctx context.Context) (*TaskStats, error) {
	var st TaskStats
	err := s.pool.QueryRow(ctx,
		`select
		   count(*) filter (where state = 'pending'),
		   count(*) filter (where state = 'running'),
		   count(*) filter (where state = 'done'),
		   count(*) filter (where state = 'failed'),
		   count(*) filter (where state = 'canceled'),
		   coalesce(extract(epoch from now() - min(run_at) filter (where state = 'pending'))::bigint, 0)
		 from tasks`).Scan(&st.Pending, &st.Running, &st.Done, &st.Failed, &st.Canceled,
		&st.OldestPendingSeconds)
	if err != nil {
		return nil, fmt.Errorf("统计任务队列失败: %w", err)
	}
	return &st, nil
}

// TaskStatsByKind 返回各类型任务的待处理数量。
func (s *Store) TaskStatsByKind(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx,
		`select kind, count(*) from tasks where state in ('pending', 'running') group by kind`)
	if err != nil {
		return nil, fmt.Errorf("统计任务类型失败: %w", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// ListTasks 列出最近的任务（按 id 倒序）。
func (s *Store) ListTasks(ctx context.Context, state string, limit int) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`select `+taskColumns+` from tasks
		 where ($1 = '' or state = $1)
		 order by id desc limit $2`, state, limit)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %w", err)
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// PurgeTasks 删除指定天数之前的已结束任务，避免表无限增长。
func (s *Store) PurgeTasks(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`delete from tasks
		 where state in ('done', 'failed', 'canceled')
		   and finished_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("清理历史任务失败: %w", err)
	}
	return tag.RowsAffected(), nil
}
