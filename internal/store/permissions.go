package store

import (
	"context"
	"errors"
)


// ViewerFor 组装某个用户的 Viewer。
func (s *Store) ViewerFor(ctx context.Context, user *User) (Viewer, error) {
	v := Viewer{
		UserID:         user.ID,
		IsAdmin:        user.IsAdmin,
		AllowTranscode: user.AllowTranscode,
		AllowLiveTV:    user.AllowLiveTV,
		MaxStreams:     int(user.MaxConcurrentStreams),
	}
	// 管理员、或没开「按库限制」→ 不过滤（＝现在的行为）
	if user.IsAdmin || !user.RestrictedLibraries {
		return v, nil
	}
	ids, err := s.ListUserLibraryIDs(ctx, user.ID)
	if err != nil {
		return v, err
	}
	if ids == nil {
		ids = []int64{} // 显式空切片 = 什么都看不到
	}
	v.libs = ids
	return v, nil
}

// LibraryIDs 返回可见库（nil = 不过滤）。给需要在 api 层额外判断的场合用。
func (v Viewer) LibraryIDs() []int64 { return v.libs }

// CanSeeLibrary 判断某个库是否可见。
func (v Viewer) CanSeeLibrary(libraryID int64) bool {
	if v.libs == nil {
		return true
	}
	for _, id := range v.libs {
		if id == libraryID {
			return true
		}
	}
	return false
}

// SQLArgs 是给 libraryFilter 用的参数：nil → 空数组（= 不过滤）。
func (v Viewer) SQLArgs() []int64 {
	if v.libs == nil {
		return []int64{}
	}
	return v.libs
}

// libraryFilter 是「列表类」SQL 的统一可见性条件。
//
//	cardinality($n::bigint[]) = 0 → 不过滤（管理员 / 未限制的用户）
//	否则只放行 libs 里的库
//
// col 是库 id 列（注意用表的别名，比如 "i.library_id"），arg 是参数占位符（"$7"）。
func libraryFilter(col, arg string) string {
	return "(cardinality(" + arg + "::bigint[]) = 0 or " + col + " = any(" + arg + "))"
}

// ListUserLibraryIDs 返回某个用户被授权的库（顺手按 id 排序，便于比较）。
func (s *Store) ListUserLibraryIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx,
		`select library_id from user_libraries where user_id = $1 order by library_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReplaceUserLibraries 整份替换某个用户的可见库（与媒体库路径一个风格：
// 给什么就是什么，不是增量）。
func (s *Store) ReplaceUserLibraries(ctx context.Context, userID int64, libraryIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `delete from user_libraries where user_id = $1`, userID); err != nil {
		return err
	}
	for _, id := range libraryIDs {
		if _, err := tx.Exec(ctx,
			`insert into user_libraries (user_id, library_id) values ($1, $2)
			 on conflict do nothing`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UserPatch 是管理员对用户的一次修改（nil 字段＝不改）。
type UserPatch struct {
	DisplayName          *string
	IsAdmin              *bool
	IsDisabled           *bool
	MaxConcurrentStreams *int
	RestrictedLibraries  *bool
	AllowTranscode       *bool
	AllowLiveTV          *bool
}

// ErrLastAdmin 表示这次修改会「把最后一个能用的管理员弄没」。
var ErrLastAdmin = errors.New("这是最后一个管理员，不能禁用 / 降级 / 删除")

// UpdateUser 改用户的属性，返回改完的用户。
//
// 这里守一条硬规则：**改完不能一个活跃管理员都不剩**（禁用、降级都算）——
// 放在 store 而不是 api，是为了「不管哪个入口都绕不过去」。
func (s *Store) UpdateUser(ctx context.Context, id int64, p UserPatch) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 先拿当前状态：判断这次改动会不会动到「最后一个活跃管理员」
	cur, err := scanUser(tx.QueryRow(ctx, `select `+userColumns+` from users where id = $1 for update`, id))
	if err != nil {
		return nil, err
	}
	wasActiveAdmin := cur.IsAdmin && !cur.IsDisabled
	willBeActiveAdmin := wasActiveAdmin
	if p.IsAdmin != nil {
		willBeActiveAdmin = *p.IsAdmin && !cur.IsDisabled
	}
	if p.IsDisabled != nil {
		willBeActiveAdmin = willBeActiveAdmin && !*p.IsDisabled
	}
	if wasActiveAdmin && !willBeActiveAdmin {
		var admins int
		if err := tx.QueryRow(ctx,
			`select count(*)::int from users where is_admin and not is_disabled`).Scan(&admins); err != nil {
			return nil, err
		}
		if admins <= 1 {
			return nil, ErrLastAdmin
		}
	}

	if _, err := tx.Exec(ctx,
		`update users set
		   display_name           = coalesce($2, display_name),
		   is_admin               = coalesce($3, is_admin),
		   is_disabled            = coalesce($4, is_disabled),
		   max_concurrent_streams = coalesce($5, max_concurrent_streams),
		   restrict_libraries     = coalesce($6, restrict_libraries),
		   allow_transcode        = coalesce($7, allow_transcode),
		   allow_livetv           = coalesce($8, allow_livetv)
		 where id = $1`,
		id, p.DisplayName, p.IsAdmin, p.IsDisabled, p.MaxConcurrentStreams,
		p.RestrictedLibraries, p.AllowTranscode, p.AllowLiveTV); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// commit 之后再读一次：返回的是落库后的真实状态（前端拿它直接刷新那一行）
	u, err := scanUser(s.pool.QueryRow(ctx, `select `+userColumns+` from users where id = $1`, id))
	if err != nil {
		return nil, err
	}
	return u, nil
}

// DeleteUser 删用户（连带收藏 / 播放列表 / 观看进度等，靠外键级联）。
//
// 与 UpdateUser 同一条硬规则：不能把最后一个活跃管理员删掉。
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u, err := scanUser(tx.QueryRow(ctx, `select `+userColumns+` from users where id = $1 for update`, id))
	if err != nil {
		return err
	}
	if u.IsAdmin && !u.IsDisabled {
		var admins int
		if err := tx.QueryRow(ctx,
			`select count(*)::int from users where is_admin and not is_disabled`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx, `delete from users where id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FindLibraryByID 只用于校验「授权时给的库 id 真的存在」。
func (s *Store) FindLibraryByID(ctx context.Context, id int64) (*Library, error) {
	return scanLibrary(s.pool.QueryRow(ctx, `select `+libraryColumns+` from libraries where id = $1`, id))
}

// libsArg 把「可见库」参数规范化成 pgx 能直接绑定的形状：
// nil / 空 → 空数组（SQL 里 cardinality = 0 表示不过滤）。
func libsArg(libs []int64) []int64 {
	if len(libs) == 0 {
		return []int64{}
	}
	return libs
}
