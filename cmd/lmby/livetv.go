package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/livetvsync"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// cmdLiveTV 是直播电视（M5）的命令行入口：刷新订阅源、探测频道。
//
// 为什么要有命令行：定时刷新在服务里是个协程，验收/排查时不该为了「刷一次」
// 去等一个调度周期、或者改配置重启服务；探测同理（全量探测要几分钟）。
// 三个子命令与服务里的实现共用 internal/livetvsync —— 不是另写一套逻辑，
// 所以「命令行探得通、服务里播不了」这种事不会发生。

const liveTVUsage = `lmby livetv —— 直播电视（M5）

用法:
  lmby livetv status [--config 路径]              直播源清单 + 频道探测进度（JSON）
  lmby livetv refresh [--all] [--config 路径]     刷新订阅源
             默认只刷「到点了」的（与服务的定时刷新同一套判定）；--all 忽略间隔全刷。
  lmby livetv probe [--only-unknown] [--group 分组] [--config 路径]
             真连一次源站探测频道，结果写回 probe / probe_ok / probe_at。
             默认探全部启用中的频道；--only-unknown 只补探没探过的，
             --group 只探一个分组。
             ffprobe 路径、超时与并发取自配置（[ffmpeg] probe_path、[livetv] ...）。
`

func cmdLiveTV(args []string) error {
	if len(args) == 0 {
		fmt.Print(liveTVUsage)
		return nil
	}
	sub := args[0]

	fs := flag.NewFlagSet("livetv "+sub, flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	all := fs.Bool("all", false, "refresh：忽略刷新间隔，刷新全部启用的订阅源")
	onlyUnknown := fs.Bool("only-unknown", false, "probe：只探从没探过的频道")
	group := fs.String("group", "", "probe：只探这个分组")
	if err := fs.Parse(args[1:]); err != nil {
		return err
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

	svc := livetvsync.New(st, livetvsync.Options{
		ProbePath:        cfg.FFmpeg.ProbePath,
		ProbeTimeout:     time.Duration(cfg.LiveTV.ProbeTimeoutSeconds) * time.Second,
		ProbeConcurrency: cfg.LiveTV.ProbeConcurrency,
		Logger:           log,
	})

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	switch sub {
	case "status":
		sources, err := st.ListTVSources(ctx)
		if err != nil {
			return err
		}
		counts, err := st.CountTVChannelsBySource(ctx)
		if err != nil {
			return err
		}
		stats, err := svc.ProbeStats(ctx)
		if err != nil {
			return err
		}
		now := time.Now()
		out := make([]map[string]any, 0, len(sources))
		for _, s := range sources {
			item := map[string]any{
				"id":                     s.ID,
				"name":                   s.Name,
				"kind":                   s.Kind,
				"url":                    s.URL,
				"enabled":                s.Enabled,
				"refreshIntervalMinutes": s.RefreshIntervalMinutes,
				"lastStatus":             s.LastStatus,
				"lastChannelCount":       s.LastChannelCount,
				"channelCount":           counts[s.ID],
				"due":                    sourceIsDue(s, now),
			}
			if s.LastRefreshAt != nil {
				item["lastRefreshAt"] = s.LastRefreshAt.Format(time.RFC3339)
			}
			out = append(out, item)
		}
		return enc.Encode(map[string]any{
			"sources":            out,
			"autoRefresh":        cfg.LiveTV.AutoRefresh,
			"refreshTickSeconds": cfg.LiveTV.RefreshTickSeconds,
			"probe": map[string]any{
				"total":   stats.Total,
				"pending": stats.Pending,
				"ok":      stats.OK,
				"failed":  stats.Failed,
			},
		})

	case "refresh":
		res, err := svc.RefreshSources(ctx, !*all)
		if err != nil {
			return err
		}
		return enc.Encode(res)

	case "probe":
		start := time.Now()
		run, err := svc.ProbeChannels(ctx, livetvsync.ProbeOptions{
			OnlyUnknown: *onlyUnknown,
			Group:       *group,
		}, func(p livetvsync.ProbeProgress) {
			fmt.Fprintf(os.Stderr, "\r探测中 %d/%d（通 %d / 不通 %d）      ", p.Done, p.Total, p.OK, p.Failed)
		})
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		return enc.Encode(map[string]any{
			"total":          run.Total,
			"done":           run.Done,
			"ok":             run.OK,
			"failed":         run.Failed,
			"canceled":       run.Canceled,
			"elapsedSeconds": int(time.Since(start).Seconds()),
		})

	default:
		fmt.Print(liveTVUsage)
		return fmt.Errorf("未知子命令 %q（可用：status / refresh / probe）", sub)
	}
}

// sourceIsDue 判断一个源现在该不该被自动刷新（与 store.ListTVSourcesDue 同一套规则）。
//
// 命令行里复述一遍是为了在 status 里能一眼看出「为什么这个源没被刷」——
// 而这条 SQL 条件的口径必须与库里那条保持一致，否则状态页会撒谎。
func sourceIsDue(s store.TVSource, now time.Time) bool {
	if !s.Enabled || s.Kind != "url" || s.RefreshIntervalMinutes <= 0 {
		return false
	}
	if s.LastRefreshAt == nil {
		return true
	}
	return s.LastRefreshAt.Before(now.Add(-time.Duration(s.RefreshIntervalMinutes) * time.Minute))
}
