package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// match_state 的取值。见 migrations/0006_scrape.sql 的注释。
const (
	// MatchStateLocal 只有扫描得到的信息，还没刮过，也没 nfo。
	MatchStateLocal = "local"
	// MatchStateNFO 元数据来自媒体同目录的 nfo —— **人工整理的，刮削不许覆盖**。
	//
	// 为什么单独一个状态，不共用 manual：nfo 是「离线人工整理」的结果，
	// 用户可能积累了成千上万条，与「在界面上手改过一条」是两件事、
	// 展示上也应当分开数（进度条上要能看出「有多少条是 nfo 给的」）。
	MatchStateNFO = "nfo"
	// MatchStateMatched 自动匹配成功，元数据来自 provider。
	MatchStateMatched = "matched"
	// MatchStateReview 有候选但不够确定，等人工确认。
	MatchStateReview = "review"
	// MatchStateManual 人工在界面上指定过或字段被锁，重扫不覆盖。
	MatchStateManual = "manual"
	// MatchStateFailed 找不到候选 / 反复失败。
	MatchStateFailed = "failed"
)

// metadata_source 的取值（元数据是谁写的）。与 match_state 是两个维度：
// 前者说「数据从哪来」，后者说「自动流程还要不要动它」。
const (
	MetadataSourceNFO = "nfo"
	// MetadataSourceManual 表示元数据是人在界面上写的（逐字段编辑）。
	//
	// 它与 match_state=manual 是两个概念：后者是「人工指定过 provider 条目/字段被锁，
	// 自动流程别动它」，前者只是回答「这一格的值是谁写的」。
	MetadataSourceManual = "manual"
)

// MatchOutcome 是一次刮削的结局，落到 media_items 的刮削相关字段上。
type MatchOutcome struct {
	// State 是新的 match_state（用上面的常量）。
	State string
	// Score 是匹配打分（0~1）。
	//
	// nil 表示这个条目没有「相似度」这回事 —— 季与集是按 (剧集, 季号, 集号)
	// 直接对应的，没有候选可比；记 NULL 比编一个 1.0 诚实
	// （也就不会把「自动匹配的最低分」这类抽查指标带偏）。
	Score *float64
	// Source 是元数据来源（如 "tmdb"；空表示不改动原值）。
	Source string
	// Error 是给人看的失败原因 / 提示（会写进 scrape_error）。
	Error string
	// Candidates 是进人工队列时的候选列表（会存成 match_candidates）。
	// 为 nil 时不动已有候选。
	Candidates any
}

// SaveMatchOutcome 写入一次刮削的结局（状态、分数、来源、候选、时间戳）。
//
// 元数据本体不走这里，走 ApplyItemMeta —— 「空值不覆盖」的合并语义
// 只在一处实现，nfo 导入与刮削共用，免得两套规则各写一遍。
func (s *Store) SaveMatchOutcome(ctx context.Context, itemID int64, o MatchOutcome) error {
	candidates := "[]"
	if o.Candidates != nil {
		raw, err := json.Marshal(o.Candidates)
		if err != nil {
			return fmt.Errorf("序列化候选列表失败: %w", err)
		}
		candidates = string(raw)
	}

	_, err := s.pool.Exec(ctx,
		`update media_items set
		   match_state      = $2,
		   match_score      = $3,
		   metadata_source  = coalesce(nullif($4, ''), metadata_source),
		   scrape_error     = $5,
		   scrape_attempts  = scrape_attempts + 1,
		   last_scraped_at  = now(),
		   match_candidates = case when $6::jsonb = '[]'::jsonb then match_candidates else $6::jsonb end,
		   updated_at       = now()
		 where id = $1`,
		itemID, o.State, o.Score, o.Source, o.Error, candidates)
	if err != nil {
		return fmt.Errorf("写入刮削结果失败: %w", err)
	}
	return nil
}

// SeriesStructure 返回某个剧集里「集数最多的那一季」的季号与集数。
//
// 用作匹配的结构信号：本地库的 S01E01 编号与 TMDB 的季集编号是同一套，
// 所以拿最大的那一季去比最稳 —— 直接比总集数会被「特别篇/特典」的计数差异带偏
// （TMDB 把特别篇算进 season 0，本地目录里的特典往往不在同一层）。
// 没有任何单集时返回 (0, 0, nil)，表示这项信号不可用。
func (s *Store) SeriesStructure(ctx context.Context, seriesID int64) (season, episodes int, err error) {
	err = s.pool.QueryRow(ctx,
		`select season_number, count(*)
		 from media_items
		 where series_id = $1 and kind = 'episode' and deleted_at is null
		   and season_number is not null
		 group by season_number
		 order by count(*) desc, season_number
		 limit 1`, seriesID).Scan(&season, &episodes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("统计剧集结构失败: %w", err)
	}
	return season, episodes, nil
}

// EnqueueScrapesForLibrary 把某个库里需要刮削的条目一次性入队。
//
// 与探测一样用 insert ... select，避免几万条目逐条往返。分两批：
//
//  1. **电影与剧集** —— 它们靠「搜标题 + 打分」自己就能定位；
//  2. **季与集** —— 它们靠所属剧集的 provider id 才能定位，
//     所以只排「剧集已经匹配上」的；剧集刚匹配上时处理器会再补一批
//     （见 EnqueueSeriesScrapes）。
//
// kind 为空表示四类都要；force 为真时连已刮过的也重刮。
//
// **nfo 与人工锁定的条目不在此列**（除非 force）：nfo 是人工整理的元数据，
// 默认策略是「有 nfo 就用 nfo，没有才去刮」（见 docs/REQUIREMENTS.md 的决策表）。
//
// 优先级比探测高：探测是纯粹的背景工作（网盘上一条要十几秒），
// 而刮削是用户点了一下就想看到结果的事 —— 同优先级时
// 一次「刚扫完的 300 条待探测」会把刮削压到十几分钟之后（已踩到）。
func (s *Store) EnqueueScrapesForLibrary(ctx context.Context, libraryID int64, kind string, force bool) (int64, error) {
	topKinds := []string{"movie", "series"}
	extraKinds := []string{"season", "episode"}
	switch kind {
	case "movie", "series":
		topKinds = []string{kind}
		extraKinds = nil
	case "season", "episode":
		topKinds = nil
		extraKinds = []string{kind}
	}

	var total int64

	if len(topKinds) > 0 {
		tag, err := s.pool.Exec(ctx,
			`insert into tasks (kind, payload, dedupe_key, priority)
				 select $2, jsonb_build_object('itemId', i.id), 'item:' || i.id, $5
				 from media_items i
				 where i.library_id = $1 and i.deleted_at is null
			   and i.kind = any($3)
			   and i.match_state <> 'manual'
			   -- nfo 优先：已有 nfo 元数据的条目默认不刮（force 才覆盖）。
			   -- 这里同时看 state 与 source —— 光看 state 的话，万一有
			   -- 「nfo 导入写得早、状态没跟上」的历史数据就漏了。
			   and ($4 or (i.metadata_source <> 'nfo' and i.match_state in ('local', 'failed', 'review')))
			 on conflict do nothing`,
			libraryID, TaskKindScrape, topKinds, force, ScrapeTaskPriority)
		if err != nil {
			return total, fmt.Errorf("批量入队刮削任务失败: %w", err)
		}
		total += tag.RowsAffected()
	}

	if len(extraKinds) > 0 {
		tag, err := s.pool.Exec(ctx,
			`insert into tasks (kind, payload, dedupe_key, priority)
			 select $2, jsonb_build_object('itemId', e.id), 'item:' || e.id, $5
			 from media_items e
			 join media_items s on s.id = e.series_id
			 where e.library_id = $1 and e.deleted_at is null
			   and e.kind = any($3)
			   -- 剧集得已经「被认出来」：matched（自动匹配）/ nfo / manual 都算；
			   -- 具体能不能定位到 provider 上的那一部，交给处理器判断
			   -- （nfo 里有 tmdbid 就直用，没有就按标题搜一次）。
			   and s.match_state in ('matched', 'nfo', 'manual')
			   and e.match_state <> 'manual'
			   and ($4 or (e.metadata_source <> 'nfo' and e.match_state in ('local', 'failed', 'review')))
			 on conflict do nothing`,
			libraryID, TaskKindScrape, extraKinds, force, ScrapeTaskPriority)
		if err != nil {
			return total, fmt.Errorf("批量入队季/集刮削任务失败: %w", err)
		}
		total += tag.RowsAffected()
	}

	return total, nil
}

// EnqueueSeriesScrapes 把某个剧集下的季与集入队（剧集刚匹配上时调用）。
//
// 为什么由剧集处理器来触发：季/集的元数据要靠剧集的 provider id 才能定位，
// 剧集还没匹配时它们排上队也只能白跑一趟。
func (s *Store) EnqueueSeriesScrapes(ctx context.Context, seriesID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`insert into tasks (kind, payload, dedupe_key, priority)
			 select $2, jsonb_build_object('itemId', e.id), 'item:' || e.id, $3
			 from media_items e
			 where e.series_id = $1 and e.deleted_at is null
			   and e.kind in ('season', 'episode')
			   and e.match_state <> 'manual'
			   and e.metadata_source <> 'nfo'
			   and e.match_state in ('local', 'failed', 'review')
			 on conflict do nothing`, seriesID, TaskKindScrape, ScrapeTaskPriority)
	if err != nil {
		return 0, fmt.Errorf("入队剧集下的季/集失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ScrapeProgress 是刮削进度（按匹配状态分）。
type ScrapeProgress struct {
	NFO     int64 `json:"nfo"`
	Local   int64 `json:"local"`
	Matched int64 `json:"matched"`
	Review  int64 `json:"review"`
	Manual  int64 `json:"manual"`
	Failed  int64 `json:"failed"`
}

// ScrapeProgressOf 统计某个库的刮削进度。
func (s *Store) ScrapeProgressOf(ctx context.Context, libraryID int64) (*ScrapeProgress, error) {
	var p ScrapeProgress
	err := s.pool.QueryRow(ctx,
		`select
		   count(*) filter (where match_state = 'nfo'),
		   count(*) filter (where match_state = 'local'),
		   count(*) filter (where match_state = 'matched'),
		   count(*) filter (where match_state = 'review'),
		   count(*) filter (where match_state = 'manual'),
		   count(*) filter (where match_state = 'failed')
		 from media_items
		 where library_id = $1 and deleted_at is null
		   and kind in ('movie', 'series', 'season', 'episode')`,
		libraryID).Scan(&p.NFO, &p.Local, &p.Matched, &p.Review, &p.Manual, &p.Failed)
	if err != nil {
		return nil, fmt.Errorf("统计刮削进度失败: %w", err)
	}
	return &p, nil
}

// ResetFailedScrapes 把刮削失败的条目重置回 local 并清掉错误信息。
//
// 典型场景：TMDB 短暂不可用 / 凭据配错导致一批条目判失败，修好后一键重来。
func (s *Store) ResetFailedScrapes(ctx context.Context, libraryID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`update media_items
		 set match_state = 'local', scrape_error = '', match_score = null, updated_at = now()
		 where library_id = $1 and deleted_at is null
		   and kind in ('movie', 'series') and match_state = 'failed'`, libraryID)
	if err != nil {
		return 0, fmt.Errorf("重置失败的刮削失败: %w", err)
	}
	return tag.RowsAffected(), nil
}
