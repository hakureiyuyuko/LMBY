package scan

// 扫描计划的调度器：按**每个库自己的间隔**触发扫描。
//
// 为什么是「轮询 tick + 现算到期」而不是给每个库排一个定时器：
//
//   - 计划随时会被改（界面上一改就该生效）。定时器方案要在改动时增删定时器，
//     还要处理「服务重启后把定时器重建回来」——多一套状态就多一处和真相不一致的地方；
//   - 现算到期只有一条 SQL（见 store.ListLibrariesDueForScan），**计划本身就存在库里**，
//     重启即恢复，也不需要把「下次什么时候跑」记在内存里。
//
// 代价是精度=一个 tick（默认 60 秒）—— 对「每天扫一次」这种计划完全够用。

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Scheduler 周期性检查「哪些库该扫了」，到期的就交给 Manager 跑。
type Scheduler struct {
	mgr  *Manager
	log  *slog.Logger
	tick time.Duration
}

// NewScheduler 创建调度器。tick <= 0 时用 60 秒。
func NewScheduler(mgr *Manager, log *slog.Logger, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{mgr: mgr, log: log, tick: tick}
}

// Run 阻塞运行调度循环（调用方起一个 goroutine；ctx 取消即退出）。
func (s *Scheduler) Run(ctx context.Context) {
	s.log.Info("扫描计划调度器已启动", "tick", s.tick.String())
	t := time.NewTicker(s.tick)
	defer t.Stop()
	// 启动时先查一次：上次关机期间到期的计划不该等到下一个 tick 才跑。
	s.checkOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			s.log.Info("扫描计划调度器退出")
			return
		case <-t.C:
			s.checkOnce(ctx)
		}
	}
}

// checkOnce 查一次到期库并逐个触发（导出给真库验收用）。
func (s *Scheduler) checkOnce(ctx context.Context) {
	due, err := s.mgr.st.ListLibrariesDueForScan(ctx)
	if err != nil {
		s.log.Warn("查询待扫描的媒体库失败", "err", err)
		return
	}
	for _, lib := range due {
		if id, err := s.mgr.Start(lib.ID, StartOptions{Trigger: "schedule"}); err != nil {
			if errors.Is(err, ErrAlreadyRunning) {
				// 正在扫（可能是上一次计划还没跑完，或用户手动扫着）—— 正常，跳过等下轮
				s.log.Debug("按计划跳过：该库已有扫描在跑", "library", lib.ID, "name", lib.Name)
				continue
			}
			s.log.Warn("按计划启动扫描失败", "library", lib.ID, "name", lib.Name, "err", err)
			continue
		} else {
			s.log.Info("按计划开始扫描", "library", lib.ID, "name", lib.Name,
				"intervalMinutes", lib.ScanIntervalMinutes, "scanRunId", id)
		}
	}
}
