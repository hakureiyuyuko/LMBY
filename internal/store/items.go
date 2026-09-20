package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// ApplyItemMeta 写入元数据（供 nfo 导入与将来的刮削使用）。
func (s *Store) ApplyItemMeta(ctx context.Context, itemID int64, m ItemMeta) error {
	_, err := s.pool.Exec(ctx,
		`update media_items set
		   title          = coalesce(nullif($2, ''), title),
		   sort_title     = coalesce(nullif($3, ''), sort_title),
		   original_title = coalesce(nullif($4, ''), original_title),
		   year           = coalesce($5, year),
		   overview       = coalesce(nullif($6, ''), overview),
		   tagline        = coalesce(nullif($7, ''), tagline),
		   runtime_ticks  = coalesce($8, runtime_ticks),
		   community_rating = coalesce($9, community_rating),
		   official_rating = coalesce(nullif($10, ''), official_rating),
		   genres         = case when jsonb_array_length($11::jsonb) > 0 then $11::jsonb else genres end,
		   tags           = case when jsonb_array_length($12::jsonb) > 0 then $12::jsonb else tags end,
		   studios        = case when jsonb_array_length($13::jsonb) > 0 then $13::jsonb else studios end,
		   provider_ids   = case when $14::jsonb <> '{}'::jsonb then $14::jsonb else provider_ids end,
		   premiere_date  = coalesce($15, premiere_date),
		   match_state    = coalesce(nullif($16, ''), match_state),
		   updated_at     = now()
		 where id = $1`,
		itemID, m.Title, m.SortTitle, m.OriginalTitle, m.Year, m.Overview, m.Tagline,
		m.RuntimeTicks, m.Rating, m.OfficialRating,
		jsonArray(m.Genres), jsonArray(m.Tags), jsonArray(m.Studios),
		jsonMap(m.ProviderIDs), m.PremiereDate, m.MatchState)
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
		        episode_end, extra_type, title, sort_title, original_title, year, overview,
		        tagline, runtime_ticks, community_rating, official_rating,
		        genres, tags, studios, provider_ids, file_tech, match_state,
		        match_score, metadata_source, locked_fields, scrape_error, last_scraped_at,
		        updated_at
		 from media_items where id = $1 and deleted_at is null`, id).
		Scan(&it.ID, &it.LibraryID, &it.Kind, &it.ParentID, &it.SeriesID, &it.SeasonNum,
			&it.EpisodeNum, &it.EpisodeEnd, &it.ExtraType, &it.Title, &it.SortTitle,
			&it.OriginalTitle, &it.Year, &it.Overview, &it.Tagline, &it.RuntimeTicks,
			&it.Rating, &it.OfficialRated, &it.Genres, &it.Tags, &it.Studios,
			&it.ProviderIDs, &it.FileTech, &it.MatchState, &it.MatchScore,
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

// ListItems 分页列出一个库的条目。
func (s *Store) ListItems(ctx context.Context, libraryID int64, kind string, limit, offset int) ([]Item, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`select id, library_id, kind, parent_id, series_id, season_number, episode_number,
		        episode_end, extra_type, title, sort_title, original_title, year, overview,
		        tagline, runtime_ticks, community_rating, official_rating,
		        genres, tags, studios, provider_ids, file_tech, match_state, updated_at
		 from media_items
		 where library_id = $1 and deleted_at is null
		   and ($2 = '' or kind = $2)
		 order by kind, sort_title, title, season_number nulls last, episode_number nulls last, id
		 limit $3 offset $4`, libraryID, kind, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("查询条目列表失败: %w", err)
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.LibraryID, &it.Kind, &it.ParentID, &it.SeriesID,
			&it.SeasonNum, &it.EpisodeNum, &it.EpisodeEnd, &it.ExtraType, &it.Title,
			&it.SortTitle, &it.OriginalTitle, &it.Year, &it.Overview, &it.Tagline,
			&it.RuntimeTicks, &it.Rating, &it.OfficialRated, &it.Genres, &it.Tags,
			&it.Studios, &it.ProviderIDs, &it.FileTech, &it.MatchState, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountItems 统计一个库的条目数。
func (s *Store) CountItems(ctx context.Context, libraryID int64, kind string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`select count(*) from media_items
		 where library_id = $1 and deleted_at is null and ($2 = '' or kind = $2)`,
		libraryID, kind).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计条目失败: %w", err)
	}
	return n, nil
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

// ---------------------------------------------------------------- 图片

// UpsertImage 登记一张图片（只存路径与元信息，二进制永不入库）。
func (s *Store) UpsertImage(ctx context.Context, itemID int64, kind, path string, size, mtimeNS int64) error {
	_, err := s.pool.Exec(ctx,
		`insert into images (item_id, kind, path, size_bytes, mtime_ns)
		 values ($1, $2, $3, $4, $5)
		 on conflict (item_id, kind, path) do update
		   set size_bytes = excluded.size_bytes, mtime_ns = excluded.mtime_ns`,
		itemID, kind, path, size, mtimeNS)
	if err != nil {
		return fmt.Errorf("登记图片失败: %w", err)
	}
	return nil
}

// DeleteImagesExcept 删除某个条目下不在给定路径集合中、且来自本地文件的图片记录。
//
// 只删 source='local'：远程下载图与用户上传图不能因为「本轮扫描没看到」而被清掉。
func (s *Store) DeleteImagesExcept(ctx context.Context, itemID int64, keepPaths []string) (int64, error) {
	if keepPaths == nil {
		keepPaths = []string{}
	}
	tag, err := s.pool.Exec(ctx,
		`delete from images
		 where item_id = $1 and source = 'local' and not (path = any($2))`, itemID, keepPaths)
	if err != nil {
		return 0, fmt.Errorf("清理图片记录失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountImages 统计图片数（库列表展示用）。
func (s *Store) CountImages(ctx context.Context, libraryID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`select count(*) from images g
		 join media_items i on i.id = g.item_id
		 where i.library_id = $1`, libraryID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计图片失败: %w", err)
	}
	return n, nil
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
