package stream

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// 这一组钉住「节流」的判定逻辑：位置怎么算、什么时候暂停、什么时候恢复。
//
// 不启真的 ffmpeg：判定全是纯计算（已生成 vs 客户端消费到哪），把 pauseFn/resumeFn
// 换成计数器就能观察它到底有没有要求暂停/恢复。真发信号那一步在
// proc_unix.go / proc_windows.go 里，端到端由 scripts/dev/verify-transcode.sh 验
//（它会去看进程状态是不是真的 T）。

func TestSegmentIndex(t *testing.T) {
	good := map[string]int{"seg_00000.m4s": 0, "seg_00012.m4s": 12, "seg_3.ts": 3}
	for name, want := range good {
		got, ok := SegmentIndex(name)
		if !ok || got != want {
			t.Errorf("SegmentIndex(%q) = %d,%v，想要 %d,true", name, got, ok, want)
		}
	}
	for _, bad := range []string{"index.m3u8", "init.mp4", "seg_x.m4s", "../seg_1.m4s", ""} {
		if _, ok := SegmentIndex(bad); ok {
			t.Errorf("SegmentIndex(%q) 不该被接受", bad)
		}
	}
}

// newThrottleSession 造一个「已生成 100 秒内容」的会话，并把暂停/恢复换成计数器。
func newThrottleSession(t *testing.T) (s *Session, pauses, resumes *int) {
	t.Helper()
	opts := Options{
		FFmpeg: "ffmpeg", Root: t.TempDir(), SegmentSeconds: 4,
		WindowSeconds: 300, MaxSessions: 2, IdleSeconds: 45, ThrottleSeconds: 60,
	}
	opts = opts.Normalize()
	s = newSession(opts, Spec{Key: "k", Path: "/x.mkv"})
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// 播放列表里放 25 个分片 = 25 × 4s = 100 秒
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, "seg_%05d.m4s\n", i)
	}
	if err := os.WriteFile(s.playlistPath(), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	p, r := 0, 0
	s.pauseFn = func() error { p++; return nil }
	s.resumeFn = func() error { r++; return nil }
	return s, &p, &r
}

func TestThrottlePausesAndResumes(t *testing.T) {
	s, pauses, resumes := newThrottleSession(t)
	if got := s.GeneratedSeconds(); got != 100 {
		t.Fatalf("已生成位置 = %v，想要 100", got)
	}

	// 客户端才到 5s → 领先 95s > 60s → 暂停
	s.MarkClientPosition(5)
	s.throttle(60)
	if !s.Paused() || *pauses != 1 {
		t.Fatalf("应当暂停：paused=%v pauses=%d", s.Paused(), *pauses)
	}
	// 已经暂停了就不该重复发信号（每 5 秒发一次会把日志刷爆）
	s.throttle(60)
	if *pauses != 1 {
		t.Fatalf("已经暂停时不该重复发信号：pauses=%d", *pauses)
	}

	// 客户端追到 80s → 领先 20s < 30s（阈值的一半）→ 恢复
	s.MarkClientPosition(80)
	s.throttle(60)
	if s.Paused() || *resumes != 1 {
		t.Fatalf("应当恢复：paused=%v resumes=%d", s.Paused(), *resumes)
	}

	// 客户端位置只增不减：分片请求与进度上报会一前一后，
	// 取小的话会出现「刚恢复又暂停」的抖动。
	s.MarkClientPosition(10)
	if got := s.ClientSeconds(); got != 80 {
		t.Errorf("客户端位置不该回退：%v", got)
	}
}

// TestThrottleMidMovieResume 钉住一个真跑踩到的坑：从影片中途续播时，节流器
// 不该在起播瞬间就暂停 ffmpeg。
//
// 「已生成」是**绝对**媒体位置（窗口起点 + 分片数×分片时长），而客户端位置一开始
// 是空的（还没拉任何分片、也没上报进度）。不把窗口起点先垫成客户端位置的话，
// 续播（起点 1403s）一算就是「领先 1403s」→ 立即暂停 → 20 秒产不出第一个分片
// → 整路被判成放不了（表现：从中间接着看直接开不起来）。
func TestThrottleMidMovieResume(t *testing.T) {
	opts := Options {FFmpeg: "ffmpeg", Root: t.TempDir(), SegmentSeconds: 4,
		WindowSeconds: 300, MaxSessions: 2, IdleSeconds: 45, ThrottleSeconds: 60}
	opts = opts.Normalize()
	s := newSession(opts, Spec{Key: "k", Path: "/x.mkv", StartSeconds: 1403})
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	pauses := 0
	s.pauseFn = func() error { pauses++; return nil }
	s.resumeFn = func() error { return nil }

	// 刚建好、还没产出分片：客户端就在起点，什么都不该发生
	s.throttle(60)
	if pauses != 0 || s.Paused() {
		t.Fatalf("起播瞬间不该暂停：pauses=%d paused=%v", pauses, s.Paused())
	}

	// 产出一个分片（已生成 1407s）：领先 4s，仍不该暂停
	if err := os.WriteFile(s.playlistPath(), []byte("#EXTM3U\nseg_00000.m4s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.throttle(60)
	if pauses != 0 {
		t.Fatalf("领先 4s 不该暂停：pauses=%d", pauses)
	}
	if got := s.ClientSeconds(); got != 1403 {
		t.Fatalf("客户端位置应当从窗口起点算起，得到 %v", got)
	}

	// 一路编到领先超过阀值（1403 + 19×4 = 1479，领先 76s）→ 这才该暂停
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := 0; i < 19; i++ {
		fmt.Fprintf(&b, "seg_%05d.m4s\n", i)
	}
	if err := os.WriteFile(s.playlistPath(), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s.throttle(60)
	if pauses != 1 || !s.Paused() {
		t.Fatalf("领先 76s 应当暂停：pauses=%d paused=%v", pauses, s.Paused())
	}
}

func TestThrottleDisabledWithZero(t *testing.T) {
	opts := Options{FFmpeg: "ffmpeg", Root: t.TempDir(), SegmentSeconds: 4,
		WindowSeconds: 300, MaxSessions: 2, IdleSeconds: 45, ThrottleSeconds: 0}
	opts = opts.Normalize()
	if opts.ThrottleSeconds != 0 {
		t.Fatalf("0 应当保留为「关闭节流」，得到 %d", opts.ThrottleSeconds)
	}
	s := newSession(opts, Spec{Key: "k", Path: "/x.mkv"})
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.playlistPath(), []byte("#EXTM3U\nseg_00000.m4s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sent := 0
	s.pauseFn = func() error { sent++; return nil }
	s.resumeFn = func() error { sent++; return nil }
	s.throttle(0)
	if s.Paused() || sent != 0 {
		t.Errorf("关闭节流时不该发信号：paused=%v sent=%d", s.Paused(), sent)
	}
}

func TestThrottleGivesUpOnUnsupportedPlatform(t *testing.T) {
	// Windows 上没法暂停进程：应当永久关掉节流、只记一次日志，
	// 而不是每 5 秒报一次错、或者一直记成「已暂停」（那会让状态接口说谎）。
	s, _, _ := newThrottleSession(t)
	s.pauseFn = func() error { return errThrottleUnsupported }
	s.MarkClientPosition(5)
	s.throttle(60)
	if !s.throttleOff.Load() {
		t.Error("平台不支持时应当永久关掉节流")
	}
	if s.Paused() {
		t.Error("没真的暂停，就不该记成已暂停")
	}
	if log := s.logs.Tail(40); !strings.Contains(log, "不支持暂停 ffmpeg") {
		t.Errorf("应当把原因写进会话日志：%s", log)
	}
}
