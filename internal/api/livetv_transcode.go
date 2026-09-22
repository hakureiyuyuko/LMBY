package api

// 直播的「转封装 vs 转码」决策与转码参数。
//
// 背景：直播一直是**转封装**（视频 `-c copy`），因为 IPTV 源绝大多数是 H.264 ——
// 浏览器直接就能播，服务端几乎不花 CPU。但源里也有 H.265/HEVC、MPEG-2 这类
// 浏览器根本解不开的编码；那时再转封装，就是发一路客户端放不了的分片（黑屏/报错）。
//
// 所以起播前判一下：
//
//   - 源编码已知（探测时记下的 `tv_channels.video_codec`）+ 客户端报了能力 → 按能力判；
//   - 源编码未知（没探测过）或客户端没报能力（老前端）→ **仍然转封装**，
//     真放不出来时前端会带 `force=transcode` 重试一次（未知编码的兜底路径）；
//   - `force` 优先（前端重试 / 手工指定）。
//
// 转码参数**全部交给 internal/encoder 拼**（与点播同一条路）：硬件后端、码率模式、
// 关键帧对齐（1 秒分片必须对齐关键帧，否则分片边界不干净）都只在那一个地方负责，
// api 层不手写 ffmpeg 参数。
//
// 两个刻意保守的地方（不是疏漏）：
//   - 位深按 8bit 处理：探测只记编码与高度，没记位深；IPTV 源几乎都是 8bit。
//     万一遇到 10bit 源，转码链可能失败 —— 那时前端会退回转封装并提示错误。
//   - 不缩放到目标以上的分辨率：只有知道源高度、且高于转码上限时才往下缩。

import (
	"context"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/encoder"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/stream"
)

// liveVideoMode 是这一路直播的视频处理方式。
type liveVideoMode string

const (
	// liveModeCopy：转封装 —— 视频原样复制（默认，也是绝大多数情况）。
	liveModeCopy liveVideoMode = "copy"
	// liveModeTranscode：转码成 H.264（浏览器解不开源编码时）。
	liveModeTranscode liveVideoMode = "transcode"
)

// liveVideoModeOf 决定这一路直播转封装还是转码。
//
// 纯函数（不碰 store/encoder），便于把「什么情况该转码」这条规则用单测钉住。
func liveVideoModeOf(ch *store.TVChannel, clientCodecs []string, force string) liveVideoMode {
	switch strings.ToLower(strings.TrimSpace(force)) {
	case string(liveModeTranscode):
		return liveModeTranscode
	case string(liveModeCopy):
		// 前端明确说「别再转了」（例如用户手动切回原画）——尊重它
		return liveModeCopy
	}

	codec := normalizeVideoCodec(ch.VideoCodec)
	if codec == "" || len(clientCodecs) == 0 {
		// 不知道源编码，或客户端没报能力：按老行为转封装。
		// 真放不出来，前端会带 force=transcode 再来一次。
		return liveModeCopy
	}
	for _, c := range clientCodecs {
		if normalizeVideoCodec(c) == codec {
			return liveModeCopy
		}
	}
	return liveModeTranscode
}

// normalizeVideoCodec 把浏览器报的 MIME/别名归一到 ffprobe 的 codec_name，
// 免得同一种编码写成两种名字就判错。
func normalizeVideoCodec(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "avc", "avc1", "h264", "x264", "h.264":
		return "h264"
	case "h265", "hevc", "hvc1", "hev1", "h.265":
		return "hevc"
	case "mpeg2", "mpeg2video", "mpeg-2":
		return "mpeg2video"
	case "av1", "av01":
		return "av1"
	case "vp9", "vp09":
		return "vp9"
	case "vp8":
		return "vp8"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// liveVideoEncode 给出这一路直播的视频参数。
//
// 转码目标是 H.264：浏览器的普遍底线就是它（HEVC 只有部分 Safari/Edge 能解）。
// 具体的编解码器/滤镜链/关键帧对齐由 encoder 包按本机能力拼 —— 与点播同源。
func (s *Server) liveVideoEncode(ctx context.Context, ch *store.TVChannel, mode liveVideoMode) stream.VideoEncode {
	input := liveInputArgs(*ch)
	if mode != liveModeTranscode {
		return stream.VideoEncode{Copy: true, InputArgs: input}
	}

	caps, err := s.encoders.Get(ctx)
	if err != nil {
		// 拿不到能力表就老老实实转封装（宁可客户端放不了，也别起一路拼错参数的 ffmpeg）
		s.log.Warn("直播转码：读不到编码能力，退回转封装", "channel", ch.ID, "err", err.Error())
		return stream.VideoEncode{Copy: true, InputArgs: input}
	}
	backend := s.encoders.Preferred(caps)

	va := backend.VideoArgs(encoder.ArgsRequest{
		SourceCodec:  ch.VideoCodec,
		TargetCodec:  "h264",
		Height:       ch.VideoHeight,
		BitDepth:     8, // 探测不记位深；IPTV 源几乎都是 8bit
		TargetHeight: s.cfg.Playback.TranscodeMaxHeight,
		Quality:      encoder.QualityByName("medium"),
		// 分片是 1 秒的，关键帧必须跟着 1 秒对齐，否则每个分片都要等下一个关键帧
		KeyframeSeconds: liveSegmentSeconds,
	})
	return stream.VideoEncode{
		// 直播源参数（RTSP transport / 请求头）与硬件解码参数都在 -i 之前
		InputArgs:  append(input, va.InputArgs...),
		FilterArgs: va.FilterArgs,
		CodecArgs:  va.CodecArgs,
		Tag:        va.Tag,
	}
}
