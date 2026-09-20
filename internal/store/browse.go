package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 本文件是「海报墙 + 剧集视图」与「批量操作」要的存储能力。

// ChildSummary 是一个条目的子项列表 + 每个子项自己的子项数。
//
// 剧集视图要显示「第 1 季 · 12 集」，所以子项数跟子项一起返回：
// 否则界面要为每一季再发一次请求（网盘上点开一季要等好几个来回）。
type ChildSummary struct {
	Items []Item `json:"items"`
	// Counts 的键是子项 id，值是它的子项数（目前只有「季 → 集数」有意义）。
	Counts map[int64]int `json:"counts"`
}

// ChildrenOf 返回一个条目的直接子项，按季号/集号排序。
//
// 层级：剧集(series) → 季(season) → 集(episode)。媒体库里没有季目录时，
// 集可能直接挂在剧集下（扫描器按目录结构推断），所以两种都如实返回。
func (s *Store) ChildrenOf(ctx context.Context, itemID int64) (*ChildSummary, error) {
	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 where parent_id = $1 and deleted_at is null
		 order by season_number nulls last, episode_number nulls last, sort_title, title, id`,
		itemID)
	if err != nil {
		return nil, fmt.Errorf("读取子项失败: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}

	out := &ChildSummary{Items: items, Counts: map[int64]int{}}
	if len(items) == 0 {
		return out, nil
	}

	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	// 一次把所有子项的子项数查出来（GROUP BY parent_id），不在界面上逐条请求
	rows2, err := s.pool.Query(ctx,
		`select parent_id, count(*) from media_items
		 where parent_id = any($1) and deleted_at is null
		 group by parent_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("统计子项数量失败: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var parent int64
		var n int
		if err := rows2.Scan(&parent, &n); err != nil {
			return nil, err
		}
		out.Counts[parent] = n
	}
	return out, rows2.Err()
}

// ---------------------------------------------------------------- 批量操作

// BatchResult 是一次批量操作的结果。
type BatchResult struct {
	// Applied 是真正生效的条目数（新排上队 / 标记成功 / force 升级）。
	Applied int `json:"applied"`
	// Skipped 是没生效的条目数：不存在，或（force 时）队列里已经有它在跑。
	Skipped int `json:"skipped"`
}

// maxBatchItems 是一次批量操作的条目数上限。
//
// 界面上「全选本页」最多也就一两百条；设上限是为了防一个手写的请求一次塞几万条进来，
// 把任务队列灌满。
const maxBatchItems = 500

// EnqueueItemScrapes 给一批条目排刮削任务（界面上的「批量重新刮削」）。
//
// 用一条 `insert ... select` 排完：逐条 Enqueue 在几百条时要跑几百个来回，
// 而这是用户点一下就要看到反馈的操作。
func (s *Store) EnqueueItemScrapes(ctx context.Context, itemIDs []int64, force bool) (BatchResult, error) {
	var res BatchResult
	ids, err := normalizeIDs(itemIDs)
	if err != nil {
		return res, err
	}
	if len(ids) == 0 {
		return res, nil
	}

	// dedupe key 与单条入队（EnqueueItemScrape）完全一致：item:<id>
	tag, err := s.pool.Exec(ctx,
		`insert into tasks (kind, payload, dedupe_key, priority)
		 select $1, jsonb_build_object('itemId', i.id), 'item:' || i.id, $2
		 from media_items i
		 where i.id = any($3) and i.deleted_at is null
		 on conflict do nothing`,
		TaskKindScrape, ScrapeTaskPriority, ids)
	if err != nil {
		return res, fmt.Errorf("批量入队刮削任务失败: %w", err)
	}
	res.Applied = int(tag.RowsAffected())

	if force {
		// 队列里已经有这些条目时（上面没插进去），把 pending 的那条升级成 force：
		// 与单条入队同一套语义，免得用户点了「重新刮削」却因为任务早就在队列里而没反应。
		keys := make([]string, 0, len(ids))
		for _, id := range ids {
			keys = append(keys, fmt.Sprintf("item:%d", id))
		}
		up, err := s.pool.Exec(ctx,
			`update tasks set payload = payload || '{"force": true}'::jsonb, run_at = now()
			 where kind = $1 and dedupe_key = any($2) and state = 'pending'`,
			TaskKindScrape, keys)
		if err != nil {
			return res, fmt.Errorf("批量升级为 force 失败: %w", err)
		}
		res.Applied += int(up.RowsAffected())
	}

	res.Skipped = len(ids) - res.Applied
	return res, nil
}

// MarkItemsUnmatched 把一批条目标记为「人工确认过：不需要自动匹配」。
//
// 只动 movie/series：季与集是按位置对应的，没有「需不需要匹配」这回事。
// 字段与单条版（scrape.MarkUnmatched → SaveMatchOutcome）保持一致：
// 状态 manual、原因写进 scrape_error、尝试次数 +1、记时间。
func (s *Store) MarkItemsUnmatched(ctx context.Context, itemIDs []int64, reason string) (BatchResult, error) {
	var res BatchResult
	ids, err := normalizeIDs(itemIDs)
	if err != nil {
		return res, err
	}
	if len(ids) == 0 {
		return res, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "人工标记：不需要自动匹配"
	}
	tag, err := s.pool.Exec(ctx,
		`update media_items
		 set match_state = 'manual', scrape_error = $2,
		     scrape_attempts = scrape_attempts + 1, last_scraped_at = now(), updated_at = now()
		 where id = any($1) and kind in ('movie', 'series') and deleted_at is null`,
		ids, reason)
	if err != nil {
		return res, fmt.Errorf("批量标记不需要匹配失败: %w", err)
	}
	res.Applied = int(tag.RowsAffected())
	res.Skipped = len(ids) - res.Applied
	return res, nil
}

// normalizeIDs 去重、丢掉非法值，并检查条数上限。
func normalizeIDs(itemIDs []int64) ([]int64, error) {
	if len(itemIDs) > maxBatchItems {
		return nil, fmt.Errorf("一次最多处理 %d 条（收到 %d 条）", maxBatchItems, len(itemIDs))
	}
	seen := make(map[int64]bool, len(itemIDs))
	out := make([]int64, 0, len(itemIDs))
	for _, id := range itemIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// ---------------------------------------------------------------- 系统信息

// SystemInfo 是设置页「系统信息」卡片要显示的东西。
//
// 把数据库字符集放在这里是有意的：中文检索出问题时，第一眼就该看到它
// （编码不是 UTF8 或 lc_ctype 是 C，都会让搜索静默少结果，见 store.go 的 checkEncoding）。
type SystemInfo struct {
	DatabaseEncoding string    `json:"databaseEncoding"`
	DatabaseCollate  string    `json:"databaseCollate"`
	DatabaseCtype    string    `json:"databaseCtype"`
	SchemaVersion    int64     `json:"schemaVersion"`
	ItemCount        int64     `json:"itemCount"`
	FileCount        int64     `json:"fileCount"`
	ImageCount       int64     `json:"imageCount"`
	TaskPending      int64     `json:"taskPending"`
	ServerTime       time.Time `json:"serverTime"`
}

// SystemInfoOf 读取系统信息（只读）。
func (s *Store) SystemInfoOf(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	err := s.pool.QueryRow(ctx,
		`select current_setting('server_encoding'),
		        (select datcollate from pg_database where datname = current_database()),
		        (select datctype from pg_database where datname = current_database()),
		        coalesce((select max(version) from schema_migrations), 0),
		        (select count(*) from media_items where deleted_at is null),
		        (select count(*) from media_files where deleted_at is null),
		        (select count(*) from images),
		        (select count(*) from tasks where state in ('pending', 'running')),
		        now()`).
		Scan(&info.DatabaseEncoding, &info.DatabaseCollate, &info.DatabaseCtype,
			&info.SchemaVersion, &info.ItemCount, &info.FileCount, &info.ImageCount,
			&info.TaskPending, &info.ServerTime)
	if err != nil {
		return nil, fmt.Errorf("读取系统信息失败: %w", err)
	}
	return &info, nil
}
