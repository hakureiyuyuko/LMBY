// Package livetvsync 是直播电视（M5）的**运行层**：订阅源刷新与频道探测。
//
// 为什么不写进 internal/livetv：那边是纯解析/签名，而 store 反过来要用
// livetv.Entry（store → livetv），把「拿库里的行去拉流、探流、再写回」放进去就成环了。
// 这里只做这一件事，解析与落库分别由 livetv 与 store 负责。
package livetvsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/livetv"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// ErrNoSourceURL 表示这个源没有可重拉的地址。
//
// 粘贴/上传的源只在导入那一刻有内容，之后就只剩频道表了 ——
// 想更新只能重新导入一份，不能「刷新」。接口层把它翻成 400。
var ErrNoSourceURL = errors.New("粘贴/上传的直播源没有可重拉的地址，请重新导入")

// sourceTimeout 是刷新单个订阅源的上限（拉取 + 导入）。
const sourceTimeout = 2 * time.Minute

// Options 是 Service 的构造参数。
type Options struct {
	// ProbePath 是 ffprobe 路径（空 = 找 PATH 里的 ffprobe）。
	ProbePath string
	// ProbeTimeout 是单个频道的探测超时，默认 10 秒。
	//
	// 这个是**实例相关的**：源站离得远/慢就调大，名单里死源多就调小
	// （探测时间基本上就是死源数量 × 这个值 ÷ 并发数）。
	ProbeTimeout time.Duration
	// ProbeConcurrency 是并发探测数，默认 4。
	ProbeConcurrency int
	// Fetcher 拉取订阅源；nil 时用默认的。
	Fetcher *livetv.Fetcher
	// Logger；nil 时用 slog 默认的。
	Logger *slog.Logger
}

// Service 提供「刷新订阅源」与「探测频道」两件事。
type Service struct {
	st               *store.Store
	fetch            *livetv.Fetcher
	log              *slog.Logger
	probePath        string
	probeTimeout     time.Duration
	probeConcurrency int

	// importMu 串行化导入。
	//
	// 手动刷新、定时刷新、新建源三条路都可能同时进来（定时任务到点的瞬间
	// 用户正好点了刷新很常见）。同一个源被并发导入，会在地址唯一索引上
	// 互相打架，最后表现为「刷新失败」这种看不懂的错。
	importMu sync.Mutex
}

// New 构造 Service。
func New(st *store.Store, opt Options) *Service {
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	fetch := opt.Fetcher
	if fetch == nil {
		fetch = livetv.NewFetcher()
	}
	timeout := opt.ProbeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	concurrency := opt.ProbeConcurrency
	if concurrency < 1 {
		concurrency = 4
	}
	return &Service{
		st:               st,
		fetch:            fetch,
		log:              log,
		probePath:        strings.TrimSpace(opt.ProbePath),
		probeTimeout:     timeout,
		probeConcurrency: concurrency,
	}
}

// ---------------------------------------------------------------- 源：拉取与导入

// Resolve 把「订阅地址」或「粘贴/上传的文本」变成条目。
//
// 单独立出来是为了「失败不留痕」：内容拉不到/解析不出频道时，
// 调用方不应该已经在库里建好一条没有频道的空源。
func (s *Service) Resolve(ctx context.Context, rawurl, content string) ([]livetv.Entry, error) {
	if strings.TrimSpace(content) == "" {
		return s.fetch.Fetch(ctx, rawurl)
	}
	return livetv.ParseContent(content)
}

// Import 把解析好的条目写进库里，并把结果记进源的 last_status。
//
// 全程持有 importMu（见字段注释）；真正的落库语义在 store.ImportTVChannels。
func (s *Service) Import(ctx context.Context, src *store.TVSource, entries []livetv.Entry) (store.TVImportResult, error) {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	res, err := s.st.ImportTVChannels(ctx, src.ID, entries)
	if err != nil {
		// 失败也留痕：界面上要能看到「上次刷新为什么失败」
		_ = s.st.MarkTVSourceRefreshed(ctx, src.ID, "失败："+err.Error(), 0)
		return res, err
	}
	_ = s.st.MarkTVSourceRefreshed(ctx, src.ID, "ok", res.Total)
	return res, nil
}

// Refresh 重新拉取（或解析传入的内容）并导入一个源。
//
// content 为空时按源的订阅地址去拉；非空时用它（导入的那一刻已经在手上，
// 不必再拉一次）。
func (s *Service) Refresh(ctx context.Context, src *store.TVSource, content string) (store.TVImportResult, error) {
	if strings.TrimSpace(content) == "" && (src.Kind != "url" || strings.TrimSpace(src.URL) == "") {
		return store.TVImportResult{}, ErrNoSourceURL
	}
	entries, err := s.Resolve(ctx, src.URL, content)
	if err != nil {
		// 拉取/解析失败也写进 last_status，否则界面上只会看到「上次成功」的时间
		_ = s.st.MarkTVSourceRefreshed(ctx, src.ID, "失败："+err.Error(), 0)
		return store.TVImportResult{}, err
	}
	return s.Import(ctx, src, entries)
}

// SourceRefresh 是单个源的刷新结果（CLI/日志/接口都能直接用）。
type SourceRefresh struct {
	SourceID int64  `json:"sourceId"`
	Name     string `json:"name"`
	Added    int    `json:"added"`
	Updated  int    `json:"updated"`
	Removed  int    `json:"removed"`
	Total    int    `json:"total"`
	Err      string `json:"error,omitempty"`
}

// RefreshSummary 是一次「批量刷新订阅源」的结果。
type RefreshSummary struct {
	Refreshed int             `json:"refreshed"`
	Failed    int             `json:"failed"`
	Sources   []SourceRefresh `json:"sources"`
}

// RefreshSources 批量刷新订阅源。
//
// onlyDue=true 时只刷「到点了」的（定时任务用，间隔由每个源自己的
// refresh_interval_minutes 决定）；false 时刷全部启用的 url 型源（手动全刷）。
//
// 单个源失败不影响其它源：结果里逐个源都带 Err，返回的 error 只表示
// 「连该刷哪些源都查不出来」这种整体性失败。
func (s *Service) RefreshSources(ctx context.Context, onlyDue bool) (RefreshSummary, error) {
	var (
		sources []store.TVSource
		err     error
	)
	if onlyDue {
		sources, err = s.st.ListTVSourcesDue(ctx, time.Now())
	} else {
		var all []store.TVSource
		all, err = s.st.ListTVSources(ctx)
		for _, src := range all {
			if src.Enabled && src.Kind == "url" && strings.TrimSpace(src.URL) != "" {
				sources = append(sources, src)
			}
		}
	}
	if err != nil {
		return RefreshSummary{}, err
	}

	out := RefreshSummary{Sources: make([]SourceRefresh, 0, len(sources))}
	for i := range sources {
		src := sources[i]
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		one, cancel := context.WithTimeout(ctx, sourceTimeout)
		res, err := s.Refresh(one, &src, "")
		cancel()

		item := SourceRefresh{SourceID: src.ID, Name: src.Name}
		if err != nil {
			item.Err = err.Error()
			out.Failed++
			s.log.Warn("刷新直播源失败", "source", src.ID, "name", src.Name, "err", err)
		} else {
			item.Added, item.Updated, item.Removed, item.Total = res.Added, res.Updated, res.Removed, res.Total
			out.Refreshed++
			s.log.Info("已刷新直播源", "source", src.ID, "name", src.Name,
				"added", res.Added, "updated", res.Updated, "removed", res.Removed, "total", res.Total)
		}
		out.Sources = append(out.Sources, item)
	}
	return out, nil
}

// ---------------------------------------------------------------- 频道：探测

// ProbeStats 返回探测进度（从库里现算，所以进程重启也不丢）。
func (s *Service) ProbeStats(ctx context.Context) (store.TVChannelProbeStats, error) {
	return s.st.TVChannelProbeStats(ctx)
}

// ProbeOptions 选要探测哪些频道。
//
// 三个条件都是可选的（与的关系）。为什么要能选：全量探测要真连几百个源站、
// 以分钟计；「刚导入的这一组/这一条通不通」是更常用的用法。
type ProbeOptions struct {
	// OnlyUnknown 只探从没探过的（probe_at 为空）——补探用。
	OnlyUnknown bool
	// Group 只探这个分组（空 = 不限）。
	Group string
	// ChannelIDs 只探这几条（空 = 不限）。
	ChannelIDs []int64
}

// ProbeProgress 是一次探测的实时进度（给接口/CLI 显示用）。
type ProbeProgress struct {
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	OK       int    `json:"ok"`
	Failed   int    `json:"failed"`
	Current  string `json:"current,omitempty"`
	Canceled bool   `json:"canceled"`
}

// ProbeChannels 探测频道并把结果写回库里。
//
// 默认探全部启用中的频道（源站地址没变但源站坏了的，只有重探才知道）；
// 用 ProbeOptions 缩小范围（只补未探的 / 只探一个分组 / 只探指定的几条）。
//
// 两条安全措施：
//   - **先确认 ffprobe 真能跑**，跑不了就一条结果都不写 ——
//     把「工具没装」写成「全部频道失效」是最糟的失败模式；
//   - 逐个频道失败互不影响，ctx 取消就停下（已经探过的结果保留）。
//
// progress 可为 nil；非 nil 时在每次写完库后回调一次（并发安全）。
func (s *Service) ProbeChannels(ctx context.Context, opt ProbeOptions, progress func(ProbeProgress)) (ProbeProgress, error) {
	channels, err := s.st.ListTVChannelsForProbe(ctx, store.TVProbeFilter{
		OnlyUnknown: opt.OnlyUnknown,
		Group:       opt.Group,
		IDs:         opt.ChannelIDs,
	})
	if err != nil {
		return ProbeProgress{}, err
	}
	run := ProbeProgress{Total: len(channels)}
	if len(channels) == 0 {
		return run, nil
	}
	if err := s.CheckProbeTool(ctx); err != nil {
		return run, err
	}

	log := s.log
	log.Info("开始探测直播频道", "total", run.Total, "concurrency", s.probeConcurrency,
		"timeout", s.probeTimeout.String(),
		"onlyUnknown", opt.OnlyUnknown, "group", opt.Group, "ids", len(opt.ChannelIDs))

	var mu sync.Mutex
	queue := make(chan int)
	var wg sync.WaitGroup

	for w := 0; w < s.probeConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					return
				}
				ch := channels[i]
				res := Probe(ctx, s.probePath, ch.URL, livetv.InputArgs(ch.URL, ch.Headers), s.probeTimeout)
				// 顺带记下源视频编码/高度：起播要靠它判断「转封装够不够」
				if err := s.st.SetTVChannelProbe(ctx, ch.ID, res.Summary, res.OK, res.VideoCodec, res.VideoHeight); err != nil {
					log.Warn("保存频道探测结果失败", "channel", ch.ID, "name", ch.Name, "err", err)
				}

				mu.Lock()
				run.Done++
				if res.OK {
					run.OK++
				} else {
					run.Failed++
				}
				snapshot := run
				snapshot.Current = ch.Name
				if progress != nil {
					progress(snapshot)
				}
				mu.Unlock()

				if !res.OK {
					log.Info("频道探测不通", "channel", ch.ID, "name", ch.Name, "summary", res.Summary)
				}
			}
		}()
	}

	go func() {
		defer close(queue)
		for i := range channels {
			select {
			case queue <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()
	run.Canceled = ctx.Err() != nil
	log.Info("直播频道探测结束", "total", run.Total, "done", run.Done,
		"ok", run.OK, "failed", run.Failed, "canceled", run.Canceled)
	return run, nil
}

// ProbeOne 探测**一条**频道，并把结果写回库里。
//
// 给「起播失败后自动重探」用：`probe_ok` / `probe_at` 是一份**快照**，而源站是外部世界
// （RTSP 会 302 换调度节点、节点会挂）。起播失败时真探一次，前台（`hide_failed`）
// 下一次刷新就能按最新结果决定这一台还该不该出现；探通了也顺手把快照刷新一遍。
//
// 与 ProbeChannels 共用同一套输入参数（`livetv.InputArgs`）和同一条底线：
// **先确认 ffprobe 真能跑，跑不了就一条结果都不写** —— 否则「工具没了」会被写成
// 「这个频道失效」，前台把好频道藏起来是最坏的失败模式。
//
// 不复用那个「单飞」状态：全量探测在跑时也该允许重探一条（两者互不妨碍）。
func (s *Service) ProbeOne(ctx context.Context, channelID int64) (ProbeResult, error) {
	chans, err := s.st.ListTVChannelsForProbe(ctx, store.TVProbeFilter{IDs: []int64{channelID}})
	if err != nil {
		return ProbeResult{}, err
	}
	if len(chans) == 0 {
		return ProbeResult{}, store.ErrNotFound
	}
	if err := s.CheckProbeTool(ctx); err != nil {
		return ProbeResult{}, err
	}
	ch := chans[0]
	res := Probe(ctx, s.probePath, ch.URL, livetv.InputArgs(ch.URL, ch.Headers), s.probeTimeout)
	if err := s.st.SetTVChannelProbe(ctx, ch.ID, res.Summary, res.OK, res.VideoCodec, res.VideoHeight); err != nil {
		return res, err
	}
	return res, nil
}

// CheckProbeTool 确认 ffprobe 真的能跑（起探测前调）。
//
// 探测的判定结果会**覆盖**用户之前看到的状态，所以「工具不可用」这种情况
// 绝对不能变成「所有频道都失效」—— 这里拦住，一条结果都不写。
// 导出是给接口层用：POST 起探测时先检一次，起了一趟「全都失败」的探测
// 比当场回 503 难解释得多。
func (s *Service) CheckProbeTool(ctx context.Context) error {
	path := s.probePath
	if path == "" {
		path = "ffprobe"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "-version")
	if out, err := cmd.Output(); err != nil {
		return fmt.Errorf("ffprobe 不可用（%s）：%w", path, err)
	} else if len(out) == 0 {
		return fmt.Errorf("ffprobe 不可用（%s）：没有任何输出", path)
	}
	return nil
}
