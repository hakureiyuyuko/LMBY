package store

// 构建「贡献给 KeqDB」的作品包时用到的读取。
//
// 单独列一个字段子集（ContribItem），不复用完整的 Item：组装器只要这些，
// 少扫几列、也少一处要跟着改的地方。

import (
	"context"
	"fmt"
	"time"
)

// ContribItem 是贡献组装用到的条目字段。
type ContribItem struct {
	ID            int64             `json:"id"`
	Kind          string            `json:"kind"`
	ParentID      *int64            `json:"parentId,omitempty"`
	SeriesID      *int64            `json:"seriesId,omitempty"`
	SeasonNum     *int32            `json:"seasonNumber,omitempty"`
	EpisodeNum    *int32            `json:"episodeNumber,omitempty"`
	Title         string            `json:"title"`
	OriginalTitle string            `json:"originalTitle"`
	Overview      string            `json:"overview"`
	Tagline       string            `json:"tagline"`
	Year          *int32            `json:"year,omitempty"`
	PremiereDate  *time.Time        `json:"premiereDate,omitempty"`
	RuntimeTicks  *int64            `json:"runtimeTicks,omitempty"`
	Rating        *float64          `json:"communityRating,omitempty"`
	Genres        []string          `json:"genres"`
	Tags          []string          `json:"tags"`
	Studios       []string          `json:"studios"`
	ProviderIDs   map[string]string `json:"providerIds"`
}

const contribCols = `id, kind, parent_id, series_id, season_number, episode_number,
	title, original_title, overview, tagline, year, premiere_date, runtime_ticks,
	community_rating, genres, tags, studios, provider_ids`

type contribScanner interface {
	Scan(dest ...any) error
}

func scanContrib(s contribScanner) (*ContribItem, error) {
	var c ContribItem
	if err := s.Scan(&c.ID, &c.Kind, &c.ParentID, &c.SeriesID, &c.SeasonNum, &c.EpisodeNum,
		&c.Title, &c.OriginalTitle, &c.Overview, &c.Tagline, &c.Year, &c.PremiereDate,
		&c.RuntimeTicks, &c.Rating, &c.Genres, &c.Tags, &c.Studios, &c.ProviderIDs); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetContribItem 读一个条目（贡献组装用）。
func (s *Store) GetContribItem(ctx context.Context, id int64) (*ContribItem, error) {
	row := s.pool.QueryRow(ctx,
		`select `+contribCols+` from media_items where id = $1 and deleted_at is null`, id)
	c, err := scanContrib(row)
	if err != nil {
		return nil, fmt.Errorf("读取条目 %d 失败: %w", id, err)
	}
	return c, nil
}

// ListContribChildren 列某条目的子项（剧→季→集），按季/集号排序。
func (s *Store) ListContribChildren(ctx context.Context, parentID int64) ([]ContribItem, error) {
	rows, err := s.pool.Query(ctx,
		`select `+contribCols+` from media_items
		 where parent_id = $1 and deleted_at is null
		 order by season_number nulls last, episode_number nulls last, id`, parentID)
	if err != nil {
		return nil, fmt.Errorf("列子项失败: %w", err)
	}
	defer rows.Close()

	var out []ContribItem
	for rows.Next() {
		c, err := scanContrib(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}
