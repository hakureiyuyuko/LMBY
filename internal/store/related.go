package store

import (
	"context"
	"fmt"
)

// ListRelatedItems 找「相关推荐」。
//
// 口径刻意做成**离线可算**的：同库、同为顶层条目（电影 / 剧集）、有共同流派；
// 排序是「评分高的在前、最近添加的在前」。没有共同流派可算时（条目自己没流派）
// 就退化成「同库同类型」。
//
// 为什么不用 TMDB 的 recommendations：那要额外一次外网请求，而且**没配 TMDB 的实例
// （全本地 nfo 库）就完全没有推荐了**；同流派 + 同库这个信号对「看完这部再看点啥」
// 够用，而且永远可用。等真接了 provider，这里可以再加一路并合并 —— 接口形状不用改。
func (s *Store) ListRelatedItems(ctx context.Context, item *Item, limit int) ([]Item, error) {
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	// 没流派时传 NULL：SQL 里 `$3::text[] is null` 让整个流派条件失效
	var genres any
	if len(item.Genres) > 0 {
		genres = item.Genres
	}

	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 where deleted_at is null
		   and id <> $1
		   and library_id = $2
		   and kind in ('movie', 'series')
		   and ($3::text[] is null or genres ?| $3::text[])
		 order by community_rating desc nulls last, created_at desc, id
		 limit $4`, item.ID, item.LibraryID, genres, limit)
	if err != nil {
		return nil, fmt.Errorf("查询相关推荐失败: %w", err)
	}
	return scanItems(rows)
}
