package store

// 观看统计：谁看了什么、什么最热（设置页「观看统计」）。
//
// 数据源就是 playback_progress（每人每片一行：play_count / position / last_played_at），
// **不另起一张「播放事件」表** —— 那张表（0008 注释里写过理由）已经是「播放」的唯一
// 事实来源，首页「继续观看」和「已看标记」都读它；再记一份必然两处对不上。
//
// 代价是「只统计有进度行的条目」：没开播过的当然没有，而 play_count 在**开播那一刻**
// 就 +1，所以「点开看了几秒就退」也算一次 —— 这正是我们要的口径。

import (
	"context"
	"fmt"
	"time"
)

// WatchTotals 是总览数字。
type WatchTotals struct {
	TotalPlays   int64      `json:"totalPlays"`   // 所有用户的总播放次数
	WatchedItems int64      `json:"watchedItems"` // 被看过的条目数
	ActiveUsers  int64      `json:"activeUsers"`  // 有观看记录的用户数
	LastPlayedAt *time.Time `json:"lastPlayedAt,omitempty"`
}

// WatchedItem 是「看得最多的影片」排行里的一条（剧集按单集算）。
type WatchedItem struct {
	ItemID       int64      `json:"itemId"`
	Title        string     `json:"title"`
	Kind         string     `json:"kind"`
	SeriesTitle  string     `json:"seriesTitle,omitempty"`
	SeasonNum    *int32     `json:"seasonNumber,omitempty"`
	EpisodeNum   *int32     `json:"episodeNumber,omitempty"`
	Plays        int64      `json:"plays"`
	Viewers      int64      `json:"viewers"`
	LastPlayedAt *time.Time `json:"lastPlayedAt,omitempty"`
}

// Watcher 是「看得最多的用户」排行里的一条。
type Watcher struct {
	UserID       int64      `json:"userId"`
	Username     string     `json:"username"`
	DisplayName  string     `json:"displayName"`
	Plays        int64      `json:"plays"`
	Items        int64      `json:"items"`
	LastPlayedAt *time.Time `json:"lastPlayedAt,omitempty"`
}

// WatchRecord 是「谁看了什么」明细里的一条。
type WatchRecord struct {
	UserID        int64      `json:"userId"`
	Username      string     `json:"username"`
	DisplayName   string     `json:"displayName"`
	ItemID        int64      `json:"itemId"`
	Title         string     `json:"title"`
	Kind          string     `json:"kind"`
	SeriesTitle   string     `json:"seriesTitle,omitempty"`
	SeasonNum     *int32     `json:"seasonNumber,omitempty"`
	EpisodeNum    *int32     `json:"episodeNumber,omitempty"`
	Plays         int64      `json:"plays"`
	PositionTicks int64      `json:"positionTicks"`
	DurationTicks int64      `json:"durationTicks"`
	Played        bool       `json:"played"`
	LastPlayedAt  *time.Time `json:"lastPlayedAt,omitempty"`
}

// WatchTotals 聚合总览数字。
func (s *Store) WatchTotals(ctx context.Context) (WatchTotals, error) {
	var t WatchTotals
	err := s.pool.QueryRow(ctx,
		`select coalesce(sum(play_count), 0)::bigint,
		        count(distinct item_id)::bigint,
		        count(distinct user_id)::bigint,
		        max(last_played_at)
		 from playback_progress`).
		Scan(&t.TotalPlays, &t.WatchedItems, &t.ActiveUsers, &t.LastPlayedAt)
	if err != nil {
		return WatchTotals{}, fmt.Errorf("统计观看总览失败: %w", err)
	}
	return t, nil
}

// TopWatchedItems 按总播放次数排「最热影片」（并列时看有多少不同的人看过）。
func (s *Store) TopWatchedItems(ctx context.Context, limit int) ([]WatchedItem, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx,
		`select mi.id, mi.title, mi.kind, coalesce(ser.title, ''),
		        mi.season_number, mi.episode_number,
		        sum(pp.play_count)::bigint, count(distinct pp.user_id)::bigint,
		        max(pp.last_played_at)
		 from playback_progress pp
		 join media_items mi on mi.id = pp.item_id
		 left join media_items ser on ser.id = mi.series_id
		 where pp.play_count > 0
		 group by mi.id, mi.title, mi.kind, ser.title, mi.season_number, mi.episode_number
		 order by sum(pp.play_count) desc, count(distinct pp.user_id) desc,
		          max(pp.last_played_at) desc nulls last, mi.id
		 limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("统计热门影片失败: %w", err)
	}
	defer rows.Close()

	out := make([]WatchedItem, 0, limit)
	for rows.Next() {
		var w WatchedItem
		if err := rows.Scan(&w.ItemID, &w.Title, &w.Kind, &w.SeriesTitle,
			&w.SeasonNum, &w.EpisodeNum, &w.Plays, &w.Viewers, &w.LastPlayedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// TopWatchers 按总播放次数排「看得最多的用户」。
func (s *Store) TopWatchers(ctx context.Context, limit int) ([]Watcher, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx,
		`select u.id, u.username, u.display_name,
		        coalesce(sum(pp.play_count), 0)::bigint, count(*)::bigint,
		        max(pp.last_played_at)
		 from playback_progress pp
		 join users u on u.id = pp.user_id
		 group by u.id, u.username, u.display_name
		 order by sum(pp.play_count) desc, count(*) desc,
		          max(pp.last_played_at) desc nulls last, u.id
		 limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("统计活跃用户失败: %w", err)
	}
	defer rows.Close()

	out := make([]Watcher, 0, limit)
	for rows.Next() {
		var w Watcher
		if err := rows.Scan(&w.UserID, &w.Username, &w.DisplayName,
			&w.Plays, &w.Items, &w.LastPlayedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListWatchRecords 查「谁看了什么」明细（userID = 0 表示不按用户过滤），
// 按最近播放倒序，并返回符合条件的总数（用于分页）。
func (s *Store) ListWatchRecords(ctx context.Context, userID int64, limit, offset int) ([]WatchRecord, int, error) {
	if limit <= 0 {
		limit = 50
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		`select count(*) from playback_progress where ($1 = 0 or user_id = $1)`,
		userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计观看记录失败: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`select pp.user_id, u.username, u.display_name,
		        pp.item_id, mi.title, mi.kind, coalesce(ser.title, ''),
		        mi.season_number, mi.episode_number,
		        pp.play_count, pp.position_ticks, pp.duration_ticks, pp.played, pp.last_played_at
		 from playback_progress pp
		 join users u on u.id = pp.user_id
		 join media_items mi on mi.id = pp.item_id
		 left join media_items ser on ser.id = mi.series_id
		 where ($1 = 0 or pp.user_id = $1)
		 order by pp.last_played_at desc nulls last, pp.updated_at desc
		 limit $2 offset $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("查询观看记录失败: %w", err)
	}
	defer rows.Close()

	out := make([]WatchRecord, 0, limit)
	for rows.Next() {
		var w WatchRecord
		if err := rows.Scan(&w.UserID, &w.Username, &w.DisplayName,
			&w.ItemID, &w.Title, &w.Kind, &w.SeriesTitle,
			&w.SeasonNum, &w.EpisodeNum,
			&w.Plays, &w.PositionTicks, &w.DurationTicks, &w.Played, &w.LastPlayedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, w)
	}
	return out, total, rows.Err()
}
