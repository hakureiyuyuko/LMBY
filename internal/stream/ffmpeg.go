package stream

import (
	"path/filepath"
	"strconv"
	"strings"
)

// hlsArgs 构造 HLS 转封装命令。
//
// 逐段的用意：
//   - `-nostdin`：ffmpeg 会抢 stdin，作为后台进程跑时会导致它在收到 EOF 后退出；
//   - `-ss` 放在 `-i` **之前**：输入侧快速跳转（对 `-c copy` 是唯一可行的做法，
//     输出侧 `-ss` 需要先解码到目标位置）。代价是只能落到关键帧上，
//     这与 Jellyfin 的行为一致；
//   - `-map 0:<绝对序号>`：不用「第几个视频流」这种相对序号，避免决策层
//     与实际流序对不上；
//   - `-tag:v hvc1`：HEVC 装进 fMP4 必须打 hvc1 标签，否则 Safari 与部分
//     Chromium 直接拒绝播放（画面黑着但音频正常，极难排查）；
//   - `-sn -dn`：字幕与数据流不进分片（字幕单独走 WebVTT 旁路）；
//   - `-t 窗口`：只生成这一段，防止 `-c copy` 把整部电影瞬间拷完。
func hlsArgs(opts Options, spec Spec, outDir string) []string {
	opts = opts.Normalize()
	window := spec.WindowSeconds
	if window <= 0 {
		window = opts.WindowSeconds
	}

	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning", "-nostats",
	}
	if spec.StartSeconds > 0 {
		args = append(args, "-ss", formatSeconds(spec.StartSeconds))
	}
	args = append(args, "-i", spec.Path)

	vIdx := spec.VideoIndex
	if vIdx < 0 {
		vIdx = 0
	}
	args = append(args, "-map", "0:"+strconv.Itoa(vIdx))
	if spec.AudioIndex >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(spec.AudioIndex))
	}
	args = append(args, "-sn", "-dn", "-c:v", "copy")

	if strings.EqualFold(spec.VideoCodec, "hevc") || strings.EqualFold(spec.VideoCodec, "h265") {
		args = append(args, "-tag:v", "hvc1")
	}

	if spec.AudioIndex >= 0 {
		if spec.AudioCopy {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "192k")
			if spec.Downmix {
				args = append(args, "-ac", "2")
			}
		}
	}

	args = append(args, "-t", strconv.Itoa(window))

	segFormat := spec.SegmentFormat
	if segFormat != "ts" {
		segFormat = "fmp4"
	}

	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(opts.SegmentSeconds),
		// event 而不是 vod：播放列表只追加、不重写，播放器可以在生成的这段里自由拖动。
		// 窗口结束时会自动补 EXT-X-ENDLIST，播放器据此知道这一段到底了。
		"-hls_playlist_type", "event",
		"-hls_segment_type", segFormat,
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", filepath.Join(outDir, "seg_%05d"+segmentExt(segFormat)),
	)
	if segFormat == "fmp4" {
		args = append(args, "-hls_fmp4_init_filename", "init.mp4")
	}
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
