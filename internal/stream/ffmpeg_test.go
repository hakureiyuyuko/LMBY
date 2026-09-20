package stream

import (
	"strings"
	"testing"
)

// argsString 把参数拼起来方便断言顺序关系。
func argsString(args []string) string { return strings.Join(args, " ") }

func TestHLSArgs(t *testing.T) {
	opts := Options{FFmpeg: "ffmpeg", SegmentSeconds: 4, WindowSeconds: 300, MaxSessions: 2, IdleSeconds: 45}
	opts = opts.Normalize()

	t.Run("fmp4 + 音频复制 + 中途起播", func(t *testing.T) {
		spec := Spec{
			Path: "/mnt/media/a.mkv", VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: 1, AudioCopy: true, SegmentFormat: "fmp4", StartSeconds: 123.456,
		}
		args := hlsArgs(opts, spec, "/var/lib/lmby/streams/x")
		s := argsString(args)

		// -ss 必须在 -i 之前：只有输入侧跳转才能配 -c copy
		if strings.Index(s, "-ss") > strings.Index(s, "-i /mnt/media/a.mkv") {
			t.Fatalf("-ss 必须在 -i 之前（否则 -c copy 跳转会失败）：%s", s)
		}
		for _, want := range []string{
			"-ss 123.456",
			"-map 0:0", "-map 0:1",
			"-c:v copy", "-c:a copy",
			"-t 300",
			"-hls_segment_type fmp4",
			"-hls_fmp4_init_filename init.mp4",
			"-hls_time 4",
			"-hls_playlist_type event",
			"/var/lib/lmby/streams/x/seg_%05d.m4s",
			"/var/lib/lmby/streams/x/index.m3u8",
			"-nostdin",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("缺少参数 %q：\n%s", want, s)
			}
		}
		if strings.Contains(s, "-tag:v") {
			t.Error("h264 不该打 hvc1 标签")
		}
	})

	t.Run("hevc 要打 hvc1 标签，音频转 AAC 并降声道", func(t *testing.T) {
		spec := Spec{
			Path: "/mnt/media/b.mkv", VideoIndex: 0, VideoCodec: "hevc",
			AudioIndex: 2, AudioCopy: false, Downmix: true, SegmentFormat: "fmp4",
		}
		s := argsString(hlsArgs(opts, spec, "/out"))
		for _, want := range []string{"-tag:v hvc1", "-c:a aac", "-ac 2", "-b:a 192k", "-map 0:2"} {
			if !strings.Contains(s, want) {
				t.Errorf("缺少参数 %q：\n%s", want, s)
			}
		}
	})

	t.Run("没有音轨时不写音频参数", func(t *testing.T) {
		spec := Spec{Path: "/mnt/media/c.mp4", VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: -1, SegmentFormat: "fmp4"}
		s := argsString(hlsArgs(opts, spec, "/out"))
		if strings.Contains(s, "-c:a") || strings.Contains(s, "-map 0:-1") {
			t.Errorf("无音轨时不该出现音频参数：\n%s", s)
		}
	})

	t.Run("ts 分片用 .ts 扩展名且不写 init 段", func(t *testing.T) {
		spec := Spec{Path: "/mnt/media/d.mkv", VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: 1, AudioCopy: true, SegmentFormat: "ts"}
		s := argsString(hlsArgs(opts, spec, "/out"))
		if !strings.Contains(s, "-hls_segment_type ts") || !strings.Contains(s, "seg_%05d.ts") {
			t.Errorf("ts 分片参数不对：\n%s", s)
		}
		if strings.Contains(s, "-hls_fmp4_init_filename") {
			t.Errorf("ts 分片不该有 fMP4 的 init 段：\n%s", s)
		}
	})

	t.Run("从 0 起播时不写 -ss", func(t *testing.T) {
		spec := Spec{Path: "/mnt/media/e.mkv", VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: 1, AudioCopy: true, SegmentFormat: "fmp4", StartSeconds: 0}
		if s := argsString(hlsArgs(opts, spec, "/out")); strings.Contains(s, "-ss") {
			t.Errorf("起点为 0 时不该有 -ss：\n%s", s)
		}
	})

	t.Run("窗口参数用会话自己的值", func(t *testing.T) {
		spec := Spec{Path: "/mnt/media/f.mkv", VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: 1, AudioCopy: true, SegmentFormat: "fmp4", WindowSeconds: 60}
		if s := argsString(hlsArgs(opts, spec, "/out")); !strings.Contains(s, "-t 60") {
			t.Errorf("窗口没按会话的值走：\n%s", s)
		}
	})
}

func TestFormatSecondsUsesDot(t *testing.T) {
	// 某些 locale 下 fmt 会输出逗号，ffmpeg 会当成两个参数 —— 必须固定小数点
	if got := formatSeconds(12.5); got != "12.500" {
		t.Errorf("formatSeconds(12.5) = %q", got)
	}
}

func TestRingLogKeepsTail(t *testing.T) {
	r := newRingLog(3)
	_, _ = r.Write([]byte("a\nb\nc\nd\ne\n"))
	tail := r.Tail(2)
	if tail != "d\ne" {
		t.Errorf("Tail(2) = %q，期望 \"d\\ne\"", tail)
	}
	// 没有换行的残行也要能被看到（ffmpeg 报错常常不换行就卡住）
	_, _ = r.Write([]byte("boom"))
	if !strings.Contains(r.Tail(10), "boom") {
		t.Error("未换行的残行丢了：" + r.Tail(10))
	}
}
