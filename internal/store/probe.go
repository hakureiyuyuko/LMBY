package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// 任务类型常量。集中放在这里，避免各处手写字符串。
const (
	TaskKindProbe  = "probe"  // ffprobe 读取流信息
	TaskKindScrape = "scrape" // 从 provider（TMDB 等）刮削元数据
)

// MediaFileRow 是探测任务需要的最小文件视图。
type MediaFileRow struct {
	ID         int64
	ItemID     int64
	Path       string
	SizeBytes  int64
	ProbeState string
}

// GetMediaFile 读取一个媒体文件。
func (s *Store) GetMediaFile(ctx context.Context, id int64) (*MediaFileRow, error) {
	var f MediaFileRow
	err := s.pool.QueryRow(ctx,
		`select id, item_id, path, size_bytes, probe_state
		 from media_files where id = $1 and deleted_at is null`, id).
		Scan(&f.ID, &f.ItemID, &f.Path, &f.SizeBytes, &f.ProbeState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取媒体文件失败: %w", err)
	}
	return &f, nil
}

// ProbeRecord 是探测结果落库所需的字段（jsonb 部分直接传结构体，由 pgx 编码）。
type ProbeRecord struct {
	Container       string
	DurationTicks   int64
	SizeBytes       int64
	VideoStreams    any
	AudioStreams    any
	SubtitleStreams any
	Chapters        any
	HDR             any
}

// SaveFileProbe 保存探测结果，并在条目还没有时长时回填条目时长。
//
// 两处写入放在同一个事务里：探测结果与条目时长不一致会让后续的「跳过片头」
// 「续播」等功能全部失准。
func (s *Store) SaveFileProbe(ctx context.Context, fileID int64, r ProbeRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx,
		`update media_files set container = $2,
		                        duration_ticks = $3,
		                        size_bytes = case when size_bytes = 0 then $4 else size_bytes end,
		                        video_streams = $5,
		                        audio_streams = $6,
		                        subtitle_streams = $7,
		                        chapters = $8,
		                        hdr = $9,
		                        probe_state = 'ok',
		                        probe_error = '',
		                        probed_at = now(),
		                        updated_at = now()
		 where id = $1`,
		fileID, r.Container, r.DurationTicks, r.SizeBytes,
		r.VideoStreams, r.AudioStreams, r.SubtitleStreams, r.Chapters, r.HDR)
	if err != nil {
		return fmt.Errorf("写入探测结果失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 文件在探测期间被删掉了，属于正常竞态
		return nil
	}

	// 只在条目还没有时长时回填：nfo 或人工填过的时长优先。
	if r.DurationTicks > 0 {
		if _, err := tx.Exec(ctx,
			`update media_items set runtime_ticks = $2, updated_at = now()
			 where id = (select item_id from media_files where id = $1)
			   and (runtime_ticks is null or runtime_ticks = 0)`, fileID, r.DurationTicks); err != nil {
			return fmt.Errorf("回填条目时长失败: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交探测结果失败: %w", err)
	}
	return nil
}

// MarkFileProbeFailed 标记探测失败。
//
// 用于「文件本身有问题」的情况（不是媒体文件、损坏、格式不支持）——
// 这类失败重试多少次都一样，所以标记后不再重试，让用户能在界面上看到。
func (s *Store) MarkFileProbeFailed(ctx context.Context, fileID int64, msg string) error {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, err := s.pool.Exec(ctx,
		`update media_files set probe_state = 'failed', probe_error = $2, probed_at = now(), updated_at = now()
		 where id = $1`, fileID, msg)
	if err != nil {
		return fmt.Errorf("标记探测失败失败: %w", err)
	}
	return nil
}

// CountPendingProbes 统计某个库里还没探测的文件数。
func (s *Store) CountPendingProbes(ctx context.Context, libraryID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`select count(*) from media_files f
		 join media_items i on i.id = f.item_id
		 where i.library_id = $1 and f.probe_state = 'pending' and f.deleted_at is null`,
		libraryID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计待探测文件失败: %w", err)
	}
	return n, nil
}

// EnqueueProbesForLibrary 把某个库里所有待探测文件一次性入队。
//
// 用一条 insert ... select 完成，而不是逐个 Enqueue：
// 一个库几万个文件时，逐条执行的往返开销会被放大成分钟级。
func (s *Store) EnqueueProbesForLibrary(ctx context.Context, libraryID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`insert into tasks (kind, payload, dedupe_key, priority)
		 select $2, jsonb_build_object('fileId', f.id), 'file:' || f.id, 0
		 from media_files f
		 join media_items i on i.id = f.item_id
		 where i.library_id = $1 and f.probe_state = 'pending' and f.deleted_at is null
		 on conflict do nothing`, libraryID, TaskKindProbe)
	if err != nil {
		return 0, fmt.Errorf("批量入队探测任务失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ProbeProgress 是探测进度。
type ProbeProgress struct {
	Pending int64 `json:"pending"`
	OK      int64 `json:"ok"`
	Failed  int64 `json:"failed"`
}

// ProbeProgressOf 统计某个库的探测进度。
func (s *Store) ProbeProgressOf(ctx context.Context, libraryID int64) (*ProbeProgress, error) {
	var p ProbeProgress
	err := s.pool.QueryRow(ctx,
		`select
		   count(*) filter (where f.probe_state = 'pending'),
		   count(*) filter (where f.probe_state = 'ok'),
		   count(*) filter (where f.probe_state = 'failed')
		 from media_files f
		 join media_items i on i.id = f.item_id
		 where i.library_id = $1 and f.deleted_at is null`, libraryID).
		Scan(&p.Pending, &p.OK, &p.Failed)
	if err != nil {
		return nil, fmt.Errorf("统计探测进度失败: %w", err)
	}
	return &p, nil
}

// ResetFailedProbes 把失败的探测重置为待探测（用户修好文件后重试用）。
func (s *Store) ResetFailedProbes(ctx context.Context, libraryID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`update media_files f set probe_state = 'pending', probe_error = ''
		 from media_items i
		 where i.id = f.item_id and i.library_id = $1
		   and f.probe_state = 'failed' and f.deleted_at is null`, libraryID)
	if err != nil {
		return 0, fmt.Errorf("重置失败的探测失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// FileStreamInfo 用于展示：某个文件的流信息摘要。
type FileStreamInfo struct {
	FileID          int64
	Path            string
	Container       string
	DurationTicks   int64
	ProbeState      string
	ProbeError      string
	ProbedAt        *time.Time
	VideoStreams    any
	AudioStreams    any
	SubtitleStreams any
}

// GetFileStreamInfo 读取某个文件的流信息。
func (s *Store) GetFileStreamInfo(ctx context.Context, fileID int64) (*FileStreamInfo, error) {
	var f FileStreamInfo
	err := s.pool.QueryRow(ctx,
		`select id, path, container, coalesce(duration_ticks, 0), probe_state, probe_error, probed_at,
		        video_streams, audio_streams, subtitle_streams
		 from media_files where id = $1`, fileID).
		Scan(&f.FileID, &f.Path, &f.Container, &f.DurationTicks, &f.ProbeState, &f.ProbeError,
			&f.ProbedAt, &f.VideoStreams, &f.AudioStreams, &f.SubtitleStreams)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取文件流信息失败: %w", err)
	}
	return &f, nil
}
