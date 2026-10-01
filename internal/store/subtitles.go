package store

import (
	"context"
	"fmt"
)

// Subtitle 是一条外挂字幕的登记信息。二进制永不入库，只存路径与元信息。
//
// Language 存的是文件名里的原始标记（chs / cht / zh-CN / eng…），不做归一化 ——
// 跟媒体目录里的命名保持一致，界面上「#12 ass chs」比「#12 ass zh-Hans」更好认。
// 认不出来的标记不会被塞进 Language，而是落进 Title（见 parser.ParseSubtitle）。
type Subtitle struct {
	ID        int64  `json:"id"`
	ItemID    int64  `json:"itemId"`
	Path      string `json:"path"`
	Language  string `json:"language,omitempty"`
	Title     string `json:"title,omitempty"`
	Format    string `json:"format"`
	Forced    bool   `json:"forced,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
}

// ListSubtitles 列出某个条目的全部外挂字幕，按登记顺序（id）排。
//
// 顺序必须稳定：播放器用「序号 = 合成基数 + 在列表里的位置」来寻址外挂字幕，
// 顺序一变，已经开着的播放页就会指到另一条上去。
func (s *Store) ListSubtitles(ctx context.Context, itemID int64) ([]Subtitle, error) {
	rows, err := s.pool.Query(ctx,
		`select id, item_id, path, language, title, format, forced, coalesce(size_bytes, 0)
		 from subtitles where item_id = $1
		 order by id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("读取外挂字幕列表失败: %w", err)
	}
	defer rows.Close()

	var out []Subtitle
	for rows.Next() {
		var sub Subtitle
		if err := rows.Scan(&sub.ID, &sub.ItemID, &sub.Path, &sub.Language,
			&sub.Title, &sub.Format, &sub.Forced, &sub.SizeBytes); err != nil {
			return nil, fmt.Errorf("读取外挂字幕行失败: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// UpsertSubtitle 登记一条外挂字幕。
func (s *Store) UpsertSubtitle(ctx context.Context, itemID int64, path, language, title, format string, forced bool, size, mtimeNS int64) error {
	_, err := s.pool.Exec(ctx,
		`insert into subtitles (item_id, path, language, title, format, forced, size_bytes, mtime_ns)
		 values ($1, $2, $3, $4, $5, $6, $7, $8)
		 on conflict (item_id, path) do update
		   set language = excluded.language,
		       title = excluded.title,
		       format = excluded.format,
		       forced = excluded.forced,
		       size_bytes = excluded.size_bytes,
		       mtime_ns = excluded.mtime_ns`,
		itemID, path, language, title, format, forced, size, mtimeNS)
	if err != nil {
		return fmt.Errorf("登记外挂字幕失败: %w", err)
	}
	return nil
}

// ListSubtitleItemIDs 列出「当前库里存有外挂字幕记录」的全部条目 id。
//
// 扫描收尾时用它把清理范围压到「本来就有记录的条目」上 —— 没记录的条目不用为它发 delete，
// 几万个条目逐个清理是扛不住的。
func (s *Store) ListSubtitleItemIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `select distinct item_id from subtitles`)
	if err != nil {
		return nil, fmt.Errorf("读取外挂字幕条目失败: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("读取外挂字幕条目失败: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteSubtitlesExcept 删掉某个条目下本轮没再看到的外挂字幕记录。
//
// 外挂字幕只有本地来源，所以不用像图片那样区分 source。
func (s *Store) DeleteSubtitlesExcept(ctx context.Context, itemID int64, keepPaths []string) (int64, error) {
	if keepPaths == nil {
		keepPaths = []string{}
	}
	tag, err := s.pool.Exec(ctx,
		`delete from subtitles where item_id = $1 and not (path = any($2))`, itemID, keepPaths)
	if err != nil {
		return 0, fmt.Errorf("清理外挂字幕记录失败: %w", err)
	}
	return tag.RowsAffected(), nil
}
