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

// Machine 是「这台机器能编什么」的最小视图。
//
// 刻意只有几个字段、且**不依赖 internal/encoder**：决策层是纯函数（能离线单测），
// 而「用哪个编码器、拼什么参数」是执行层的事（见 encoder.VideoArgs）。
// 零值 = 什么也编不了，此时需要转码的内容会被如实判成放不了。
type Machine struct {
	// EncodeCodecs 是**真跑验证过**能编的编码，按偏好排序（如 ["h264","hevc"]）。
	EncodeCodecs []string
	// DecodeHW 是能硬件解码的编码（只影响 CPU 占用，不影响能不能播）。
	DecodeHW []string
	// Hardware 表示首选后端是硬件（目前只用于措辞与默认质量档）。
	Hardware bool
	// Tonemap 表示能不能做 HDR→SDR 色调映射；不能的话 HDR 内容会发灰。
	Tonemap bool
	// Name 是后端名（写进理由链，方便用户知道是硬编还是软编）。
	Name string
}

// CanEncode 报告能否编指定编码。
func (m Machine) CanEncode(codec string) bool {
	for _, c := range m.EncodeCodecs {
		if c == codec {
			return true
		}
	}
	return false
}

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

	// Machine 描述这台机器能编什么。零值 = 不能转码，此时需要转码的内容会被判成放不了。
	Machine Machine

	// TranscodeMaxHeight 是转码输出的高度上限（像素），0 = 不限。
	//
	// 只约束**转码**：直出与转封装不重编码，源多大就送多大（4K 原样送比转成
	// 1080p 又快又清晰）。转码是「边编边播」，输出分辨率直接决定能不能实时，
	// 所以额外压一档 —— 默认值由配置给（见 [playback] transcode_max_height）。
	TranscodeMaxHeight int

	// MaxHeight 是**用户在播放器里选的画质档**（输出高度上限）。
	//
	// 三态：nil = 没选（按上面的 TranscodeMaxHeight 走）；
	// 0 = 明确要「原生分辨率」（不因转码上限而缩放）；> 0 = 上限。
	// 它优先于配置里的转码上限（但仍不能超过客户端能放的上限）。
	// 选了比源低的档就必须转码 —— 否则等于没选。
	MaxHeight *int
	// HighBitrate 为真 = 用户选了「Premium」档：同分辨率、更高码率。
	// 只在真的要转码时有意义（直出不会重编码，码率跟着源走）。
	HighBitrate bool
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
	// DeliverAs 是字幕交给前端的形态：webvtt 或 libass（见 DeliverWebVTT /
	// DeliverLibass）。空 = 不送字幕（没字幕 / 被用户关掉 / 图形字幕且不烧录）。
	DeliverAs string `json:"deliverAs,omitempty"`

	// 转码专用（Action == ActionTranscode 时有意义）：目标与处理方式。
	// 「用哪个编码器、拼什么参数」不在这里 —— 那是执行层照本机能力表的活。
	TargetCodec  string `json:"targetCodec,omitempty"`
	TargetWidth  int    `json:"targetWidth,omitempty"`
	TargetHeight int    `json:"targetHeight,omitempty"`
	Tonemap      bool   `json:"tonemap,omitempty"`       // HDR → SDR
	Deinterlace  bool   `json:"deinterlace,omitempty"`   // 去隔行
	TenBit       bool   `json:"tenBitToEight,omitempty"` // 10bit → 8bit
	Quality      string `json:"quality,omitempty"`       // high / medium / low
	Backend      string `json:"backend,omitempty"`       // 本机后端名（写给人看）

	// 源信息：执行层要用它们拼转码参数（滤镜链看分辨率与隔行，方式看位深与 HDR）。
	// 放在结果里而不是让执行层回头去查，是为了让「决策用的输入」与
	// 「执行用的输入」是同一份，不会两边对不上。
	SourceCodec      string `json:"sourceCodec,omitempty"`
	SourceWidth      int    `json:"sourceWidth,omitempty"`
	SourceHeight     int    `json:"sourceHeight,omitempty"`
	SourceBitDepth   int    `json:"sourceBitDepth,omitempty"`
	SourceHDR        bool   `json:"sourceHdr,omitempty"`
	SourceInterlaced bool   `json:"sourceInterlaced,omitempty"`
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
	plan.Video = planVideo(p, req, file, vs, vReason)
	plan.Audio = planAudio(p, file, req.AudioIndex, plan.Video)
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

// isHDR 判定源是不是 HDR。
//
// 判据用 color_transfer（PQ / HLG）而不是位深：10bit 也可能是 SDR（我们库里
// 大量 10bit HEVC 就是 SDR），而 HDR 必须做色调映射才能看。
func isHDR(vs probe.VideoStream) bool {
	switch strings.ToLower(vs.ColorTransfer) {
	case "smpte2084", "arib-std-b67":
		return true
	}
	return false
}

// isInterlaced 判定源是不是隔行。
//
// 空值当逐行：探测字段是后加的，老数据里没有它，不能因为“不知道”就去隔行
//（对着逐行素材做去隔行会真切掉一半垂直分辨率）。
func isInterlaced(vs probe.VideoStream) bool {
	switch strings.ToLower(vs.FieldOrder) {
	case "tt", "bb", "tb", "bt":
		return true
	}
	return false
}

// planVideo 决定视频流怎么处理。
//
// 三档结果：直接复制（能直出/能转封装）→ 转码（本机能编、目标明确）→ 放不了
//（本机没有可用的编码器，或者目标编码客户端也不支持）。
func planVideo(p Profile, req Request, file *File, vs probe.VideoStream, prefix string) StreamPlan {
	m := req.Machine
	out := StreamPlan{Action: ActionCopy, Index: vs.Index, Codec: vs.Codec,
		Language: vs.Language, Title: vs.Title, Default: vs.Default,
		SourceCodec: vs.Codec, SourceWidth: vs.Width, SourceHeight: vs.Height,
		SourceBitDepth: vs.BitDepth, SourceHDR: isHDR(vs), SourceInterlaced: isInterlaced(vs)}

	res := fmt.Sprintf("%d×%d %s", vs.Width, vs.Height, vs.Codec)
	if vs.BitDepth > 0 {
		res = fmt.Sprintf("%d×%d %dbit %s", vs.Width, vs.Height, vs.BitDepth, vs.Codec)
	}

	// 为什么需要转：先收集原因，再统一决定目标（这样理由链不会丢信息）
	var why []string
	if !p.SupportsVideoCodec(vs.Codec) {
		why = append(why, fmt.Sprintf("视频编码 %s 浏览器不能解", vs.Codec))
	}
	if vs.BitDepth > p.MaxBitDepth {
		why = append(why, fmt.Sprintf("%dbit 超出客户端上限 %dbit", vs.BitDepth, p.MaxBitDepth))
	}
	if vs.Width > p.MaxWidth || vs.Height > p.MaxHeight {
		why = append(why, fmt.Sprintf("分辨率 %d×%d 超出客户端上限 %d×%d", vs.Width, vs.Height, p.MaxWidth, p.MaxHeight))
	}
	if vs.Height > 0 && vs.Width > 0 && !p.SupportsContainer(file.Kind()) {
		if seg := segmentFormat(p, vs.Codec); seg == "" {
			why = append(why, fmt.Sprintf("视频 %s 无法放进可用的分片格式", vs.Codec))
		}
	}
	// 用户选的档位低于源：这是**用户要求**的转码，不是能力不够 —— 理由要说清楚，
	// 否则用户会以为是服务端不给他看原画质。
	userAskedLower := req.MaxHeight != nil && *req.MaxHeight > 0 && vs.Height > *req.MaxHeight
	if userAskedLower {
		why = append(why, fmt.Sprintf("你选了 %dp 输出（源是 %dp）", *req.MaxHeight, vs.Height))
	}

	if len(why) == 0 {
		out.Reason = join(prefix, fmt.Sprintf("视频 %s 原样复制", res))
		return out
	}

	// 需要转码：目标编码只能从「本机真跑能编」∩「客户端能播」里选。
	// h264 优先（兼容性最好），其次 hevc（省带宽，但编起来慢）。
	target := ""
	if m.CanEncode("h264") {
		target = "h264"
	} else if m.CanEncode("hevc") && p.SupportsVideoCodec("hevc") {
		target = "hevc"
	}
	if target == "" {
		// 只是用户想降画质、而这台机器编不了：别把本来能看的片子变成「放不了」——
		// 原样送出去，并在理由里说清。此时 out.Action 还是 Copy（上面没改过）。
		if userAskedLower && len(why) == 1 {
			out.Reason = join(prefix, fmt.Sprintf("视频 %s 原样复制（这台机器不能转码，没法按所选画质输出）", res))
			return out
		}
		out.Action = ActionTranscode
		out.Reason = join(prefix, fmt.Sprintf("视频 %s 需要转码（%s），但这台机器没有可用的编码器",
			res, strings.Join(why, "；")))
		out.TargetCodec = ""
		return out
	}

	out.Action = ActionTranscode
	out.TargetCodec = target
	out.Quality = qualityFor(vs)
	if req.HighBitrate {
		// 「Premium」= 同分辨率、更高码率
		out.Quality = "high"
	}
	out.Backend = m.Name
	// 目标分辨率取「客户端上限」「用户选的档位」「配置里的转码上限」里最严的那个，
	// 保持宽高比由滤镜负责。
	//
	// 用户选「原生」时（MaxHeight = 0）不加转码上限：那是他明确要原分辨率，
	// 代价（可能跑不到实时）由界面上的理由链告知。
	maxW, maxH := p.MaxWidth, p.MaxHeight
	switch {
	case req.MaxHeight != nil && *req.MaxHeight > 0:
		if maxH <= 0 || *req.MaxHeight < maxH {
			maxH = *req.MaxHeight
		}
	case req.MaxHeight != nil:
		// 原生：不叠加转码上限
	case req.TranscodeMaxHeight > 0 && (maxH <= 0 || req.TranscodeMaxHeight < maxH):
		maxH = req.TranscodeMaxHeight
	}
	if (maxW > 0 && vs.Width > maxW) || (maxH > 0 && vs.Height > maxH) {
		out.TargetWidth, out.TargetHeight = fitWithin(vs.Width, vs.Height, maxW, maxH)
		why = append(why, fmt.Sprintf("并缩放到 %d×%d", out.TargetWidth, out.TargetHeight))
	}
	if vs.BitDepth > 8 {
		out.TenBit = true
		why = append(why, "降到 8bit")
	}
	if isHDR(vs) {
		out.Tonemap = m.Tonemap
		if m.Tonemap {
			why = append(why, "并从 HDR 色调映射到 SDR")
		} else {
			why = append(why, "HDR 内容将失去色调映射（这台机器没探测到能力），画面会发灰")
		}
	}
	if isInterlaced(vs) {
		out.Deinterlace = true
		why = append(why, "去隔行")
	}
	out.Reason = join(prefix, fmt.Sprintf("视频 %s 转码为 %s（%s）", res, target, strings.Join(why, "；")))
	return out
}

// qualityFor 选质量档：小片子/低分辨率用低档，4K 用高档。
//
// 目标是“别把 4 核机器打死”：硬件后端本来就有余量，所以档位主要是给软编兜底用的。
func qualityFor(vs probe.VideoStream) string {
	if vs.Height >= 1800 {
		return "high"
	}
	if vs.Height <= 480 {
		return "low"
	}
	return "medium"
}

// fitWithin 把 (w,h) 等比缩放到 (maxW,maxH) 之内，并保证是偶数
//（编码器不吃奇数尺寸，尤其是 h264 的 yuv420p）。
func fitWithin(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if maxW <= 0 {
		maxW = w
	}
	if maxH <= 0 {
		maxH = h
	}
	scale := 1.0
	if w > maxW {
		scale = float64(maxW) / float64(w)
	}
	if h > maxH && float64(maxH)/float64(h) < scale {
		scale = float64(maxH) / float64(h)
	}
	nw := int(float64(w)*scale) &^ 1
	nh := int(float64(h)*scale) &^ 1
	if nw < 2 {
		nw = 2
	}
	if nh < 2 {
		nh = 2
	}
	return nw, nh
}

// planAudio 决定音频流怎么处理。
//
// video 是同一个文件上的视频计划：只有视频**真的会转码**（本机有可用编码器）时
// 音频才跟着转成 AAC；视频根本放不了时只如实标记，不承诺「会降到几声道」——
// 那是开空头支票，用户看到的理由链会自相矛盾。
func planAudio(p Profile, file *File, want int, video StreamPlan) StreamPlan {
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

	copied := video.Action == ActionCopy
	willTranscode := video.Action == ActionTranscode && video.TargetCodec != ""

	switch {
	case willTranscode:
		// 视频要转码，音频顺带一起转成 AAC（不额外增加什么开销）
		out.Action = ActionConvert
		out.Downmix = p.MaxAudioChannels > 0 && as.Channels > p.MaxAudioChannels
		out.Reason = fmt.Sprintf("音频 %s 随视频一起转成 AAC", name)
	case !copied:
		// 视频需要转码但本机没有可用编码器：整条放不了。音频如实标记，
		// 但不写降混——转码根本没发生。
		out.Action = ActionConvert
		out.Reason = fmt.Sprintf("音频 %s 需随视频一起转码（视频当前放不了）", name)
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
		out.Reason = fmt.Sprintf("字幕 #%d（%s）是图形字幕，需要烧录进画面（烧录链路还没接上），本次不显示", ss.Index, ss.Codec)
		return out
	}

	codec := strings.ToLower(ss.Codec)
	out.Action = ActionConvert
	switch codec {
	case "ass", "ssa":
		// 交给前端 libass 渲染：转 WebVTT 会把定位、动画、样式全丢掉。
		out.DeliverAs = DeliverLibass
		out.Reason = fmt.Sprintf("字幕 #%d（%s）交给前端的 libass 渲染（保留特效与样式）", ss.Index, ss.Codec)
	default:
		out.DeliverAs = DeliverWebVTT
		out.Reason = fmt.Sprintf("字幕 #%d（%s）转成 WebVTT 由浏览器渲染", ss.Index, ss.Codec)
	}
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
		// 要转码：能不能放取决于「本机有没有可用的编码器」——
		// 由 planVideo 根据能力表写在 TargetCodec 里（空 = 编不了）。
		if plan.Video.TargetCodec == "" {
			return ModeTranscode, "", false
		}
		// 转码输出必须能装进分片（h264/hevc 都能进 fMP4，TS 只兼容 h264 系）
		seg := segmentFormat(p, plan.Video.TargetCodec)
		if seg == "" {
			return ModeTranscode, "", false
		}
		return ModeTranscode, seg, true
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
		if plan.Video.TargetCodec != "" {
			return fmt.Sprintf("视频转码为 %s（后端 %s）", plan.Video.TargetCodec, plan.Video.Backend)
		}
		return "需要视频转码，但这台机器没有可用的编码器"
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
