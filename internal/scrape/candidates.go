package scrape

import (
	"context"
	"log/slog"
	"sort"

	"github.com/hakureiyuyuko/lmby/internal/match"
	"github.com/hakureiyuyuko/lmby/internal/provider"
)

// Detail 是候选的详情。自动匹配成功后要拿它落库，所以顺手返回，
// 避免「匹配时取一次详情、写元数据时再取一次」（虽然有缓存兜底，但没必要）。
type Detail struct {
	Movie  *provider.Movie
	Series *provider.Series
}

// Enrich 给候选补上「集数 / 单集时长 / 别名」这些只在详情接口里才有的信号，
// 并把详情一并返回（键是候选 id）。
//
// 两条来自实盘的硬经验（见 docs/ROADMAP.md 的 M2 验收记录）：
//
//  1. **只靠 search 的主标题会漏配**：本地中文译名与 TMDB 主标题差得远的情况不少
//     ——《孔中窥见真理之貌》只搜主标题得 0.309，把 alternative_titles 拉进来
//     命中《孔中窥见真理之貌OVA》后是 0.918。所以这一步不是「可选优化」。
//  2. 只对前几条候选做：搜索能返回 20 条，值得细看细比的没那么多，
//     每条要 1~2 次详情请求，收敛住 API 用量。
//
// 取详情的失败只记日志不中断 —— 少一个候选的信号不该让整个条目刮不了。
func Enrich(
	ctx context.Context,
	client provider.Client,
	kind string,
	cands []match.Candidate,
	season int,
	log *slog.Logger,
) map[int]Detail {
	details := make(map[int]Detail, len(cands))

	for i := range cands {
		switch kind {
		case provider.KindMovie:
			m, err := client.Movie(ctx, cands[i].ID, "")
			if err != nil {
				log.Warn("取电影详情失败", "tmdb", cands[i].ID, "err", err)
				continue
			}
			cands[i].RuntimeMinutes = m.RuntimeMinutes
			cands[i].AltTitles = m.AlternativeTitles
			details[cands[i].ID] = Detail{Movie: m}

		default:
			s, err := client.Series(ctx, cands[i].ID, "")
			if err != nil {
				log.Warn("取剧集详情失败", "tmdb", cands[i].ID, "err", err)
				continue
			}
			cands[i].AltTitles = s.AlternativeTitles
			for _, se := range s.Seasons {
				cands[i].SeasonEpisodes = append(cands[i].SeasonEpisodes, match.SeasonEpisodes{
					Season:   se.SeasonNumber,
					Episodes: se.EpisodeCount,
				})
			}
			details[cands[i].ID] = Detail{Series: s}

			// 单集时长：拿本地季号对应的那一季（一次返回整季，不逐集请求）
			want := season
			if want <= 0 {
				want = 1
			}
			det, err := client.Season(ctx, cands[i].ID, want, "")
			if err != nil || len(det.Episodes) == 0 {
				continue
			}
			var runtimes []int
			for _, ep := range det.Episodes {
				if ep.RuntimeMin > 0 {
					runtimes = append(runtimes, ep.RuntimeMin)
				}
			}
			if len(runtimes) > 0 {
				sort.Ints(runtimes)
				cands[i].RuntimeMinutes = runtimes[len(runtimes)/2]
			}
		}
	}
	return details
}
