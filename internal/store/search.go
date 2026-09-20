package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 本文件是条目搜索。
//
// 切词与索引见 migrations/0007_search.sql（中文二元组，纯函数 + 生成列）。
// 查询侧三路并存，各管一件事：
//
//  1. `search_vec @@ plainto_tsquery('simple', lmby_bigram($q))` —— 中文二元组 /
//     英文整词的**分词命中**，带权重（标题 A、原始标题 B），能参与排序；
//  2. `ILIKE '%词%'` —— **子串兜底**：单字查询（「钢」）在 bigram 索引里查不到
//     （索引里只有两字的单元），而用户敲下第一个字就期待有反应；
//  3. `$3 <% title` 词相似（pg_trgm）—— **错字容忍**：「钢之炼金术土」也能命中。
//     要注意这是**字符类**的三元组：只有数据库的 lc_ctype 认识中文（如 C.UTF-8）
//     它才切得出中文三元组 —— lc_ctype=C 的库里 show_trgm('中文') 是空的，
//     这一路会静默失效（见 store.go 的 checkEncoding）。
//
// 排序：完全相同 > ts_rank（标题权重大于原始标题）> trigram 相似度 > 年份。
// 库里几百上千条时这个组合既准又够快；真到几十万条时再看是否需要把第 2 路收紧。

// SearchQuery 是一次标题搜索。
type SearchQuery struct {
	// Text 是查询词。调用方负责 trim；空字符串直接报错（不做「空词返回全部」，
	// 那是列表接口的活）。
	Text string
	// LibraryID 为 nil 表示搜全部库。
	LibraryID *int64
	// Kind 限定条目类型（空 = 不限）。
	Kind string
	// Limit/Offset 分页；Limit <= 0 用默认 24，上限 100。
	Limit  int
	Offset int
}

// SearchHit 是一条命中：条目本体 + 排序依据（界面可以用来解释「为什么它排第一」）。
type SearchHit struct {
	Item
	// Rank 是 tsvector 相关度（0 表示只靠 ILIKE / trigram 命中）。
	Rank float64 `json:"rank"`
	// Similarity 是标题的 trigram 相似度（错字容忍那一路的分数）。
	Similarity float64 `json:"similarity"`
}

// ValidItemKind 校验条目类型（与 migrations 里 media_items_kind_check 一致）。
func ValidItemKind(kind string) bool {
	switch kind {
	case "movie", "series", "season", "episode", "extra":
		return true
	}
	return false
}

// searchColumns 与 ListItems 读的是同一组列（另加两个排序依据）。
//
// 两处都列一遍是有意的：`select i.*` 会把 search_vec 一起带回来，
// 而 tsvector 没法扫进 Item（也没必要给界面看）。
const searchColumns = `i.id, i.library_id, i.kind, i.parent_id, i.series_id, i.season_number,
	i.episode_number, i.episode_end, i.extra_type, i.title, i.sort_title, i.original_title,
	i.year, i.premiere_date, i.overview, i.tagline, i.runtime_ticks, i.community_rating,
	i.official_rating, i.genres, i.tags, i.studios, i.provider_ids, i.file_tech, i.match_state,
	i.match_score, i.metadata_source, i.scrape_error, i.updated_at`

// searchCondition 是命中条件（参数固定：$1 库 id 或 null、$2 类型或空、$3 查询词）。
//
// 三路并存，缺一不可：
//   - `search_vec @@ plainto_tsquery(...)`：分词命中（中文二元组、英文整词），要求**全部**
//     单元都命中 —— 准，但错字会让整条查不到；
//   - `ilike`：子串兜底（单字查询、索引里没有的单字单元）；
//   - `$3 <% i.title`：**词相似**（word_similarity），容忍错字。
//     用 `<%` 而不是 `%`：`%` 比的是整个字符串的相似度，而我们的标题往往
//     带一堆前后缀（`AVC 4K …《某科学的超电磁炮OP2》`），整串比会被稀释到 0.1；
//     `<%` 只找「最像的那一段」，才是「用户只记得标题里几个字」的真实情形。
//     阈值在 SearchItems 里按会话调低到 0.4（默认 0.6 对中文太严）。
const searchCondition = `
	i.deleted_at is null
	and ($1::bigint is null or i.library_id = $1)
	and ($2::text = '' or i.kind = $2)
	and (
		i.search_vec @@ plainto_tsquery('simple', lmby_bigram($3))
		or i.title ilike '%' || $3 || '%'
		or i.original_title ilike '%' || $3 || '%'
		or $3 <% i.title
		or $3 <% i.original_title
	)`

// searchOrder 里 $3 仍然是查询词。
const searchOrder = `
	order by
	  (case
	     when lower(i.title) = lower($3) then 0
	     when lower(i.original_title) = lower($3) then 1
	     else 2
	   end),
	  ts_rank(i.search_vec, plainto_tsquery('simple', lmby_bigram($3))) desc,
	  greatest(word_similarity($3, i.title), word_similarity($3, i.original_title)) desc,
	  i.year desc nulls last,
	  i.id`

// wordSimilarityThreshold 是「词相似」的阈值。
//
// 为什么要调：默认 0.6 对中文太严 —— 一段 8 个字的中文里错一个字，
// word_similarity 大约只有 0.42（三元组大面积被破坏），
// 于是「钢之炼金术土」这种很正常的输入会一条都搜不到。
// 0.4 能把「一个错字」拉回来，同时因为分词命中与子串兜底都在前面，
// 排序上不会让模糊结果盖过精确结果。
const wordSimilarityThreshold = "0.4"

// SearchItems 搜索条目，返回命中列表与总数。
func (s *Store) SearchItems(ctx context.Context, q SearchQuery) ([]SearchHit, int64, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, 0, errors.New("搜索词不能为空")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	args := []any{q.LibraryID, q.Kind, text}

	// 两条语句要落在**同一个连接**上：pg_trgm 的共享库是懒加载的，
	// 自定义 GUC（pg_trgm.*）只有在库加载之后才存在，
	// 所以必须先在这个会话里摸一下 pg_trgm 的函数，再改阈值。
	// 用事务而不是池上的两次 Exec：后者可能落到不同连接上（踩过一次思路）。
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("开启搜索事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `select similarity('lmby', 'lmby')`); err != nil {
		return nil, 0, fmt.Errorf("加载 pg_trgm 失败（迁移 0003 里有 create extension）: %w", err)
	}
	if _, err := tx.Exec(ctx, `set local pg_trgm.word_similarity_threshold = `+wordSimilarityThreshold); err != nil {
		return nil, 0, fmt.Errorf("设置词相似阈值失败: %w", err)
	}

	rows, err := tx.Query(ctx,
		`select `+searchColumns+`,
		        ts_rank(i.search_vec, plainto_tsquery('simple', lmby_bigram($3))) as rank,
		        greatest(word_similarity($3, i.title),
		                 word_similarity($3, i.original_title)) as similarity
		 from media_items i
		 where `+searchCondition+searchOrder+`
		 limit $4 offset $5`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("搜索条目失败: %w", err)
	}
	defer rows.Close()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ID, &h.LibraryID, &h.Kind, &h.ParentID, &h.SeriesID, &h.SeasonNum,
			&h.EpisodeNum, &h.EpisodeEnd, &h.ExtraType, &h.Title, &h.SortTitle, &h.OriginalTitle,
			&h.Year, &h.PremiereDate, &h.Overview, &h.Tagline, &h.RuntimeTicks, &h.Rating,
			&h.OfficialRated, &h.Genres, &h.Tags, &h.Studios, &h.ProviderIDs, &h.FileTech,
			&h.MatchState, &h.MatchScore, &h.MetadataSource, &h.ScrapeError, &h.UpdatedAt,
			&h.Rank, &h.Similarity); err != nil {
			return nil, 0, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()

	var total int64
	if err := tx.QueryRow(ctx,
		`select count(*) from media_items i where `+searchCondition, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计搜索结果失败: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, fmt.Errorf("提交搜索事务失败: %w", err)
	}
	return out, total, nil
}
