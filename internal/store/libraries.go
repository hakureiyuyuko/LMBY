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

	// ReadOnly：这个库的根路径**不允许写**（网盘挂载 / 只读挂载）。
	// 打开后 LMBY 一个字节也不往库目录里写：刮削到的元数据与图片落进
	// 数据目录的 overlay 层（每个库一块，不参与缓存淘汰），
	// 将来启用「写回媒体目录」时也会先看这个开关。
	ReadOnly bool `json:"readonly"`

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

const libraryColumns = `id, name, kind, options, created_at, updated_at, readonly`

func scanLibrary(row pgx.Row) (*Library, error) {
	var l Library
	err := row.Scan(&l.ID, &l.Name, &l.Kind, &l.Options, &l.CreatedAt, &l.UpdatedAt, &l.ReadOnly)
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

	lib, err := scanLibrary(tx.QueryRow(ctx,
		`insert into libraries (name, kind, options) values ($1, $2, $3)
		 returning `+libraryColumns, name, kind, options))
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
// ListLibraries 列媒体库。libs 是权限可见库（nil/空 = 不过滤，管理员用）——
// 这是「看不见的库不该出现在界面上」的第一道门。
func (s *Store) ListLibraries(ctx context.Context, libs []int64) ([]Library, error) {
	rows, err := s.pool.Query(ctx,
		`select `+libraryColumns+` from libraries
		 where `+libraryFilter("libraries.id", "$1")+`
		 order by id`, libsArg(libs))
	if err != nil {
		return nil, fmt.Errorf("查询媒体库列表失败: %w", err)
	}
	defer rows.Close()

	var out []Library
	for rows.Next() {
		var l Library
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Options, &l.CreatedAt, &l.UpdatedAt, &l.ReadOnly); err != nil {
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

// UpdateLibraryKind 改库类型（movie | tv | homevideo | mixed），不改动任何条目。
//
// 条目的 kind 已经定死了，所以改库类型只影响「扫描时怎么认它们」与界面展示 ——
// 想让新规则生效要重扫（界面上写明了）。
func (s *Store) UpdateLibraryKind(ctx context.Context, id int64, kind string) error {
	tag, err := s.pool.Exec(ctx,
		`update libraries set kind = $2, updated_at = now() where id = $1`, id, kind)
	if err != nil {
		return fmt.Errorf("更新库类型失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceLibraryPaths 把库的根路径换成给定的一份（事务里：新增的插进去、不再列的删掉）。
//
// ⚠️ 只是换「扫描时去哪些目录」，**已入库的条目不会被删**：
// 移掉一条根路径，那条路径下的条目与文件记录仍然在库里（重新扫描时也不会被标成删除，
// 因为扫描根本不会再走到那棵树）。想要彻底清掉得删库重建 —— 界面上写明了这一点。
func (s *Store) ReplaceLibraryPaths(ctx context.Context, id int64, paths []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var exists bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from libraries where id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("查询媒体库失败: %w", err)
	}
	if !exists {
		return ErrNotFound
	}

	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}

	// 先删掉不在新集合里的（用数组参数，避免拼 SQL）
	if _, err := tx.Exec(ctx,
		`delete from library_paths where library_id = $1 and not (path = any($2::text[]))`,
		id, clean); err != nil {
		return fmt.Errorf("移除根路径失败: %w", err)
	}
	for i, p := range clean {
		if _, err := tx.Exec(ctx,
			`insert into library_paths (library_id, path, sort_order) values ($1, $2, $3)
			 on conflict (path) do update set library_id = excluded.library_id, sort_order = excluded.sort_order`,
			id, p, i); err != nil {
			return fmt.Errorf("添加根路径 %s 失败: %w", p, err)
		}
	}
	if _, err := tx.Exec(ctx, `update libraries set updated_at = now() where id = $1`, id); err != nil {
		return fmt.Errorf("更新媒体库时间失败: %w", err)
	}
	return tx.Commit(ctx)
}

// SetLibraryReadOnly 开关媒体库的「只读」模式。
//
// 同时把 library_paths.readonly 一起改掉：表上那一列是「根路径级」的事实，
// 而开关是库级的 —— 两者不同步的话，以后按路径判断写入权限的人会拿到错的答案。
// 整个动作在一个事务里，没有中间态。
func (s *Store) SetLibraryReadOnly(ctx context.Context, id int64, readOnly bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx,
		`update libraries set readonly = $2, updated_at = now() where id = $1`, id, readOnly)
	if err != nil {
		return fmt.Errorf("更新只读开关失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`update library_paths set readonly = $2 where library_id = $1`, id, readOnly); err != nil {
		return fmt.Errorf("同步根路径只读标记失败: %w", err)
	}
	return tx.Commit(ctx)
}

// LibraryReadOnly 返回某个库是否处于只读模式。
func (s *Store) LibraryReadOnly(ctx context.Context, libraryID int64) (bool, error) {
	var ro bool
	err := s.pool.QueryRow(ctx, `select readonly from libraries where id = $1`, libraryID).Scan(&ro)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("读取只读开关失败: %w", err)
	}
	return ro, nil
}

// ReadOnlyRoots 返回全部「只读」库的根路径。
//
// 用途只有一个：**写入前的最后一道闸** —— 任何要落到媒体目录里的写操作
// 都得先拿目标路径问一遍（见 internal/overlay 的 AssertWritable）。
func (s *Store) ReadOnlyRoots(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`select p.path from library_paths p
		 join libraries l on l.id = p.library_id
		 where p.readonly or l.readonly
		 order by p.path`)
	if err != nil {
		return nil, fmt.Errorf("查询只读根路径失败: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
