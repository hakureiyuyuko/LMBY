package stream

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveHLSArgs 盯住「直播分支」的关键参数：这些每一条错了都会表现为
// 「能播但不对」——分片越滚越大、播放器以为直播结束、或者起播慢到不可用。
func TestLiveHLSArgs(t *testing.T) {
	opts := Options{FFmpeg: "ffmpeg", SegmentSeconds: 4, WindowSeconds: 300, MaxSessions: 4, IdleSeconds: 45}.Normalize()

	t.Run("rtsp 源 + 音频转 AAC", func(t *testing.T) {
		spec := Spec{
			Key:  "live:1",
			Path: "rtsp://10.0.0.1:554/live/cctv1?AuthInfo=x",
			Live: true,
			Video: VideoEncode{
				Copy: true,
				// 直播源的输入参数（-i 之前）
				InputArgs: []string{"-rtsp_transport", "tcp", "-timeout", "15000000"},
			},
			SegmentSeconds: 2,
		}
		const outDir = "/var/lib/lmby/streams/live-ch1"
		s := argsString(hlsArgs(opts, spec, outDir))
		// 输入参数必须在 -i 之前
		if strings.Index(s, "-rtsp_transport tcp") > strings.Index(s, "-i rtsp://") {
			t.Fatalf("输入参数必须在 -i 之前：%s", s)
		}
		for _, want := range []string{
			"-map 0:v:0?", // 可选映射：有的源没有音轨
			"-map 0:a:0?",
			"-c:v copy",                // 直播不转码
			"-c:a aac -b:a 192k -ac 2", // MP2 → AAC，浏览器才放得了
			"-hls_time 2",              // 分片时长就是起播延迟下限
			"-hls_list_size 6",         // 滚动窗口
			"-hls_delete_threshold 3",
			"-hls_flags delete_segments+omit_endlist+independent_segments+temp_file",
			"-hls_segment_type mpegts",
			filepath.Join(filepath.FromSlash(outDir), "seg_%05d.ts"),
		} {
			if !strings.Contains(s, want) {
				t.Errorf("缺少参数 %q：\n%s", want, s)
			}
		}
		// 直播不能带窗口限制：`-t` 会把直播按秒切断，`-ss` 会跳走
		if strings.Contains(s, "-t 300") || strings.Contains(s, "-ss") {
			t.Errorf("直播不该出现 -t / -ss：\n%s", s)
		}
		if strings.Contains(s, "-hls_list_size 0") {
			t.Errorf("直播不能保留全部分片（会无限增长）：\n%s", s)
		}
	})

	t.Run("自定义窗口大小与分片时长", func(t *testing.T) {
		spec := Spec{
			Key: "live:2", Path: "http://10.0.0.2/live.ts", Live: true,
			LiveListSize: 10, SegmentSeconds: 3, Video: CopyVideoEncode(),
		}
		s := argsString(hlsArgs(opts, spec, "/out"))
		for _, want := range []string{"-hls_list_size 10", "-hls_time 3"} {
			if !strings.Contains(s, want) {
				t.Errorf("缺少参数 %q：\n%s", want, s)
			}
		}
	})

	t.Run("非法窗口大小回落到默认值", func(t *testing.T) {
		spec := Spec{Key: "live:3", Path: "http://x/y.ts", Live: true, LiveListSize: 999, Video: CopyVideoEncode()}
		s := argsString(hlsArgs(opts, spec, "/out"))
		if !strings.Contains(s, "-hls_list_size 6") {
			t.Errorf("越界的窗口大小应当回落到 6：\n%s", s)
		}
	})
}

// TestLiveSkipsThrottle 直播会话不参与节流：源站按实时投递，
// 暂停 ffmpeg 只会把画面卡住（点播节流是「预生成超前」才暂停）。
func TestLiveSkipsThrottle(t *testing.T) {
	s := &Session{Spec: Spec{Key: "live:1", Live: true}, segSecs: 2}
	called := false
	s.pauseFn = func() error { called = true; return nil }
	s.throttle(1)
	if called {
		t.Error("直播会话不该被节流（不该调用 pause）")
	}
	if s.Paused() {
		t.Error("直播会话不该处于暂停状态")
	}
}
