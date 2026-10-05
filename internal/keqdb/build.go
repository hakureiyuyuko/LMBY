package keqdb

// Build：把 LMBY 的一个条目组装成 KeqDB 的「作品包」（对接文档 §5.2 字段表 + §5.5 映射表）。
//
// 几条有依据的取舍：
//   - **按「一部剧一个包」**：对季/集调用时先上溯到所属的剧，把全部季集一次带上（§5.4 的建议）。
//   - 中文类型名映射成 TMDB genre id；认不出的（「日韩」「萌系」这类自定义标签）放进
//     `keywords`，不硬塞 `genres`（§5.5 明确提醒过）。
//   - **暂不带 images**：贡献里要的是 KeqDB 侧的 path（得先 `POST /api/ingest/image` 换成
//     它那边的路径），而 LMBY 的图多是 TMDB 的远程路径；等图片服务就绪再接。

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// tmdbGenreIDs 是「中文类型名 → TMDB genre id」（对接文档 §5.5 的表）。
var tmdbGenreIDs = map[string]int{
	"动作": 28, "冒险": 12, "动画": 16, "喜剧": 35, "犯罪": 80, "纪录片": 99,
	"剧情": 18, "家庭": 10751, "奇幻": 14, "历史": 36, "恐怖": 27, "音乐": 10402,
	"悬疑": 9648, "爱情": 10749, "科幻": 878, "惊悚": 53, "战争": 10752, "西部": 37,
	"动作冒险": 10759, "儿童": 10762, "新闻": 10763, "真人秀": 10764,
	"科幻奇幻": 10765, "肥皂剧": 10766, "脱口秀": 10767, "战争政治": 10768,
	"电视电影": 10770,
}

// Builder 组装作品包。
type Builder struct {
	st *store.Store
}

// NewBuilder 构造。
func NewBuilder(st *store.Store) *Builder { return &Builder{st: st} }

// Build 组装一个条目；剧集/季会自动上溯到它所属的剧。
func (b *Builder) Build(ctx context.Context, itemID int64) (map[string]any, error) {
	it, err := b.st.GetContribItem(ctx, itemID)
	if err != nil {
		return nil, err
	}

	// 上溯到「作品」层级：电影是它自己；剧集/季归到它所属的剧。
	root := it
	for root.Kind != "movie" && root.Kind != "series" {
		if root.ParentID == nil {
			return nil, fmt.Errorf("条目 %d（%s）没有可贡献的上层作品", itemID, root.Kind)
		}
		if root, err = b.st.GetContribItem(ctx, *root.ParentID); err != nil {
			return nil, err
		}
	}

	switch root.Kind {
	case "movie":
		return b.buildMovie(ctx, root)
	case "series":
		return b.buildSeries(ctx, root)
	default:
		return nil, fmt.Errorf("暂不支持贡献 %s 类型的条目", root.Kind)
	}
}

func (b *Builder) buildMovie(ctx context.Context, it *store.ContribItem) (map[string]any, error) {
	pkg := map[string]any{
		"mediaType": "movie",
		"language":  "zh-CN",
		"title":     titleBlock(it, "movie"),
	}
	if id := tmdbID(it.ProviderIDs); id > 0 {
		pkg["tmdbId"] = id
	}
	if c := b.credits(ctx, it.ID); len(c) > 0 {
		pkg["credits"] = c
	}
	return pkg, nil
}

func (b *Builder) buildSeries(ctx context.Context, series *store.ContribItem) (map[string]any, error) {
	pkg := map[string]any{
		"mediaType": "tv",
		"language":  "zh-CN",
		"title":     titleBlock(series, "tv"),
	}
	if id := tmdbID(series.ProviderIDs); id > 0 {
		pkg["tmdbId"] = id
	}
	if c := b.credits(ctx, series.ID); len(c) > 0 {
		pkg["credits"] = c
	}

	children, err := b.st.ListContribChildren(ctx, series.ID)
	if err != nil {
		return nil, err
	}

	seasons := make([]map[string]any, 0, len(children))
	for i := range children {
		s := &children[i]
		if s.Kind != "season" {
			continue
		}
		blk := map[string]any{
			"seasonNumber": numOrZero(s.SeasonNum),
			"name":         s.Title,
			"overview":     s.Overview,
		}
		if s.PremiereDate != nil {
			blk["airDate"] = s.PremiereDate.Format("2006-01-02")
		}
		if id := tmdbID(s.ProviderIDs); id > 0 {
			blk["tmdbId"] = id
		}

		kids, err := b.st.ListContribChildren(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		episodes := make([]map[string]any, 0, len(kids))
		for j := range kids {
			e := &kids[j]
			if e.Kind != "episode" {
				continue
			}
			eb := map[string]any{
				"seasonNumber":  numOrZero(e.SeasonNum),
				"episodeNumber": numOrZero(e.EpisodeNum),
				"name":          e.Title,
				"overview":      e.Overview,
			}
			if e.PremiereDate != nil {
				eb["airDate"] = e.PremiereDate.Format("2006-01-02")
			}
			if e.RuntimeTicks != nil && *e.RuntimeTicks > 0 {
				eb["runtime"] = *e.RuntimeTicks / 600000000 // ticks(100ns) → 分钟
			}
			if e.Rating != nil && *e.Rating > 0 {
				eb["voteAverage"] = *e.Rating
			}
			if id := tmdbID(e.ProviderIDs); id > 0 {
				eb["tmdbId"] = id
			}
			if c := b.credits(ctx, e.ID); len(c) > 0 {
				eb["credits"] = c
			}
			episodes = append(episodes, eb)
		}
		blk["episodeCount"] = len(episodes)
		if len(episodes) > 0 {
			blk["episodes"] = episodes
		}
		seasons = append(seasons, blk)
	}
	if len(seasons) > 0 {
		pkg["seasons"] = seasons
	}
	return pkg, nil
}

// titleBlock 组装作品/条目的文本块。
func titleBlock(it *store.ContribItem, mediaType string) map[string]any {
	blk := map[string]any{}
	setIf(blk, "title", it.Title)
	setIf(blk, "originalTitle", it.OriginalTitle)
	setIf(blk, "overview", it.Overview)
	setIf(blk, "tagline", it.Tagline)

	if it.PremiereDate != nil {
		d := it.PremiereDate.Format("2006-01-02")
		if mediaType == "movie" {
			blk["releaseDate"] = d
		} else {
			blk["firstAirDate"] = d
		}
	}
	if it.RuntimeTicks != nil && *it.RuntimeTicks > 0 {
		blk["runtime"] = *it.RuntimeTicks / 600000000
	}
	if it.Rating != nil && *it.Rating > 0 {
		blk["voteAverage"] = *it.Rating
	}

	genres := make([]map[string]any, 0, len(it.Genres))
	keywords := make([]map[string]any, 0, len(it.Tags)+2)
	for _, g := range it.Genres {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if id, ok := tmdbGenreIDs[g]; ok {
			genres = append(genres, map[string]any{"id": id, "name": g})
		} else {
			// 自定义标签（日韩/萌系/校园…）不是 TMDB 类型，按文档放 keywords。
			keywords = append(keywords, map[string]any{"name": g})
		}
	}
	for _, t := range it.Tags {
		if t = strings.TrimSpace(t); t != "" {
			keywords = append(keywords, map[string]any{"name": t})
		}
	}
	if len(genres) > 0 {
		blk["genres"] = genres
	}
	if len(keywords) > 0 {
		blk["keywords"] = keywords
	}

	comps := make([]map[string]any, 0, len(it.Studios))
	for _, s := range it.Studios {
		if s = strings.TrimSpace(s); s != "" {
			comps = append(comps, map[string]any{"name": s})
		}
	}
	if len(comps) > 0 {
		blk["productionCompanies"] = comps
	}
	return blk
}

// credits 组装演职员：演员类 → cast，其余（Director/Writer/…）→ crew。
func (b *Builder) credits(ctx context.Context, itemID int64) []map[string]any {
	ppl, err := b.st.ListItemPeople(ctx, itemID)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(ppl))
	for _, p := range ppl {
		c := map[string]any{"name": p.Name, "order": p.Order}
		if id := tmdbID(p.ProviderIDs); id > 0 {
			c["personId"] = id
		}
		switch strings.ToLower(strings.TrimSpace(p.Role)) {
		case "", "actor", "gueststar", "guest_star", "guest star":
			c["creditType"] = "cast"
			if p.Character != "" {
				c["character"] = p.Character
			}
		default:
			c["creditType"] = "crew"
			c["job"] = p.Role // 原样带上（Director / Writer / Producer…）
		}
		out = append(out, c)
	}
	return out
}

func tmdbID(ids map[string]string) int64 {
	if ids == nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(ids["tmdb"]), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func numOrZero(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func setIf(m map[string]any, k, v string) {
	if strings.TrimSpace(v) != "" {
		m[k] = v
	}
}
