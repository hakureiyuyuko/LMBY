package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/scrape"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// cmdScrape 是刮削的操作入口。
//
// 为什么要有「就地跑完」这个能力（scrape run）：验收刮削得能盯着看
// 「这一条到底匹配成什么了」，而服务进程里跑的任务只能翻日志。
// 就地跑与 worker 的语义一致（领取 → 处理 → 完成/退回队列），
// 所以拿它做的验证同样能代表线上行为。
//
// 用法：
//
//	lmby scrape enqueue [--library N] [--kind movie|series] [--force]
//	lmby scrape run     [--library N] [--kind movie|series] [--force] [--limit N]
//	lmby scrape status  [--library N]
//	lmby scrape reset   [--library N]
func cmdScrape(args []string) error {
	fs := flag.NewFlagSet("scrape", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	libraryID := fs.Int64("library", 0, "只处理指定媒体库（0 = 全部）")
	kind := fs.String("kind", "", "只处理 movie 或 series（默认两者）")
	force := fs.Bool("force", false, "连已匹配过的条目也重刮")
	limit := fs.Int("limit", 0, "最多处理多少条（0 = 不限，仅 run 有效）")
	asJSON := fs.Bool("json", false, "输出 JSON（默认输出人读的摘要）")

	flags, positional := splitFlagsAndPositionals(args)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if *kind != "" && *kind != "movie" && *kind != "series" {
		return fmt.Errorf("--kind 只能是 movie 或 series，收到 %q", *kind)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	// 每条条目的匹配决策都打到 stderr，stdout 留给摘要（便于脚本消费）
	log := newCLILogger(cfg.LogLevel)

	st, err := store.Open(ctx, cfg.Database.DSN, cfg.Database.MaxConns)
	if err != nil {
		return err
	}
	defer st.Close()

	if len(positional) == 0 {
		// 与 `lmby provider` 一致：不给子命令就打印用法，比默认干一件事更容错
		fmt.Println(scrapeUsage())
		return nil
	}
	sub := positional[0]

	switch sub {
	case "enqueue":
		n, err := enqueueScrapes(ctx, st, *libraryID, *kind, *force)
		if err != nil {
			return err
		}
		fmt.Printf("已入队 %d 条刮削任务\n", n)
		return printScrapeSummary(ctx, st, *libraryID, *asJSON)

	case "run":
		if n, err := enqueueScrapes(ctx, st, *libraryID, *kind, *force); err != nil {
			return err
		} else if n == 0 {
			fmt.Println("没有需要刮削的条目（加 --force 可重刮已匹配的）")
		}

		cached, _ := buildTMDBProvider(cfg, st, log)
		if cached == nil {
			return fmt.Errorf("未配置 TMDB 凭据（config.toml 的 [tmdb] 段，或 LMBY_TMDB_READ_TOKEN）")
		}
		handler := scrape.NewHandler(st, cached, log)

		done, failed := 0, 0
		started := time.Now()
		// limit 为 0 表示不限；否则处理够条数就停（方便先小范围试跑）
		for *limit <= 0 || done < *limit {
			t, err := st.ClaimTask(ctx, "cli-scrape", []string{store.TaskKindScrape})
			if err != nil {
				return err
			}
			if t == nil {
				break // 队列里没有刮削任务了
			}
			if err := handler.Handle(ctx, *t); err != nil {
				failed++
				// 与 worker 池同样的语义：失败退回队列（30s 后再试）
				if _, ferr := st.FailTask(ctx, t.ID, err.Error(), 30*time.Second); ferr != nil {
					log.Error("记录任务失败时出错", "taskId", t.ID, "err", ferr)
				}
				continue
			}
			if err := st.CompleteTask(ctx, t.ID); err != nil {
				return err
			}
			done++
		}
		// 把缓存命中率打出来：这是「重复刮削零 API 调用」最直接的证据
		hits, misses := cached.Stats()
		fmt.Printf("已处理 %d 条（任务级出错 %d 条），耗时 %s；元数据源缓存：命中 %d / 回源 %d\n",
			done, failed, time.Since(started).Round(time.Second), hits, misses)
		return printScrapeSummary(ctx, st, *libraryID, *asJSON)

	case "status":
		return printScrapeSummary(ctx, st, *libraryID, *asJSON)

	case "reset":
		n, err := resetScrapes(ctx, st, *libraryID)
		if err != nil {
			return err
		}
		fmt.Printf("已把 %d 条条目的匹配状态重置为待刮\n", n)
		return printScrapeSummary(ctx, st, *libraryID, *asJSON)

	default:
		return fmt.Errorf("未知子命令 %q（enqueue / run / status / reset）", sub)
	}
}

// enqueueScrapes 给指定的库（或全部库）入队刮削任务。
func enqueueScrapes(ctx context.Context, st *store.Store, libraryID int64, kind string, force bool) (int64, error) {
	ids, err := targetLibraryIDs(ctx, st, libraryID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, id := range ids {
		n, err := st.EnqueueScrapesForLibrary(ctx, id, kind, force)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func resetScrapes(ctx context.Context, st *store.Store, libraryID int64) (int64, error) {
	ids, err := targetLibraryIDs(ctx, st, libraryID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, id := range ids {
		n, err := st.ResetFailedScrapes(ctx, id)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func targetLibraryIDs(ctx context.Context, st *store.Store, libraryID int64) ([]int64, error) {
	if libraryID > 0 {
		if _, err := st.GetLibrary(ctx, libraryID); err != nil {
			return nil, fmt.Errorf("媒体库 %d 不存在", libraryID)
		}
		return []int64{libraryID}, nil
	}
	libs, err := st.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(libs))
	for _, l := range libs {
		ids = append(ids, l.ID)
	}
	return ids, nil
}

// printScrapeSummary 打印队列水位与各库的匹配状态分布。
func printScrapeSummary(ctx context.Context, st *store.Store, libraryID int64, asJSON bool) error {
	byKind, err := st.TaskStatsByKind(ctx)
	if err != nil {
		return err
	}
	stats, err := st.TaskStatsOf(ctx)
	if err != nil {
		return err
	}

	type libRow struct {
		ID   int64                 `json:"libraryId"`
		Name string                `json:"name"`
		Prog *store.ScrapeProgress `json:"progress"`
	}
	var rows []libRow
	ids, err := targetLibraryIDs(ctx, st, libraryID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		lib, err := st.GetLibrary(ctx, id)
		if err != nil {
			continue
		}
		prog, err := st.ScrapeProgressOf(ctx, id)
		if err != nil {
			return err
		}
		rows = append(rows, libRow{ID: id, Name: lib.Name, Prog: prog})
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"queue":      stats,
			"scrapeTodo": byKind[store.TaskKindScrape],
			"libraries":  rows,
		})
	}

	fmt.Printf("队列：待处理 %d / 进行中 %d / 失败 %d（其中刮削任务待处理 %d）\n",
		stats.Pending, stats.Running, stats.Failed, byKind[store.TaskKindScrape])
	for _, r := range rows {
		p := r.Prog
		fmt.Printf("库 #%d %s：nfo 元数据 %d / 已匹配 %d / 待人工 %d / 已锁定 %d / 失败 %d / 未刮 %d\n",
			r.ID, r.Name, p.NFO, p.Matched, p.Review, p.Manual, p.Failed, p.Local)
	}
	return nil
}

// scrapeUsage 在 `lmby scrape` 无参数时打印用法（与 cmdScrape 的文档保持一致）。
func scrapeUsage() string {
	return strings.Join([]string{
		"用法：",
		"  lmby scrape enqueue [--library N] [--kind movie|series] [--force]  入队待刮条目",
		"  lmby scrape run     [--library N] [--kind ...] [--force] [--limit N]",
		"                      入队并就地跑完（不依赖服务进程，便于验收）",
		"  lmby scrape status  [--library N]                                  看队列水位与匹配分布",
		"  lmby scrape reset   [--library N]                                  把失败的条目重置为待刮",
		"",
		"默认不刮「已有 nfo 元数据」与「人工锁定」的条目 —— 有 nfo 就用 nfo，",
		"只有没 nfo 的才去刮；--force 才会覆盖（会先把警告打到 stderr）。",
	}, "\n")
}
