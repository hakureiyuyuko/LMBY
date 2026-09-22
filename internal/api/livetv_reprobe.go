package api

// 直播起播失败之后**自动重探一次**（配合 livetvsync.Service.ProbeOne）。
//
// 为什么需要这一步：前台「只列能用能看的」（`enabled=1&hide_failed=1`）用的是探测
// **快照**（判据 `probe_ok is not false`）。源站是外部世界 —— 会 302 换调度节点、
// 节点会挂 —— 没有这一步的话，一台「上次探通过、现在打不开」的频道会一直挂在前台，
// 用户点一次失败一次，只能等管理员手动跑一次全量重探（那要真连几百个源站、以分钟计）。
//
// 三条克制，每条都是必须的：
//
//  1. **本地资源占满时绝不重探**：并发上限打满（`stream.ErrTooMany`）是本地的事，
//     跟源站没关系 —— 拿它去重探会把好频道标成失效，那是最坏的误判
//     （前台从此不显示一台其实健康的台，而管理员只能自己再手动探测一次才能恢复）；
//  2. 同一频道**节流**：观众反复点、多人同时点，同一台只探一次；
//  3. **异步 + 自己的超时**：探测要真连源站（失效源要等满 `probe_timeout`），
//     绝不能把 HTTP 请求挂在那儿等 —— 用户那一侧该立刻看到「拉流失败」。
//
// 重探的结果由探测自己判定（探通 = 保持可见并刷新快照；探不通 = 标记失效，
// 前台下次刷新就不显示它了）。**不拿「起播失败」直接当失效** —— 抖动与并发
// 都不该让一台健康的频道消失。

import (
	"context"
	"errors"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/stream"
)

const (
	// liveReprobeMinInterval 是同一频道触发重探的最小间隔。
	liveReprobeMinInterval = 60 * time.Second
	// liveReprobeGraceSeconds 是重探 ctx 在 probe_timeout 之外多给的余量
	// （探测自身也有超时，多给一点是为了让「探测超时」这条路径能自己收尾）。
	liveReprobeGraceSeconds = 10
)

// reprobeAfterPlayFailure 判断「这次起播失败要不要触发重探」。
//
// 纯函数，把这几个例外单独钉住（便于单测）：
//   - 本地并发打满 → 不探（与源站无关）；
//   - 没有失败（err == nil）→ 不探；
//   - 同一频道刚探过（节流窗口内）→ 不探。
func reprobeAfterPlayFailure(err error, lastReprobe time.Time, hasLast bool, now time.Time) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, stream.ErrTooMany) {
		return false
	}
	if hasLast && now.Sub(lastReprobe) < liveReprobeMinInterval {
		return false
	}
	return true
}

// scheduleChannelReprobe 在后台重探一条频道；异步，不阻塞调用方。
func (s *Server) scheduleChannelReprobe(ch store.TVChannel, playErr error) {
	if s.live == nil {
		return
	}
	now := time.Now()
	last, hasLast := time.Time{}, false
	if v, ok := s.liveReprobeLast.Load(ch.ID); ok {
		if t, ok2 := v.(time.Time); ok2 {
			last, hasLast = t, true
		}
	}
	if !reprobeAfterPlayFailure(playErr, last, hasLast, now) {
		return
	}
	s.liveReprobeLast.Store(ch.ID, now)

	go func() {
		parent := s.base()
		timeout := time.Duration(s.cfg.LiveTV.ProbeTimeoutSeconds)*time.Second +
			liveReprobeGraceSeconds*time.Second
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()

		res, err := s.live.ProbeOne(ctx, ch.ID)
		if err != nil {
			// 探测**自己**出错（工具不可用、查库失败…）：一条结果都没写，
			// 库里还是原样 —— 这时候什么都不该断言，只留一条日志。
			s.log.Warn("起播失败后重探没跑成（频道状态未改动）",
				"channel", ch.ID, "name", ch.Name, "err", err.Error())
			return
		}
		if res.OK {
			s.log.Info("起播失败后重探：这条其实还通（并发或源站抖动），保持可见",
				"channel", ch.ID, "name", ch.Name, "probe", res.Summary)
			return
		}
		s.log.Warn("起播失败后重探：判定失效，前台不再显示这一台",
			"channel", ch.ID, "name", ch.Name, "probe", res.Summary)
	}()
}
