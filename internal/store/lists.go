package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 本文件是**收藏**（迁移 0012）。
//
// 三件事各占一个函数，职责不重叠：
//   - SetFavorite 写（幂等：同一个条目收藏两次不会报错，也不会多一行）；
//   - FavoriteState 读「单条条目的状态」——详情页要知道「我收藏了没」与「一共多少人收藏」；
//   - ListFavorites 读「我的收藏」列表。
//
// 口径：收藏**按账号**（M7 的地基），而「收藏数」是**全站**视角 —— 两个数字都从同一张表来，
// 不会漂移（多写一个计数器列就一定会漂移）。

// SetFavorite 收藏 / 取消收藏一个条目（幂等）。
func (s *Store) SetFavorite(ctx context.Context, userID, itemID int64, favorite bool) error {
	if userID <= 0 || itemID <= 0 {
		return errors.New("用户或条目非法")
	}
	if favorite {
		if _, err := s.pool.Exec(ctx,
			`insert into favorites (user_id, item_id) values ($1, $2)
			 on conflict (user_id, item_id) do nothing`, userID, itemID); err != nil {
			return fmt.Errorf("收藏失败: %w", err)
		}
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`delete from favorites where user_id = $1 and item_id = $2`, userID, itemID); err != nil {
		return fmt.Errorf("取消收藏失败: %w", err)
	}
	return nil
}

// FavoriteState 返回「这个账号收藏了没」与**全站收藏数**（详情页两个都要显示）。
//
// 一次查询取两个数（子查询），省一次往返；条目不存在时两个值分别是 false / 0。
func (s *Store) FavoriteState(ctx context.Context, userID, itemID int64) (bool, int, error) {
	var mine bool
	var total int
	err := s.pool.QueryRow(ctx,
		`select exists (select 1 from favorites where user_id = $1 and item_id = $2),
		        (select count(*)::int from favorites where item_id = $2)`,
		userID, itemID).Scan(&mine, &total)
	if err != nil {
		return false, 0, fmt.Errorf("读取收藏状态失败: %w", err)
	}
	return mine, total, nil
}

// ListFavorites 列出某个账号的收藏（按收藏时间倒序）。
//
// kind 为空表示不限类型；`deleted_at is null` 把已经扫不见的条目挡在外面
// （文件被删掉之后收藏行还在，但用户不该看到一个点不开的条目）。
func (s *Store) ListFavorites(ctx context.Context, userID int64, kind string, limit, offset int) ([]Item, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	kind = strings.TrimSpace(kind)

	const cond = `f.user_id = $1
		and media_items.deleted_at is null
		and ($2::text = '' or media_items.kind = $2)`

	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 join favorites f on f.item_id = media_items.id
		 where `+cond+`
		 order by f.created_at desc, media_items.id desc
		 limit $3 offset $4`, userID, kind, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("查询收藏失败: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := s.pool.QueryRow(ctx,
		`select count(*) from media_items
		 join favorites f on f.item_id = media_items.id
		 where `+cond, userID, kind).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计收藏失败: %w", err)
	}
	return items, total, nil
}
