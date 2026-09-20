package api

import (
	"sync"
	"time"
)

// loginLimiter 按 key（用户名+IP）统计失败次数并做退避。
//
// 用内存实现：进程重启后计数清零，对家庭自用场景足够。
// 目标是拖慢在线爆破，不追求精确配额。
type loginLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	items  map[string]*attemptRecord
}

type attemptRecord struct {
	count int
	last  time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		max:    max,
		window: window,
		items:  make(map[string]*attemptRecord),
	}
}

// Allow 判断当前是否允许尝试；不允许时返回还需等待的时长。
func (l *loginLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.items[key]
	if !ok {
		return true, 0
	}
	if time.Since(rec.last) > l.window {
		delete(l.items, key)
		return true, 0
	}
	if rec.count < l.max {
		return true, 0
	}
	return false, l.window - time.Since(rec.last)
}

// Fail 记录一次失败尝试。
func (l *loginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.items[key]
	if !ok || time.Since(rec.last) > l.window {
		l.items[key] = &attemptRecord{count: 1, last: time.Now()}
		return
	}
	rec.count++
	rec.last = time.Now()
}

// Reset 清除某个 key 的计数（登录成功后调用）。
func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.items, key)
}

// Cleanup 清理窗口外或从未再失败的记录，返回清理条数。
// 没有它的话，被大量不同 IP 撞库时 map 会一直涨。
func (l *loginLimiter) Cleanup() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := 0
	for k, rec := range l.items {
		if time.Since(rec.last) > l.window {
			delete(l.items, k)
			n++
		}
	}
	return n
}
