package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session 是一条登录会话。ID 存的是令牌的 sha256，不是令牌明文。
type Session struct {
	ID         string    `json:"id"`
	UserID     int64     `json:"userId"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	UserAgent  string    `json:"userAgent"`
	IP         string    `json:"ip"`

	// Current 由上层填充，用于「我的设备」列表里标出当前会话。
	Current bool `json:"current"`
}

const sessionColumns = `s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at,
	s.user_agent, coalesce(host(s.ip), '')`

func scanSession(row pgx.Row) (*Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.UserAgent, &s.IP)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取会话失败: %w", err)
	}
	return &s, nil
}

// CreateSession 写入一条新会话。
func (s *Store) CreateSession(ctx context.Context, id string, userID int64, ttl time.Duration, userAgent, ip string) error {
	_, err := s.pool.Exec(ctx,
		`insert into sessions (id, user_id, expires_at, user_agent, ip)
		 values ($1, $2, now() + make_interval(secs => $3), $4, nullif($5, '')::inet)`,
		id, userID, int(ttl.Seconds()), truncate(userAgent, 400), ip)
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	return nil
}

// GetSessionUser 按会话 ID 取出会话与对应用户。
//
// 过期、已撤销、或用户被禁用的会话一律视为不存在。
func (s *Store) GetSessionUser(ctx context.Context, id string) (*Session, *User, error) {
	sess, err := scanSession(s.pool.QueryRow(ctx,
		`select `+sessionColumns+`
		 from sessions s
		 where s.id = $1 and s.revoked_at is null and s.expires_at > now()`, id))
	if err != nil {
		return nil, nil, err
	}

	// ⚠️ 用 scanUser 而不是手写 Scan：列一变（这里是加权限字段那次）手写的那份就会
	// 静默错位成「12 列 vs 9 个目的地」——表现是**每个带 token 的请求都 401**
	// （登录能过、之后全挂），排查起来很费劲。真踩到过。
	u, err := scanUser(s.pool.QueryRow(ctx,
		`select `+userColumns+` from users where id = $1 and not is_disabled`, sess.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("读取会话用户失败: %w", err)
	}
	sess.Current = true
	return sess, u, nil
}

// TouchSessionIfStale 刷新会话活跃时间。
//
// 只有在超过 minInterval 未刷新时才写库，避免每个请求一次写入。
func (s *Store) TouchSessionIfStale(ctx context.Context, id string, minInterval time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`update sessions set last_seen_at = now()
		 where id = $1 and last_seen_at < now() - make_interval(secs => $2)`,
		id, int(minInterval.Seconds()))
	return err
}

// RevokeSession 撤销（登出）一条会话。
func (s *Store) RevokeSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`update sessions set revoked_at = now() where id = $1 and revoked_at is null`, id)
	if err != nil {
		return fmt.Errorf("撤销会话失败: %w", err)
	}
	return nil
}

// RevokeSessionForUser 撤销属于指定用户的一条会话，返回是否真的撤销了。
// 用 user_id 做条件，避免越权踢别人的会话。
func (s *Store) RevokeSessionForUser(ctx context.Context, userID int64, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`update sessions set revoked_at = now()
		 where id = $1 and user_id = $2 and revoked_at is null`, id, userID)
	if err != nil {
		return false, fmt.Errorf("撤销会话失败: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeOtherSessions 撤销除 keepID 之外的全部会话，返回受影响条数。
// 用于「改密码后踢掉其他设备」。
func (s *Store) RevokeOtherSessions(ctx context.Context, userID int64, keepID string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`update sessions set revoked_at = now()
		 where user_id = $1 and id <> $2 and revoked_at is null`, userID, keepID)
	if err != nil {
		return 0, fmt.Errorf("撤销其他会话失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListSessions 列出某用户当前有效的会话（「我的设备」页面用）。
func (s *Store) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.pool.Query(ctx,
		`select `+sessionColumns+`
		 from sessions s
		 where s.user_id = $1 and s.revoked_at is null and s.expires_at > now()
		 order by s.last_seen_at desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询会话列表失败: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.UserAgent, &s.IP); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CleanupSessions 清理过期或已撤销超过 7 天的会话，返回删除条数。
func (s *Store) CleanupSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`delete from sessions
		 where expires_at < now()
		    or (revoked_at is not null and revoked_at < now() - interval '7 days')`)
	if err != nil {
		return 0, fmt.Errorf("清理会话失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// truncate 按字节安全地截断字符串（用于 User-Agent 这类不受控输入）。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// CountUserSessions 数某个用户当前有几个有效会话（用户管理界面显示用）。
func (s *Store) CountUserSessions(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`select count(*)::int from sessions where user_id = $1 and expires_at > now()`, userID).Scan(&n)
	return n, err
}

// DeleteUserSessions 吊销某个用户的**全部**会话。
//
// 用在「改口令 / 禁用 / 收紧库授权」之后：不吊销的话，旧 token 还带着旧的权限集合，
// 权限改了等于没改（这是权限系统最容易漏的一处）。
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where user_id = $1`, userID)
	return err
}
