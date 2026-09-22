package api

import (
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 直播「转封装 vs 转码」的判据只有一条：浏览器解不解得开源里的视频编码。
// 这条规则错了的代价很直观 —— 要么发一路放不了的分片（黑屏），
// 要么白白把 CPU 烧在林 264 上（本来能直接转封装）。所以用表测试钉住。
func TestLiveVideoModeOf(t *testing.T) {
	ch := func(codec string) *store.TVChannel { return &store.TVChannel{VideoCodec: codec} }
	chrome := []string{"h264", "vp9", "av1"}  // 常见 Chrome：不解 HEVC
	safari := []string{"h264", "hevc", "av1"} // 能解 HEVC 的那类

	cases := []struct {
		name   string
		ch     *store.TVChannel
		codecs []string
		force  string
		want   liveVideoMode
	}{
		{"H.264 源 + Chrome：转封装就够", ch("h264"), chrome, "", liveModeCopy},
		{"H.264 源 + Safari：转封装", ch("h264"), safari, "", liveModeCopy},
		{"HEVC 源 + Chrome：必须转码", ch("hevc"), chrome, "", liveModeTranscode},
		{"HEVC 源 + Safari：能解，转封装", ch("hevc"), safari, "", liveModeCopy},
		{"MPEG-2 源 + Chrome：转码", ch("mpeg2video"), chrome, "", liveModeTranscode},
		{"别名要归一：avc1 == h264", ch("avc1"), []string{"h264"}, "", liveModeCopy},
		{"别名要归一：h265 == hevc", ch("h265"), chrome, "", liveModeTranscode},
		{"大小写不敏感", ch("HEVC"), []string{"H264", "Hevc"}, "", liveModeCopy},
		{"源编码未知（没探测过）：照旧转封装，靠前端重试兜底", ch(""), chrome, "", liveModeCopy},
		{"客户端没报能力（老前端）：转封装", ch("hevc"), nil, "", liveModeCopy},
		{"force=transcode 优先（前端重试）", ch("h264"), chrome, "transcode", liveModeTranscode},
		{"force=copy 优先（用户明确要求别转）", ch("hevc"), chrome, "copy", liveModeCopy},
		{"force 大小写/空格都认", ch(""), chrome, "  TRANSCODE ", liveModeTranscode},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := liveVideoModeOf(c.ch, c.codecs, c.force); got != c.want {
				t.Fatalf("liveVideoModeOf(%q, %v, %q) = %q，期望 %q",
					c.ch.VideoCodec, c.codecs, c.force, got, c.want)
			}
		})
	}
}

// 会话键必须把处理方式带上：转封装与转码是两路不同的 ffmpeg，
// 混用会让能解 HEVC 的浏览器拿到另一路转码流（或反过来拿到放不了的那路）。
func TestLiveStreamKeyIncludesMode(t *testing.T) {
	if liveStreamKey(7, liveModeCopy) == liveStreamKey(7, liveModeTranscode) {
		t.Fatal("同一个频道的 copy / transcode 必须是两个不同的会话键")
	}
	if liveStreamKey(7, liveModeCopy) == liveStreamKey(8, liveModeCopy) {
		t.Fatal("不同频道不能共用一个会话键")
	}
}
