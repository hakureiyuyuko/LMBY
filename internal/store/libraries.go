package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Library 是一个媒体库。
type Library struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"` // movie | tv | homevideo | mixed
	Options   map[string]any `json:"options"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`

	Paths []LibraryPath `json:"paths,omitempty"`
}

// LibraryPath 是媒体库的一个根路径。
type LibraryPath struct {
	ID        int64  `json:"id"`
	LibraryID int64  `json:"libraryId"`
	Path      string `json:"path"`
	ReadOnly  bool   `json:"readonly"`
	SortOrder int32  `json:"sortOrder"`
}

// ValidLibraryKind 校验库类型。
func ValidLibraryKind(k string) bool {
	switch k {
	case "movie", "tv", "homevideo", "mixed":
		return true
	}
	return false
}

const libraryColumns = `id, name, kind, options, created_at, updated_at`

func scanLibrary(row pgx.Row) (*Library, error) {
	var l Library
	err := row.Scan(&l.ID, &l.Name, &l.Kind, &l.Options, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取媒体库失败: %w", err)
	}
	return &l, nil
}

// CreateLibrary 新建媒体库（含根路径），整个过程在一个事务里。
func (s *Store) CreateLibrary(ctx context.Context, name, kind string, options map[string]any, paths []string) (*Library, error) {
	if options == nil {
		options = map[string]any{}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var lib Library
	err = tx.QueryRow(ctx,
		`insert into libraries (name, kind, options) values ($1, $2, $3)
		 returning `+libraryColumns, name, kind, options).
		Scan(&lib.ID, &lib.Name, &lib.Kind, &lib.Options, &lib.CreatedAt, &lib.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("创建媒体库失败: %w", err)
	}

	for i, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`insert into library_paths (library_id, path, sort_order) values ($1, $2, $3)
			 on conflict (path) do update set library_id = excluded.library_id, sort_order = excluded.sort_order`,
			lib.ID, p, i); err != nil {
			return nil, fmt.Errorf("添加根路径 %s 失败: %w", p, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交事务失败: %w", err)
	}
	return s.GetLibrary(ctx, lib.ID)
}

// GetLibrary 读取媒体库（含根路径）。
func (s *Store) GetLibrary(ctx context.Context, id int64) (*Library, error) {
	lib, err := scanLibrary(s.pool.QueryRow(ctx,
		`select `+libraryColumns+` from libraries where id = $1`, id))
	if err != nil {
		return nil, err
	}
	paths, err := s.ListLibraryPaths(ctx, id)
	if err != nil {
		return nil, err
	}
	lib.Paths = paths
	return lib, nil
}

// ListLibraries 读取全部媒体库（含根路径）。
func (s *Store) ListLibraries(ctx context.Context) ([]Library, error) {
	rows, err := s.pool.Query(ctx,
		`select `+libraryColumns+` from libraries order by id`)
	if err != nil {
		return nil, fmt.Errorf("查询媒体库列表失败: %w", err)
	}
	defer rows.Close()

	var out []Library
	for rows.Next() {
		var l Library
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Options, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		paths, err := s.ListLibraryPaths(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Paths = paths
	}
	return out, nil
}

// ListLibraryPaths 读取某个库的根路径。
func (s *Store) ListLibraryPaths(ctx context.Context, libraryID int64) ([]LibraryPath, error) {
	rows, err := s.pool.Query(ctx,
		`select id, library_id, path, readonly, sort_order
		 from library_paths where library_id = $1 order by sort_order, id`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("查询根路径失败: %w", err)
	}
	defer rows.Close()

	var out []LibraryPath
	for rows.Next() {
		var p LibraryPath
		if err := rows.Scan(&p.ID, &p.LibraryID, &p.Path, &p.ReadOnly, &p.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateLibrary 修改库名（M1 只暴露改名的必要能力）。
func (s *Store) UpdateLibrary(ctx context.Context, id int64, name string) error {
	tag, err := s.pool.Exec(ctx,
		`update libraries set name = $2, updated_at = now() where id = $1`, id, name)
	if err != nil {
		return fmt.Errorf("更新媒体库失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteLibrary 删除媒体库（条目与文件靠外键级联清理）。
func (s *Store) DeleteLibrary(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `delete from libraries where id = $1`, id)
	if err != nil {
		return fmt.Errorf("删除媒体库失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountItemsByKind 统计某个库下各类型的条目数（库列表展示用）。
func (s *Store) CountItemsByKind(ctx context.Context, libraryID int64) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx,
		`select kind, count(*) from media_items
		 where library_id = $1 and deleted_at is null group by kind`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("统计条目失败: %w", err)
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
