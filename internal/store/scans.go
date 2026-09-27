package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakureiyuyuko/lmby/internal/textutil"
)

// ScanRun 是一次扫描运行。
type ScanRun struct {
	ID         int64          `json:"id"`
	LibraryID  int64          `json:"libraryId"`
	State      string         `json:"state"`
	Trigger    string         `json:"trigger"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	Stats      map[string]any `json:"stats"`
	Error      string         `json:"error,omitempty"`
}

// ScanIssue 是扫描过程中发现的问题（未识别文件、路径冲突等）。
type ScanIssue struct {
	ID       int64     `json:"id"`
	Severity string    `json:"severity"`
	Path     string    `json:"path"`
	Message  string    `json:"message"`
	At       time.Time `json:"at"`
}

// StartScanRun 创建一条 running 记录。
func (s *Store) StartScanRun(ctx context.Context, libraryID int64, trigger string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`insert into scan_runs (library_id, trigger) values ($1, $2) returning id`,
		libraryID, trigger).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("创建扫描记录失败: %w", err)
	}
	return id, nil
}

// FinishScanRun 收尾扫描记录。
func (s *Store) FinishScanRun(ctx context.Context, runID int64, state string, stats map[string]any, errMsg string) error {
	if stats == nil {
		stats = map[string]any{}
	}
	_, err := s.pool.Exec(ctx,
		`update scan_runs set state = $2, stats = $3, error = $4, finished_at = now() where id = $1`,
		runID, state, jsonMapAny(stats), errMsg)
	if err != nil {
		return fmt.Errorf("更新扫描记录失败: %w", err)
	}
	return nil
}

// AddScanIssues 批量写入扫描问题。
//
// 入库前对 path / message 做一次净化：这两个字段可能来自文件系统（SMB / 网盘共享上的
// 文件名不保证是 UTF-8）或内嵌在错误信息里，而库是 UTF-8 的。净化是**兜底** ——
// 扫描器已经会跳过非法名字，但这里的代价只有一次 utf8.ValidString，值得。
//
// 为什么必须兜住：以前 `CopyFrom` 失败后逐条兜底的实现会在坏行**中断并返回错误**，
// 于是一条坏路径就让整批问题写不进去，扫描被判定为「失败」（2026-09-25 实测）。
func (s *Store) AddScanIssues(ctx context.Context, runID, libraryID int64, issues []ScanIssue) error {
	if len(issues) == 0 {
		return nil
	}
	// 用一条多值 insert 批量写，30k 文件的库可能产生数千条 issue
	batch := make([][]any, 0, len(issues))
	for _, is := range issues {
		sev := is.Severity
		if sev == "" {
			sev = "warning"
		}
		batch = append(batch, []any{runID, libraryID, sev, textutil.Valid(is.Path), textutil.Valid(is.Message)})
	}

	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"scan_issues"},
		[]string{"scan_run_id", "library_id", "severity", "path", "message"},
		pgx.CopyFromRows(batch))
	if err != nil {
		// CopyFrom 失败时退化成逐条插入，保证流程不中断
		for i, row := range batch {
			if _, e := s.pool.Exec(ctx,
				`insert into scan_issues (scan_run_id, library_id, severity, path, message)
				 values ($1, $2, $3, $4, $5)`, row...); e != nil {
				// 把出问题的条目说出来：否则界面只有一句「写入扫描问题失败」，
				// 根本不知道是哪一条、也就无从下手（这是本次事故最费时间的地方）。
				path, _ := row[3].(string)
				return fmt.Errorf("写入扫描问题失败（第 %d/%d 条：%s）: %w",
					i+1, len(batch), textutil.Truncate(path, 160), e)
			}
		}
	}
	return nil
}

// LatestScanRun 取某个库最近一次扫描。
func (s *Store) LatestScanRun(ctx context.Context, libraryID int64) (*ScanRun, error) {
	var r ScanRun
	err := s.pool.QueryRow(ctx,
		`select id, library_id, state, trigger, started_at, finished_at, stats, error
		 from scan_runs where library_id = $1 order by started_at desc limit 1`, libraryID).
		Scan(&r.ID, &r.LibraryID, &r.State, &r.Trigger, &r.StartedAt, &r.FinishedAt, &r.Stats, &r.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取扫描记录失败: %w", err)
	}
	return &r, nil
}

// ListScanIssues 列出某个库最近的扫描问题。
func (s *Store) ListScanIssues(ctx context.Context, libraryID int64, limit int) ([]ScanIssue, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx,
		`select id, severity, path, message, created_at
		 from scan_issues where library_id = $1 order by created_at desc, id desc limit $2`,
		libraryID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询扫描问题失败: %w", err)
	}
	defer rows.Close()

	var out []ScanIssue
	for rows.Next() {
		var is ScanIssue
		if err := rows.Scan(&is.ID, &is.Severity, &is.Path, &is.Message, &is.At); err != nil {
			return nil, err
		}
		out = append(out, is)
	}
	return out, rows.Err()
}

// ClearScanIssues 清空某个库的历史问题（新扫描开始时调用）。
func (s *Store) ClearScanIssues(ctx context.Context, libraryID int64) error {
	_, err := s.pool.Exec(ctx, `delete from scan_issues where library_id = $1`, libraryID)
	return err
}

// MarkStaleRunsFailed 把上次进程崩溃时留下的 running 记录收尾。
//
// 没有它的话，库里会一直挂着一个「正在扫描」的幽灵记录。
func (s *Store) MarkStaleRunsFailed(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`update scan_runs set state = 'failed', finished_at = now(),
		        error = coalesce(nullif(error, ''), '进程重启，扫描被中断')
		 where state = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("清理中断的扫描记录失败: %w", err)
	}
	return tag.RowsAffected(), nil
}
