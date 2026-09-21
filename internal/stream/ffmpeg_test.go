package stream

import (
	"path/filepath"
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
			AudioIndex: 1, Audio: AudioEncode{Copy: true}, SegmentFormat: "fmp4", StartSeconds: 123.456,
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
			// 默认值是 5（只留 20 秒），必须显式 0
			"-hls_list_size 0",
			"-hls_segment_options movflags=+frag_discont+skip_sidx",
			"-map_metadata -1",
			"-map_chapters -1",
			"/var/lib/lmby/streams/x/seg_%05d.m4s",
			"/var/lib/lmby/streams/x/index.m3u8",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("缺少参数 %q：\n%s", want, s)
			}
		}
		// 节流走 SIGSTOP/SIGCONT（不是 keys），所以照旧带 -nostdin：
		// 免得 ffmpeg 去读我们这个后台进程的 stdin。
		if !strings.Contains(s, "-nostdin") {
			t.Error("应当带 -nostdin：节流走信号，不需要 stdin 通道")
		}
		if strings.Contains(s, "-tag:v") {
			t.Error("h264 不该打 hvc1 标签")
		}
	})

	t.Run("hevc 要打 hvc1 标签，音频转 AAC 并降声道", func(t *testing.T) {
		spec := Spec{
			Path: "/mnt/media/b.mkv", VideoIndex: 0, VideoCodec: "hevc",
			AudioIndex: 2, Audio: AudioEncode{Args: []string{"-c:a", "aac", "-b:a", "192k", "-ac", "2"}}, SegmentFormat: "fmp4",
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
			AudioIndex: 1, Audio: AudioEncode{Copy: true}, SegmentFormat: "ts"}
		s := argsString(hlsArgs(opts, spec, "/out"))
		if !strings.Contains(s, "-hls_segment_type ts") || !strings.Contains(s, "seg_%05d.ts") {
			t.Errorf("ts 分片参数不对：\n%s", s)
		}
		if strings.Contains(s, "-hls_fmp4_init_filename") || strings.Contains(s, "-hls_segment_options") {
			t.Errorf("ts 分片不该有 fMP4 专有参数：\n%s", s)
		}
	})

	t.Run("从 0 起播时不写 -ss", func(t *testing.T) {
		spec := Spec{Path: "/mnt/media/e.mkv", VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: 1, Audio: AudioEncode{Copy: true}, SegmentFormat: "fmp4", StartSeconds: 0}
		if s := argsString(hlsArgs(opts, spec, "/out")); strings.Contains(s, "-ss") {
			t.Errorf("起点为 0 时不该有 -ss：\n%s", s)
		}
	})

	t.Run("窗口参数用会话自己的值", func(t *testing.T) {
		spec := Spec{Path: "/mnt/media/f.mkv", VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: 1, Audio: AudioEncode{Copy: true}, SegmentFormat: "fmp4", WindowSeconds: 60}
		if s := argsString(hlsArgs(opts, spec, "/out")); !strings.Contains(s, "-t 60") {
			t.Errorf("窗口没按会话的值走：\n%s", s)
		}
	})
}

func TestHLSArgsBurnSubtitle(t *testing.T) {
	// 烧录图形字幕：画面由 -filter_complex 产出，必须 map 它给的标签。
	//
	// 这里卡住的是一条真踩过的坑：如果同时保留了 `-map 0:0`（原始视频流）
	// 与滤镜输出，同一个流会被送两遍，ffmpeg 在过滤器协商阶段直接失败
	//（"Impossible to convert between the formats supported by…"），
	// 用户看到的是「放不了」。
	opts := Options{FFmpeg: "ffmpeg", SegmentSeconds: 4, WindowSeconds: 300, MaxSessions: 2, IdleSeconds: 45}
	opts = opts.Normalize()
	spec := Spec{
		Path: "/mnt/media/a.mkv", VideoIndex: 0, VideoCodec: "hevc", AudioIndex: 1,
		Audio: AudioEncode{Args: []string{"-c:a", "aac", "-b:a", "192k"}},
		Video: VideoEncode{
			InputArgs:     []string{"-vaapi_device", "/dev/dri/renderD128", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"},
			ComplexFilter: "[0:2]format=yuva420p[sub];[0:0]hwdownload,format=nv12[main];[main][sub]overlay=eof_action=pass:repeatlast=0,format=nv12,hwupload[vout]",
			MapLabel:      "[vout]",
			CodecArgs:     []string{"-c:v", "h264_vaapi", "-rc_mode", "CQP", "-qp", "24"},
		},
		SegmentFormat: "fmp4", StartSeconds: 12,
	}
	s := argsString(hlsArgs(opts, spec, "/out"))

	for _, want := range []string{
		"-map [vout]",
		"-filter_complex [0:2]format=yuva420p[sub]",
		"-map 0:1", // 音频照旧按绝对序号 map
		"-c:v h264_vaapi",
		"-hide_banner",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少参数 %q：\n%s", want, s)
		}
	}
	if strings.Contains(s, "-map 0:0") {
		t.Errorf("烧录时不能再 map 原始视频流（会被送两遍导致协商失败）：\n%s", s)
	}
	if strings.Contains(s, "-c:v copy") {
		t.Errorf("烧录必须重新编码，不能 -c:v copy：\n%s", s)
	}
	// -filter_complex 是输出选项，必须在本轮输出文件之前。
	// 用 filepath.Join 拼期望值：测试在 Windows 上跑时路径分隔符不一样。
	out := filepath.Join("/out", "index.m3u8")
	if strings.Index(s, "-filter_complex") > strings.Index(s, out) {
		t.Errorf("-filter_complex 必须在输出文件之前：\n%s", s)
	}
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
