package probe

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这些夹具不是手写的：它们是对真实媒体库里的文件跑
//
//	ffprobe -v error -print_format json -show_format -show_streams -show_chapters -i <文件>
//
// 得到的原始输出（见 docs/LIBRARY-NOTES.md 里描述的那套测试片）。
// 期望值则是我逐条核对过的「归一化后应该是什么」，不是从实现里回抄的。

func loadFixture(t *testing.T, name string) *Info {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取夹具 %s 失败: %v", name, err)
	}
	info, err := Normalize(data)
	if err != nil {
		t.Fatalf("归一化 %s 失败: %v", name, err)
	}
	return info
}

func eqString(t *testing.T, what, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: 期望 %q，实际 %q", what, want, got)
	}
}

func eqInt(t *testing.T, what string, want, got int) {
	t.Helper()
	if want != got {
		t.Errorf("%s: 期望 %d，实际 %d", what, want, got)
	}
}

func eqFloat(t *testing.T, what string, want, got float64) {
	t.Helper()
	if math.Abs(want-got) > 0.01 {
		t.Errorf("%s: 期望 %.3f，实际 %.3f", what, want, got)
	}
}

func countImageSubs(info *Info) int {
	n := 0
	for _, s := range info.Subtitles {
		if s.IsImage {
			n++
		}
	}
	return n
}

// TestNormalizeHEVC10bitWithChaptersAndDualASS 是「典型番剧文件」：
// HEVC Main10、两条 ASS 字幕、带章节。
func TestNormalizeHEVC10bitWithChaptersAndDualASS(t *testing.T) {
	info := loadFixture(t, "ass-subtitle.json")

	eqString(t, "容器", "matroska", info.Container)
	eqString(t, "格式名（原始）", "matroska,webm", info.FormatName)
	if len(info.Video) != 1 {
		t.Fatalf("视频流数：期望 1，实际 %d", len(info.Video))
	}
	v := info.Video[0]
	eqString(t, "视频编码", "hevc", v.Codec)
	eqString(t, "profile", "Main 10", v.Profile)
	eqInt(t, "宽", 1920, v.Width)
	eqInt(t, "高", 1080, v.Height)
	// pix_fmt=yuv420p10le 且 bits_per_raw_sample 为空 —— 位深必须从像素格式推出来
	eqInt(t, "位深", 10, v.BitDepth)
	eqFloat(t, "帧率（24000/1001）", 23.976, v.FrameRate)

	if len(info.Audio) != 1 {
		t.Fatalf("音频流数：期望 1，实际 %d", len(info.Audio))
	}
	eqString(t, "音频编码", "aac", info.Audio[0].Codec)
	eqInt(t, "声道数", 2, info.Audio[0].Channels)

	eqInt(t, "字幕流数", 2, len(info.Subtitles))
	eqInt(t, "图形字幕数", 0, countImageSubs(info))
	eqInt(t, "章节数", 5, len(info.Chapters))
	// 坑：ffprobe 的 chapters[].start 的单位是 time_base（mkv 下是纳秒），
	// 直接当秒会得到 9.4e16 这种天文数字。这个文件的章节从 94.011 秒开始。
	eqFloat(t, "首章起点（秒）", 94.011, float64(info.Chapters[0].StartTicks)/TicksPerSecond)
	eqString(t, "首章标题", "Chapter 01", info.Chapters[0].Title)
	if info.Chapters[4].EndTicks <= info.Chapters[4].StartTicks {
		t.Error("末章结束时间应大于开始时间")
	}
	if info.HDR != nil {
		t.Errorf("这个文件是 SDR，不应识别出 HDR：%+v", info.HDR)
	}
	if info.DurationSec < 1400 || info.DurationSec > 1440 {
		t.Errorf("时长异常: %.1f 秒", info.DurationSec)
	}
	if info.DurationTicks != int64(math.Round(info.DurationSec*TicksPerSecond)) {
		t.Errorf("时长 tick 换算不一致: %d", info.DurationTicks)
	}
}

// TestNormalizeAV1NoAudio 覆盖「只有视频流、没有音轨」的情况。
func TestNormalizeAV1NoAudio(t *testing.T) {
	info := loadFixture(t, "av1-4k-10bit.json")

	eqString(t, "容器", "matroska", info.Container)
	eqString(t, "视频编码", "av1", info.Video[0].Codec)
	eqInt(t, "宽", 3840, info.Video[0].Width)
	eqInt(t, "高", 2160, info.Video[0].Height)
	eqInt(t, "位深", 10, info.Video[0].BitDepth)
	eqInt(t, "音频流数", 0, len(info.Audio))
	eqInt(t, "字幕流数", 0, len(info.Subtitles))
}

// TestNormalizeH264YUV444P10WithPCM 覆盖 4:4:4 10bit 与 PCM 音轨，
// 并确认位深优先取 bits_per_raw_sample。
func TestNormalizeH264YUV444P10WithPCM(t *testing.T) {
	info := loadFixture(t, "avc-1080-444p10.json")

	v := info.Video[0]
	eqString(t, "视频编码", "h264", v.Codec)
	eqString(t, "总像素格式", "yuv444p10le", v.PixelFormat)
	eqInt(t, "位深", 10, v.BitDepth)
	eqString(t, "profile", "High 4:4:4 Predictive", v.Profile)
	eqString(t, "色彩原色", "bt709", v.ColorPrimaries)

	eqString(t, "音频编码", "pcm_s16le", info.Audio[0].Codec)
	// PCM 也报码率：48kHz × 2ch × 16bit = 1536000，顺便验证解析无误
	eqInt(t, "音频码率", 1536000, int(info.Audio[0].BitRate))
	eqInt(t, "音频采样率", 48000, info.Audio[0].SampleRate)
}

// TestNormalizeOldAVI 覆盖老容器 + 分数帧率 2997/100。
func TestNormalizeOldAVI(t *testing.T) {
	info := loadFixture(t, "avi-mpeg4.json")

	eqString(t, "容器", "avi", info.Container)
	eqString(t, "视频编码", "mpeg4", info.Video[0].Codec)
	eqInt(t, "宽", 1280, info.Video[0].Width)
	eqFloat(t, "帧率（2997/100）", 29.97, info.Video[0].FrameRate)
	eqString(t, "音频编码", "mp3", info.Audio[0].Codec)
	eqInt(t, "位深（yuv420p 默认 8）", 8, info.Video[0].BitDepth)
}

// TestNormalizeDolbyVision 覆盖杜比视界：靠 side_data 而不是 color_transfer 识别，
// 并且高度是非标准的 1634（上下黑边裁切过的片源）。
func TestNormalizeDolbyVision(t *testing.T) {
	info := loadFixture(t, "dv-4k-hevc.json")

	eqString(t, "容器（mp4 家族取第一个）", "mov", info.Container)
	eqString(t, "视频编码", "hevc", info.Video[0].Codec)
	eqInt(t, "宽", 3840, info.Video[0].Width)
	eqInt(t, "高（非标准高度）", 1634, info.Video[0].Height)
	eqInt(t, "位深", 10, info.Video[0].BitDepth)
	eqInt(t, "音频流数（双 AAC）", 2, len(info.Audio))

	if info.HDR == nil {
		t.Fatal("应识别出杜比视界")
	}
	eqString(t, "HDR 类型", "DolbyVision", info.HDR.Format)
	if info.HDR.DolbyProfile == 0 {
		t.Error("杜比视界 profile 应被解析出来")
	}
}

// TestNormalizeHDR10 覆盖 HDR10：color_transfer=smpte2084 + bt2020。
func TestNormalizeHDR10(t *testing.T) {
	info := loadFixture(t, "hevc-4k-hdr.json")

	if info.HDR == nil {
		t.Fatal("应识别出 HDR")
	}
	eqString(t, "HDR 类型", "HDR10", info.HDR.Format)
	eqString(t, "传输特性", "smpte2084", info.HDR.Transfer)
	eqString(t, "原色", "bt2020", info.HDR.Primaries)
	// 19001/317 ≈ 59.94
	eqFloat(t, "帧率", 59.94, info.Video[0].FrameRate)
}

// TestNormalizeTrueHDWithForcedASS 覆盖 Dolby TrueHD 与 forced 标记。
func TestNormalizeTrueHDWithForcedASS(t *testing.T) {
	info := loadFixture(t, "multi-sub-audio.json")

	eqString(t, "音频编码", "truehd", info.Audio[0].Codec)
	eqInt(t, "章节数", 6, len(info.Chapters))
	if len(info.Subtitles) != 1 {
		t.Fatalf("字幕流数：期望 1，实际 %d", len(info.Subtitles))
	}
	s := info.Subtitles[0]
	eqString(t, "字幕编码", "ass", s.Codec)
	eqString(t, "字幕语言", "chi", s.Language)
	if !s.Forced {
		t.Error("这条字幕应带 forced 标记")
	}
}

// TestNormalizePGSImageSubtitles 覆盖图形字幕：PGS 无法直接转文本，
// 播放决策必须知道这一点，所以 IsImage 必须正确。
func TestNormalizePGSImageSubtitles(t *testing.T) {
	info := loadFixture(t, "pgs-subtitle.json")

	eqString(t, "视频编码", "h264", info.Video[0].Codec)
	eqString(t, "profile", "Constrained Baseline", info.Video[0].Profile)
	eqString(t, "音频编码", "ac3", info.Audio[0].Codec)
	eqInt(t, "声道数", 6, info.Audio[0].Channels)

	eqInt(t, "字幕流数（中英各一条）", 2, len(info.Subtitles))
	eqInt(t, "图形字幕数", 2, countImageSubs(info))
	eqString(t, "字幕语言（第一条）", "chi", info.Subtitles[0].Language)
	// 时长 11822 秒 ≈ 3.28 小时，顺便验证大时长没有溢出
	if info.DurationSec < 11800 {
		t.Errorf("时长异常: %.1f", info.DurationSec)
	}
}

// TestNormalizeFileNameCanLie 覆盖一个真实的反例：
// 文件名叫「HEVC 1080P 杜比视界」，但里面实际是 H.264 8bit、既没有杜比视界也没有 HDR。
// 这提醒我们：一切以探测结果为准，不能相信文件名。
func TestNormalizeFileNameCanLie(t *testing.T) {
	info := loadFixture(t, "dv-1080-hevc.json")

	eqString(t, "容器", "mov", info.Container)
	eqString(t, "实际视频编码（文件名说 HEVC）", "h264", info.Video[0].Codec)
	eqInt(t, "位深", 8, info.Video[0].BitDepth)
	if info.HDR != nil {
		t.Errorf("文件名叫杜比视界，但实际是 SDR：%+v", info.HDR)
	}
}

// TestNormalizeImageIsNotMedia 用真实的 jpg（被误当成视频文件喂进来）验证：
// 归一化不会崩，并且暴露出「这不是正经媒体」的特征（无音轨、时长为 0）。
func TestNormalizeImageIsNotMedia(t *testing.T) {
	info := loadFixture(t, "image-jpeg.json")

	eqString(t, "容器", "image2", info.Container)
	eqString(t, "视频编码", "mjpeg", info.Video[0].Codec)
	eqInt(t, "音频流数", 0, len(info.Audio))
	// image2 容器会给出一个极小的名义时长（0.04s），实用上就是「没有时长」
	if info.DurationSec >= 1 {
		t.Errorf("图片不应有可用时长，实际 %.3f", info.DurationSec)
	}
}

// TestNormalizeRejectsEmptyStreams 确认异常输入会被判为「文件无法解析」，
// 这样调用方会标记失败而不是无限重试。
func TestNormalizeRejectsEmptyStreams(t *testing.T) {
	_, err := Normalize([]byte(`{"streams":[],"format":{"format_name":"unknown"}}`))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("期望 ErrUnsupported，实际 %v", err)
	}

	_, err = Normalize([]byte(`不是 JSON`))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("期望 ErrUnsupported，实际 %v", err)
	}
}

// TestPrimaryContainer 覆盖 ffprobe 用逗号分隔多个容器名的情况。
func TestPrimaryContainer(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"matroska,webm", "matroska"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "mov"},
		{"avi", "avi"},
		{"", ""},
	} {
		eqString(t, "primaryContainer("+c.in+")", c.want, primaryContainer(c.in))
	}
}

// TestBitDepthFromPixFmt 覆盖像素格式到位深的推导规则。
func TestBitDepthFromPixFmt(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"yuv420p", 8},
		{"yuv420p10le", 10},
		{"yuv420p12be", 12},
		{"yuv444p10le", 10},
		{"yuvj444p", 8},
		// 灰度默认 8bit
		{"gray", 8},
		// 空字符串无法判断
		{"", 0},
	} {
		eqInt(t, "bitDepthFromPixFmt("+c.in+")", c.want, bitDepthFromPixFmt(c.in))
	}
}

// TestIsFileProblem 确认「文件本身有问题」与「环境问题」被正确区分：
// 前者不该重试，后者必须重试。
func TestIsFileProblem(t *testing.T) {
	fileProblems := []string{
		"moov atom not found",
		"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x55] moov atom not found\nInvalid data found when processing input",
		"End of file",
		"Error opening input file xxx.mkv.",
	}
	for _, s := range fileProblems {
		if !isFileProblem(s) {
			t.Errorf("应判定为文件问题: %q", s)
		}
	}

	envProblems := []string{
		"Connection timed out",
		"ffprobe: command not found",
		"Permission denied",
		"",
	}
	for _, s := range envProblems {
		if isFileProblem(s) {
			t.Errorf("不应判定为文件问题（应重试）: %q", s)
		}
	}
}

// 确保夹具文件名清单与测试覆盖一致，避免以后替换夹具时漏改测试。
func TestFixturesAreReferenced(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("probe_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if !strings.Contains(string(src), name) {
			t.Errorf("夹具 %s 没有被任何用例引用", name)
		}
	}
}
