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
//   - `-hls_list_size 0`：列表里保留全部分片，客户端才能在窗口内就地跳。
func hlsArgs(opts Options, spec Spec, outDir string) []string {
	window := opts.WindowSeconds
	if spec.WindowSeconds > 0 {
		window = spec.WindowSeconds
	}

	segFormat := spec.SegmentFormat
	if segFormat != "ts" {
		segFormat = "fmp4"
	}

	args := []string{
		"-hide_banner",
		"-nostdin",
		"-loglevel", "warning",
		"-nostats",
	}

	// 视频段：直出/转封装时不带硬件参数；转码时由 encoder 包给出完整配方。
	args = append(args, spec.Video.InputArgs...)

	if spec.StartSeconds > 0.001 {
		args = append(args, "-ss", formatSeconds(spec.StartSeconds))
	}
	args = append(args, "-i", spec.Path)
	args = append(args, "-map", "0:"+strconv.Itoa(spec.VideoIndex))
	if spec.AudioIndex >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(spec.AudioIndex))
	}
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
		"-hls_time", strconv.Itoa(opts.SegmentSeconds),
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
