// 本文件负责「把一段视频编成浏览器能播的样子」所涉及的**参数配方**。
//
// 为什么单独成文件、而且不碰进程：这部分是最容易出错、也最难从外部观察的地方
// （参数写错的表现是黑屏 / 无声 / 卡顿），所以它被写成**纯函数**：
// 给「源是什么 + 目标是什么 + 这台机器能用什么后端」，返回一串可以直接拼进
// ffmpeg 命令行的参数。这样它能被离线单测钉死，不需要真跑 ffmpeg。
//
// 参数配方参考 Jellyfin（见 docs/notes/jellyfin-reference.md 的 文件:行）：
// 关键帧对齐、GOP、滤镜链、码率模式的分档都是照着它对齐的。
package encoder

import (
	"fmt"
	"strconv"
	"strings"
)

// Quality 是一档画质的抽象：具体数值按后端/编码器换算。
//
// 为什么不直接暴露 CRF/QP：同一个数字在不同编码器/硬件上完全不是一回事
// （x264 的 CRF 21 与 VAAPI 的 QP 21 视觉质量差很远），所以上层只选「档」，
// 换算交给这里。
type Quality struct {
	// Preset 是软件编码器的 preset（veryfast / medium…）。
	Preset string
	// CRF 是软件编码的目标质量（libx264/libx265）。
	CRF int
	// QP 是硬件编码 CQP 模式的量化参数（越小越好）。
	QP int
	// BitrateKbps 是 CBR/VBR 模式的目标码率。
	BitrateKbps int
}

// 三档预设。之所以给「档」而不是让用户填数字：填错了要么糊要么卡，
// 而用户没有参照物。想细调就改配置里的这几个数（见 [playback] 段）。
var (
	QualityHigh   = Quality{Preset: "medium", CRF: 19, QP: 21, BitrateKbps: 12000}
	QualityMedium = Quality{Preset: "veryfast", CRF: 21, QP: 24, BitrateKbps: 8000}
	QualityLow    = Quality{Preset: "veryfast", CRF: 26, QP: 30, BitrateKbps: 3000}
)

// QualityByName 解析配置里的档位名；认不出的一律按 medium。
func QualityByName(name string) Quality {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "high", "高":
		return QualityHigh
	case "low", "低", "省流":
		return QualityLow
	default:
		return QualityMedium
	}
}

// VideoArgs 是「视频这一段怎么处理」的最终参数，由 stream 层原样拼进命令行。
type VideoArgs struct {
	// InputArgs 放在 -i **之前**（硬件设备与硬解开关）。
	InputArgs []string
	// FilterArgs 是 -vf 的整条链（空表示不加滤镜）。
	FilterArgs []string
	// CodecArgs 是 -c:v 与质量参数（含关键帧对齐）。
	CodecArgs []string
	// Tag 是额外标签（如 HEVC 装 fMP4 必须打 hvc1）。
	Tag []string
}

// CopyVideoArgs 表示视频原样复制（转封装）。
func CopyVideoArgs() VideoArgs { return VideoArgs{CodecArgs: []string{"-c:v", "copy"}} }

// ArgsRequest 是生成视频参数所需的全部信息。
type ArgsRequest struct {
	SourceCodec string // h264 / hevc / av1 / vp9 / mpeg2video …
	Width       int
	Height      int
	BitDepth    int
	HDR         bool // HDR10（smpte2084）或 HLG（arib-std-b67）
	Interlaced  bool

	TargetCodec  string // h264 | hevc
	TargetWidth  int    // 0 = 不缩放
	TargetHeight int
	Quality      Quality
	// KeyframeSeconds 是分片长度：强制关键帧按它对齐，否则分片长度会漂。
	KeyframeSeconds int
}

// VideoArgs 按后端生成视频参数。纯函数：同样的输入永远给同样的输出。
func (b Backend) VideoArgs(req ArgsRequest) VideoArgs {
	if req.TargetCodec == "" {
		req.TargetCodec = "h264"
	}
	if req.KeyframeSeconds <= 0 {
		req.KeyframeSeconds = 4
	}
	forceKey := []string{"-force_key_frames:v", fmt.Sprintf("expr:gte(t,n_forced*%d)", req.KeyframeSeconds)}

	switch b.Kind {
	case KindVAAPI:
		return vaapiVideoArgs(b, req, forceKey)
	case KindQSV:
		return qsvVideoArgs(b, req)
	case KindNVENC:
		return nvencVideoArgs(req, forceKey)
	case KindVideoToolbox:
		return vtVideoArgs(req, forceKey)
	}
	return softwareVideoArgs(req, forceKey)
}

// softwareVideoArgs 是 CPU 路径（也是最后的兜底）。
func softwareVideoArgs(req ArgsRequest, forceKey []string) VideoArgs {
	out := VideoArgs{}
	// 软件解码时把帧转成 8bit yuv420p 再编：10bit 源编成 10bit h264 浏览器放不了
	// （Hi10P 任何浏览器都不认），所以这里必须显式降到 8bit。
	filters := videoFilters(req, true)
	if len(filters) > 0 {
		out.FilterArgs = append(out.FilterArgs, "-vf", strings.Join(filters, ","))
	}

	enc, preset := "libx264", req.Quality.Preset
	if req.TargetCodec == "hevc" {
		enc = "libx265"
	}
	if preset == "" {
		preset = "veryfast"
	}
	crf := req.Quality.CRF
	if crf <= 0 {
		crf = 21
	}
	out.CodecArgs = []string{"-c:v", enc, "-preset", preset, "-crf", strconv.Itoa(crf)}
	if enc == "libx264" {
		// 场景切换会插关键帧，破坏「每片都从关键帧开始」，必须关掉
		out.CodecArgs = append(out.CodecArgs, "-sc_threshold:v", "0")
	}
	out.CodecArgs = append(out.CodecArgs, forceKey...)
	if req.TargetCodec == "hevc" {
		out.Tag = []string{"-tag:v", "hvc1"}
	}
	return out
}

// vaapiVideoArgs 是 Intel/AMD 的 VAAPI 路径：能硬解就硬解，滤镜也尽量留在显存里。
func vaapiVideoArgs(b Backend, req ArgsRequest, forceKey []string) VideoArgs {
	out := VideoArgs{}
	device := b.Device
	if device == "" {
		device = "/dev/dri/renderD128"
	}
	out.InputArgs = []string{"-vaapi_device", device}

	// 硬解：只有探测说这个编码能硬解时才开，否则交给软件解码器
	// （盲目开 -hwaccel 在部分驱动/编码上会直接失败，而不是自动回退）。
	hwDecode := b.Decode[req.SourceCodec]
	if req.SourceCodec == "h265" {
		hwDecode = b.Decode["hevc"]
	}
	if hwDecode {
		out.InputArgs = append(out.InputArgs, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
	}

	// 目标 h264 只吃 8bit：源是 10bit（HEVC 10bit / Hi10P）就必须降位深，
	// 否则帧格式与编码器不匹配，编码器初始化会直接失败。
	to8bit := req.TargetCodec != "hevc" && req.BitDepth > 8

	filters := []string{}
	if req.HDR {
		// HDR→SDR 一律走**软件**色调查映射链（zscale + tonemap）。
		//
		// 为什么不省这一步用 tonemap_vaapi：iHD 驱动要求输入带 mastering display
		// 元数据，而实测素材（bt2020 + PQ、就是没有那段元数据）会直接失败：
		//   [tonemap_vaapi] No mastering display data from input → Invalid argument
		// 后果不是画质差，而是**整路转码起不来**（用户看到的是放不了）。
		// 软件链不吃元数据，代价是 CPU——这是实测过的取舍。
		if hwDecode {
			// 硬解：**先在显存里缩放**再取回内存。色调映射是 CPU 上的重活，
			// 先把 4K 降到 1080p 能把它的数据量砍到 1/4（实测 0.10x → 0.39x）。
			if req.TargetWidth > 0 {
				filters = append(filters, fmt.Sprintf("scale_vaapi=w=%d:h=%d", req.TargetWidth, req.TargetHeight))
			}
			filters = append(filters, "hwdownload", "format=p010")
		}
		filters = append(filters,
			"zscale=t=linear:npl=100",
			"tonemap=hable",
			"zscale=p=bt709:t=bt709:m=bt709:r=tv",
		)
		if req.TargetWidth > 0 && !hwDecode {
			// 软解时帧本来就在内存里，缩放在软件域做（放在色调映射之后）。
			filters = append(filters, fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2",
				req.TargetWidth, req.TargetHeight))
		}
		filters = append(filters, "format=nv12,hwupload")
	} else if req.TargetWidth > 0 {
		filters = append(filters, scaleVAApiFilter(req))
	} else if to8bit {
		// 不缩放但不能不降位深：h264_vaapi 只接受 nv12。
		filters = append(filters, "scale_vaapi=format=nv12")
	}
	if req.Interlaced {
		filters = append(filters, "deinterlace_vaapi")
	}
	// 硬解直通且不需要缩放/色调映射时：帧已经在显存里，什么都不用加。
	// 软件解码却不缩放时：至少要把帧上传上去。
	if len(filters) == 0 && !hwDecode {
		filters = append(filters, "format=nv12,hwupload")
	}
	if len(filters) > 0 {
		out.FilterArgs = append(out.FilterArgs, "-vf", strings.Join(filters, ","))
	}

	enc := "h264_vaapi"
	if req.TargetCodec == "hevc" {
		enc = "hevc_vaapi"
	}
	out.CodecArgs = []string{"-c:v", enc}
	out.CodecArgs = append(out.CodecArgs, qualityArgs(b, req.Quality)...)
	out.CodecArgs = append(out.CodecArgs, forceKey...)
	if req.TargetCodec == "hevc" {
		out.Tag = []string{"-tag:v", "hvc1"}
	}
	return out
}

// qualityArgs 按**探测到的**可用模式生成码率参数。
//
// 这是 M4 里唯一必须看能力表的地方：老 i965 只吃 CQP、AMD 的 VAAPI 更习惯 VBR，
// 写死一种就会在别的机器上编不出来。b.Prefer 是探测时第一个真跑通过的模式。
func qualityArgs(b Backend, q Quality) []string {
	mode := b.Prefer
	if mode == "" && len(b.Quality) > 0 {
		mode = b.Quality[0]
	}
	switch mode {
	case "cqp":
		qp := q.QP
		if qp <= 0 {
			qp = 24
		}
		return []string{"-rc_mode", "CQP", "-qp", strconv.Itoa(qp)}
	case "cbr":
		kbps := q.BitrateKbps
		if kbps <= 0 {
			kbps = 8000
		}
		bv := strconv.Itoa(kbps) + "k"
		return []string{"-rc_mode", "CBR", "-b:v", bv, "-maxrate", bv, "-bufsize", strconv.Itoa(kbps*2) + "k"}
	case "vbr", "":
		kbps := q.BitrateKbps
		if kbps <= 0 {
			kbps = 8000
		}
		return []string{"-rc_mode", "VBR", "-b:v", strconv.Itoa(kbps) + "k"}
	default:
		// 其它标签（icq / cq …）由各自后端的分支处理
		return nil
	}
}

func scaleVAApiFilter(req ArgsRequest) string {
	return fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=nv12", req.TargetWidth, req.TargetHeight)
}

// videoFilters 拼「缩放 + 去隔行 + 色调映射 + 像素格式」这条软件滤镜链。
func videoFilters(req ArgsRequest, to8bit bool) []string {
	var filters []string
	if req.Interlaced {
		filters = append(filters, "yadif=0:-1:0")
	}
	if req.HDR {
		// HDR→SDR：线性化 → tone map → 回到 bt709。少了这一步画面会发灰发暗。
		filters = append(filters,
			"zscale=t=linear:npl=100",
			"tonemap=hable",
			"zscale=p=bt709:t=bt709:m=bt709:r=tv",
		)
	}
	if req.TargetWidth > 0 && req.TargetHeight > 0 {
		// force_original_aspect_ratio=decrease + 偶数对齐：宽高比不丢、编码器不吃奇数尺寸
		filters = append(filters, fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2",
			req.TargetWidth, req.TargetHeight))
	}
	if to8bit {
		filters = append(filters, "format=yuv420p")
	}
	return filters
}

// qsvVideoArgs / nvencVideoArgs / vtVideoArgs：这三条在本机没有硬件可验，
// 参数照 Jellyfin 的写法给；只有探测**真跑通过**的后端才会走到这里。
func qsvVideoArgs(b Backend, req ArgsRequest) VideoArgs {
	out := VideoArgs{}
	filters := videoFilters(req, false)
	filters = append(filters, "format=nv12,hwupload=extra_hw_frames=64")
	out.FilterArgs = []string{"-vf", strings.Join(filters, ",")}
	enc := "h264_qsv"
	if req.TargetCodec == "hevc" {
		enc = "hevc_qsv"
	}
	out.CodecArgs = []string{"-c:v", enc}
	switch b.Prefer {
	case "icq":
		out.CodecArgs = append(out.CodecArgs, "-global_quality", strconv.Itoa(req.Quality.CRF))
	default:
		out.CodecArgs = append(out.CodecArgs, "-b:v", strconv.Itoa(req.Quality.BitrateKbps)+"k")
	}
	// QSV 不认强制关键帧，只能靠 GOP 长度：ceil(分片秒 × 帧率)。这里用 30fps 估计，
	// 实际帧率由上层在需要时改传（M4 后续细化）。
	out.CodecArgs = append(out.CodecArgs, "-g", strconv.Itoa(req.KeyframeSeconds*30))
	return out
}

func nvencVideoArgs(req ArgsRequest, forceKey []string) VideoArgs {
	out := VideoArgs{}
	// 与软编一样降到 8bit：h264_nvenc 不吃 10bit 输入，
	// 而浏览器也播不了 10bit 的 h264（Hi10P）。
	filters := videoFilters(req, true)
	if len(filters) > 0 {
		out.FilterArgs = []string{"-vf", strings.Join(filters, ",")}
	}
	enc := "h264_nvenc"
	if req.TargetCodec == "hevc" {
		enc = "hevc_nvenc"
	}
	out.CodecArgs = []string{"-c:v", enc, "-rc", "vbr", "-cq", strconv.Itoa(req.Quality.CRF)}
	out.CodecArgs = append(out.CodecArgs, "-b:v", strconv.Itoa(req.Quality.BitrateKbps)+"k")
	out.CodecArgs = append(out.CodecArgs, forceKey...)
	if req.TargetCodec == "hevc" {
		out.Tag = []string{"-tag:v", "hvc1"}
	}
	return out
}

func vtVideoArgs(req ArgsRequest, forceKey []string) VideoArgs {
	out := VideoArgs{}
	filters := videoFilters(req, true)
	if len(filters) > 0 {
		out.FilterArgs = []string{"-vf", strings.Join(filters, ",")}
	}
	enc := "h264_videotoolbox"
	if req.TargetCodec == "hevc" {
		enc = "hevc_videotoolbox"
	}
	out.CodecArgs = []string{"-c:v", enc, "-b:v", strconv.Itoa(req.Quality.BitrateKbps) + "k"}
	out.CodecArgs = append(out.CodecArgs, forceKey...)
	if req.TargetCodec == "hevc" {
		out.Tag = []string{"-tag:v", "hvc1"}
	}
	return out
}

// AudioArgs 是「音频这一段怎么处理」的最终参数。
type AudioArgs struct {
	Copy bool
	Args []string
}

// AudioArgsRequest 描述音频转码需求。
type AudioArgsRequest struct {
	SourceCodec string
	Channels    int
	// MaxChannels 是客户端能收的声道数上限（0 = 不限制）。
	MaxChannels int
	// BitrateKbps 目标码率（0 = 按声道数取默认）。
	BitrateKbps int
}

// AudioEncodeArgs 生成音频参数。
//
// 默认转 AAC：浏览器只认 aac/mp3/opus，而 aac 装进 fMP4 的兼容性最好
// （Safari 尤其挑剔）。多声道超过客户端上限时降到立体声。
func AudioEncodeArgs(req AudioArgsRequest) AudioArgs {
	kbps := req.BitrateKbps
	if kbps <= 0 {
		kbps = 192
		if req.Channels > 2 && (req.MaxChannels == 0 || req.MaxChannels > 2) {
			kbps = 384
		}
	}
	args := []string{"-c:a", "aac", "-b:a", strconv.Itoa(kbps) + "k"}
	if req.MaxChannels > 0 && req.Channels > req.MaxChannels {
		args = append(args, "-ac", strconv.Itoa(req.MaxChannels))
	}
	return AudioArgs{Args: args}
}

// CopyAudioArgs 表示音频原样复制。
func CopyAudioArgs() AudioArgs { return AudioArgs{Copy: true} }
