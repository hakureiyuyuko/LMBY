package stream

import (
	"strings"
	"sync"
)

// ringLog 是一个只保留最近 N 行的环形日志。
//
// 为什么要留日志：转封装失败的原因基本全在 ffmpeg 的 stderr 里
// （「找不到编码器」「时间戳不单调」「文件读不到」），而进程是后台跑的，
// 不留下这些行就只能靠复现。同时又不能无限堆：一路电影跑几小时能刷出几十万行。
type ringLog struct {
	mu    sync.Mutex
	lines []string
	max   int
	cur   strings.Builder
}

func newRingLog(max int) *ringLog {
	if max <= 0 {
		max = 80
	}
	return &ringLog{max: max}
}

// Write 实现 io.Writer，按行切分后入环。
func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			r.push(r.cur.String())
			r.cur.Reset()
			continue
		}
		// 单行过长（例如进度行）时截断，避免把内存吃满。
		if r.cur.Len() < 2000 {
			r.cur.WriteByte(b)
		}
	}
	if r.cur.Len() >= 2000 {
		r.push(r.cur.String())
		r.cur.Reset()
	}
	return len(p), nil
}

func (r *ringLog) push(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

// Tail 返回最后 n 行（含没写完的当前行）。
func (r *ringLog) Tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := r.lines
	if cur := strings.TrimSpace(r.cur.String()); cur != "" {
		lines = append(append([]string{}, lines...), cur)
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
