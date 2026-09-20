package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------- 可播放文件

// PlayableFile 是播放决策需要的文件信息。
//
// 流信息保持 JSON 原始字节：store 层不引入 internal/probe 的类型，
// 由 internal/playback 反序列化成它自己的模型（谁用谁解析，
// 避免同一份结构在两处定义后悄悄漂移）。
type PlayableFile struct {
	ID              int64
	ItemID          int64
	Path            string
	SizeBytes       int64
	MtimeNS         int64
	Container       string
	DurationTicks   int64
	ProbeState      string
	VideoStreams    []byte
	AudioStreams    []byte
	SubtitleStreams []byte
}

const playableFileColumns = `id, item_id, path, size_bytes, mtime_ns, container,
	coalesce(duration_ticks, 0), probe_state, video_streams, audio_streams, subtitle_streams`

func scanPlayableFile(row pgx.Row) (*PlayableFile, error) {
	var f PlayableFile
	err := row.Scan(&f.ID, &f.ItemID, &f.Path, &f.SizeBytes, &f.MtimeNS, &f.Container,
		&f.DurationTicks, &f.ProbeState, &f.VideoStreams, &f.AudioStreams, &f.SubtitleStreams)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取媒体文件信息失败: %w", err)
	}
	return &f, nil
}

// GetPlayableFile 按文件 id 读取文件信息。
func (s *Store) GetPlayableFile(ctx context.Context, fileID int64) (*PlayableFile, error) {
	return scanPlayableFile(s.pool.QueryRow(ctx,
		`select `+playableFileColumns+` from media_files where id = $1 and deleted_at is null`, fileID))
}

// ListPlayableFiles 读取条目下的全部文件（一个条目可能有多个版本）。
//
// 排序让「探测成功、体积大」的排在前面：决策引擎挑版本时按顺序取第一个合适的，
// 探测失败的文件看不出流信息，不该被当成首选。
func (s *Store) ListPlayableFiles(ctx context.Context, itemID int64) ([]PlayableFile, error) {
	rows, err := s.pool.Query(ctx,
		`select `+playableFileColumns+`
		 from media_files
		 where item_id = $1 and deleted_at is null
		 order by (probe_state = 'ok') desc, size_bytes desc, id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("读取条目文件列表失败: %w", err)
	}
	defer rows.Close()

	var out []PlayableFile
	for rows.Next() {
		f, err := scanPlayableFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// LibraryRootsForItem 返回条目所属媒体库的全部根路径。
//
// 播放前要用它做一次「文件确实在库根目录下」的校验：库里存的是路径字符串，
// 万一被改过（或数据库被别处写入），serve 出去的就是任意文件读。
func (s *Store) LibraryRootsForItem(ctx context.Context, itemID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`select p.path
		 from library_paths p
		 join media_items i on i.library_id = p.library_id
		 where i.id = $1
		 order by p.sort_order, p.id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("读取媒体库根路径失败: %w", err)
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

// ---------------------------------------------------------------- 播放进度

// PlaybackProgress 是一个用户在一个条目上的观看进度。
type PlaybackProgress struct {
	UserID        int64      `json:"-"`
	ItemID        int64      `json:"itemId"`
	PositionTicks int64      `json:"positionTicks"`
	DurationTicks int64      `json:"durationTicks"`
	Played        bool       `json:"played"`
	PlayCount     int32      `json:"playCount"`
	LastPlayedAt  *time.Time `json:"lastPlayedAt,omitempty"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// SavePlaybackProgress 写进度。started 为真表示这是「一次新的播放开始」：
// 播放次数 +1，并刷新 last_played_at（首页「继续观看」的排序依据）。
//
// 用 upsert 而不是先查后写：播放心跳是高频调用，两次往返既有竞态也浪费。
func (s *Store) SavePlaybackProgress(ctx context.Context, userID, itemID, position, duration int64, started bool) error {
	if position < 0 {
		position = 0
	}
	if duration < 0 {
		duration = 0
	}
	_, err := s.pool.Exec(ctx,
		`insert into playback_progress
		   (user_id, item_id, position_ticks, duration_ticks, play_count, last_played_at, updated_at)
		 values ($1, $2, $3, $4, case when $5 then 1 else 0 end,
		         case when $5 then now() else null end, now())
		 on conflict (user_id, item_id) do update set
		   position_ticks = excluded.position_ticks,
		   duration_ticks = greatest(playback_progress.duration_ticks, excluded.duration_ticks),
		   play_count     = playback_progress.play_count + case when $5 then 1 else 0 end,
		   last_played_at = case when $5 then now() else playback_progress.last_played_at end,
		   updated_at     = now()`,
		userID, itemID, position, duration, started)
	if err != nil {
		return fmt.Errorf("写入播放进度失败: %w", err)
	}
	return nil
}

// GetPlaybackProgress 读取一个用户在一个条目上的进度。没有记录时返回 nil。
func (s *Store) GetPlaybackProgress(ctx context.Context, userID, itemID int64) (*PlaybackProgress, error) {
	return scanProgress(s.pool.QueryRow(ctx,
		`select user_id, item_id, position_ticks, duration_ticks, played, play_count,
		        last_played_at, updated_at
		 from playback_progress where user_id = $1 and item_id = $2`, userID, itemID))
}

// ProgressForItems 批量读取进度（列表页要一次性标出「看到哪了」）。
func (s *Store) ProgressForItems(ctx context.Context, userID int64, itemIDs []int64) (map[int64]PlaybackProgress, error) {
	out := map[int64]PlaybackProgress{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`select user_id, item_id, position_ticks, duration_ticks, played, play_count,
		        last_played_at, updated_at
		 from playback_progress where user_id = $1 and item_id = any($2)`, userID, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("批量读取播放进度失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, err
		}
		out[p.ItemID] = *p
	}
	return out, rows.Err()
}

func scanProgress(row pgx.Row) (*PlaybackProgress, error) {
	var p PlaybackProgress
	err := row.Scan(&p.UserID, &p.ItemID, &p.PositionTicks, &p.DurationTicks, &p.Played,
		&p.PlayCount, &p.LastPlayedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取播放进度失败: %w", err)
	}
	return &p, nil
}

// SetPlayed 把若干条目标记为已看 / 未看。
//
// 两种方向都把 position_ticks 归零：标记已看之后进度条还停在一半，
// 对用户是自相矛盾的状态（首页会继续把他拉回这一集）。
func (s *Store) SetPlayed(ctx context.Context, userID int64, itemIDs []int64, played bool) error {
	if len(itemIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`insert into playback_progress
		   (user_id, item_id, position_ticks, duration_ticks, played, last_played_at, updated_at)
		 select $1, i.id, 0, coalesce(i.runtime_ticks, 0), $3,
		        case when $3 then now() else null end, now()
		 from media_items i
		 where i.id = any($2)
		 on conflict (user_id, item_id) do update set
		   played = excluded.played,
		   position_ticks = 0,
		   last_played_at = coalesce(playback_progress.last_played_at, excluded.last_played_at),
		   updated_at = now()`,
		userID, itemIDs, played)
	if err != nil {
		return fmt.Errorf("标记已看状态失败: %w", err)
	}
	return nil
}

// ContinueWatching 是「继续观看」的一条记录：条目本身 + 进度。
type ContinueWatching struct {
	Item      Item             `json:"item"`
	Progress  PlaybackProgress `json:"progress"`
	Remaining int64            `json:"remainingTicks"`
}

// ListContinueWatching 取没看完的条目，按最近播放排序。
//
// 剧集在这里是「集」而不是「剧」：用户想接着看的是下一集，
// 而不是回到剧集这一层再点两下（「下一集」由前端用剧集结构算）。
func (s *Store) ListContinueWatching(ctx context.Context, userID int64, limit int) ([]ContinueWatching, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`select item_id from playback_progress
		 where user_id = $1 and played = false and position_ticks > 0
		 order by last_played_at desc nulls last, updated_at desc
		 limit $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("读取继续观看列表失败: %w", err)
	}
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	items, err := s.ListItemsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	progress, err := s.ProgressForItems(ctx, userID, ids)
	if err != nil {
		return nil, err
	}

	out := make([]ContinueWatching, 0, len(ids))
	for _, id := range ids {
		it, ok := items[id]
		if !ok {
			continue
		}
		p := progress[id]
		remaining := p.DurationTicks - p.PositionTicks
		if remaining < 0 {
			remaining = 0
		}
		out = append(out, ContinueWatching{Item: it, Progress: p, Remaining: remaining})
	}
	return out, nil
}

// ListItemsByIDs 按 id 批量取条目，返回 id → 条目的映射。
//
// 顺序无关（调用方按自己的顺序取用），所以可以放心用 any()。
func (s *Store) ListItemsByIDs(ctx context.Context, ids []int64) (map[int64]Item, error) {
	out := map[int64]Item{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+` from media_items
		 where id = any($1) and deleted_at is null`, ids)
	if err != nil {
		return nil, fmt.Errorf("批量读取条目失败: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		out[it.ID] = it
	}
	return out, nil
}
