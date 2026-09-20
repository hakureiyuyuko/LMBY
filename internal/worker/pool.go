// Package worker 是后台任务队列的消费者。
//
// 设计要点：
//   - 领取用 `for update skip locked`，多个 worker 并发互不阻塞；
//   - 每个 worker 串行处理任务，并发度 = worker 数量（便于推理资源占用）；
//   - 任务失败按指数退避重试，超过 max_attempts 才判定失败；
//   - 维护协程负责回收「worker 被杀留下的 running 任务」与清理历史记录。
//
// 为什么不引入专门的队列中间件：任务量级是「几万个文件 × 几次重试」，
// PostgreSQL 完全能扛；而少一个组件就少一份部署与运维负担。
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// Handler 处理一类任务。实现必须自己处理好「任务已无意义」的情况
// （例如文件已被删除），那种情况应当返回 nil 而不是错误。
type Handler interface {
	// Kind 返回任务类型，需与入队时使用的 kind 一致。
	Kind() string
	// Handle 处理任务。返回错误表示失败，由队列决定是否重试。
	Handle(ctx context.Context, t store.Task) error
}

// Pool 管理一组 worker。
type Pool struct {
	st       *store.Store
	log      *slog.Logger
	workers  int
	idleWait time.Duration
	name     string

	mu       sync.RWMutex
	handlers map[string]Handler
	kinds    []string
}

// New 构造 worker 池。workers <= 0 时按 2 处理。
func New(st *store.Store, log *slog.Logger, workers int) *Pool {
	if workers <= 0 {
		workers = 2
	}
	host, _ := os.Hostname()
	return &Pool{
		st:       st,
		log:      log,
		workers:  workers,
		idleWait: 2 * time.Second,
		name:     fmt.Sprintf("%s-%d", host, os.Getpid()),
		handlers: map[string]Handler{},
	}
}

// Register 注册任务处理器。
func (p *Pool) Register(h Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[h.Kind()] = h
	p.kinds = append(p.kinds, h.Kind())
}

// Run 启动 worker 与维护协程，阻塞直到 ctx 结束。
func (p *Pool) Run(ctx context.Context) {
	p.mu.RLock()
	kinds := append([]string(nil), p.kinds...)
	p.mu.RUnlock()

	if len(kinds) == 0 {
		p.log.Warn("没有注册任务处理器，后台队列未启动")
		<-ctx.Done()
		return
	}

	p.log.Info("后台任务队列已启动",
		"kinds", strings.Join(kinds, ","), "workers", p.workers)

	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p.loop(ctx, fmt.Sprintf("%s#%d", p.name, n))
		}(i)
	}
	go p.maintain(ctx)

	<-ctx.Done()
	wg.Wait()
	p.log.Info("后台任务队列已停止")
}

func (p *Pool) loop(ctx context.Context, workerID string) {
	for {
		if ctx.Err() != nil {
			return
		}
		t, err := p.st.ClaimTask(ctx, workerID, p.registeredKinds())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Error("领取任务失败", "err", err)
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		if t == nil {
			// 队列空：等待一会儿再看。任务量大时 worker 会一直忙，几乎不会走到这里。
			if !sleepCtx(ctx, p.idleWait) {
				return
			}
			continue
		}
		p.exec(ctx, workerID, *t)
	}
}

func (p *Pool) registeredKinds() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.kinds...)
}

func (p *Pool) handler(kind string) (Handler, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	h, ok := p.handlers[kind]
	return h, ok
}

func (p *Pool) exec(ctx context.Context, workerID string, t store.Task) {
	h, ok := p.handler(t.Kind)
	if !ok {
		p.log.Error("没有对应的任务处理器", "kind", t.Kind, "taskId", t.ID)
		if _, err := p.st.FailTask(ctx, t.ID, "没有对应的任务处理器", time.Hour); err != nil {
			p.log.Error("标记任务失败时出错", "err", err)
		}
		return
	}

	started := time.Now()
	// 单个任务给足时间（网盘上探测可能很慢），但不能无限期占住 worker。
	taskCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	err := h.Handle(taskCtx, t)
	cancel()

	if err == nil {
		if cerr := p.st.CompleteTask(ctx, t.ID); cerr != nil {
			p.log.Error("标记任务完成时出错", "taskId", t.ID, "err", cerr)
		}
		p.log.Debug("任务完成",
			"kind", t.Kind, "taskId", t.ID, "worker", workerID,
			"attempt", t.Attempts, "ms", time.Since(started).Milliseconds())
		return
	}

	// 指数退避：30s → 2m → 8m …
	delay := time.Duration(math.Pow(4, float64(max(t.Attempts-1, 0)))) * 30 * time.Second
	retried, ferr := p.st.FailTask(ctx, t.ID, err.Error(), delay)
	if ferr != nil {
		p.log.Error("记录任务失败时出错", "taskId", t.ID, "err", ferr)
		return
	}

	attrs := []any{
		"kind", t.Kind, "taskId", t.ID, "worker", workerID,
		"attempt", t.Attempts, "of", t.MaxAttempts, "retry", retried,
		"err", err.Error(),
	}
	if retried {
		p.log.Warn("任务失败，将重试", attrs...)
	} else {
		p.log.Error("任务最终失败", attrs...)
	}
}

// maintain 周期性回收卡住的任务并清理历史记录。
func (p *Pool) maintain(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := p.st.RecoverStaleTasks(ctx, 30*time.Minute); err != nil {
				p.log.Warn("回收卡住的任务失败", "err", err)
			} else if n > 0 {
				p.log.Warn("已回收卡住的任务", "count", n)
			}
			if n, err := p.st.PurgeTasks(ctx, 7*24*time.Hour); err != nil {
				p.log.Warn("清理历史任务失败", "err", err)
			} else if n > 0 {
				p.log.Info("已清理历史任务", "count", n)
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ErrTaskObsolete 是处理器可以返回的哨兵错误，表示任务已无意义，
// 队列会直接丢弃而不重试。等价于返回 nil，保留它是为了代码可读性。
var ErrTaskObsolete = errors.New("worker: 任务已失效")
