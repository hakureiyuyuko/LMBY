// Package playback 是播放决策引擎：给定「文件里有什么流」与「播放端支持什么」，
// 决定每条流该怎么送到浏览器面前。
//
// 这里是纯逻辑，不碰 HTTP、不碰数据库、不起进程 —— 决策错了是「用户看到
// 转码或黑屏」，很难从外部观察出来，所以必须能用离线单测钉死（见 decide_test.go）。
package playback

import (
	"path/filepath"
	"sort"
	"strings"
)

// 播放方式。与 Jellyfin 的 PlayMethod 语义对齐，前后端共用这三个取值。
const (
	// ModeDirect 原样直出：文件本体 + HTTP Range，浏览器自己解。
	ModeDirect = "direct"
	// ModeRemux 转封装：视频流原样复制，只换容器（必要时音频转 AAC）。
	ModeRemux = "remux"
	// ModeTranscode 转码：视频流需要重新编码。M4 的能力，M3 只做决策不做执行。
	ModeTranscode = "transcode"
)

// 流级动作。
const (
	// ActionCopy 原样复制。
	ActionCopy = "copy"
	// ActionConvert 重新编码（音频转 AAC / 字幕转 WebVTT）。
	ActionConvert = "convert"
	// ActionTranscode 需要重新编码视频（M4 才具备的能力）。
	ActionTranscode = "transcode"
	// ActionBurn 烧录进画面（图形字幕只能这么走，M4）。
	ActionBurn = "burn"
	// ActionDrop 丢弃这条流。
	ActionDrop = "drop"
	// ActionNone 没有这条流 / 不送。
	ActionNone = "none"
)

// 容器归一化后的取值。
const (
	ContainerMP4   = "mp4"
	ContainerWebM  = "webm"
	ContainerMKV   = "mkv"
	ContainerTS    = "ts"
	ContainerAVI   = "avi"
	ContainerOther = "other"
)

// ContainerKind 把 ffprobe 的 format_name 与文件扩展名归一化成容器族。
//
// 为什么必须两个都要：ffprobe 的 matroska 解复用器对 .mkv 与 .webm 报的是**同一个**
// format_name（"matroska,webm"），光看它分不出来。而这两个的浏览器支持完全不同
// （webm 可以直出，mkv 必须转封装），所以拿扩展名补上这一刀。
func ContainerKind(formatName, path string) string {
	s := strings.ToLower(strings.TrimSpace(formatName))
	ext := strings.ToLower(filepath.Ext(path))

	switch {
	case strings.Contains(s, "matroska") || strings.Contains(s, "webm"):
		if ext == ".webm" {
			return ContainerWebM
		}
		return ContainerMKV
	case strings.Contains(s, "mpegts") || ext == ".ts":
		return ContainerTS
	case strings.Contains(s, "mp4") || strings.Contains(s, "mov") || strings.Contains(s, "m4a") ||
		strings.Contains(s, "3gp") || ext == ".mp4" || ext == ".m4v" || ext == ".mov":
		return ContainerMP4
	case strings.Contains(s, "avi"):
		return ContainerAVI
	}
	return ContainerOther
}

// Profile 描述播放端（浏览器）的能力。
//
// 「服务端 DeviceProfile 表 + 客户端能力上报」两层：默认给一份保守的内置档
// （三端浏览器交集），前端再用 MediaSource.isTypeSupported / canPlayType
// 实测一遍上报覆盖。宁可少直出、多转封装 —— 猜错的代价是黑屏或无声。
type Profile struct {
	Name string `json:"name"`

	// Containers 是能直接播放的容器（ContainerKind 的取值）。
	Containers []string `json:"containers"`
	// VideoCodecs / AudioCodecs 是能解码的编码名（ffprobe 的 codec_name）。
	VideoCodecs []string `json:"videoCodecs"`
	AudioCodecs []string `json:"audioCodecs"`
	// SubtitleFormats 是能渲染的字幕格式（浏览器实际只有 WebVTT）。
	SubtitleFormats []string `json:"subtitleFormats"`

	MaxWidth         int `json:"maxWidth"`
	MaxHeight        int `json:"maxHeight"`
	MaxBitDepth      int `json:"maxBitDepth"`
	MaxAudioChannels int `json:"maxAudioChannels"`

	// SupportsHLS 表示能用 HLS 播放（Safari 原生、其余走 hls.js）。
	SupportsHLS bool `json:"supportsHls"`
	// SupportsFMP4 表示 HLS 分片能用 fMP4（Safari 与 hls.js 都行；
	// 只有 MPEG-TS 分片的老客户端才需要 TS）。
	SupportsFMP4 bool `json:"supportsFmp4"`
	// SupportsTS 表示 HLS 分片能用 MPEG-TS（hls.js 与 Safari 都支持）。
	SupportsTS bool `json:"supportsTs"`
}

// BrowserProfile 是保守的内置默认档：Chrome / Firefox / Safari / iOS 的交集。
//
// 刻意不写 hevc、av1、ac3 —— 它们在部分平台上能放，但「部分平台」正是
// 播放问题最难查的来源。前端可以实测后上报更宽的能力（见 api 的 capabilities 接口）。
func BrowserProfile() Profile {
	return Profile{
		Name:             "browser",
		Containers:       []string{ContainerMP4, ContainerWebM},
		VideoCodecs:      []string{"h264", "vp8", "vp9"},
		AudioCodecs:      []string{"aac", "mp3", "opus", "vorbis", "flac"},
		SubtitleFormats:  []string{"webvtt"},
		MaxWidth:         3840,
		MaxHeight:        2160,
		MaxBitDepth:      8,
		MaxAudioChannels: 2,
		SupportsHLS:      true,
		SupportsFMP4:     true,
		SupportsTS:       true,
	}
}

// SafariProfile 是 Safari / iOS 的档位：容器能力更强（hevc、多声道、TS HLS），
// 但对 mkv 与 fMP4 之外的封装更挑剔。
func SafariProfile() Profile {
	p := BrowserProfile()
	p.Name = "safari"
	p.VideoCodecs = []string{"h264", "hevc", "vp8", "vp9"}
	p.AudioCodecs = []string{"aac", "mp3", "opus", "flac", "ac3", "eac3"}
	p.MaxBitDepth = 10
	p.MaxAudioChannels = 6
	return p
}

// 各列表的长度上限：能力上报来自客户端，不能让它无上限地灌进内存，
// 也不允许出现会渗进 ffmpeg 参数的怪字符串。
const (
	maxListEntries  = 32
	maxEntryRunes   = 32
	maxMaxDimension = 16384
)

// Normalize 清洗能力描述：小写、去空、去重、限长。
//
// 不做「猜你要什么」的纠错：非法值直接丢，最后空列表表示「什么都不支持」，
// 决策引擎会走转封装而不是直出。
func (p Profile) Normalize() Profile {
	out := p
	out.Name = trimEntry(p.Name, 64)
	out.Containers = cleanList(p.Containers, []string{ContainerMP4, ContainerWebM, ContainerMKV, ContainerTS, ContainerAVI, ContainerOther})
	out.VideoCodecs = cleanList(p.VideoCodecs, nil)
	out.AudioCodecs = cleanList(p.AudioCodecs, nil)
	out.SubtitleFormats = cleanList(p.SubtitleFormats, nil)

	out.MaxWidth = clampDim(p.MaxWidth, 1280)
	out.MaxHeight = clampDim(p.MaxHeight, 720)
	// 8 / 10 位是现实里存在的两档；其余一律当 8 位（更保守）。
	if out.MaxBitDepth != 10 {
		out.MaxBitDepth = 8
	}
	if out.MaxAudioChannels < 1 || out.MaxAudioChannels > 8 {
		out.MaxAudioChannels = 2
	}
	if out.Name == "" {
		out.Name = "custom"
	}
	return out
}

// clampDim 修正分辨率上限。给 0（客户端没报）时用 fallback。
func clampDim(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	if v > maxMaxDimension {
		return maxMaxDimension
	}
	return v
}

// cleanList 清洗字符串列表；allow 非空时只保留列表内的条目。
func cleanList(in []string, allow []string) []string {
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[a] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if !validEntry(v, maxEntryRunes) || seen[v] {
			continue
		}
		if len(allowed) > 0 && !allowed[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= maxListEntries {
			break
		}
	}
	sort.Strings(out)
	return out
}

// validEntry 检查一个能力名是否可以采信。
//
// 刻意**不**做「剔掉非法字符后继续用」：那样 "h264; rm -rf /" 会变成
// "h264rm-rf/" —— 既不是 h264，也不是任何真实编码名，不如直接丢弃。
func validEntry(v string, max int) bool {
	if v == "" || len(v) > max {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// trimEntry 只留给展示用的名字（Profile.Name）：
// 去掉非法字符而不是整体丢弃，因为名字只是标签，不影响任何决策。
func trimEntry(v string, max int) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= max {
			break
		}
	}
	return b.String()
}

// SupportsContainer 报告容器能否直出。
func (p Profile) SupportsContainer(kind string) bool {
	return contains(p.Containers, kind)
}

// SupportsVideo 报告这条视频流能否原样解码。
func (p Profile) SupportsVideo(codec string, width, height, bitDepth int) bool {
	if !contains(p.VideoCodecs, strings.ToLower(codec)) {
		return false
	}
	if width > 0 && p.MaxWidth > 0 && width > p.MaxWidth {
		return false
	}
	if height > 0 && p.MaxHeight > 0 && height > p.MaxHeight {
		return false
	}
	// bd 为 0 时按 8 位算（探测不出色深的老文件基本都是 8 位）
	bd := bitDepth
	if bd <= 0 {
		bd = 8
	}
	return bd <= p.MaxBitDepth
}

// SupportsAudio 报告这条音频流能否原样解码。
func (p Profile) SupportsAudio(codec string, channels int) bool {
	if !contains(p.AudioCodecs, strings.ToLower(codec)) {
		return false
	}
	if channels > 0 && p.MaxAudioChannels > 0 && channels > p.MaxAudioChannels {
		return false
	}
	return true
}

// SupportsSubtitleFormat 报告字幕格式能否直接交给浏览器渲染。
func (p Profile) SupportsSubtitleFormat(codec string) bool {
	return contains(p.SubtitleFormats, strings.ToLower(codec))
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
