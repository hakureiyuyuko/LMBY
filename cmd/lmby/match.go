package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/match"
	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// cmdMatch 是对着真实 provider 验证匹配打分器的命令行工具。
//
// 为什么需要它：匹配质量是「感觉」不出来的，必须拿真库里的怪标题反复跑。
// 本地解析出的标题（可能带 FULLMETAL ALCHEMIST 这种中英混排、可能漏年份）
// 直接喂给打分器，看候选排序与每一项明细 —— 这是调阈值唯一靠谱的办法。
//
// 用法：
//
//	lmby match --kind tv --title "钢之炼金术师 FULLMETAL ALCHEMIST" --year 2009 \
//	           --season 1 --episodes 64 --deep
//	lmby match --kind movie --title "言叶之庭" --year 2013 --runtime 46
func cmdMatch(args []string) error {
	fs := flag.NewFlagSet("match", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	kind := fs.String("kind", provider.KindTV, "条目类型：movie | tv")
	title := fs.String("title", "", "本地标题（必填）")
	origTitle := fs.String("orig-title", "", "本地原名（可选）")
	aliases := fs.String("aliases", "", "本地别名，逗号分隔（可选）")
	year := fs.Int("year", 0, "本地年份（只参与打分，不用来过滤搜索）")
	season := fs.Int("season", 0, "本地季号（剧集）")
	episodes := fs.Int("episodes", 0, "本地该季集数（剧集）")
	runtimeMin := fs.Int("runtime", 0, "本地单集/影片时长（分钟）")
	deep := fs.Bool("deep", false, "对每个候选再取详情，把集数与单集时长也拉进打分")
	top := fs.Int("top", 8, "最多看前几条候选")
	asJSON := fs.Bool("json", false, "输出 JSON（默认输出便于人读的明细）")

	flags, positional := splitFlagsAndPositionals(args)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" && len(positional) > 0 {
		*title = strings.Join(positional, " ")
	}
	if strings.TrimSpace(*title) == "" {
		return fmt.Errorf("缺少 --title")
	}
	if *kind != provider.KindMovie && *kind != provider.KindTV {
		return fmt.Errorf("--kind 只能是 movie 或 tv，收到 %q", *kind)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := newCLILogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Database.DSN, cfg.Database.MaxConns)
	if err != nil {
		return err
	}
	defer st.Close()

	cached, _, err := buildTMDBProvider(cfg, st, log)
	if err != nil {
		return err
	}
	if cached == nil {
		return fmt.Errorf("未配置 TMDB 凭据（config.toml 的 [tmdb] 段，或 LMBY_TMDB_READ_TOKEN）")
	}

	local := match.Local{
		Kind:          *kind,
		Title:         strings.TrimSpace(*title),
		OriginalTitle: strings.TrimSpace(*origTitle),
		Aliases:       splitList(*aliases),
		Year:          *year,
		SeasonNumber:  *season,
		EpisodeCount:  *episodes,
	}
	if *runtimeMin > 0 {
		local.RuntimesSeconds = []int{*runtimeMin * 60}
	}

	// 刻意不给搜索传年份：TMDB 的 year 过滤是硬过滤，
	// 而本地年份可能来自某一季、或干脆解析错了，硬过滤会把正确答案直接筛掉。
	// 年份交给打分器判（差得多的直接 0 分）。
	opts := provider.SearchOptions{Lang: cfg.TMDB.Language}
	var results []provider.SearchResult
	if *kind == provider.KindMovie {
		results, err = cached.SearchMovie(ctx, local.Title, opts)
	} else {
		results, err = cached.SearchSeries(ctx, local.Title, opts)
	}
	if err != nil {
		return err
	}
	if len(results) == 0 && local.Year > 0 {
		// 一条都没有时再退一步用本地标题 + 年份试一次
		log.Warn("按标题搜不到候选，改用「标题 + 年份」重试", "title", local.Title, "year", local.Year)
		opts.Year = local.Year
		if *kind == provider.KindMovie {
			results, err = cached.SearchMovie(ctx, local.Title, opts)
		} else {
			results, err = cached.SearchSeries(ctx, local.Title, opts)
		}
		if err != nil {
			return err
		}
	}

	cands := make([]match.Candidate, 0, len(results))
	for _, r := range results {
		cands = append(cands, match.FromSearch(r))
	}
	if *top > 0 && len(cands) > *top {
		cands = cands[:*top]
	}

	if *deep {
		enrichCandidates(ctx, cached, *kind, cfg.TMDB.Language, cands, local.SeasonNumber, log)
	}

	ranked := match.DefaultScorer().Rank(local, cands)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"local":      local,
			"deep":       *deep,
			"candidates": len(cands),
			"results":    ranked,
		})
	}

	fmt.Printf("本地：%s", local.Title)
	if local.Year > 0 {
		fmt.Printf("（%d）", local.Year)
	}
	fmt.Printf("  类型 %s", local.Kind)
	if local.SeasonNumber > 0 {
		fmt.Printf("  S%d", local.SeasonNumber)
	}
	if local.EpisodeCount > 0 {
		fmt.Printf("  %d 集", local.EpisodeCount)
	}
	fmt.Printf("  候选 %d 条", len(cands))
	if *deep {
		fmt.Print("（已取详情）")
	}
	fmt.Println()

	if len(ranked) == 0 {
		fmt.Println("没有候选：搜索没结果，或全被类型过滤掉了")
		return nil
	}
	for i, v := range ranked {
		mark := "  "
		if i == 0 {
			mark = "→ "
		}
		fmt.Printf("%s%d. %-6s %.3f  tmdb=%d 《%s》(%d)  %d 票/%.1f 热度\n",
			mark, i+1, v.Decision, v.Score, v.CandidateID, v.Title, v.Year, v.VoteCount, v.Popularity)
		for _, p := range v.Parts {
			fmt.Printf("        %-10s 权重 %.2f 得分 %.2f  %s\n", p.Name, p.Weight, p.Score, p.Note)
		}
		if i == 0 && len(ranked) > 1 {
			fmt.Printf("        领先第二名 %.3f（阈值 %.2f）\n", v.Margin, match.DefaultScorer().Margin)
		}
	}
	return nil
}

// enrichCandidates 给候选补上只在详情接口里才有的结构信号。
//
// 剧集：先从 series.seasons 拿对应季的集数；单集时长要再请求一次那一季
// （一次返回整季，不逐集请求）。电影：时长直接来自详情。
//
// 只对已经截断到前几名的候选做 —— 搜索能返回 20 条，值得细看的通常没那么多。
// 后续刮削处理器要做同样的事，届时应当把这套逻辑挪到能共用的地方。
func enrichCandidates(
	ctx context.Context,
	client provider.Client,
	kind, lang string,
	cands []match.Candidate,
	season int,
	log *slog.Logger,
) {
	for i := range cands {
		switch kind {
		case provider.KindMovie:
			m, err := client.Movie(ctx, cands[i].ID, lang)
			if err != nil {
				log.Warn("取电影详情失败", "tmdb", cands[i].ID, "err", err)
				continue
			}
			cands[i].RuntimeMinutes = m.RuntimeMinutes
			cands[i].AltTitles = m.AlternativeTitles

		default:
			s, err := client.Series(ctx, cands[i].ID, lang)
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

			want := season
			if want <= 0 {
				want = 1
			}
			det, err := client.Season(ctx, cands[i].ID, want, lang)
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
}

// splitList 把逗号（中英文都认）分隔的列表切开并去掉空项。
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '|'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
