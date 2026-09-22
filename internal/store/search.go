package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 本文件是搜索：条目、人、即时联想、结果分面（M6 补齐）。
//
// 切词与索引见 migrations/0007_search.sql（标题）与 0011_search_people.sql（人名）。
// 两处用的是**同一个纯函数** lmby_bigram（中文二元组 + 英文整词），
// 所以「人」与「作品」的命中行为一致，前端也不需要为两者写两套说明。
//
// 查询侧三路并存，各管一件事：
//
//  1. `search_vec @@ plainto_tsquery('simple', lmby_bigram($q))` —— 分词命中
//     （标题权重 A、原始标题 / 人名权重 B、A），要求**全部**单元命中，
//     准，但错字会让整条查不到；同时它带权重，能参与排序；
//  2. `ILIKE '%词%'` —— **子串兜底**：单字查询（「钢」）在 bigram 索引里查不到
//     （索引里只有两字的单元），而用户敲下第一个字就期待有反应；
//  3. `$q <% title` 词相似（pg_trgm）—— **错字容忍**：「钢之炼金术土」也能命中。
//     用 `<%` 而不是 `%`：`%` 比的是整个字符串的相似度，而我们的标题往往
//     带一堆前后缀（`AVC 4K …《某科学的超电磁炮OP2》`），整串比会被稀释到 0.1；
//     `<%` 只找「最像的那一段」，才是「用户只记得标题里几个字」的真实情形。
//     阈值在 withTrgm 里按会话调低到 0.4（默认 0.6 对中文太严）。
//
// ⚠️ 第 3 路是**字符类**的三元组：只有数据库的 lc_ctype 认识中文（如 C.UTF-8）
// 它才切得出中文三元组 —— lc_ctype=C 的库里 show_trgm('中文') 是空的，
// 这一路会静默失效（见 store.go 的 checkEncoding）。
//
// 排序：完全相同 > ts_rank（标题权重大于原始标题）> trigram 相似度 > 年份。
// 库里几百上千条时这个组合既准又够快；真到几十万条时再看是否需要把第 2 路收紧。

// SearchQuery 是一次搜索：查询词 + 四个筛选维度。
//
// Text 可以留空 —— 但**必须**至少给一个筛选维度，否则就是「空词返回全部」，
// 那是列表接口（GET /libraries/{id}/items）的活。典型场景是「在联想里点了一个
// 演员名」：那条查询里根本没有标题词，只有 personId。
type SearchQuery struct {
	// Text 是查询词。空字符串只有在给了筛选维度时才合法。
	Text string
	// LibraryID 为 nil 表示搜全部库。
	LibraryID *int64
	// Kind 限定条目类型（空 = 不限）。
	Kind string
	// Genre 限定流派（空 = 不限）。genres 是 jsonb 数组，用 jsonb 的 ? 判包含。
	Genre string
	// PersonID 限定「这个人参与过的条目」（空 = 不限），走 item_people 表。
	PersonID *int64
	// LibraryIDs 是**权限**意义上的可见库（nil/空 = 不过滤）。
	// 与上面的 LibraryID 不是一回事：那个是用户自己选的筛选（$1），
	// 这个是「这个人本来就不该看到别的库」，列表、总数、分面、联想都要带。
	LibraryIDs []int64
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

// searchDim 是四个筛选维度之一。
//
// 分面统计时要能**单独把某一维关掉**（正在生效的「类型=电影」不该让
// 「剧集」那一格变成 0，否则用户永远切不过去），所以维度要能被指名。
type searchDim string

const (
	dimLibrary searchDim = "library"
	dimKind    searchDim = "kind"
	dimGenre   searchDim = "genre"
	dimPerson  searchDim = "person"
)

// filters 是「除了 excluded 之外的维度」对应的参数值。
//
// 参数位置固定（$1 库 · $2 类型 · $3 流派 · $4 人 · $5 词）而不是按需拼接：
// 分面要复用同一段 where，把某一维关掉只需要把它换成空值 ——
// 位置一变，条件串就得跟着重拼，既啰嗦又容易出错。
func (q SearchQuery) filters(excluded searchDim) []any {
	lib, kind, genre, person := q.LibraryID, q.Kind, q.Genre, q.PersonID
	switch excluded {
	case dimLibrary:
		lib = nil
	case dimKind:
		kind = ""
	case dimGenre:
		genre = ""
	case dimPerson:
		person = nil
	}
	return []any{lib, kind, genre, person, strings.TrimSpace(q.Text)}
}

// HasFilter 判断「除了查询词之外，还给没给筛选维度」。
//
// 查询词可以为空的条件就靠它：只有给了筛选维度，空词才有意义（见 SearchQuery.Text）。
// 导出是因为 API 层校验参数时要问同一个问题 —— 规则只有一处定义。
func (q SearchQuery) HasFilter() bool {
	return q.LibraryID != nil || q.Kind != "" || q.Genre != "" || q.PersonID != nil
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

// searchCondition 是条目命中条件（参数见 SearchQuery.filters）。
//
// 筛选维度都写成 `$n 为空 = 不限`：空值判断在前，整个条件少一层拼装。
// 词的最后一组三路并列，$5 为空串时整组恒为真（= 不加词过滤）。
// searchConditionOf 在共用搜索条件后接上「权限可见库」条件。
//
// 占位符由调用方给（"$6"/"$7"/"$8"）：各查询里 limit/offset 的编号不同，
// 硬写一个编号必然错位（而这类错位只在运行时才炸，所以宁可显式传）。
func searchConditionOf(libs string) string {
	return searchCondition + ` and (cardinality(` + libs + `::bigint[]) = 0 or i.library_id = any(` + libs + `))`
}

const searchCondition = `
	i.deleted_at is null
	and ($1::bigint is null or i.library_id = $1)
	and ($2::text = '' or i.kind = $2)
	and ($3::text = '' or i.genres ? $3::text)
	and ($4::bigint is null or exists (
	      select 1 from item_people ip where ip.item_id = i.id and ip.person_id = $4))
	and (
		$5::text = ''
		or i.search_vec @@ plainto_tsquery('simple', lmby_bigram($5))
		or i.title ilike '%' || $5 || '%'
		or i.original_title ilike '%' || $5 || '%'
		or $5 <% i.title
		or $5 <% i.original_title
	)`

// searchOrder 是条目命中的排序（$5 仍然是查询词）。
//
// 同一个排序给「搜索结果」和「即时联想」共用：联想里第一条就是结果页的第一条，
// 不然会出现「回车之后东西换了个位置」这种让人怀疑的体验。
const searchOrder = `
	order by
	  (case
	     when lower(i.title) = lower($5) then 0
	     when lower(i.original_title) = lower($5) then 1
	     else 2
	   end),
	  ts_rank(i.search_vec, plainto_tsquery('simple', lmby_bigram($5))) desc,
	  greatest(word_similarity($5, i.title), word_similarity($5, i.original_title)) desc,
	  i.year desc nulls last,
	  i.id`

// peopleCondition 是人名的命中条件（$1 查询词）。
//
// 与条目同一套三路，只是字段换成 name。单独编参数位置（$1 就是词）：
// 人名查询用不到那几个筛选维度，硬凑到 $5 只会让两边的条件串互相牵制。
const peopleCondition = `
	$1::text <> ''
	and (
		p.search_vec @@ plainto_tsquery('simple', lmby_bigram($1))
		or p.name ilike '%' || $1 || '%'
		or $1 <% p.name
	)`

// wordSimilarityThreshold 是「词相似」的阈值。
//
// 为什么要调：默认 0.6 对中文太严 —— 一段 8 个字的中文里错一个字，
// word_similarity 大约只有 0.42（三元组大面积被破坏），
// 于是「钢之炼金术土」这种很正常的输入会一条都搜不到。
// 0.4 能把「一个错字」拉回来，同时因为分词命中与子串兜底都在前面，
// 排序上不会让模糊结果盖过精确结果。
const wordSimilarityThreshold = "0.4"

// withTrgm 在**同一个连接**上跑一段查询，并先把 pg_trgm 的词相似阈值调好。
//
// 为什么用事务而不是池上的几次 Exec：pg_trgm 的共享库是懒加载的，
// 自定义 GUC（pg_trgm.*）只有在库加载之后才存在，
// 所以必须先在这个会话里摸一下 pg_trgm 的函数，再改阈值；
// 池上的两次 Exec 可能落到不同连接上（踩过一次思路）。
func (s *Store) withTrgm(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启搜索事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `select similarity('lmby', 'lmby')`); err != nil {
		return fmt.Errorf("加载 pg_trgm 失败（迁移 0003 里有 create extension）: %w", err)
	}
	if _, err := tx.Exec(ctx, `set local pg_trgm.word_similarity_threshold = `+wordSimilarityThreshold); err != nil {
		return fmt.Errorf("设置词相似阈值失败: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交搜索事务失败: %w", err)
	}
	return nil
}

// SearchItems 搜索条目，返回命中列表与总数。
func (s *Store) SearchItems(ctx context.Context, q SearchQuery) ([]SearchHit, int64, error) {
	if strings.TrimSpace(q.Text) == "" && !q.HasFilter() {
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

	args := q.filters("")

	var out []SearchHit
	var total int64
	err := s.withTrgm(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`select `+searchColumns+`,
			        ts_rank(i.search_vec, plainto_tsquery('simple', lmby_bigram($5))) as rank,
			        greatest(word_similarity($5, i.title),
			                 word_similarity($5, i.original_title)) as similarity
			 from media_items i
			 where `+searchConditionOf("$8")+searchOrder+`
			 limit $6 offset $7`,
			append(append(append([]any{}, args...), limit, offset), q.LibraryIDs)...)
		if err != nil {
			return fmt.Errorf("搜索条目失败: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var h SearchHit
			if err := rows.Scan(&h.ID, &h.LibraryID, &h.Kind, &h.ParentID, &h.SeriesID, &h.SeasonNum,
				&h.EpisodeNum, &h.EpisodeEnd, &h.ExtraType, &h.Title, &h.SortTitle, &h.OriginalTitle,
				&h.Year, &h.PremiereDate, &h.Overview, &h.Tagline, &h.RuntimeTicks, &h.Rating,
				&h.OfficialRated, &h.Genres, &h.Tags, &h.Studios, &h.ProviderIDs, &h.FileTech,
				&h.MatchState, &h.MatchScore, &h.MetadataSource, &h.ScrapeError, &h.UpdatedAt,
				&h.Rank, &h.Similarity); err != nil {
				return err
			}
			out = append(out, h)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		return tx.QueryRow(ctx,
			`select count(*) from media_items i where `+searchConditionOf("$6"),
			append(append([]any{}, args...), q.LibraryIDs)...).Scan(&total)
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// SearchFacet 是一个分面取值与命中数。
//
// Value 的语义随维度而定：类型/流派就是文本，**媒体库是库 id 的十进制字符串**
// （名字由调用方用库列表贴上去，界面本来就为了筛选项拉了库列表，不必在这里再查一遍）。
type SearchFacet struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// SearchFacets 是「结果分面」：按类型 / 媒体库 / 流派各切一刀的命中数 + 人的命中数。
//
// 每一维统计时都**把这一维自己的筛选关掉**：正在生效的「类型=电影」不该让
// 「剧集」那一格变成 0，否则用户永远切不过去。于是有两个可以用来自检的不变量
// （验收脚本就按它们断言）：
//
//	sum(kind) == total  且  sum(library) == total   （每个条目恰好一个类型、一个库）
//	sum(genre) >= total                             （一个条目可以有多个流派）
type SearchFacets struct {
	Kind    []SearchFacet `json:"kind"`
	Library []SearchFacet `json:"library"`
	Genre   []SearchFacet `json:"genre"`
	// People 是查询词命中的人数（条目分面之外的另一档，人名从 people 表来）。
	People int64 `json:"people"`
	// Total 是当前筛选下条目的命中总数（与 SearchItems 的 total 一致）。
	Total int64 `json:"total"`
}

// SearchFacets 统计结果分面。
func (s *Store) SearchFacets(ctx context.Context, q SearchQuery) (SearchFacets, error) {
	var out SearchFacets
	if strings.TrimSpace(q.Text) == "" && !q.HasFilter() {
		return out, errors.New("搜索词不能为空")
	}
	text := strings.TrimSpace(q.Text)

	err := s.withTrgm(ctx, func(tx pgx.Tx) error {
		terms := []struct {
			dim  searchDim
			sql  string
			dst  *[]SearchFacet
			take int
		}{
			{dimKind, `select i.kind, count(*) from media_items i where ` + searchConditionOf("$6") +
				` group by 1 order by 2 desc, 1`, &out.Kind, 0},
			// 库这一维也带可见库条件：分面里出现「看不见的库」比列表里出现更糟
			// （等于把库的存在连同条数一起漏出去）。
			{dimLibrary, `select i.library_id::text, count(*) from media_items i where ` + searchConditionOf("$6") +
				` group by 1 order by 2 desc, 1`, &out.Library, 0},
			// 流派要先把 jsonb 数组摊开：一条条目有多个流派，所以每行只算一个流派
			// （于是 sum(genre) 会大于条目数，这是对的，不该被「修」成相等）。
			// 只取前 20 个流派：几百条命中里长尾流派对「筛一下」没有帮助。
			{dimGenre, `select g, count(*) from media_items i, jsonb_array_elements_text(i.genres) g where ` +
				searchConditionOf("$6") + ` group by 1 order by 2 desc, 1 limit 20`, &out.Genre, 0},
		}
		for _, t := range terms {
			terms_args := append(append([]any{}, q.filters(t.dim)...), q.LibraryIDs)
			rows, err := tx.Query(ctx, t.sql, terms_args...)
			if err != nil {
				return fmt.Errorf("统计 %s 分面失败: %w", t.dim, err)
			}
			list := []SearchFacet{}
			for rows.Next() {
				var f SearchFacet
				if err := rows.Scan(&f.Value, &f.Count); err != nil {
					rows.Close()
					return err
				}
				list = append(list, f)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			if t.take > 0 && len(list) > t.take {
				list = list[:t.take]
			}
			*t.dst = list
		}

		// 人这一档：与词命中的人数无关的那些筛选维度（库/类型/流派/人）
		// 在人表上无从施加，所以人的命中数只由查询词决定，这是有意的
		// （人不是「条目」，没有库与流派归属）。
		if text != "" {
			if err := tx.QueryRow(ctx,
				`select count(*) from people p where `+peopleCondition, text).Scan(&out.People); err != nil {
				return fmt.Errorf("统计人的命中数失败: %w", err)
			}
		}

		// 总数：用完整的筛选（与 SearchItems 完全一致的那一段）
		if err := tx.QueryRow(ctx,
			`select count(*) from media_items i where `+searchConditionOf("$6"), append(q.filters(""), q.LibraryIDs)...).Scan(&out.Total); err != nil {
			return fmt.Errorf("统计搜索结果失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return SearchFacets{}, err
	}
	return out, nil
}

// PersonHit 是「人」这一档的一条命中。
type PersonHit struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Roles 是这个人在这份数据里出现过的角色（actor / director / …，照 nfo 原样）。
	Roles []string `json:"roles"`
	// Works 是参演作品数：**集数会被折进它所属的剧集**（同一个 series_id 只算一部），
	// 否则一部 20 集的剧会把一个人算成 20 部作品。
	Works      int64   `json:"works"`
	Rank       float64 `json:"rank"`
	Similarity float64 `json:"similarity"`
}

// SearchPeople 搜人（即时联想与「人」这一档共用）。
//
// 限定参数用独立的 $1（词）、$2（limit）、$3（offset），与条目的 $1..$5 无关。
func (s *Store) SearchPeople(ctx context.Context, text string, limit, offset int) ([]PersonHit, int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, 0, errors.New("搜索词不能为空")
	}
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var out []PersonHit
	var total int64
	err := s.withTrgm(ctx, func(tx pgx.Tx) error {
		return s.searchPeopleTx(ctx, tx, text, limit, offset, &out, &total)
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// searchPeopleTx 是搜人的实际查询（已经在一个带好 pg_trgm 阈值的会话里）。
//
// 排序与条目一致：完全相同 > 分词相关度 > 词相似 > 作品数多的在前。
func (s *Store) searchPeopleTx(ctx context.Context, tx pgx.Tx, text string, limit, offset int,
	out *[]PersonHit, total *int64) error {
	// 作品数用 coalesce(i.series_id, i.id)：集折进剧集（见 PersonHit.Works 的说明）。
	// 注意 join 的条件写在 on 里而不是 where：演职员表里有作品但那些作品都被删掉的
	// 情况不该让这个人整条消失（works 记 0 就好）。
	const peopleSelect = `
		select p.id, p.name,
		       coalesce(array_agg(distinct ip.role) filter (where ip.role is not null), '{}') as roles,
		       count(distinct coalesce(i.series_id, i.id)) as works,
		       max(ts_rank(p.search_vec, plainto_tsquery('simple', lmby_bigram($1)))) as rel,
		       word_similarity($1, p.name) as sim
		from people p
		left join item_people ip on ip.person_id = p.id
		left join media_items i on i.id = ip.item_id and i.deleted_at is null
		where ` + peopleCondition + `
		group by p.id, p.name
		order by
		  (case when lower(p.name) = lower($1) then 0 else 1 end),
		  rel desc, sim desc, works desc, p.name
		limit $2 offset $3`

	rows, err := tx.Query(ctx, peopleSelect, text, limit, offset)
	if err != nil {
		return fmt.Errorf("搜索演职员失败: %w", err)
	}
	defer rows.Close()

	list := []PersonHit{}
	for rows.Next() {
		var h PersonHit
		if err := rows.Scan(&h.ID, &h.Name, &h.Roles, &h.Works, &h.Rank, &h.Similarity); err != nil {
			return err
		}
		list = append(list, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	*out = list

	if err := tx.QueryRow(ctx, `select count(*) from people p where `+peopleCondition, text).Scan(total); err != nil {
		return fmt.Errorf("统计演职员搜索结果失败: %w", err)
	}
	return nil
}

// Suggestion 是即时联想下拉里的一项。
type Suggestion struct {
	// Type 是 item / person：前端据此决定点下去干什么
	// （条目去详情页；人去「按这个人筛作品」）。
	Type  string `json:"type"`
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// Kind / Year 只有条目有。
	Kind string `json:"kind,omitempty"`
	Year *int32 `json:"year,omitempty"`
	// Works / Roles 只有人有。
	Works int64    `json:"works,omitempty"`
	Roles []string `json:"roles,omitempty"`
}

// Suggestions 是联想的结果：作品与人分两组，前端分两段渲染。
type Suggestions struct {
	Items  []Suggestion `json:"items"`
	People []Suggestion `json:"people"`
}

// SearchSuggest 即时联想：给查询词，返回最快能猜到的几个作品与人。
//
// 为什么不复用 SearchItems 的查询：联想每次按键都要跑（还带防抖），
// 只需要 id/title/kind/year 这几列，把 29 列与 overview 一起拉回来是浪费；
// 但**排的是同一个序**（searchOrder），否则「回车后东西换了位置」。
func (s *Store) SearchSuggest(ctx context.Context, text string, limit int, libs []int64) (Suggestions, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Suggestions{}, errors.New("搜索词不能为空")
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}

	out := Suggestions{Items: []Suggestion{}, People: []Suggestion{}}
	err := s.withTrgm(ctx, func(tx pgx.Tx) error {
		// 条目：筛选维度全空（联想不看当前筛选，永远给全局最像的几个 ——
		// 这是「联想」与「结果分面」的分工，见 docs/notes/search.md）
		args := append(append(append([]any{}, SearchQuery{Text: text}.filters("")...), limit), libsArg(libs))
		rows, err := tx.Query(ctx,
			`select i.id, i.title, i.kind, i.year from media_items i
			 where `+searchConditionOf("$7")+searchOrder+` limit $6`, args...)
		if err != nil {
			return fmt.Errorf("联想作品失败: %w", err)
		}
		for rows.Next() {
			var sug Suggestion
			if err := rows.Scan(&sug.ID, &sug.Title, &sug.Kind, &sug.Year); err != nil {
				rows.Close()
				return err
			}
			sug.Type = "item"
			out.Items = append(out.Items, sug)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		// 人：给一半的数量（人的价值在「点进去筛作品」，不需要和作品抢位置）
		peopleLimit := limit / 2
		if peopleLimit < 3 {
			peopleLimit = 3
		}
		var hits []PersonHit
		var total int64
		if err := s.searchPeopleTx(ctx, tx, text, peopleLimit, 0, &hits, &total); err != nil {
			return err
		}
		for _, h := range hits {
			out.People = append(out.People, Suggestion{
				Type: "person", ID: h.ID, Title: h.Name, Works: h.Works, Roles: h.Roles,
			})
		}
		return nil
	})
	if err != nil {
		return Suggestions{}, err
	}
	return out, nil
}
