package stream

import (
	"path/filepath"
	"strconv"
	"strings"
)

// VideoEncode 描述「视频这一段怎么送」。
//
// 由 internal/encoder 按**本机真跑探测出来的能力**生成（转码时），
// 或者直接标记 Copy（直出/转封装时）。stream 层不做任何判断，只负责拼参数 ——
// 「用哪个编码器、什么码率模式」的知识全在 encoder 包里，两边不重复。
type VideoEncode struct {
	// Copy 为真表示原样复制（转封装）：忽略下面所有字段。
	Copy bool
	// InputArgs 放在 -i 之前（-vaapi_device / -hwaccel vaapi …）。
	InputArgs []string
	// FilterArgs 是 -vf 的整条链（空表示不加）。
	FilterArgs []string
	// ComplexFilter 是 -filter_complex 的整条图（烧录图形字幕时用）：
	// overlay 有两路输入（画面 + 字幕位图），-vf 只有一路，做不了。
	ComplexFilter string
	// MapLabel 是 ComplexFilter 产出画面的标签（如 "[vout]"）。
	//
	// 有它时**不能**再 -map 原始视频流：同一个视频流送两遍会让 ffmpeg 在
	// 过滤器协商阶段直接失败（实测报 "Impossible to convert between…"）。
	MapLabel string
	// CodecArgs 是 -c:v 与质量/关键帧参数。
	CodecArgs []string
	// Tag 是额外标签（HEVC 装 fMP4 必须打 hvc1，否则部分客户端直接不出画面）。
	Tag []string
}

// AudioEncode 描述「音频这一段怎么送」。
type AudioEncode struct {
	Copy bool
	Args []string // -c:a aac -b:a 192k -ac 2 …
}

// CopyVideoEncode 是「原样复制」的简写。
func CopyVideoEncode() VideoEncode { return VideoEncode{Copy: true} }

// CopyAudioEncode 是「原样复制」的简写。
func CopyAudioEncode() AudioEncode { return AudioEncode{Copy: true} }

// hlsArgs 构造 HLS 命令行。
//
// 逐段的用意：
//   - `-nostdin`：ffmpeg 会抢 stdin，作为后台进程跑时会导致它在收到 EOF 后退出；
//     （节流走的是 SIGSTOP/SIGCONT，不靠 stdin 按键 —— 原因见 proc_unix.go）
//   - `-ss` 放在 `-i` **之前**：输入侧快速跳转（对 `-c copy` 是唯一可行的做法，
//     也是转码时最快起播的做法）。代价是只能落到关键帧上，与 Jellyfin 行为一致；
//   - `-map 0:<绝对序号>`：不用「第几个视频流」这种相对序号，避免决策层
//     与实际流序对不上；
//   - `-tag:v hvc1`：HEVC 装进 fMP4 必须打这个标签；
//   - `-t <窗口秒>`：只预生成一小段（`-c copy` 比实时快几十倍，不限制的话
//     一次拖到片尾会把整部电影拷进磁盘）；
//   - `-hls_list_size 0`：列表里保留全部分片，客户端才能在窗口内就地跳；
//   - 烧录图形字幕时改用 `-filter_complex` + `-map [vout]`：overlay 需要两路输入，
//     `-map 0:<视频序号>` 与滤镜输出同时存在会让同一个流被送两遍（会直接报错）。
func hlsArgs(opts Options, spec Spec, outDir string) []string {
	if spec.Live {
		return liveHLSArgs(opts, spec, outDir)
	}
	window := opts.WindowSeconds
	if spec.WindowSeconds > 0 {
		window = spec.WindowSeconds
	}

	segSeconds := opts.SegmentSeconds
	if spec.SegmentSeconds > 0 {
		segSeconds = spec.SegmentSeconds
	}

	segFormat := spec.SegmentFormat
	if segFormat != "ts" {
		segFormat = "fmp4"
	}

	args := []string{
		"-hide_banner",
		"-nostdin",
		"-loglevel", "warning",
		// 进度：用 `-progress` 而不是默认的 stats 行 —— 后者是 av_log(INFO)
		// 打出来的，会被我们压到 warning 的 loglevel 一起滤掉。
		// 而且写到**独立文件**而不是 stderr：源文件时间戳不规范时 ffmpeg 会刷
		// 大量警告（"pts has no value" 之类），混在一起会把进度行挤出环形日志
		//（真跑踩到：fps/speed 全是空）。
		"-stats_period", "3",
		"-progress", filepath.Join(outDir, "progress.txt"),
	}

	// 视频段：直出/转封装时不带硬件参数；转码时由 encoder 包给出完整配方。
	args = append(args, spec.Video.InputArgs...)

	if spec.StartSeconds > 0.001 {
		args = append(args, "-ss", formatSeconds(spec.StartSeconds))
	}
	args = append(args, "-i", spec.Path)

	// 视频映射：烧录图形字幕时画面由 -filter_complex 产出，必须 map 它给出的标签
	//（不能再 map 原始视频流，否则同一个流会被送两遍，滤镜协商直接失败 —— 实测踩到）。
	if spec.Video.ComplexFilter != "" {
		args = append(args, "-filter_complex", spec.Video.ComplexFilter)
		args = append(args, "-map", spec.Video.MapLabel)
	} else {
		args = append(args, "-map", "0:"+strconv.Itoa(spec.VideoIndex))
	}
	if spec.AudioIndex >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(spec.AudioIndex))
	}
	// -sn/-dn：内嵌字幕要么走单独的字幕响应（WebVTT/libass），要么已经被
	// filter_complex 吃掉；切片里再带一份只会白占地方。
	args = append(args, "-sn", "-dn")

	// 视频参数
	if spec.Video.Copy || len(spec.Video.CodecArgs) == 0 {
		args = append(args, "-c:v", "copy")
		// 原样复制的 HEVC 装进 fMP4 也要打 hvc1：少了这个标签 Safari 与部分
		// 播放器不认这条轨道（表现是不出画面）。转码路径由 encoder 包给同样的标签。
		if segFormat == "fmp4" && strings.EqualFold(spec.VideoCodec, "hevc") {
			args = append(args, "-tag:v", "hvc1")
		}
	} else {
		args = append(args, spec.Video.FilterArgs...)
		args = append(args, spec.Video.CodecArgs...)
	}
	args = append(args, spec.Video.Tag...)

	// 音频参数
	if spec.AudioIndex >= 0 {
		switch {
		case spec.Audio.Copy:
			args = append(args, "-c:a", "copy")
		case len(spec.Audio.Args) > 0:
			args = append(args, spec.Audio.Args...)
		default:
			// 兜底：没给参数就按最通用的转 AAC 立体声（浏览器都能放）
			args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
		}
	}

	args = append(args, "-t", strconv.Itoa(window))

	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(segSeconds),
		// event 而不是 vod：播放列表只追加、不重写。窗口结束时会自动补
		// EXT-X-ENDLIST，播放器据此知道这一段到底了。
		"-hls_playlist_type", "event",
		// ⚠ 必须显式设 0（= 列表里保留全部分片）。默认值是 5 —— 只留最近
		// 约 20 秒（5×4s），客户端能「就地跳」的区间也就只有 20 秒，
		// 其余位置都得让服务端重开一段 ffmpeg。
		// （Jellyfin 在 DynamicHlsController 里也是 `-hls_list_size 0`。）
		"-hls_list_size", "0",
		"-hls_segment_type", segFormat,
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", filepath.Join(outDir, "seg_%05d"+segmentExt(segFormat)),
	)
	if segFormat == "fmp4" {
		args = append(args, "-hls_fmp4_init_filename", "init.mp4",
			// 音视频起步时间不一致（转码时常见）也能连贯播放；skip_sidx 只是瘦身。
			"-hls_segment_options", "movflags=+frag_discont+skip_sidx")
	}
	// 源文件的元数据/章节对播放器毫无意义，拷进分片只是白占空间。
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1")
	// 网络盘（CIFS）读源时给输入/输出一点缓冲，避免瞬时抖动直接断流。
	args = append(args, "-max_delay", "5000000")
	return append(args, filepath.Join(outDir, "index.m3u8"))
}

// liveHLSArgs 构造直播转封装的命令行。
//
// 与点播的差别（每一条都有原因）：
//   - 没有 `-t`：直播没有终点，只靠 `hls_list_size` 滚滚向前；
//   - `-hls_list_size N` + `delete_segments`：播放列表只留最近 N 个分片，
//     旧分片随开随删（不删的话 24 小时能写掉几百 GB）；
//   - `+omit_endlist`：旋转播放列表时不能补 `#EXT-X-ENDLIST`，
//     否则播放器会以为直播结束了；
//   - `delete_segments` + `-hls_delete_threshold 3`：比列表多留 3 个，
//     给正在拉旧分片的客户端一点容错；
//   - `-hls_segment_type mpegts`：直播固定用 TS。fMP4 的 init 段
//     在滚动窗口里要额外处理（客户端可能拿到已被删掉的 init），没必要；
//   - `0:v:0?` / `0:a:0?`：直播源的流序号不固定，而且可选（有的源没音轨）。
//     带 `?` 表示「没有就不映射」，否则 ffmpeg 会直接报错退出；
//   - 仅直播时音频默认转 AAC：IPTV 源大多是 MP2，浏览器放不了。
func liveHLSArgs(opts Options, spec Spec, outDir string) []string {
	listSize := spec.LiveListSize
	if listSize < 3 || listSize > 30 {
		listSize = 6
	}
	segSeconds := spec.SegmentSeconds
	if segSeconds < 1 || segSeconds > 10 {
		// 直播用 2 秒分片：分片时长就是起播延迟的下限（4 秒分片不可能 2 秒内起播）
		segSeconds = 2
	}

	args := []string{
		"-hide_banner",
		"-nostdin",
		"-loglevel", "warning",
		"-stats_period", "3",
		"-progress", filepath.Join(outDir, "progress.txt"),
	}
	// 输入侧：源站参数（-rtsp_transport tcp / -timeout / -headers …）
	args = append(args, spec.Video.InputArgs...)
	args = append(args, "-i", spec.Path)
	args = append(args, "-map", "0:v:0?", "-map", "0:a:0?", "-sn", "-dn")

	if spec.Video.Copy || len(spec.Video.CodecArgs) == 0 {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, spec.Video.FilterArgs...)
		args = append(args, spec.Video.CodecArgs...)
	}
	args = append(args, spec.Video.Tag...)

	switch {
	case spec.Audio.Copy:
		args = append(args, "-c:a", "copy")
	case len(spec.Audio.Args) > 0:
		args = append(args, spec.Audio.Args...)
	default:
		args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
	}

	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(segSeconds),
		"-hls_list_size", strconv.Itoa(listSize),
		"-hls_delete_threshold", "3",
		"-hls_flags", "delete_segments+omit_endlist+independent_segments+temp_file",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(outDir, "seg_%05d.ts"),
		// 源站的元数据/章节对播放器没意义
		"-map_metadata", "-1", "-map_chapters", "-1",
	)
	return append(args, filepath.Join(outDir, "index.m3u8"))
}

// segmentExt 返回分片扩展名。
func segmentExt(segFormat string) string {
	if segFormat == "ts" {
		return ".ts"
	}
	return ".m4s"
}

// formatSeconds 用固定小数位格式化秒数，避免本地化小数点（某些 locale 会输出逗号，
// ffmpeg 会把 `-ss 12,5` 解析成两个参数）。
func formatSeconds(v float64) string {
	return strconv.FormatFloat(v, 'f', 3, 64)
}
