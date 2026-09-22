package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 本文件是**首页**（Netflix 风格）要的数据。
//
// 为什么一个接口把整页都给了（而不是前端发三四个请求）：
//   - 首页是「一进站就看到」的页面，请求数直接决定首屏感觉；
//   - 几行之间有**先后关系**（有没有观看记录，决定了出「为你推荐」还是「评分最高」），
//     拆开就得让前端去判断后端才知道的事；
//   - 三行数据共用同一个「现在」，不会出现两行里同一条目的状态不一致。
//
// 代价是一次响应偏大（每行 20 条条目的元数据）。真要优化时应该先压字段
// （列表只需要海报与标题那一小撮），而不是把接口拆散 —— 见 docs/notes/home.md。

const (
	// homeHeroLimit 是轮播用几条。10 条足够「每次刷新都有点新东西」，
	// 又不会让首屏多拉十来张宽幅背景图。
	homeHeroLimit = 10
	// homeRowLimit 是每行几条。
	homeRowLimit = 20
	// homeContinueLimit 是「继续观看」几条：它在首屏最显眼的位置，不该自己撑满一屏。
	homeContinueLimit = 12
)

// handleHome 首页一次取全：轮播 + 继续观看 + 推荐行。
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	authCtx := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, 25*time.Second)
	defer cancel()

	hero, err := s.store.ListRecentItems(ctx, homeHeroLimit)
	if err != nil {
		s.serverError(w, "读取首页轮播失败", err)
		return
	}
	if hero == nil {
		// 空数组而不是 null：界面不必为「空」单独写一支分支
		hero = []store.Item{}
	}

	continueList, err := s.store.ListContinueWatching(ctx, authCtx.User.ID, homeContinueLimit)
	if err != nil {
		s.serverError(w, "读取继续观看失败", err)
		return
	}
	if continueList == nil {
		continueList = []store.ContinueWatching{}
	}

	sections, err := s.homeSections(ctx, authCtx.User.ID)
	if err != nil {
		s.serverError(w, "读取首页推荐失败", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hero":     hero,
		"continue": continueList,
		"sections": sections,
	})
}

// homeSections 组出首页的推荐行。
//
// 取舍：**有观看记录就出「为你推荐」，没有就退成「评分最高」** ——
// 新装的实例首页不该是一片空白，而空着一行「为你推荐（暂无数据）」也没意义。
// 「最近添加」永远都在（它不依赖账号状态）。
func (s *Server) homeSections(ctx context.Context, userID int64) ([]store.HomeSection, error) {
	sections := []store.HomeSection{}

	// 收藏排在最前面（它是用户自己挑的，比算法推的更有分量），
	// 但要放在「继续观看」后面：正在看的东西优先于「以后想看」的。
	favorites, _, err := s.store.ListFavorites(ctx, userID, "", homeRowLimit, 0)
	if err != nil {
		return nil, err
	}
	if len(favorites) > 0 {
		sections = append(sections, store.HomeSection{
			Key:      "favorites",
			Title:    "我的收藏",
			Subtitle: fmt.Sprintf("你收藏过的 %d 个条目", len(favorites)),
			Items:    favorites,
		})
	}

	rec, err := s.store.RecommendForUser(ctx, userID, homeRowLimit)
	if err != nil {
		return nil, err
	}
	switch {
	case len(rec.Items) > 0:
		sections = append(sections, store.HomeSection{
			Key:      "recommend",
			Title:    "为你推荐",
			Subtitle: recommendSubtitle(rec),
			// 画像与依据一起返回：界面显示「你爱看哪几类」/「因为你看过《X》」，
			// 验收脚本用它断言「每条推荐都命中画像」
			Taste:       rec.Taste,
			SourceWorks: rec.SourceWorks,
			SeedTitle:   rec.SeedTitle,
			Items:       rec.Items,
		})

	case rec.SourceWorks == 0:
		top, err := s.store.ListTopRatedItems(ctx, homeRowLimit)
		if err != nil {
			return nil, err
		}
		if len(top) > 0 {
			sections = append(sections, store.HomeSection{
				Key:      "top",
				Title:    "评分最高",
				Subtitle: "还没有观看记录，先从这些开始",
				Items:    top,
			})
		}
	default:
		// 有观看记录，但看过的作品都没流派（或库里没有可推荐的）——不硬凑一行
		s.log.Debug("首页推荐为空", "user", userID, "sourceWorks", rec.SourceWorks)
	}

	recent, err := s.store.ListRecentItems(ctx, homeRowLimit)
	if err != nil {
		return nil, err
	}
	if len(recent) > 0 {
		sections = append(sections, store.HomeSection{
			Key:      "recent",
			Title:    "最近添加",
			Subtitle: "刚入库或元数据刚更新过的",
			Items:    recent,
		})
	}
	return sections, nil
}

// recommendSubtitle 写「凭什么推给你」。
//
// 只根据一部作品推时直接点它的名字（最有说服力），多了就说个数量 +
// 画像里权重最高的两个流派 —— **不编造依据**，也不暴露用户没看过的信息。
func recommendSubtitle(rec store.Recommendation) string {
	if rec.SourceWorks == 1 && rec.SeedTitle != "" {
		return fmt.Sprintf("因为你看过《%s》", rec.SeedTitle)
	}
	names := make([]string, 0, 2)
	for _, t := range rec.Taste {
		if len(names) >= 2 {
			break
		}
		names = append(names, t.Genre)
	}
	if len(names) == 0 {
		return fmt.Sprintf("根据你最近看过的 %d 部作品", rec.SourceWorks)
	}
	return fmt.Sprintf("因为你喜欢 %s（最近看过 %d 部作品）", strings.Join(names, " · "), rec.SourceWorks)
}
