package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound 表示目标记录不存在。
	ErrNotFound = errors.New("store: 记录不存在")
	// ErrAlreadyExists 表示唯一约束冲突。
	ErrAlreadyExists = errors.New("store: 记录已存在")
)

// User 是一个账号。PasswordHash 用 `json:"-"` 标记，永不对外序列化。
type User struct {
	ID                   int64      `json:"id"`
	Username             string     `json:"username"`
	DisplayName          string     `json:"displayName"`
	IsAdmin              bool       `json:"isAdmin"`
	IsDisabled           bool       `json:"isDisabled"`
	MaxConcurrentStreams int32      `json:"maxConcurrentStreams"`
	CreatedAt            time.Time  `json:"createdAt"`
	LastLoginAt          *time.Time `json:"lastLoginAt,omitempty"`

	PasswordHash string `json:"-"`
}

const userColumns = `id, username, display_name, password_hash, is_admin, is_disabled,
	max_concurrent_streams, created_at, last_login_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.IsAdmin,
		&u.IsDisabled, &u.MaxConcurrentStreams, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取用户失败: %w", err)
	}
	return &u, nil
}

// CountUsers 返回账号总数，用于判断是否需要初始化管理员。
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `select count(*) from users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计用户失败: %w", err)
	}
	return n, nil
}

// CountAdmins 返回可用管理员数量。
func (s *Store) CountAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`select count(*) from users where is_admin and not is_disabled`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计管理员失败: %w", err)
	}
	return n, nil
}

// CreateUser 新建账号，并同时建立默认偏好行。
func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash string, isAdmin bool) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var u *User
	row := tx.QueryRow(ctx,
		`insert into users (username, display_name, password_hash, is_admin)
		 values ($1, $2, $3, $4)
		 returning `+userColumns,
		username, displayName, passwordHash, isAdmin)
	u, err = scanUser(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`insert into user_preferences (user_id) values ($1) on conflict do nothing`, u.ID); err != nil {
		return nil, fmt.Errorf("初始化用户偏好失败: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交创建用户事务失败: %w", err)
	}
	return u, nil
}

// CreateFirstAdmin 在系统还没有任何用户时创建初始管理员。
//
// 用「条件插入」而不是先 count 再 insert，保证并发调用只有一个能成功。
func (s *Store) CreateFirstAdmin(ctx context.Context, username, displayName, passwordHash string) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	u, err := scanUser(tx.QueryRow(ctx,
		`insert into users (username, display_name, password_hash, is_admin)
		 select $1, $2, $3, true
		 where not exists (select 1 from users)
		 returning `+userColumns,
		username, displayName, passwordHash))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrAlreadyExists
	}
	if err != nil {
		return nil, fmt.Errorf("创建初始管理员失败: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`insert into user_preferences (user_id) values ($1) on conflict do nothing`, u.ID); err != nil {
		return nil, fmt.Errorf("初始化管理员偏好失败: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交创建初始管理员事务失败: %w", err)
	}
	return u, nil
}

// GetUserByUsername 按用户名查询（大小写不敏感）。
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`select `+userColumns+` from users where lower(username) = lower($1)`, username))
}

// GetUserByID 按主键查询。
func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`select `+userColumns+` from users where id = $1`, id))
}

// ListUsers 返回全部账号（管理员视图）。
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx,
		`select `+userColumns+` from users order by is_admin desc, lower(username)`)
	if err != nil {
		return nil, fmt.Errorf("查询用户列表失败: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.IsAdmin,
			&u.IsDisabled, &u.MaxConcurrentStreams, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUserPassword 更新口令哈希。
func (s *Store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	tag, err := s.pool.Exec(ctx,
		`update users set password_hash = $2, updated_at = now() where id = $1`, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("更新口令失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserDisplayName 更新显示名。
func (s *Store) UpdateUserDisplayName(ctx context.Context, userID int64, displayName string) error {
	tag, err := s.pool.Exec(ctx,
		`update users set display_name = $2, updated_at = now() where id = $1`, userID, displayName)
	if err != nil {
		return fmt.Errorf("更新显示名失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchUserLogin 记录一次成功登录。
func (s *Store) TouchUserLogin(ctx context.Context, userID int64, ip string) error {
	_, err := s.pool.Exec(ctx,
		`update users set last_login_at = now(), last_login_ip = nullif($2, '')::inet where id = $1`,
		userID, ip)
	return err
}

// SetUserDisabled 启用/禁用账号。
func (s *Store) SetUserDisabled(ctx context.Context, userID int64, disabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`update users set is_disabled = $2, updated_at = now() where id = $1`, userID, disabled)
	if err != nil {
		return fmt.Errorf("更新账号状态失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------- 偏好

// Preferences 是每个用户的界面与播放偏好。
type Preferences struct {
	Theme         string         `json:"theme"`
	Language      string         `json:"language"`
	SubtitlePrefs map[string]any `json:"subtitlePrefs"`
	AudioPrefs    map[string]any `json:"audioPrefs"`
	LibraryViews  map[string]any `json:"libraryViews"`
}

// ValidTheme 判断主题取值是否合法。
func ValidTheme(v string) bool {
	return v == "light" || v == "dark" || v == "system"
}

// GetPreferences 读取偏好；没有记录时返回默认值而不是报错。
func (s *Store) GetPreferences(ctx context.Context, userID int64) (*Preferences, error) {
	p := &Preferences{Theme: "system", Language: "zh-CN"}
	err := s.pool.QueryRow(ctx,
		`select theme, language, subtitle_prefs, audio_prefs, library_views
		 from user_preferences where user_id = $1`, userID).
		Scan(&p.Theme, &p.Language, &p.SubtitlePrefs, &p.AudioPrefs, &p.LibraryViews)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取用户偏好失败: %w", err)
	}
	return p, nil
}

// UpsertPreferences 覆盖写入偏好。
func (s *Store) UpsertPreferences(ctx context.Context, userID int64, p Preferences) error {
	if p.Theme == "" {
		p.Theme = "system"
	}
	if !ValidTheme(p.Theme) {
		return fmt.Errorf("主题取值非法: %q", p.Theme)
	}
	if p.Language == "" {
		p.Language = "zh-CN"
	}
	if p.SubtitlePrefs == nil {
		p.SubtitlePrefs = map[string]any{}
	}
	if p.AudioPrefs == nil {
		p.AudioPrefs = map[string]any{}
	}
	if p.LibraryViews == nil {
		p.LibraryViews = map[string]any{}
	}
	_, err := s.pool.Exec(ctx,
		`insert into user_preferences (user_id, theme, language, subtitle_prefs, audio_prefs, library_views, updated_at)
		 values ($1, $2, $3, $4, $5, $6, now())
		 on conflict (user_id) do update set
		   theme = excluded.theme,
		   language = excluded.language,
		   subtitle_prefs = excluded.subtitle_prefs,
		   audio_prefs = excluded.audio_prefs,
		   library_views = excluded.library_views,
		   updated_at = now()`,
		userID, p.Theme, p.Language, p.SubtitlePrefs, p.AudioPrefs, p.LibraryViews)
	if err != nil {
		return fmt.Errorf("写入用户偏好失败: %w", err)
	}
	return nil
}

// isUniqueViolation 判断错误是否为 PostgreSQL 唯一约束冲突（23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
