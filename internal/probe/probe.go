// Package probe 调用 ffprobe 读取媒体文件的容器与流信息，并归一化成 LMBY 的模型。
//
// 归一化（Normalize）是纯函数，可以用真实 ffprobe 输出做表驱动测试 ——
// 见 testdata/*.json（都是从真实媒体库里的文件导出的）。
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TicksPerSecond 是 .NET tick（1 tick = 100ns）。数据库里统一用 tick 存时长。
const TicksPerSecond = 10_000_000

// ErrUnsupported 表示「这个文件本身有问题」：不是媒体文件、损坏、容器不支持等。
// 这类失败重试没有意义，调用方应当直接标记失败并展示给用户。
var ErrUnsupported = errors.New("probe: 文件无法解析")

// Info 是归一化后的探测结果。
type Info struct {
	Container      string           `json:"container"`  // 主容器名，如 matroska
	FormatName     string           `json:"formatName"` // ffprobe 原始 format_name，如 matroska,webm
	FormatLongName string           `json:"formatLongName"`
	DurationTicks  int64            `json:"durationTicks"`
	DurationSec    float64          `json:"durationSeconds"`
	SizeBytes      int64            `json:"sizeBytes"`
	BitRate        int64            `json:"bitRate"`
	Video          []VideoStream    `json:"video"`
	Audio          []AudioStream    `json:"audio"`
	Subtitles      []SubtitleStream `json:"subtitles"`
	Chapters       []Chapter        `json:"chapters"`
	HDR            *HDRInfo         `json:"hdr,omitempty"`
}

// HDRInfo 描述高动态范围信息。
type HDRInfo struct {
	// Format 取 HDR10 / HLG / DolbyVision
	Format        string `json:"format"`
	Transfer      string `json:"transfer,omitempty"`
	Primaries     string `json:"primaries,omitempty"`
	ColorSpace    string `json:"colorSpace,omitempty"`
	DolbyProfile  int    `json:"dolbyProfile,omitempty"`
	DolbyBLCompat int    `json:"dolbyBlCompatibilityId,omitempty"`
	DolbyEL       bool   `json:"dolbyElPresent,omitempty"`
}

// VideoStream 是一路视频流。
type VideoStream struct {
	Index          int     `json:"index"`
	Codec          string  `json:"codec"`
	Profile        string  `json:"profile,omitempty"`
	Level          int     `json:"level,omitempty"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	PixelFormat    string  `json:"pixelFormat,omitempty"`
	BitDepth       int     `json:"bitDepth,omitempty"`
	FrameRate      float64 `json:"frameRate,omitempty"`
	BitRate        int64   `json:"bitRate,omitempty"`
	ColorSpace     string  `json:"colorSpace,omitempty"`
	ColorTransfer  string  `json:"colorTransfer,omitempty"`
	ColorPrimaries string  `json:"colorPrimaries,omitempty"`
	// FieldOrder 是隔行信息（progressive / tt / bb …）。空 = 未知或逐行。
	// 素材是隔行时转码要先去隔行，否则画面会有梳齿。
	FieldOrder string `json:"fieldOrder,omitempty"`
	Language       string  `json:"language,omitempty"`
	Title          string  `json:"title,omitempty"`
	Default        bool    `json:"default,omitempty"`
}

// AudioStream 是一路音频流。
type AudioStream struct {
	Index         int    `json:"index"`
	Codec         string `json:"codec"`
	Profile       string `json:"profile,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"channelLayout,omitempty"`
	SampleRate    int    `json:"sampleRate,omitempty"`
	BitRate       int64  `json:"bitRate,omitempty"`
	Language      string `json:"language,omitempty"`
	Title         string `json:"title,omitempty"`
	Default       bool   `json:"default,omitempty"`
	// AtmosHint 是启发式判断（ffprobe 不直接报 Atmos），仅用于展示。
	AtmosHint bool `json:"atmosHint,omitempty"`
}

// SubtitleStream 是一路字幕流。
type SubtitleStream struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	// IsImage 表示图形字幕（PGS/VobSub/DVB），这类字幕无法直接转成文本，
	// 播放时要么烧录进画面、要么让客户端自己渲染 —— 决策引擎需要知道。
	IsImage bool `json:"isImage"`
}

// Chapter 是一个章节。
type Chapter struct {
	StartTicks int64  `json:"startTicks"`
	EndTicks   int64  `json:"endTicks"`
	Title      string `json:"title,omitempty"`
	Language   string `json:"language,omitempty"`
}

// ---------------------------------------------------------------- ffprobe 原始结构

type rawOutput struct {
	Streams  []rawStream  `json:"streams"`
	Format   rawFormat    `json:"format"`
	Chapters []rawChapter `json:"chapters"`
}

type rawStream struct {
	Index            int               `json:"index"`
	CodecName        string            `json:"codec_name"`
	CodecLongName    string            `json:"codec_long_name"`
	CodecType        string            `json:"codec_type"`
	Profile          string            `json:"profile"`
	Level            int               `json:"level"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	PixFmt           string            `json:"pix_fmt"`
	RFrameRate       string            `json:"r_frame_rate"`
	AvgFrameRate     string            `json:"avg_frame_rate"`
	BitRate          string            `json:"bit_rate"`
	SampleRate       string            `json:"sample_rate"`
	Channels         int               `json:"channels"`
	ChannelLayout    string            `json:"channel_layout"`
	Duration         string            `json:"duration"`
	ColorRange       string            `json:"color_range"`
	ColorSpace       string            `json:"color_space"`
	ColorTransfer    string            `json:"color_transfer"`
	ColorPrimaries   string            `json:"color_primaries"`
	FieldOrder       string            `json:"field_order"`
	BitsPerRawSample string            `json:"bits_per_raw_sample"`
	Tags             map[string]string `json:"tags"`
	Disposition      map[string]int    `json:"disposition"`
	SideDataList     []rawSideData     `json:"side_data_list"`
}

type rawSideData struct {
	SideDataType     string `json:"side_data_type"`
	DVProfile        int    `json:"dv_profile"`
	DVLevel          int    `json:"dv_level"`
	RPUPresent       int    `json:"rpu_present_flag"`
	ELPresent        int    `json:"el_present_flag"`
	BLPresent        int    `json:"bl_present_flag"`
	BLSignalCompatID int    `json:"dv_bl_signal_compatibility_id"`
}

type rawFormat struct {
	FormatName     string            `json:"format_name"`
	FormatLongName string            `json:"format_long_name"`
	Duration       string            `json:"duration"`
	Size           string            `json:"size"`
	BitRate        string            `json:"bit_rate"`
	NbStreams      int               `json:"nb_streams"`
	Tags           map[string]string `json:"tags"`
}

type rawChapter struct {
	TimeBase  string            `json:"time_base"`
	Start     int64             `json:"start"`
	End       int64             `json:"end"`
	StartTime string            `json:"start_time"`
	EndTime   string            `json:"end_time"`
	Tags      map[string]string `json:"tags"`
}

// ---------------------------------------------------------------- 运行 ffprobe

// Run 对文件跑一次 ffprobe。
//
// 返回 ErrUnsupported 表示文件本身无法解析（不该重试）；
// 其它错误（超时、ffprobe 不存在等）属于环境问题，调用方应当重试。
func Run(ctx context.Context, probePath, path string) (*Info, error) {
	if probePath == "" {
		probePath = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, probePath,
		"-v", "error",
		// 限制「为了识别流而读多少数据」。
		//
		// 默认值在本地盘上无所谓，但这里后面是网盘/网络存储，
		// ffprobe 的随机寻道（MKV 的 cues、MP4 的 moov）非常慢；
		// 限制之后：一是快，二是避免某些容器（AVI 没有索引时）
		// 为了算时长而把整个文件读一遍。
		"-probesize", "10M",
		"-analyzeduration", "10M", // 单位微秒，即最多分析 10 秒
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		"-i", path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if isFileProblem(msg) {
			return nil, fmt.Errorf("%w: %s", ErrUnsupported, truncate(msg, 200))
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 文件明明存在却读不了（例如网络盘掉了），也归为环境问题
		return nil, fmt.Errorf("执行 ffprobe 失败: %w（%s）", runErr, truncate(msg, 200))
	}

	info, err := Normalize(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	return info, nil
}

// isFileProblem 判断 ffprobe 的报错是不是「文件本身有问题」。
func isFileProblem(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, marker := range []string{
		"moov atom not found",
		"invalid data found",
		"could not find codec parameters",
		"no such file or directory",
		"end of file",
		"error opening input",
		"unsupported codec",
		"is not a valid",
		"truncated",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 归一化

// Normalize 把 ffprobe 的 JSON 输出归一化成 Info。纯函数，便于测试。
func Normalize(data []byte) (*Info, error) {
	var raw rawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: 解析 ffprobe 输出失败: %w", ErrUnsupported, err)
	}
	if len(raw.Streams) == 0 {
		return nil, fmt.Errorf("%w: 文件里没有可识别的流", ErrUnsupported)
	}

	info := &Info{
		FormatName:     raw.Format.FormatName,
		FormatLongName: raw.Format.FormatLongName,
		Container:      primaryContainer(raw.Format.FormatName),
		SizeBytes:      parseInt64(raw.Format.Size),
		BitRate:        parseInt64(raw.Format.BitRate),
	}
	info.DurationSec = parseFloat(raw.Format.Duration)
	info.DurationTicks = int64(info.DurationSec*TicksPerSecond + 0.5)
	if info.DurationTicks == 0 {
		// 少数容器（例如裸流）只在流级别给时长
		for _, s := range raw.Streams {
			if d := parseFloat(s.Duration); d > info.DurationSec {
				info.DurationSec = d
			}
		}
		info.DurationTicks = int64(info.DurationSec*TicksPerSecond + 0.5)
	}

	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			// 封面图（部分 mp3/mkv 会把封面塞成一路视频流）不算正片视频。
			if s.Disposition["attached_pic"] == 1 {
				continue
			}
			info.Video = append(info.Video, normalizeVideo(s))
			if info.HDR == nil {
				info.HDR = detectHDR(s)
			}
		case "audio":
			info.Audio = append(info.Audio, normalizeAudio(s))
		case "subtitle":
			info.Subtitles = append(info.Subtitles, normalizeSubtitle(s))
		}
	}

	for _, c := range raw.Chapters {
		ch := Chapter{
			StartTicks: secondsToTicks(chapterSeconds(c.Start, c.StartTime, c.TimeBase)),
			EndTicks:   secondsToTicks(chapterSeconds(c.End, c.EndTime, c.TimeBase)),
			Title:      c.Tags["title"],
			Language:   c.Tags["language"],
		}
		info.Chapters = append(info.Chapters, ch)
	}

	if len(info.Video) == 0 && len(info.Audio) == 0 {
		return nil, fmt.Errorf("%w: 既没有视频流也没有音频流", ErrUnsupported)
	}
	return info, nil
}

func normalizeVideo(s rawStream) VideoStream {
	fps := parseRational(s.RFrameRate)
	if fps == 0 {
		fps = parseRational(s.AvgFrameRate)
	}
	bitDepth := 0
	if n, err := strconv.Atoi(s.BitsPerRawSample); err == nil {
		bitDepth = n
	}
	if bitDepth == 0 {
		bitDepth = bitDepthFromPixFmt(s.PixFmt)
	}
	return VideoStream{
		Index:          s.Index,
		Codec:          s.CodecName,
		Profile:        s.Profile,
		Level:          s.Level,
		Width:          s.Width,
		Height:         s.Height,
		PixelFormat:    s.PixFmt,
		BitDepth:       bitDepth,
		FrameRate:      fps,
		BitRate:        parseInt64(s.BitRate),
		ColorSpace:     s.ColorSpace,
		ColorTransfer:  s.ColorTransfer,
		ColorPrimaries: s.ColorPrimaries,
		FieldOrder:     s.FieldOrder,
		Language:       s.Tags["language"],
		Title:          s.Tags["title"],
		Default:        s.Disposition["default"] == 1,
	}
}

func normalizeAudio(s rawStream) AudioStream {
	profile := s.Profile
	title := s.Tags["title"]
	long := s.CodecLongName
	atmos := strings.Contains(strings.ToLower(profile), "atmos") ||
		strings.Contains(strings.ToLower(title), "atmos") ||
		strings.Contains(strings.ToLower(long), "atmos")
	return AudioStream{
		Index:         s.Index,
		Codec:         s.CodecName,
		Profile:       profile,
		Channels:      s.Channels,
		ChannelLayout: s.ChannelLayout,
		SampleRate:    int(parseInt64(s.SampleRate)),
		BitRate:       parseInt64(s.BitRate),
		Language:      s.Tags["language"],
		Title:         title,
		Default:       s.Disposition["default"] == 1,
		AtmosHint:     atmos,
	}
}

func normalizeSubtitle(s rawStream) SubtitleStream {
	return SubtitleStream{
		Index:    s.Index,
		Codec:    s.CodecName,
		Language: s.Tags["language"],
		Title:    s.Tags["title"],
		Default:  s.Disposition["default"] == 1,
		Forced:   s.Disposition["forced"] == 1,
		IsImage:  isImageSubtitle(s.CodecName),
	}
}

// isImageSubtitle 判断是不是图形字幕（不能直接转文本）。
func isImageSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub", "pgssub":
		return true
	}
	return false
}

// detectHDR 从颜色信息与 side data 判断 HDR 类型。
func detectHDR(s rawStream) *HDRInfo {
	h := HDRInfo{
		Transfer:   s.ColorTransfer,
		Primaries:  s.ColorPrimaries,
		ColorSpace: s.ColorSpace,
	}

	for _, sd := range s.SideDataList {
		if !strings.Contains(strings.ToLower(sd.SideDataType), "dovi") &&
			!strings.Contains(strings.ToLower(sd.SideDataType), "dolby vision") {
			continue
		}
		h.Format = "DolbyVision"
		h.DolbyProfile = sd.DVProfile
		h.DolbyBLCompat = sd.BLSignalCompatID
		h.DolbyEL = sd.ELPresent == 1
		return &h
	}

	switch strings.ToLower(s.ColorTransfer) {
	case "smpte2084", "bt2020-10", "bt2020-12":
		h.Format = "HDR10"
	case "arib-std-b67":
		h.Format = "HLG"
	default:
		return nil // SDR 不记录，省得每个文件都存一份无意义信息
	}
	return &h
}

// ---------------------------------------------------------------- 小工具

// primaryContainer 从 "matroska,webm" 取主容器名。
func primaryContainer(formatName string) string {
	if i := strings.IndexByte(formatName, ','); i >= 0 {
		return strings.TrimSpace(formatName[:i])
	}
	return strings.TrimSpace(formatName)
}

// bitDepthFromPixFmt 从 yuv420p10le 这类像素格式里取位深。
//
// 规则：结尾的 p/le/be 之前若有数字，就是位深；没有则默认 8（yuv420p）。
func bitDepthFromPixFmt(pixFmt string) int {
	s := strings.ToLower(pixFmt)
	if s == "" {
		return 0
	}
	s = strings.TrimSuffix(s, "le")
	s = strings.TrimSuffix(s, "be")
	// 从尾部往前找连续数字
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == len(s) {
		return 8 // 没有数字后缀：8bit
	}
	n, err := strconv.Atoi(s[i:])
	if err != nil {
		return 0
	}
	return n
}

// parseRational 解析 "24000/1001" 这类有理数。
func parseRational(s string) float64 {
	if s == "" || s == "0/0" {
		return 0
	}
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		return parseFloat(s)
	}
	n := parseFloat(num)
	d := parseFloat(den)
	if d == 0 {
		return 0
	}
	return n / d
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func parseInt64(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func secondsToTicks(sec float64) int64 { return int64(sec*TicksPerSecond + 0.5) }

// chapterSeconds 求章节时间点的秒数。
//
// 坑：ffprobe 的 chapters[].start 是「以 time_base 为单位的值」，
// 直接当秒用会得到天文数字（mkv 的 time_base 是 1/1000000000）。
// 优先用 ffprobe 已算好的 start_time（秒），没有时再用 start × time_base 换算。
func chapterSeconds(units int64, timeString, timeBase string) float64 {
	if timeString != "" {
		if v := parseFloat(timeString); v > 0 || units == 0 {
			return v
		}
	}
	unit := parseRational(timeBase)
	if unit == 0 {
		return 0
	}
	return float64(units) * unit
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
