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
	//
	// 与 ComplexFilter 互斥：要叠加图形字幕时必须用后者 ——
	// overlay 有两路输入（画面 + 字幕位图），而 -vf 只能吃一路。
	FilterArgs []string
	// ComplexFilter 是 -filter_complex 的整条图（烧录图形字幕时走这条路）。
	ComplexFilter string
	// MapLabel 是 ComplexFilter 里最终产出的标签（如 "[vout]"）。
	//
	// 有它时 stream 层必须用它做 -map，而**不能**再 -map 原始视频流：
	// 同一个视频流被送两遍会让 ffmpeg 在过滤器协商阶段直接失败
	//（实测报 "Impossible to convert between the formats supported by…"）。
	MapLabel string
	// CodecArgs 是 -c:v 与质量参数（含关键帧对齐）。
	CodecArgs []string
	// Tag 是额外标签（如 HEVC 装 fMP4 必须打 hvc1）。
	Tag []string
}

// SubtitleBurn 表示「把这条图形字幕烧进画面」。
//
// 只对**图形字幕**（PGS / VobSub 这类位图）成立：文本字幕走 WebVTT / libass
// 两条旁路（见 internal/playback），不需要把画面整个重编一遍。
type SubtitleBurn struct {
	// Index 是源文件里字幕流的**绝对序号**（ffprobe 的 index）。
	//
	// 滤镜图里按绝对序号引用输入流：用「第几条字幕」这种相对序号会在
	// 多字幕轨的文件里指错流。
	Index int
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

	// VideoIndex 是视频流的**绝对序号**（ffprobe 的 index）—— 只在烧录字幕时用到：
	// 那时画面由 -filter_complex 产出，滤镜图里要按绝对序号引用这一路。
	// 其余路径由 stream 层自己 -map，不经过这里。
	VideoIndex int
	// NoHWDecode 强制走软件解码。
	//
	// 它是**降级开关**：能力探测只能验到「这个编码名能不能硬解」，验不到
	// 同一编码的各个变体（实测：iHD 解得了 8bit H.264，却解不了 H.264 High 10）。
	// 所以硬解起不来时上层会拿这个开关重试一次，用软件解码 + 同一个硬件编码器。
	NoHWDecode bool
	// BurnSubtitle 非 nil 表示要把这条图形字幕烧进画面。
	//
	// 它会带来两个后果：本来能 -c:v copy 的片子必须重编（位图只能烧进像素），
	// 而且画面必须先回到内存才能 overlay（硬解时帧在显存里）。
	BurnSubtitle *SubtitleBurn
}

// videoChain 是「画面该怎么处理」的两套写法。
//
// 为什么要两套：不烧字幕时可以把帧直接交给编码器（硬解时甚至一直留在显存里，
// 一路不落地）；一旦要烧图形字幕，就必须把帧取回内存做 overlay，做完再送回去。
// 这两条链在同一个后端上差别很大，但「差别在哪」是后端自己的事（见各 VideoArgs），
// 所以用一个结构把两者一并交给装配函数。
type videoChain struct {
	// Plain 是不烧字幕时的链（结束时帧的形态直接给编码器）。
	Plain []string
	// Burn 是烧字幕时的主链（**结束时帧必须已经在内存里**）。
	Burn []string
	// Post 是叠加之后的收尾（把帧再送回编码器要的形态，例如上传显存）。
	Post []string
}

// assembleVideoArgs 把链装配成最终参数。
//
// 不烧字幕：一条 -vf 就完事。
// 烧图形字幕：必须是 -filter_complex —— overlay 有两路输入（画面 + 字幕位图），
// 而 -vf 只能吃一路。图的形式是：
//
//	[0:字幕序号]缩放位图[sub];[0:视频序号]主链[main];[main][sub]overlay,收尾[vout]
//
// 主链为空时（画面原样进 overlay）省掉中间那一段。
func assembleVideoArgs(req ArgsRequest, ch videoChain, codecArgs, tag []string) VideoArgs {
	out := VideoArgs{CodecArgs: codecArgs, Tag: tag}
	if req.BurnSubtitle == nil {
		if len(ch.Plain) > 0 {
			out.FilterArgs = []string{"-vf", strings.Join(ch.Plain, ",")}
		}
		return out
	}

	overlay := "overlay=eof_action=pass:repeatlast=0"
	if len(ch.Post) > 0 {
		// eof_action=pass：字幕轨比视频短（只有前半段有字幕）时，
		// 后半个 input 到底了也不能把画面一起结束掉 —— 直接放行主输入。
		overlay += "," + strings.Join(ch.Post, ",")
	}

	parts := []string{fmt.Sprintf("[0:%d]%s[sub]", req.BurnSubtitle.Index, subtitleBitmapFilters(req))}
	if len(ch.Burn) > 0 {
		parts = append(parts,
			fmt.Sprintf("[0:%d]%s[main]", req.VideoIndex, strings.Join(ch.Burn, ",")),
			"[main][sub]"+overlay+"[vout]")
	} else {
		parts = append(parts, fmt.Sprintf("[0:%d][sub]%s[vout]", req.VideoIndex, overlay))
	}
	out.ComplexFilter = strings.Join(parts, ";")
	out.MapLabel = "[vout]"
	return out
}

// subtitleBitmapFilters 把字幕位图缩放到与**输出画面**同尺寸。
//
// 为什么必须缩放：图形字幕是位图，尺寸是**片源**的（库里带 PGS 的文件基本都是
// 1920×1080 的位图）；而画质档可能把画面缩到 720p —— 位图不跟着缩就会溢出画面。
//
// pad 的填充色用 black@0（全透明黑）而不是黑色：位图带 alpha，不透明的边
// 会在画面上糊出一条黑带（实测踩到）。
//
// force_original_aspect_ratio=decrease + 居中 pad：先等比缩进框内，再补齐——
// 位图与画面的宽高比不一致时（裁过的 PGS）也不会变形。
func subtitleBitmapFilters(req ArgsRequest) string {
	w, h := req.TargetWidth, req.TargetHeight
	if w <= 0 || h <= 0 {
		// 不缩放输出时位图尺寸本来就对得上，用源尺寸兜底。
		w, h = req.Width, req.Height
	}
	chain := make([]string, 0, 3)
	if w > 0 && h > 0 {
		chain = append(chain,
			fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:flags=bilinear", w, h),
			fmt.Sprintf("pad=w=%d:h=%d:x=(ow-iw)/2:y=(oh-ih)/2:color=black@0", w, h))
	}
	// overlay 只吃带 alpha 的输入；PGS 解出来是 pal8，统一成 yuva420p 最稳
	//（测试过不加它也能叠，但依赖解码器恰好给出带 alpha 的格式）。
	chain = append(chain, "format=yuva420p")
	return strings.Join(chain, ",")
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
	// 软件解码时把帧转成 8bit yuv420p 再编：10bit 源编成 10bit h264 浏览器放不了
	// （Hi10P 任何浏览器都不认），所以这里必须显式降到 8bit。
	main := videoFilters(req, true)

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
	codecArgs := []string{"-c:v", enc, "-preset", preset, "-crf", strconv.Itoa(crf)}
	if enc == "libx264" {
		// 场景切换会插关键帧，破坏「每片都从关键帧开始」，必须关掉
		codecArgs = append(codecArgs, "-sc_threshold:v", "0")
	}
	codecArgs = append(codecArgs, forceKey...)
	var tag []string
	if req.TargetCodec == "hevc" {
		tag = []string{"-tag:v", "hvc1"}
	}
	// 软件路径的帧本来就在内存里，overlay 直接接得上，不需要收尾。
	return assembleVideoArgs(req, videoChain{Plain: main, Burn: main}, codecArgs, tag)
}

// vaapiVideoArgs 是 Intel/AMD 的 VAAPI 路径：能硬解就硬解，滤镜也尽量留在显存里。
func vaapiVideoArgs(b Backend, req ArgsRequest, forceKey []string) VideoArgs {
	device := b.Device
	if device == "" {
		device = "/dev/dri/renderD128"
	}
	inputArgs := []string{"-vaapi_device", device}

	// 硬解：只有探测说这个编码能硬解时才开，否则交给软件解码器
	// （盲目开 -hwaccel 在部分驱动/编码上会直接失败，而不是自动回退）。
	hwDecode := b.Decode[req.SourceCodec]
	if req.SourceCodec == "h265" {
		hwDecode = b.Decode["hevc"]
	}
	if req.NoHWDecode {
		hwDecode = false
	}
	if hwDecode {
		inputArgs = append(inputArgs, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
	}

	// 目标 h264 只吃 8bit：源是 10bit（HEVC 10bit / Hi10P）就必须降位深，
	// 否则帧格式与编码器不匹配，编码器初始化会直接失败。
	to8bit := req.TargetCodec != "hevc" && req.BitDepth > 8

	// upload 是「从内存回显存」（给编码器）。它在两条路径上都用得上：
	// 不烧录时出现在链尾，烧录时出现在 overlay 之后。
	upload := []string{"format=nv12", "hwupload"}

	// chain 是处理链（不含收尾）；inMemory 记下它**结束时帧在哪**。
	// 硬解时帧默认留在显存，一路不落地；但色调映射链是软件链，必须取回内存。
	chain := []string{}
	inMemory := !hwDecode

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
				chain = append(chain, fmt.Sprintf("scale_vaapi=w=%d:h=%d", req.TargetWidth, req.TargetHeight))
			}
			chain = append(chain, "hwdownload", "format=p010")
		}
		chain = append(chain,
			"zscale=t=linear:npl=100",
			"tonemap=hable",
			"zscale=p=bt709:t=bt709:m=bt709:r=tv",
		)
		if req.TargetWidth > 0 && !hwDecode {
			// 软解时帧本来就在内存里，缩放在软件域做（放在色调映射之后）。
			chain = append(chain, softwareScaleFilter(req))
		}
		inMemory = true
	} else {
		switch {
		case req.TargetWidth > 0:
			if hwDecode {
				chain = append(chain, scaleVAApiFilter(req))
			} else {
				// 软解时帧在内存里，必须用软件缩放：硬件滤镜作用在软件帧上
				// **必然失败**（ffmpeg 不会替你补 hwupload），实测报
				// "Impossible to convert between the formats supported by…"。
				chain = append(chain, softwareScaleFilter(req))
			}
		case to8bit:
			// 不缩放但不能不降位深：h264_vaapi 只接受 nv12。
			if hwDecode {
				chain = append(chain, "scale_vaapi=format=nv12")
			}
		}
	}
	// 去隔行放在链尾，且必须与帧的位置匹配（软件帧上 deinterlace_vaapi 同样会失败）。
	if req.Interlaced {
		if inMemory {
			chain = append(chain, "yadif=0:-1:0")
		} else {
			chain = append(chain, "deinterlace_vaapi")
		}
	}

	// plain：不烧录时的链。帧在内存里就先上传回显存再交给编码器
	//（硬解的非色调映射路径帧一直在显存里，什么都不用加）。
	plain := chain
	if inMemory {
		plain = append(append([]string{}, chain...), upload...)
	}

	// burn：烧录时的链。overlay 只能在内存里做，所以帧必须先取回来。
	burnMain := chain
	if !inMemory {
		burnMain = append(append([]string{}, chain...), "hwdownload", "format=nv12")
	}

	enc := "h264_vaapi"
	if req.TargetCodec == "hevc" {
		enc = "hevc_vaapi"
	}
	qargs := qualityArgs(b, req.Quality)
	codecArgs := make([]string, 0, 12+len(forceKey))
	codecArgs = append(codecArgs, "-c:v", enc)
	codecArgs = append(codecArgs, qargs...)
	codecArgs = append(codecArgs, forceKey...)
	var tag []string
	if req.TargetCodec == "hevc" {
		tag = []string{"-tag:v", "hvc1"}
	}

	out := assembleVideoArgs(req, videoChain{Plain: plain, Burn: burnMain, Post: upload}, codecArgs, tag)
	out.InputArgs = inputArgs
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

// softwareScaleFilter 是软件域的等比缩放：force_original_aspect_ratio=decrease 保证
// 宽高比不丢，force_divisible_by=2 保证偶数尺寸（编码器不吃奇数，h264 的 yuv420p 尤其）。
func softwareScaleFilter(req ArgsRequest) string {
	return fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2",
		req.TargetWidth, req.TargetHeight)
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
		filters = append(filters, softwareScaleFilter(req))
	}
	if to8bit {
		filters = append(filters, "format=yuv420p")
	}
	return filters
}

// qsvVideoArgs / nvencVideoArgs / vtVideoArgs：这三条在本机没有硬件可验，
// 参数照 Jellyfin 的写法给；只有探测**真跑通过**的后端才会走到这里。
func qsvVideoArgs(b Backend, req ArgsRequest) VideoArgs {
	// QSV 的帧处理全在软件域做（videoFilters 就是软件滤镜链），最后上传给编码器。
	main := videoFilters(req, false)
	upload := []string{"format=nv12", "hwupload=extra_hw_frames=64"}
	enc := "h264_qsv"
	if req.TargetCodec == "hevc" {
		enc = "hevc_qsv"
	}
	codecArgs := []string{"-c:v", enc}
	switch b.Prefer {
	case "icq":
		codecArgs = append(codecArgs, "-global_quality", strconv.Itoa(req.Quality.CRF))
	default:
		codecArgs = append(codecArgs, "-b:v", strconv.Itoa(req.Quality.BitrateKbps)+"k")
	}
	// QSV 不认强制关键帧，只能靠 GOP 长度：ceil(分片秒 × 帧率)。这里用 30fps 估计，
	// 实际帧率由上层在需要时改传（M4 后续细化）。
	codecArgs = append(codecArgs, "-g", strconv.Itoa(req.KeyframeSeconds*30))
	return assembleVideoArgs(req, videoChain{
		Plain: append(append([]string{}, main...), upload...),
		Burn:  main,
		Post:  upload,
	}, codecArgs, nil)
}

func nvencVideoArgs(req ArgsRequest, forceKey []string) VideoArgs {
	// 与软编一样降到 8bit：h264_nvenc 不吃 10bit 输入，
	// 而浏览器也播不了 10bit 的 h264（Hi10P）。
	main := videoFilters(req, true)
	enc := "h264_nvenc"
	if req.TargetCodec == "hevc" {
		enc = "hevc_nvenc"
	}
	codecArgs := make([]string, 0, 8+len(forceKey))
	codecArgs = append(codecArgs, "-c:v", enc, "-rc", "vbr", "-cq", strconv.Itoa(req.Quality.CRF))
	codecArgs = append(codecArgs, "-b:v", strconv.Itoa(req.Quality.BitrateKbps)+"k")
	codecArgs = append(codecArgs, forceKey...)
	var tag []string
	if req.TargetCodec == "hevc" {
		tag = []string{"-tag:v", "hvc1"}
	}
	// nvenc 直接吃软件帧，不需要收尾。
	return assembleVideoArgs(req, videoChain{Plain: main, Burn: main}, codecArgs, tag)
}

func vtVideoArgs(req ArgsRequest, forceKey []string) VideoArgs {
	main := videoFilters(req, true)
	enc := "h264_videotoolbox"
	if req.TargetCodec == "hevc" {
		enc = "hevc_videotoolbox"
	}
	codecArgs := make([]string, 0, 4+len(forceKey))
	codecArgs = append(codecArgs, "-c:v", enc, "-b:v", strconv.Itoa(req.Quality.BitrateKbps)+"k")
	codecArgs = append(codecArgs, forceKey...)
	var tag []string
	if req.TargetCodec == "hevc" {
		tag = []string{"-tag:v", "hvc1"}
	}
	return assembleVideoArgs(req, videoChain{Plain: main, Burn: main}, codecArgs, tag)
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
