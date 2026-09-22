package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// 本文件是**首页**要用的几条查询（Netflix 风格的轮播 + 推荐行）。
//
// 三条口径（详见 docs/notes/home.md）：
//
//  1. **「最近更新/入库」按 `updated_at` 倒序**：刚扫进来的条目 `updated_at` 就是入库时间，
//     而元数据被刮削/人工编辑过也会顶上来 —— 这正是「最新更新」的语义
//     （只看 `created_at` 的话，重扫一遍老库什么都不会变）。
//  2. **「为你推荐」是离线可算的**：拿账号最近看过的作品做「口味画像」
//     （流派 → 出现次数作为权重），再给没看过的作品按「共同流派加权和」打分。
//     不联网、不依赖 TMDB，所以全本地 nfo 库也有推荐（与 `ListRelatedItems` 同一个理由）。
//  3. **排除的粒度是「作品」**（集折进它所属的剧集）：正在追的剧不该再出现在「推荐」里，
//     而电影没有 `series_id`，`coalesce(series_id, id)` 对它就是自己。
//
// 三条查询都带 limit，代价与库大小无关（几百条与十万条同一个计划）。

// HomeSection 是首页上的一行（标题 + 副标题 + 条目）。
//
// `Key` 是给界面用的稳定标识（`recommend` / `recent` / `top` …），
// 界面据此决定渲染样式与埋点，而 `Title` 可以随时改文案。
type HomeSection struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	// Taste 只有「为你推荐」有：口味画像（流派 + 权重，按权重倒序）。
	// 它的存在是给两件事用：界面显示「你爱看哪几类」、验收脚本做精确断言。
	Taste []TasteGenre `json:"taste,omitempty"`
	// SourceWorks / SeedTitle 也只有「为你推荐」有：
	// 界面用它**在客户端**拼出本地化的推荐依据（「因为你看过《X》」），
	// 这样 API 不必为每种语言准备一份文案。
	SourceWorks int    `json:"sourceWorks,omitempty"`
	SeedTitle   string `json:"seedTitle,omitempty"`
	Items       []Item `json:"items"`
}

// ListRecentItems 取最近更新/入库的顶层条目（电影 / 剧集），供轮播与「最近添加」用。
func (s *Store) ListRecentItems(ctx context.Context, limit int, libs []int64) ([]Item, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 where deleted_at is null and kind in ('movie', 'series')
		   and `+libraryFilter("media_items.library_id", "$2")+`
		 order by updated_at desc, id desc
		 limit $1`, limit, libsArg(libs))
	if err != nil {
		return nil, fmt.Errorf("查询最近更新失败: %w", err)
	}
	return scanItems(rows)
}

// ListTopRatedItems 取评分最高的顶层条目 —— 「还没有观看记录」时的兜底行。
//
// 没评分的排在后面（`nulls last`），否则一批没有评分的条目会盖住真正的高分片。
func (s *Store) ListTopRatedItems(ctx context.Context, limit int, libs []int64) ([]Item, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 where deleted_at is null and kind in ('movie', 'series')
		   and `+libraryFilter("media_items.library_id", "$2")+`
		 order by community_rating desc nulls last, year desc nulls last, id
		 limit $1`, limit, libsArg(libs))
	if err != nil {
		return nil, fmt.Errorf("查询高分条目失败: %w", err)
	}
	return scanItems(rows)
}

// tasteProfileLimit 是「口味画像」最多用多少条观看记录。
//
// 为什么不看全部：看过的条目越多，画像越会被早期口味拉平（用户三年前爱看的东西
// 会一直有权重）。最近 50 条是个折中，而且让这条查询的代价封顶。
const tasteProfileLimit = 50

// TasteGenre 是口味画像里的一档：流派 + 权重（在看过的作品里出现的次数）。
type TasteGenre struct {
	Genre string `json:"genre"`
	// Weight 是权重：该流派在看过的作品里出现过几次。
	Weight int `json:"weight"`
}

// Recommendation 是「为你推荐」的结果，**含解释自己的依据**。
//
// 为什么把画像一起返回：推荐不能是黑盒（用户会问“凭什么推给我”），
// 而且界面把它当副标题、验收脚本用它做精确断言（“每条推荐至少命中画像里的一个流派”）——
// 同一份数据同时服务两件事。
type Recommendation struct {
	Items []Item `json:"items"`
	// Taste 是口味画像，按权重倒序（界面只展示前几个，但整个画像都在这里）。
	Taste []TasteGenre `json:"taste"`
	// SourceWorks 是画像用到了多少**部**作品（集折进剧集）。
	SourceWorks int `json:"sourceWorks"`
	// SeedTitle 是最近看过的那部作品的标题（用来写「因为你看过《X》」）。
	SeedTitle string `json:"seedTitle,omitempty"`
}

// RecommendForUser 「为你推荐」：按账号的观看历史做流派画像。
//
// 没有任何观看记录时返回零值（`Items` 为空）—— 调用方自己决定兜底
// （首页会换成「评分最高」）。
//
// 打分规则（有意做得简单、可解释）：
//
//	score = Σ（命中流派在画像里的权重）   权重 = 该流派在看过的作品里出现的次数
//
// 也就是「你看得越多的类型，越容易再推给你」。平手时按评分、年份、id 兜底，
// 保证刷新与分页稳定（同一个库两次请求顺序一致）。
// libs 是可见库（nil = 不过滤）：口味、历史、候选池**都**按它过滤 ——
// 私密库的观看记录不该影响看得见的推荐，候选也不该从看不见的库里来。
func (s *Store) RecommendForUser(ctx context.Context, userID int64, limit int, libs []int64) (Recommendation, error) {
	var out Recommendation
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// 画像规模 + 最近的种子：先说清「根据什么推荐」，也顺便决定要不要出这一行
	if err := s.pool.QueryRow(ctx,
		`select count(distinct coalesce(i.series_id, i.id))::int
		 from playback_progress pp
		 join media_items i on i.id = pp.item_id
		 where pp.user_id = $1 and i.deleted_at is null
		   and `+libraryFilter("i.library_id", "$2")+``, userID, libsArg(libs)).Scan(&out.SourceWorks); err != nil {
		return out, fmt.Errorf("统计观看记录失败: %w", err)
	}
	if out.SourceWorks == 0 {
		return out, nil
	}
	if err := s.pool.QueryRow(ctx,
		`select coalesce(s.title, i.title)
		 from playback_progress pp
		 join media_items i on i.id = pp.item_id
		 left join media_items s on s.id = i.series_id
		 where pp.user_id = $1 and i.deleted_at is null
		   and `+libraryFilter("i.library_id", "$2")+`
		 order by pp.last_played_at desc nulls last, pp.updated_at desc
		 limit 1`, userID, libsArg(libs)).Scan(&out.SeedTitle); err != nil {
		return out, fmt.Errorf("查询最近观看失败: %w", err)
	}

	// 画像本身（权重倒序，给界面做副标题、给验收做断言）
	tasteRows, err := s.pool.Query(ctx,
		`select g, count(*)::int as weight
		 from (select genres from playback_progress pp
		       join media_items i on i.id = pp.item_id
		       where pp.user_id = $1 and i.deleted_at is null
		         and `+libraryFilter("i.library_id", "$3")+`
		       order by pp.last_played_at desc nulls last, pp.updated_at desc
		       limit $2) w,
		      jsonb_array_elements_text(w.genres) g
		 group by g
		 order by weight desc, g`, userID, tasteProfileLimit, libsArg(libs))
	if err != nil {
		return out, fmt.Errorf("读取口味画像失败: %w", err)
	}
	out.Taste, err = scanTaste(tasteRows)
	if err != nil {
		return out, err
	}

	rows, err := s.pool.Query(ctx,
		`with watched as (
		     select pp.item_id,
		            coalesce(i.series_id, i.id) as work_id,
		            i.genres,
		            pp.last_played_at,
		            pp.updated_at
		     from playback_progress pp
		     join media_items i on i.id = pp.item_id
		     where pp.user_id = $1 and i.deleted_at is null
		       and `+libraryFilter("i.library_id", "$4")+`
		 ),
		 taste as (
		     select g, count(*) as weight
		     from (select genres from watched
		           order by last_played_at desc nulls last, updated_at desc
		           limit $3) w,
		          jsonb_array_elements_text(w.genres) g
		     group by g
		 ),
		 cand as (
		     select c.id as item_id,
		            (select coalesce(sum(t.weight), 0)
		             from jsonb_array_elements_text(c.genres) cg
		             join taste t on t.g = cg) as score
		     from media_items c
		     where c.deleted_at is null
		       and c.kind in ('movie', 'series')
		       and `+libraryFilter("c.library_id", "$4")+`
		       -- 碰过的「作品」整体排除（正在追的剧、看完的电影都不再推荐）
		       and coalesce(c.series_id, c.id) not in (select distinct work_id from watched)
		 )
		 select `+itemListColumns+`
		 from media_items
		 -- 这里的列名有意叫 item_id 而不是 id：select 列表里再 join 一个带 id 的 CTE
		 -- 会让 id 变成 ambiguous（真踩到，500）
		 join cand on cand.item_id = media_items.id
		 where cand.score > 0
		 order by cand.score desc, community_rating desc nulls last,
		          year desc nulls last, id
		 limit $2`, userID, limit, tasteProfileLimit, libsArg(libs))
	if err != nil {
		return out, fmt.Errorf("查询推荐失败: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return out, err
	}
	out.Items = items
	return out, nil
}

// scanTaste 扫口味画像（小结果集，单独一个函数只为了让上面那段读起来短一点）。
func scanTaste(rows pgx.Rows) ([]TasteGenre, error) {
	defer rows.Close()
	out := []TasteGenre{}
	for rows.Next() {
		var t TasteGenre
		if err := rows.Scan(&t.Genre, &t.Weight); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
