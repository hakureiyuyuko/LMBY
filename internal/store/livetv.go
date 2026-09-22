package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakureiyuyuko/lmby/internal/livetv"
)

// ---------------------------------------------------------------- 订阅源

// TVSource 是一条直播源（粘贴的文本、上传的文件、或订阅地址）。
type TVSource struct {
	ID                     int64
	Name                   string
	Kind                   string // paste / file / url
	URL                    string // kind=url 时的订阅地址
	Enabled                bool
	RefreshIntervalMinutes int
	LastRefreshAt          *time.Time
	LastStatus             string
	LastChannelCount       int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

const tvSourceColumns = `id, name, kind, url, enabled, refresh_interval_minutes,
	last_refresh_at, last_status, last_channel_count, created_at, updated_at`

func scanTVSource(row pgx.Row) (*TVSource, error) {
	var s TVSource
	err := row.Scan(&s.ID, &s.Name, &s.Kind, &s.URL, &s.Enabled, &s.RefreshIntervalMinutes,
		&s.LastRefreshAt, &s.LastStatus, &s.LastChannelCount, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取直播源失败: %w", err)
	}
	return &s, nil
}

// TVSourceInput 是新建/更新直播源的入参。
type TVSourceInput struct {
	Name                   string
	Kind                   string
	URL                    string
	Enabled                *bool
	RefreshIntervalMinutes *int
}

// CreateTVSource 新建直播源。
func (s *Store) CreateTVSource(ctx context.Context, in TVSourceInput) (*TVSource, error) {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	interval := 0
	if in.RefreshIntervalMinutes != nil {
		interval = *in.RefreshIntervalMinutes
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`insert into tv_sources (name, kind, url, enabled, refresh_interval_minutes)
		 values ($1, $2, $3, $4, $5) returning id`,
		strings.TrimSpace(in.Name), in.Kind, strings.TrimSpace(in.URL), enabled, interval).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("新建直播源失败: %w", err)
	}
	return s.GetTVSource(ctx, id)
}

// ListTVSources 列出全部直播源（最近更新的在前）。
func (s *Store) ListTVSources(ctx context.Context) ([]TVSource, error) {
	rows, err := s.pool.Query(ctx,
		`select `+tvSourceColumns+` from tv_sources order by updated_at desc, id desc`)
	if err != nil {
		return nil, fmt.Errorf("查询直播源失败: %w", err)
	}
	defer rows.Close()
	var out []TVSource
	for rows.Next() {
		var x TVSource
		if err := rows.Scan(&x.ID, &x.Name, &x.Kind, &x.URL, &x.Enabled, &x.RefreshIntervalMinutes,
			&x.LastRefreshAt, &x.LastStatus, &x.LastChannelCount, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// GetTVSource 按 id 取直播源。
func (s *Store) GetTVSource(ctx context.Context, id int64) (*TVSource, error) {
	return scanTVSource(s.pool.QueryRow(ctx, `select `+tvSourceColumns+` from tv_sources where id = $1`, id))
}

// UpdateTVSource 更新直播源（只改传进来的字段）。
func (s *Store) UpdateTVSource(ctx context.Context, id int64, in TVSourceInput) (*TVSource, error) {
	sets := []string{}
	args := []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf(expr, len(args)))
	}
	if in.Name != "" {
		add("name = $%d", strings.TrimSpace(in.Name))
	}
	if in.Kind != "" {
		add("kind = $%d", in.Kind)
	}
	if in.URL != "" {
		add("url = $%d", strings.TrimSpace(in.URL))
	}
	if in.Enabled != nil {
		add("enabled = $%d", *in.Enabled)
	}
	if in.RefreshIntervalMinutes != nil {
		add("refresh_interval_minutes = $%d", *in.RefreshIntervalMinutes)
	}
	if len(sets) == 0 {
		return s.GetTVSource(ctx, id)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)
	tag, err := s.pool.Exec(ctx,
		`update tv_sources set `+strings.Join(sets, ", ")+fmt.Sprintf(` where id = $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("更新直播源失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetTVSource(ctx, id)
}

// DeleteTVSource 删除直播源。频道不跟着删（外键是 set null）：
// 用户可能只是不想再自动刷新，不是想丢掉这一批频道。
func (s *Store) DeleteTVSource(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `delete from tv_sources where id = $1`, id)
	if err != nil {
		return fmt.Errorf("删除直播源失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkTVSourceRefreshed 记录一次刷新结果。
func (s *Store) MarkTVSourceRefreshed(ctx context.Context, id int64, status string, channelCount int) error {
	_, err := s.pool.Exec(ctx,
		`update tv_sources set last_refresh_at = now(), last_status = $2, last_channel_count = $3,
		        updated_at = now()
		 where id = $1`, id, truncate(status, 500), channelCount)
	if err != nil {
		return fmt.Errorf("记录刷新结果失败: %w", err)
	}
	return nil
}

// ListTVSourcesDue 返回「开着自动刷新、且已到刷新时间」的直播源。
func (s *Store) ListTVSourcesDue(ctx context.Context, now time.Time) ([]TVSource, error) {
	rows, err := s.pool.Query(ctx,
		`select `+tvSourceColumns+`
		 from tv_sources
		 where enabled
		   and kind = 'url'
		   and url <> ''
		   and refresh_interval_minutes > 0
		   and (last_refresh_at is null
		        or last_refresh_at < $1::timestamptz - make_interval(mins => refresh_interval_minutes))
		 order by last_refresh_at asc nulls first`, now)
	if err != nil {
		return nil, fmt.Errorf("查询待刷新直播源失败: %w", err)
	}
	defer rows.Close()
	var out []TVSource
	for rows.Next() {
		var x TVSource
		if err := rows.Scan(&x.ID, &x.Name, &x.Kind, &x.URL, &x.Enabled, &x.RefreshIntervalMinutes,
			&x.LastRefreshAt, &x.LastStatus, &x.LastChannelCount, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- 频道

// TVChannel 是一条直播频道。
type TVChannel struct {
	ID        int64
	SourceID  *int64
	Name      string
	URL       string
	Group     string
	Logo      string
	TvgID     string
	Headers   string
	SortOrder int
	Disabled  bool
	// Probe 是最近一次探测的摘要；ProbeOK == nil 表示还没探过。
	Probe     string
	ProbeOK   *bool
	ProbeAt   *time.Time
	Favorite  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Kind 返回源地址的协议类型。
func (c TVChannel) Kind() string { return livetv.Kind(c.URL) }

const tvChannelColumns = `c.id, c.source_id, c.name, c.url, c.group_name, c.logo, c.tvg_id, c.headers,
	c.sort_order, c.disabled, c.probe, c.probe_ok, c.probe_at, c.created_at, c.updated_at`

// TVChannelQuery 是频道列表的筛选条件。
type TVChannelQuery struct {
	UserID        int64  // 用于带出「是否收藏」
	Search        string // 频道名模糊匹配（空 = 不过滤）
	Group         string // 分组（空 = 不过滤）
	OnlyEnabled   bool   // 只返回启用中的频道
	OnlyFavorites bool   // 只返回该用户收藏的频道
	// Probe 按探测结果过滤：pending（还没探过）/ ok / failed（空 = 不过滤）。
	Probe string
}

// ListTVChannels 按条件列出频道。
func (s *Store) ListTVChannels(ctx context.Context, q TVChannelQuery) ([]TVChannel, error) {
	where := []string{"true"}
	args := []any{q.UserID}
	add := func(expr string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		add("c.name ilike '%%' || $%d || '%%'", s)
	}
	if g := strings.TrimSpace(q.Group); g != "" {
		add("c.group_name = $%d", g)
	}
	if q.OnlyEnabled {
		where = append(where, "not c.disabled")
	}
	if q.OnlyFavorites {
		where = append(where, "f.user_id is not null")
	}
	switch strings.ToLower(strings.TrimSpace(q.Probe)) {
	case "pending":
		where = append(where, "c.probe_at is null")
	case "ok":
		where = append(where, "c.probe_ok is true")
	case "failed":
		where = append(where, "c.probe_ok is false")
	}

	rows, err := s.pool.Query(ctx,
		`select `+tvChannelColumns+`, (f.user_id is not null) as favorite
		 from tv_channels c
		 left join tv_favorites f on f.channel_id = c.id and f.user_id = $1
		 where `+strings.Join(where, " and ")+`
		 order by c.group_name, c.sort_order, c.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询频道失败: %w", err)
	}
	defer rows.Close()

	var out []TVChannel
	for rows.Next() {
		c, err := scanTVChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// scanTVChannel 解析频道行（带收藏标记的那一列）。
func scanTVChannel(row pgx.Row) (*TVChannel, error) {
	var c TVChannel
	if err := row.Scan(&c.ID, &c.SourceID, &c.Name, &c.URL, &c.Group, &c.Logo, &c.TvgID, &c.Headers,
		&c.SortOrder, &c.Disabled, &c.Probe, &c.ProbeOK, &c.ProbeAt,
		&c.CreatedAt, &c.UpdatedAt, &c.Favorite); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetTVChannel 按 id 取频道（favorite 需要 userID；传 0 表示不关心）。
func (s *Store) GetTVChannel(ctx context.Context, userID, id int64) (*TVChannel, error) {
	c, err := scanTVChannel(s.pool.QueryRow(ctx,
		`select `+tvChannelColumns+`, (f.user_id is not null) as favorite
		 from tv_channels c
		 left join tv_favorites f on f.channel_id = c.id and f.user_id = $1
		 where c.id = $2`, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取频道失败: %w", err)
	}
	return c, nil
}

// TVChannelPatch 是频道的手工编辑（nil = 不改）。
type TVChannelPatch struct {
	Name      *string
	Group     *string
	Logo      *string
	SortOrder *int
	Disabled  *bool
}

// UpdateTVChannel 手工编辑频道（导入时不会覆盖这些字段）。
func (s *Store) UpdateTVChannel(ctx context.Context, id int64, p TVChannelPatch) error {
	sets := []string{}
	args := []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf(expr, len(args)))
	}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" {
			return fmt.Errorf("频道名不能为空")
		}
		add("name = $%d", name)
	}
	if p.Group != nil {
		add("group_name = $%d", strings.TrimSpace(*p.Group))
	}
	if p.Logo != nil {
		add("logo = $%d", strings.TrimSpace(*p.Logo))
	}
	if p.SortOrder != nil {
		add("sort_order = $%d", *p.SortOrder)
	}
	if p.Disabled != nil {
		add("disabled = $%d", *p.Disabled)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)
	tag, err := s.pool.Exec(ctx,
		`update tv_channels set `+strings.Join(sets, ", ")+fmt.Sprintf(` where id = $%d`, len(args)), args...)
	if err != nil {
		return fmt.Errorf("更新频道失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTVChannelProbe 保存探测结果（ok=false 即「失效源标记」）。
func (s *Store) SetTVChannelProbe(ctx context.Context, id int64, probe string, ok bool) error {
	_, err := s.pool.Exec(ctx,
		`update tv_channels set probe = $2, probe_ok = $3, probe_at = now(), updated_at = now()
		 where id = $1`, id, truncate(probe, 500), ok)
	if err != nil {
		return fmt.Errorf("保存频道探测结果失败: %w", err)
	}
	return nil
}

// TVChannelProbeStats 是频道的探测进度。
//
// **从库里现算**（而不是内存里记）：探测是长任务，界面刷新、进程重启都不该
// 让进度归零；而「哪些频道还没探过」本来就是表里的一个条件。
type TVChannelProbeStats struct {
	Total   int `json:"total"`   // 启用中且有地址的频道
	Pending int `json:"pending"` // 还没探过
	OK      int `json:"ok"`
	Failed  int `json:"failed"`
}

// TVChannelProbeStats 统计探测进度。
func (s *Store) TVChannelProbeStats(ctx context.Context) (TVChannelProbeStats, error) {
	var st TVChannelProbeStats
	err := s.pool.QueryRow(ctx,
		`select count(*)::int,
		        coalesce(sum(case when probe_at is null then 1 else 0 end), 0)::int,
		        coalesce(sum(case when probe_ok is true then 1 else 0 end), 0)::int,
		        coalesce(sum(case when probe_ok is false then 1 else 0 end), 0)::int
		 from tv_channels
		 where not disabled and url <> ''`).
		Scan(&st.Total, &st.Pending, &st.OK, &st.Failed)
	if err != nil {
		return st, fmt.Errorf("统计频道探测进度失败: %w", err)
	}
	return st, nil
}

// TVProbeFilter 选要探测哪些频道（三个条件是「与」）。
//
// 为什么要有选择器：一次全量探测要真连几百个源站、耗时以分钟计；
// 而「刚导入的一组想先看看通不通」是更常见的用法（界面上的按钮就用 group）。
type TVProbeFilter struct {
	OnlyUnknown bool    // 只探从没探过的（probe_at 为空）
	Group       string  // 只探这个分组
	IDs         []int64 // 只探这几条
}

// ListTVChannelsForProbe 列出要探测的频道（启用中且有地址的）。
func (s *Store) ListTVChannelsForProbe(ctx context.Context, f TVProbeFilter) ([]TVChannel, error) {
	// 选列要带上 favorite（固定 false）：scanTVChannel 按 16 列扫
	q := `select ` + tvChannelColumns + `, false as favorite
		from tv_channels c
		where not c.disabled and c.url <> ''`
	args := []any{}
	if f.OnlyUnknown {
		q += ` and c.probe_at is null`
	}
	if g := strings.TrimSpace(f.Group); g != "" {
		args = append(args, g)
		q += fmt.Sprintf(` and c.group_name = $%d`, len(args))
	}
	if len(f.IDs) > 0 {
		args = append(args, f.IDs)
		q += fmt.Sprintf(` and c.id = any($%d::bigint[])`, len(args))
	}
	q += ` order by c.group_name, c.sort_order, c.id`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询待探测频道失败: %w", err)
	}
	defer rows.Close()
	var out []TVChannel
	for rows.Next() {
		c, err := scanTVChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// TVGroup 是分组及其频道数。
type TVGroup struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ListTVGroups 列出全部分组（只统计启用中的频道）。
func (s *Store) ListTVGroups(ctx context.Context) ([]TVGroup, error) {
	rows, err := s.pool.Query(ctx,
		`select group_name, count(*)::int from tv_channels where not disabled
		 group by group_name order by group_name`)
	if err != nil {
		return nil, fmt.Errorf("查询直播分组失败: %w", err)
	}
	defer rows.Close()
	var out []TVGroup
	for rows.Next() {
		var g TVGroup
		if err := rows.Scan(&g.Name, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CountTVChannels 返回频道总数与启用数。
func (s *Store) CountTVChannels(ctx context.Context) (total, enabled int, err error) {
	err = s.pool.QueryRow(ctx,
		`select count(*)::int, coalesce(sum(case when not disabled then 1 else 0 end), 0)::int
		 from tv_channels`).Scan(&total, &enabled)
	if err != nil {
		return 0, 0, fmt.Errorf("统计频道数失败: %w", err)
	}
	return total, enabled, nil
}

// CountTVChannelsBySource 按直播源统计当前挂着的频道数。
func (s *Store) CountTVChannelsBySource(ctx context.Context) (map[int64]int, error) {
	rows, err := s.pool.Query(ctx,
		`select source_id, count(*)::int from tv_channels where source_id is not null group by source_id`)
	if err != nil {
		return nil, fmt.Errorf("统计直播源频道数失败: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- 导入

// TVImportResult 是一次导入的结果。
type TVImportResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Kept    int `json:"kept"`
	Removed int `json:"removed"`
	Total   int `json:"total"`
}

// ImportTVChannels 把一个播放列表导入频道表（按地址 upsert）。
//
// 三条规则（M5 的要求）：
//  1. **地址是身份**：同一个地址只更新元数据（名字/分组/logo/tvg-id/请求头/排序），
//     不会把「停用状态」「收藏」冲掉 —— 否则每次刷新订阅都会把用户手动停用的频道又打开；
//  2. **只清理本次来源自己的频道**：播放列表里没有的地址删掉，但删除范围限定在
//     source_id = 本次来源的行 —— 否则导入一个源会把另一个源的频道全删光；
//     （同一个地址出现在两个源里时，后导入的那个接管这一行，不会产生重复频道）
//  3. 探测结果（probe / probe_ok）同样保留：它反映的是「这个地址通不通」，
//     与「这一版列表里有没有它」无关。
//
// 空列表**不清理**：订阅源偶发返回空内容（网络截断/CDN 出错）时
// 把用户的频道全删掉是最坏的失败模式，不如什么都不做并让上层报错。
//
// 一次导入是一个事务：中途失败不会留下半份列表。
func (s *Store) ImportTVChannels(ctx context.Context, sourceID int64, entries []livetv.Entry) (TVImportResult, error) {
	var res TVImportResult

	type existing struct {
		id                     int64
		name, group, logo, tvg string
		headers                string
		sortOrder              int
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("导入频道失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 先把现有频道读进内存：一次导入最多几百条，换成逐条 upsert 反而更慢也更啰嗦。
	rows, err := tx.Query(ctx, `select id, url, name, group_name, logo, tvg_id, headers, sort_order from tv_channels`)
	if err != nil {
		return res, fmt.Errorf("读取现有频道失败: %w", err)
	}
	byURL := map[string]existing{}
	for rows.Next() {
		var e existing
		var url string
		if err := rows.Scan(&e.id, &url, &e.name, &e.group, &e.logo, &e.tvg, &e.headers, &e.sortOrder); err != nil {
			rows.Close()
			return res, err
		}
		byURL[url] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	urls := make([]string, 0, len(entries))
	for i, e := range entries {
		urls = append(urls, e.URL)
		if old, ok := byURL[e.URL]; ok {
			changed := old.name != e.Name || old.group != e.Group || old.logo != e.Logo ||
				old.tvg != e.TvgID || old.headers != e.Headers || old.sortOrder != i
			if !changed {
				res.Kept++
				continue
			}
			if _, err := tx.Exec(ctx,
				`update tv_channels
				 set name = $2, group_name = $3, logo = $4, tvg_id = $5, headers = $6,
				     sort_order = $7, source_id = $8, updated_at = now()
				 where id = $1`,
				old.id, e.Name, e.Group, e.Logo, e.TvgID, e.Headers, i, sourceID); err != nil {
				return res, fmt.Errorf("更新频道失败: %w", err)
			}
			res.Updated++
			continue
		}
		if _, err := tx.Exec(ctx,
			`insert into tv_channels (source_id, name, url, group_name, logo, tvg_id, headers, sort_order)
			 values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			sourceID, e.Name, e.URL, e.Group, e.Logo, e.TvgID, e.Headers, i); err != nil {
			return res, fmt.Errorf("新增频道失败: %w", err)
		}
		res.Added++
	}

	// 清理本来源里已经消失的频道（空列表时跳过，见函数注释）
	if len(urls) > 0 {
		tag, err := tx.Exec(ctx,
			`delete from tv_channels where source_id = $1 and not (url = any($2::text[]))`,
			sourceID, urls)
		if err != nil {
			return res, fmt.Errorf("删除失效频道失败: %w", err)
		}
		res.Removed = int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("导入频道失败: %w", err)
	}
	res.Total = len(entries)
	return res, nil
}

// ExportTVEntries 把频道表导出成 m3u 条目（导出的播放列表可以喂给别的播放器）。
func (s *Store) ExportTVEntries(ctx context.Context, includeDisabled bool) ([]livetv.Entry, error) {
	q := `select name, url, group_name, logo, tvg_id, headers from tv_channels`
	if !includeDisabled {
		q += ` where not disabled`
	}
	q += ` order by group_name, sort_order, id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("导出频道失败: %w", err)
	}
	defer rows.Close()
	var out []livetv.Entry
	for rows.Next() {
		var e livetv.Entry
		if err := rows.Scan(&e.Name, &e.URL, &e.Group, &e.Logo, &e.TvgID, &e.Headers); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- 收藏

// ToggleTVFavorite 切换收藏，返回切换后的状态。
func (s *Store) ToggleTVFavorite(ctx context.Context, userID, channelID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`delete from tv_favorites where user_id = $1 and channel_id = $2`, userID, channelID)
	if err != nil {
		return false, fmt.Errorf("取消收藏失败: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return false, nil
	}
	if _, err := s.pool.Exec(ctx,
		`insert into tv_favorites (user_id, channel_id) values ($1, $2)
		 on conflict (user_id, channel_id) do nothing`, userID, channelID); err != nil {
		return false, fmt.Errorf("收藏失败: %w", err)
	}
	return true, nil
}

// TVFavoriteIDs 返回该用户收藏的频道 id 集合。
func (s *Store) TVFavoriteIDs(ctx context.Context, userID int64) (map[int64]bool, error) {
	rows, err := s.pool.Query(ctx, `select channel_id from tv_favorites where user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询直播收藏失败: %w", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
