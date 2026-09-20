package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Item 是媒体库里的一个条目。字段很多，但都是「元数据的自然形状」，
// 拆表只会让查询变复杂而没有实际收益。
type Item struct {
	ID         int64  `json:"id"`
	LibraryID  int64  `json:"libraryId"`
	Kind       string `json:"kind"`
	ParentID   *int64 `json:"parentId,omitempty"`
	SeriesID   *int64 `json:"seriesId,omitempty"`
	SeasonNum  *int32 `json:"seasonNumber,omitempty"`
	EpisodeNum *int32 `json:"episodeNumber,omitempty"`
	EpisodeEnd *int32 `json:"episodeEnd,omitempty"`
	ExtraType  string `json:"extraType,omitempty"`

	Title         string            `json:"title"`
	SortTitle     string            `json:"sortTitle,omitempty"`
	OriginalTitle string            `json:"originalTitle,omitempty"`
	Year          *int32            `json:"year,omitempty"`
	PremiereDate  *time.Time        `json:"premiereDate,omitempty"`
	Overview      string            `json:"overview,omitempty"`
	Tagline       string            `json:"tagline,omitempty"`
	RuntimeTicks  *int64            `json:"runtimeTicks,omitempty"`
	Rating        *float64          `json:"communityRating,omitempty"`
	OfficialRated string            `json:"officialRating,omitempty"`
	Genres        []string          `json:"genres"`
	Tags          []string          `json:"tags"`
	Studios       []string          `json:"studios"`
	ProviderIDs   map[string]string `json:"providerIds"`
	FileTech      map[string]any    `json:"fileTech"`
	MatchState    string            `json:"matchState"`

	// 刮削相关。MatchScore 是匹配打分的总分（0~1），
	// LockedFields 里列出的字段名在重扫时不允许被覆盖。
	MatchScore     *float64   `json:"matchScore,omitempty"`
	MetadataSource string     `json:"metadataSource,omitempty"`
	LockedFields   []string   `json:"lockedFields"`
	ScrapeError    string     `json:"scrapeError,omitempty"`
	LastScrapedAt  *time.Time `json:"lastScrapedAt,omitempty"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// NewItem 是插入条目时需要的最小信息集。
type NewItem struct {
	LibraryID  int64
	Kind       string
	ParentID   *int64
	SeriesID   *int64
	SeasonNum  *int32
	EpisodeNum *int32
	EpisodeEnd *int32
	ExtraType  string
	Title      string
	Year       *int32
}

// ItemMeta 是从 nfo 或刮削结果得到的元数据集合。
type ItemMeta struct {
	Title          string
	SortTitle      string
	OriginalTitle  string
	Year           *int32
	Overview       string
	Tagline        string
	RuntimeTicks   *int64
	Rating         *float64
	OfficialRating string
	Genres         []string
	Tags           []string
	Studios        []string
	ProviderIDs    map[string]string
	PremiereDate   *time.Time
	MatchState     string
	// MetadataSource 记录元数据是谁写的（nfo / tmdb / manual）。
	// 空表示不改动已有的来源标记。
	MetadataSource string
}

// LibraryFile 是参与增量比对的物理文件。
type LibraryFile struct {
	ID         int64
	ItemID     int64
	Path       string
	SizeBytes  int64
	MtimeNS    int64
	ProbeState string
}

// ---------------------------------------------------------------- 条目读写

// InsertItem 插入条目并返回 id。
func (s *Store) InsertItem(ctx context.Context, in NewItem) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`insert into media_items
		   (library_id, kind, parent_id, series_id, season_number, episode_number,
		    episode_end, extra_type, title, year)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 returning id`,
		in.LibraryID, in.Kind, in.ParentID, in.SeriesID, in.SeasonNum,
		in.EpisodeNum, in.EpisodeEnd, in.ExtraType, in.Title, in.Year).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("插入条目失败: %w", err)
	}
	return id, nil
}

// applyItemMetaSQL 是写入元数据的语句（供 nfo 导入、刮削与人工指定候选使用）。
//
// 两条语义都**只在这里实现一次**：
//
//  1. **字段锁定**：`locked_fields ? '<字段名>'` 为真时保持原值。
//     调用方不止一处（nfo 重读、自动刮削、人工指定候选），分散判断迟早会漏一个 ——
//     漏掉的后果是「用户手改并锁住的数据被悄悄覆盖」，而这从数据上看不出来。
//     所以执行点放在 SQL 里，谁调用都绕不过去。
//  2. **空值不覆盖**：coalesce(nullif(...)) —— 调用方只填自己知道的字段。
//     与人工编辑（UpdateItemFields：写什么就是什么）正好相反，两者不要混用。
//
// tags 没有对应的可锁字段（界面上不开放编辑），因此没有锁判断。
const applyItemMetaSQL = `update media_items set
		   title          = case when locked_fields ? 'title' then title else coalesce(nullif($2, ''), title) end,
		   sort_title     = case when locked_fields ? 'title' then sort_title else coalesce(nullif($3, ''), sort_title) end,
		   original_title = case when locked_fields ? 'originalTitle' then original_title else coalesce(nullif($4, ''), original_title) end,
		   year           = case when locked_fields ? 'year' then year else coalesce($5, year) end,
		   overview       = case when locked_fields ? 'overview' then overview else coalesce(nullif($6, ''), overview) end,
		   tagline        = case when locked_fields ? 'tagline' then tagline else coalesce(nullif($7, ''), tagline) end,
		   runtime_ticks  = case when locked_fields ? 'runtime' then runtime_ticks else coalesce($8, runtime_ticks) end,
		   community_rating = case when locked_fields ? 'rating' then community_rating else coalesce($9, community_rating) end,
		   official_rating = case when locked_fields ? 'officialRating' then official_rating else coalesce(nullif($10, ''), official_rating) end,
		   genres         = case when locked_fields ? 'genres' then genres when jsonb_array_length($11::jsonb) > 0 then $11::jsonb else genres end,
		   tags           = case when jsonb_array_length($12::jsonb) > 0 then $12::jsonb else tags end,
		   studios        = case when locked_fields ? 'studios' then studios when jsonb_array_length($13::jsonb) > 0 then $13::jsonb else studios end,
		   provider_ids   = case when locked_fields ? 'providerIds' then provider_ids when $14::jsonb <> '{}'::jsonb then $14::jsonb else provider_ids end,
		   premiere_date  = case when locked_fields ? 'premiereDate' then premiere_date else coalesce($15, premiere_date) end,
		   match_state    = coalesce(nullif($16, ''), match_state),
		   metadata_source = coalesce(nullif($17, ''), metadata_source),
		   updated_at     = now()
		 where id = $1`

// ApplyItemMeta 写入元数据（供 nfo 导入、刮削与人工指定候选使用）。
//
// 值合并与字段锁定的规则见 applyItemMetaSQL 的注释。
func (s *Store) ApplyItemMeta(ctx context.Context, itemID int64, m ItemMeta) error {
	_, err := s.pool.Exec(ctx, applyItemMetaSQL,
		itemID, m.Title, m.SortTitle, m.OriginalTitle, m.Year, m.Overview, m.Tagline,
		m.RuntimeTicks, m.Rating, m.OfficialRating,
		jsonArray(m.Genres), jsonArray(m.Tags), jsonArray(m.Studios),
		jsonMap(m.ProviderIDs), m.PremiereDate, m.MatchState, m.MetadataSource)
	if err != nil {
		// 唯一约束冲突（介质库里有同名同年条目）要能被上层识别。
		//
		// 场景：刮削把条目改名成了 TMDB 的规范标题，而库里已经有一条同名同年的
		// （同一个作品被扫成了两个条目）。这是「需要人工确认是否重复」的业务结论，
		// 不是可以重试的环境问题 —— 不区分的话任务会白白重试到 max_attempts。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: %w", ErrAlreadyExists, err)
		}
		return fmt.Errorf("写入条目元数据失败: %w", err)
	}
	return nil
}

// SetItemFileTech 记录从文件名解析出的技术标记。
func (s *Store) SetItemFileTech(ctx context.Context, itemID int64, tech map[string]any) error {
	if len(tech) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`update media_items set file_tech = $2::jsonb, updated_at = now() where id = $1`,
		itemID, jsonMapAny(tech))
	return err
}

// FindSeriesID 按（库, 标题, 年份）查剧集条目。
func (s *Store) FindSeriesID(ctx context.Context, libraryID int64, title string, year *int32) (int64, error) {
	return s.findItemID(ctx, libraryID, "series", title, year)
}

// FindMovieID 按（库, 标题, 年份）查电影条目。
func (s *Store) FindMovieID(ctx context.Context, libraryID int64, title string, year *int32) (int64, error) {
	return s.findItemID(ctx, libraryID, "movie", title, year)
}

func (s *Store) findItemID(ctx context.Context, libraryID int64, kind, title string, year *int32) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`select id from media_items
		 where library_id = $1 and kind = $2 and lower(title) = lower($3)
		   and coalesce(year, 0) = coalesce($4, 0) and deleted_at is null
		 limit 1`, libraryID, kind, title, year).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("查询条目失败: %w", err)
	}
	return id, nil
}

// FindSeasonID 按（剧集, 季号）查季条目。
func (s *Store) FindSeasonID(ctx context.Context, seriesID int64, season int32) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`select id from media_items
		 where parent_id = $1 and kind = 'season' and season_number = $2 and deleted_at is null
		 limit 1`, seriesID, season).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("查询季条目失败: %w", err)
	}
	return id, nil
}

// FindEpisodeID 按（剧集, 季, 集）查集条目。
//
// 刻意不把标题纳入查询条件：集标题可能来自 nfo 或后续刮削，
// 一旦纳入，标题变化后重扫就会找不到旧条目而造出重复。
func (s *Store) FindEpisodeID(ctx context.Context, seriesID int64, season, episode int32) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`select id from media_items
		 where series_id = $1 and kind = 'episode' and season_number = $2
		   and episode_number = $3 and deleted_at is null
		 limit 1`, seriesID, season, episode).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("查询集条目失败: %w", err)
	}
	return id, nil
}

// FindExtraID 按（父条目, 标题）查花絮条目。
func (s *Store) FindExtraID(ctx context.Context, parentID int64, title string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`select id from media_items
		 where parent_id = $1 and kind = 'extra' and lower(title) = lower($2) and deleted_at is null
		 limit 1`, parentID, title).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("查询花絮条目失败: %w", err)
	}
	return id, nil
}

// GetItem 按 id 读取一个条目（包含刮削相关的字段，供刮削处理器与编辑界面使用）。
func (s *Store) GetItem(ctx context.Context, id int64) (*Item, error) {
	var it Item
	err := s.pool.QueryRow(ctx,
		`select id, library_id, kind, parent_id, series_id, season_number, episode_number,
		        episode_end, extra_type, title, sort_title, original_title, year, premiere_date,
		        overview, tagline, runtime_ticks, community_rating, official_rating,
		        genres, tags, studios, provider_ids, file_tech, match_state,
		        match_score, metadata_source, locked_fields, scrape_error, last_scraped_at,
		        updated_at
		 from media_items where id = $1 and deleted_at is null`, id).
		Scan(&it.ID, &it.LibraryID, &it.Kind, &it.ParentID, &it.SeriesID, &it.SeasonNum,
			&it.EpisodeNum, &it.EpisodeEnd, &it.ExtraType, &it.Title, &it.SortTitle,
			&it.OriginalTitle, &it.Year, &it.PremiereDate, &it.Overview, &it.Tagline,
			&it.RuntimeTicks, &it.Rating, &it.OfficialRated, &it.Genres, &it.Tags,
			&it.Studios, &it.ProviderIDs, &it.FileTech, &it.MatchState, &it.MatchScore,
			&it.MetadataSource, &it.LockedFields, &it.ScrapeError, &it.LastScrapedAt,
			&it.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询条目 %d 失败: %w", id, err)
	}
	return &it, nil
}

// itemListColumns 是列表类查询（条目列表、海报墙、子项）统一读的列。
//
// 刻意不用 `select *`：media_items 里还有 search_vec（tsvector）之类没法扫进 Item 的列，
// 而且列顺序一变扫描会静默错位。加列时只要同步改 scanItems。
const itemListColumns = `id, library_id, kind, parent_id, series_id, season_number,
	episode_number, episode_end, extra_type, title, sort_title, original_title,
	year, premiere_date, overview, tagline, runtime_ticks, community_rating,
	official_rating, genres, tags, studios, provider_ids, file_tech, match_state,
	match_score, metadata_source, scrape_error, updated_at`

// scanItems 按 itemListColumns 的顺序扫出条目，并负责关掉 rows。
func scanItems(rows pgx.Rows) ([]Item, error) {
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.LibraryID, &it.Kind, &it.ParentID, &it.SeriesID, &it.SeasonNum,
			&it.EpisodeNum, &it.EpisodeEnd, &it.ExtraType, &it.Title, &it.SortTitle,
			&it.OriginalTitle, &it.Year, &it.PremiereDate, &it.Overview, &it.Tagline,
			&it.RuntimeTicks, &it.Rating, &it.OfficialRated, &it.Genres, &it.Tags,
			&it.Studios, &it.ProviderIDs, &it.FileTech, &it.MatchState, &it.MatchScore,
			&it.MetadataSource, &it.ScrapeError, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ItemFilter 是列条目的过滤条件（零值 = 不过滤）。
type ItemFilter struct {
	// Kind 限定 movie / series / season / episode 等。
	Kind string
	// MatchState 命中其中任一状态即算匹配（人工匹配界面要同时看 review 与 failed）。
	MatchState []string
	// TopLevel 只取顶层条目（没有父项）—— 海报墙要的是电影与剧集，
	// 不是它们下面的季与集。
	TopLevel bool
	// Sort 决定排序（见 itemOrderBy），空 = 类型 + 标题。
	Sort string
}

// itemWhere 拼出列条目的 where 子句与参数。
func itemWhere(libraryID int64, f ItemFilter) (string, []any) {
	where := []string{"library_id = $1", "deleted_at is null"}
	args := []any{libraryID}
	if f.Kind != "" {
		args = append(args, f.Kind)
		where = append(where, fmt.Sprintf("kind = $%d", len(args)))
	}
	if len(f.MatchState) > 0 {
		args = append(args, f.MatchState)
		where = append(where, fmt.Sprintf("match_state = any($%d)", len(args)))
	}
	if f.TopLevel {
		where = append(where, "parent_id is null")
	}
	return strings.Join(where, " and "), args
}

// itemOrderBy 生成列表的排序子句。
//
// 默认（空）保持原行为：按类型 + 标题 —— 人工匹配页与媒体库条目表都靠它稳定分页。
// 海报墙另外支持年份与「最近添加」。created_at 不在 itemListColumns 里，
// 但排序不需要选中它。
func itemOrderBy(sort string) string {
	switch sort {
	case "year":
		return "order by year desc nulls last, sort_title, title, id"
	case "added":
		return "order by created_at desc, id desc"
	case "title":
		return "order by sort_title, title, id"
	default:
		return "order by kind, sort_title, title, season_number nulls last, episode_number nulls last, id"
	}
}

// ListItems 分页列出一个库的条目。
func (s *Store) ListItems(ctx context.Context, libraryID int64, f ItemFilter, limit, offset int) ([]Item, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cond, args := itemWhere(libraryID, f)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 where `+cond+` `+itemOrderBy(f.Sort)+`
		 limit $`+strconv.Itoa(len(args)-1)+` offset $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("查询条目列表失败: %w", err)
	}
	return scanItems(rows)
}

// CountItems 统计一个库的条目数。
func (s *Store) CountItems(ctx context.Context, libraryID int64, f ItemFilter) (int64, error) {
	cond, args := itemWhere(libraryID, f)
	var n int64
	err := s.pool.QueryRow(ctx,
		`select count(*) from media_items where `+cond, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计条目失败: %w", err)
	}
	return n, nil
}

// MatchCandidates 读取条目的匹配候选（人工匹配界面用）。
//
// 候选是刮削时存下的（包含每个候选的打分明细），所以界面不需要重新搜一遍；
// 数据是 JSON 数组，原样透给前端。
func (s *Store) MatchCandidates(ctx context.Context, itemID int64) (json.RawMessage, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`select match_candidates from media_items where id = $1`, itemID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取匹配候选失败: %w", err)
	}
	return json.RawMessage(raw), nil
}

// ---------------------------------------------------------------- 文件读写

// ListLibraryFiles 读取一个库下所有仍有效的文件行（增量扫描的输入）。
func (s *Store) ListLibraryFiles(ctx context.Context, libraryID int64) ([]LibraryFile, error) {
	rows, err := s.pool.Query(ctx,
		`select f.id, f.item_id, f.path, f.size_bytes, f.mtime_ns, f.probe_state
		 from media_files f
		 join media_items i on i.id = f.item_id
		 where i.library_id = $1 and f.deleted_at is null`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("读取库文件列表失败: %w", err)
	}
	defer rows.Close()

	var out []LibraryFile
	for rows.Next() {
		var f LibraryFile
		if err := rows.Scan(&f.ID, &f.ItemID, &f.Path, &f.SizeBytes, &f.MtimeNS, &f.ProbeState); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertFile 新增文件行。
//
// 冲突时**不做任何更新**并返回 0 —— 说明这个路径已经属于别的条目，
// 属于异常情况（同一文件被两个条目争用），交给调用方记录 issue。
func (s *Store) InsertFile(ctx context.Context, itemID int64, path string, size, mtimeNS int64, container string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`insert into media_files (item_id, path, size_bytes, mtime_ns, container)
		 values ($1, $2, $3, $4, $5)
		 on conflict (path) do nothing
		 returning id`, itemID, path, size, mtimeNS, container).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrAlreadyExists
	}
	if err != nil {
		return 0, fmt.Errorf("插入文件失败: %w", err)
	}
	return id, nil
}

// UpdateFileChanged 文件内容变化时更新指纹，并把探测状态重置为待探测。
func (s *Store) UpdateFileChanged(ctx context.Context, fileID int64, size, mtimeNS int64) error {
	_, err := s.pool.Exec(ctx,
		`update media_files
		 set size_bytes = $2, mtime_ns = $3, probe_state = 'pending', probe_error = '',
		     deleted_at = null, updated_at = now()
		 where id = $1`, fileID, size, mtimeNS)
	return err
}

// MoveFile 把文件行改挂到新路径（移动识别：保留条目与播放进度）。
func (s *Store) MoveFile(ctx context.Context, fileID int64, newPath string) error {
	_, err := s.pool.Exec(ctx,
		`update media_files set path = $2, deleted_at = null, updated_at = now() where id = $1`,
		fileID, newPath)
	if err != nil {
		return fmt.Errorf("更新文件路径失败: %w", err)
	}
	return nil
}

// MarkFilesDeleted 把本轮未出现的文件软删除。
//
// 刻意用软删除：网络盘掉线时一次扫描会把整库标记为「消失」，
// 硬删除会造成灾难性数据丢失，留给后续的清理任务按策略真正删除。
func (s *Store) MarkFilesDeleted(ctx context.Context, fileIDs []int64) (int64, error) {
	if len(fileIDs) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`update media_files set deleted_at = now(), updated_at = now()
		 where id = any($1) and deleted_at is null`, fileIDs)
	if err != nil {
		return 0, fmt.Errorf("标记文件删除失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListPendingProbeFiles 取待探测的文件（供 ffprobe worker 使用）。
func (s *Store) ListPendingProbeFiles(ctx context.Context, limit int) ([]LibraryFile, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`select id, item_id, path, size_bytes, mtime_ns, probe_state
		 from media_files
		 where probe_state = 'pending' and deleted_at is null
		 order by id limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取待探测文件失败: %w", err)
	}
	defer rows.Close()

	var out []LibraryFile
	for rows.Next() {
		var f LibraryFile
		if err := rows.Scan(&f.ID, &f.ItemID, &f.Path, &f.SizeBytes, &f.MtimeNS, &f.ProbeState); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- JSON 小工具

func jsonArray(v []string) string {
	if v == nil {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func jsonMap(v map[string]string) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func jsonMapAny(v map[string]any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
