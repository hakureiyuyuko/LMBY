package playback

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/probe"
)

// TicksPerSecond 是 .NET tick（1 tick = 100ns）—— 与数据库里的时长同单位。
const TicksPerSecond = 10_000_000

// File 是一个可播放的文件版本（由 store 的文件行 + ffprobe 结果组装）。
type File struct {
	ID            int64
	Path          string
	SizeBytes     int64
	Container     string // ffprobe 的 format_name 原样
	DurationTicks int64
	Video         []probe.VideoStream
	Audio         []probe.AudioStream
	Subtitle      []probe.SubtitleStream
}

// Kind 返回归一化后的容器族。
func (f File) Kind() string { return ContainerKind(f.Container, f.Path) }

// Ext 返回小写扩展名（含点）。
func (f File) Ext() string { return strings.ToLower(filepath.Ext(f.Path)) }

// Request 是一次播放决策的输入。
type Request struct {
	Profile Profile
	// Files 是条目下的候选文件。顺序即优先级，决策取第一个「有视频流且能放」的。
	Files []File

	// VideoIndex / AudioIndex 是用户显式指定的流序号（ffprobe 的 index），0 表示自动。
	VideoIndex int
	AudioIndex int
	// SubtitleIndex：-1 表示「明确不要字幕」，0 表示自动（有强制字幕轨就选，否则不选）。
	SubtitleIndex int

	// StartTicks 是起播位置（续播用）。
	StartTicks int64
}

// StreamPlan 是一条流的处理决定。
type StreamPlan struct {
	Action string `json:"action"`
	Index  int    `json:"index"`
	Codec  string `json:"codec,omitempty"`
	// Reason 是给用户看的「为什么这么处理」（界面要能解释，不能只有结果）。
	Reason string `json:"reason"`

	// 音频专用
	Channels int    `json:"channels,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	// Downmix 表示需要降到立体声。
	Downmix bool `json:"downmix,omitempty"`

	// 字幕专用
	Image  bool `json:"image,omitempty"`
	Forced bool `json:"forced,omitempty"`
}

// Plan 是决策结果。
type Plan struct {
	Mode          string `json:"mode"`
	FileID        int64  `json:"fileId,omitempty"`
	ContainerKind string `json:"containerKind,omitempty"`
	Container     string `json:"container,omitempty"`
	DurationTicks int64  `json:"durationTicks"`
	StartTicks    int64  `json:"startTicks"`
	// SegmentFormat 是转封装输出的分片格式：fmp4 | ts。
	SegmentFormat string `json:"segmentFormat,omitempty"`

	Video    StreamPlan `json:"video"`
	Audio    StreamPlan `json:"audio"`
	Subtitle StreamPlan `json:"subtitle"`

	// Playable 为 false 表示当前里程碑还放不了（需要视频转码 = M4），
	// 界面应当把 Reasons 原样展示出来，不要只说「播放失败」。
	Playable bool `json:"playable"`
	// Reasons 是决策理由链，从「选了这个文件」到每条流的动作。
	Reasons []string `json:"reasons"`
}

// fmp4Carryable 是 HLS fMP4 分片能原样承载的视频编码。
//
// hevc 需要打 hvc1 标签（见 internal/stream），否则 Safari 不认。
var fmp4Carryable = map[string]bool{"h264": true, "hevc": true, "vp9": true, "av1": true}

// tsCarryable 是 MPEG-TS 分片能原样承载的视频编码（没有 vp9/av1）。
var tsCarryable = map[string]bool{"h264": true, "hevc": true, "mpeg2video": true}

// textSubtitleCodecs 是「文本类字幕」：能转成 WebVTT 交给浏览器渲染。
var textSubtitleCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true,
	"webvtt": true, "text": true, "subviewer": true, "microdvd": true,
	"jacosub": true, "sami": true, "realtext": true, "vplayer": true,
	"mpl2": true, "pjs": true, "stl": true, "eia_608": true, "ttml": true,
}

// Decide 决定「这个条目该怎么播」。
//
// 返回的 Plan 永远带上理由链：用户看到的应该是
// 「音频是 DTS，浏览器放不了，转成 AAC 立体声」，而不是一个黑屏。
//
// 一个条目可能有多个版本（同一部片子同时有 4K HEVC 与 1080p H264），
// 因此这里**逐个版本试算**，按「能直出 > 能转封装 > 要转码」挑结果最好的那个 ——
// 「挑第一个容器能放的文件」是不够的：4K 10bit HEVC 的 mp4 容器也能直放吗？
// 不能，它需要转码；而旁边那个 1080p H264 的 mp4 是能直出的（实测踩到）。
func Decide(req Request) Plan {
	p := req.Profile.Normalize()

	var best Plan
	bestRank := -1
	candidates := 0
	for i := range req.Files {
		f := &req.Files[i]
		if len(f.Video) == 0 {
			continue
		}
		candidates++
		plan := decideForFile(p, req, f)
		if rank := modeRank(plan.Mode); rank > bestRank {
			best, bestRank = plan, rank
		}
		// 同分时保留先出现的：文件已按体积倒序，也就是质量高的那个
	}

	if candidates == 0 {
		video := StreamPlan{Action: ActionNone, Reason: "这个条目没有可播放的视频文件（可能还没探测出流信息）"}
		return Plan{
			StartTicks: req.StartTicks,
			Video:      video,
			Audio:      StreamPlan{Action: ActionNone},
			Subtitle:   StreamPlan{Action: ActionNone},
			Reasons:    []string{"条目下没有找到带视频流的文件", video.Reason},
			Playable:   false,
		}
	}

	// 把「选片」这条理由插在最前面：后面所有理由都是在解释它。
	pick := pickReason(req.Files, best, candidates)
	best.Reasons = append([]string{pick}, best.Reasons...)
	return best
}

// decideForFile 对单个文件版本做一次完整决策。
func decideForFile(p Profile, req Request, file *File) Plan {
	plan := Plan{
		StartTicks:    req.StartTicks,
		FileID:        file.ID,
		Container:     file.Container,
		ContainerKind: file.Kind(),
		DurationTicks: file.DurationTicks,
	}
	vs, vReason := pickVideo(file, req.VideoIndex)
	plan.Video = planVideo(p, file, vs, vReason)
	plan.Audio = planAudio(p, file, req.AudioIndex, plan.Video.Action == ActionCopy)
	plan.Subtitle = planSubtitle(file, req.SubtitleIndex)
	plan.Mode, plan.SegmentFormat, plan.Playable = planMode(p, file, plan)

	for _, r := range []string{plan.Video.Reason, plan.Audio.Reason, plan.Subtitle.Reason} {
		if r != "" {
			plan.Reasons = append(plan.Reasons, r)
		}
	}
	plan.Reasons = append(plan.Reasons, modeReason(plan))
	return plan
}

// modeRank 给播放方式打分，用于在多个版本间挑最好的。
func modeRank(mode string) int {
	switch mode {
	case ModeDirect:
		return 3
	case ModeRemux:
		return 2
	case ModeTranscode:
		return 1
	}
	return 0
}

// pickReason 生成「为什么选这个版本」的说明。
func pickReason(files []File, best Plan, candidates int) string {
	for i := range files {
		if files[i].ID != best.FileID {
			continue
		}
		ext := files[i].Ext()
		if ext == "" {
			ext = files[i].Kind()
		}
		if candidates > 1 {
			return fmt.Sprintf("条目下有 %d 个可播版本，按「能直出 > 转封装 > 转码」选了 %s", candidates, ext)
		}
		return fmt.Sprintf("选了 %s 版本", ext)
	}
	return ""
}

// pickVideo 选视频流。
func pickVideo(file *File, want int) (probe.VideoStream, string) {
	if want > 0 {
		for _, v := range file.Video {
			if v.Index == want {
				return v, fmt.Sprintf("使用指定的视频流 #%d（%s）", v.Index, v.Codec)
			}
		}
		return probe.VideoStream{}, fmt.Sprintf("指定的视频流 #%d 不存在，改用默认视频流", want)
	}
	for _, v := range file.Video {
		if v.Default {
			return v, ""
		}
	}
	return file.Video[0], ""
}

// planVideo 决定视频流怎么处理。
func planVideo(p Profile, file *File, vs probe.VideoStream, prefix string) StreamPlan {
	out := StreamPlan{Action: ActionCopy, Index: vs.Index, Codec: vs.Codec,
		Language: vs.Language, Title: vs.Title, Default: vs.Default}

	res := fmt.Sprintf("%d×%d %s", vs.Width, vs.Height, vs.Codec)
	if vs.BitDepth > 0 {
		res = fmt.Sprintf("%d×%d %dbit %s", vs.Width, vs.Height, vs.BitDepth, vs.Codec)
	}

	if !p.SupportsVideo(vs.Codec, vs.Width, vs.Height, vs.BitDepth) {
		out.Action = ActionTranscode
		out.Reason = join(prefix, fmt.Sprintf("视频 %s 浏览器不能解码，需要转码（M4）", res))
		return out
	}

	reason := fmt.Sprintf("视频 %s 原样复制", res)
	// 容器不能直出时，要确认目标分片格式能承载这个编码（vp9 进不了 TS）。
	if !p.SupportsContainer(file.Kind()) {
		seg := segmentFormat(p, vs.Codec)
		if seg == "" {
			out.Action = ActionTranscode
			out.Reason = join(prefix, fmt.Sprintf("视频 %s 无法放进 %s 分片，需要转码（M4）", vs.Codec, file.Kind()))
			return out
		}
	}
	out.Reason = join(prefix, reason)
	return out
}

// planAudio 决定音频流怎么处理。videoCopied 为真时才是「直出 / 转封装」的场景。
func planAudio(p Profile, file *File, want int, videoCopied bool) StreamPlan {
	out := StreamPlan{Action: ActionNone, Index: -1}
	as, ok := pickAudio(file, want)
	if !ok {
		out.Reason = "没有音轨（将静音播放）"
		return out
	}
	out.Index = as.Index
	out.Codec = as.Codec
	out.Channels = as.Channels
	out.Language = as.Language
	out.Title = as.Title
	out.Default = as.Default

	name := fmt.Sprintf("%s %d 声道", as.Codec, as.Channels)
	if as.Channels <= 1 {
		name = as.Codec
	}

	switch {
	case !videoCopied:
		// 视频要转码，音频顺带一起转（M4）
		out.Action = ActionConvert
		out.Reason = fmt.Sprintf("音频 %s 交给转码流程（M4）", name)
	case p.SupportsAudio(as.Codec, as.Channels):
		out.Action = ActionCopy
		out.Reason = fmt.Sprintf("音频 %s 原样复制", name)
	default:
		out.Action = ActionConvert
		out.Downmix = as.Channels > p.MaxAudioChannels
		switch {
		case as.Channels > p.MaxAudioChannels:
			out.Reason = fmt.Sprintf("音频 %s 超过 %d 声道，转成 AAC 立体声", name, p.MaxAudioChannels)
		default:
			out.Reason = fmt.Sprintf("音频 %s 浏览器放不了，转成 AAC", name)
		}
	}
	return out
}

// pickAudio 选音轨：显式指定 > 标记 default > 第一条。
func pickAudio(file *File, want int) (probe.AudioStream, bool) {
	if len(file.Audio) == 0 {
		return probe.AudioStream{}, false
	}
	if want > 0 {
		for _, a := range file.Audio {
			if a.Index == want {
				return a, true
			}
		}
	}
	for _, a := range file.Audio {
		if a.Default {
			return a, true
		}
	}
	return file.Audio[0], true
}

// planSubtitle 决定字幕怎么处理。
//
// 默认**不烧录也不自动开字幕**：浏览器原生只有 WebVTT 一条路，
// 而自动开一条中英双语字幕（用户没要）比不开更烦人。
//
// 这里不接 Profile：字幕走的是独立 WebVTT 旁路，与客户端解码能力无关
// （客户端最后也只用得着 WebVTT 一种形式）。
func planSubtitle(file *File, want int) StreamPlan {
	out := StreamPlan{Action: ActionNone, Index: -1}
	if want < 0 {
		out.Reason = "按用户设置关闭字幕"
		return out
	}

	ss, ok := pickSubtitle(file, want)
	if !ok {
		if want > 0 {
			out.Reason = fmt.Sprintf("指定的字幕轨 #%d 不存在", want)
		}
		return out
	}
	out.Index = ss.Index
	out.Codec = ss.Codec
	out.Language = ss.Language
	out.Title = ss.Title
	out.Default = ss.Default
	out.Forced = ss.Forced

	if ss.IsImage || !textSubtitleCodecs[strings.ToLower(ss.Codec)] {
		// 图形字幕只能烧进画面，而烧录要重编码视频 —— M4 的事。
		// 这里明确给出「这次不显示」，而不是悄悄丢掉。
		out.Action = ActionDrop
		out.Image = ss.IsImage
		out.Reason = fmt.Sprintf("字幕 #%d（%s）是图形字幕，需要烧录进画面（M4），本次不显示", ss.Index, ss.Codec)
		return out
	}

	out.Action = ActionConvert
	out.Reason = fmt.Sprintf("字幕 #%d（%s）转成 WebVTT 由浏览器渲染", ss.Index, ss.Codec)
	return out
}

// pickSubtitle 选字幕轨：显式指定 > 默认且强制 > 无。
func pickSubtitle(file *File, want int) (probe.SubtitleStream, bool) {
	if want > 0 {
		for _, s := range file.Subtitle {
			if s.Index == want {
				return s, true
			}
		}
		return probe.SubtitleStream{}, false
	}
	for _, s := range file.Subtitle {
		if s.Default && s.Forced {
			return s, true
		}
	}
	return probe.SubtitleStream{}, false
}

// planMode 汇总出播放方式与分片格式。
//
// 返回的三个值：播放方式、分片格式（仅转封装时有意义）、能不能放。
// 参数名刻意不叫 segmentFormat —— 那样会遮蔽同名的辅助函数。
func planMode(p Profile, file *File, plan Plan) (mode, segFormat string, playable bool) {
	if plan.Video.Action == ActionTranscode {
		return ModeTranscode, "", false
	}
	// 视频复制的前提下，只要有一处不能原样送，就得走转封装（HLS）。
	//
	// 字幕转 VTT 不算在内：它是**另一个 HTTP 响应**（<track src=...>），
	// 从原文件里单独抽出来就行，不需要把视频也塞进 HLS。
	needsRemux := !p.SupportsContainer(file.Kind()) || plan.Audio.Action == ActionConvert
	if !needsRemux {
		return ModeDirect, "", true
	}
	seg := segmentFormat(p, plan.Video.Codec)
	if seg == "" {
		return ModeTranscode, "", false
	}
	return ModeRemux, seg, true
}

// segmentFormat 选择 HLS 分片格式：优先 fMP4（更小的起播延迟、支持更多编码），
// 只在客户端明确不支持 fMP4 时退回 TS。
func segmentFormat(p Profile, videoCodec string) string {
	if p.SupportsHLS && p.SupportsFMP4 && fmp4Carryable[strings.ToLower(videoCodec)] {
		return "fmp4"
	}
	if p.SupportsHLS && p.SupportsTS && tsCarryable[strings.ToLower(videoCodec)] {
		return "ts"
	}
	return ""
}

// modeReason 生成一句话总结播放方式。
func modeReason(plan Plan) string {
	switch plan.Mode {
	case ModeDirect:
		return "直接播放原文件（HTTP Range 分段传输）"
	case ModeRemux:
		return fmt.Sprintf("转封装成 %s 分片（视频不重新编码，几乎不占 CPU）", plan.SegmentFormat)
	default:
		return "需要视频转码，当前版本（M3）还不支持"
	}
}

// join 拼接可选前缀与理由。
func join(prefix, reason string) string {
	if prefix == "" {
		return reason
	}
	if reason == "" {
		return prefix
	}
	return prefix + "；" + reason
}
