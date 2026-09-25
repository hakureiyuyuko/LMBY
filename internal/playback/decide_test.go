package playback

import (
	"strings"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/probe"
)

// 单测的形状：拿真实库里会遇到的组合（mp4 直出、mkv 转封装、DTS 音轨、
// 10bit HEVC、PGS 字幕……）钉住决策结果。
//
// 决策错了从外面看只是「黑屏/转圈」，所以这里必须把「为什么」也钉住：
// 每条流的 Action 与 Mode，以及理由链里出现了关键信息。

func movie(path, container string, v []probe.VideoStream, a []probe.AudioStream, s []probe.SubtitleStream) File {
	return File{
		ID:            1,
		Path:          path,
		Container:     container,
		DurationTicks: 7200 * 10_000_000,
		Video:         v,
		Audio:         a,
		Subtitle:      s,
	}
}

func h264(w, h, bd int, def bool) probe.VideoStream {
	return probe.VideoStream{Index: 0, Codec: "h264", Width: w, Height: h, BitDepth: bd, Default: def}
}

func hevc10(w, h int) probe.VideoStream {
	return probe.VideoStream{Index: 0, Codec: "hevc", Width: w, Height: h, BitDepth: 10, Default: true}
}

func audio(idx int, codec string, ch int, def bool) probe.AudioStream {
	return probe.AudioStream{Index: idx, Codec: codec, Channels: ch, Default: def, Language: "jpn"}
}

func textSub(idx int, codec string, def, forced bool) probe.SubtitleStream {
	return probe.SubtitleStream{Index: idx, Codec: codec, Default: def, Forced: forced, Language: "chi"}
}

func imageSub(idx int) probe.SubtitleStream {
	return probe.SubtitleStream{Index: idx, Codec: "hdmv_pgs_subtitle", IsImage: true, Default: true}
}

func TestDecide(t *testing.T) {
	mp4h264 := movie("/m/a.mp4", "mov,mp4,m4a,3gp,3g2,mj2",
		[]probe.VideoStream{h264(1920, 1080, 8, true)}, []probe.AudioStream{audio(1, "aac", 2, true)}, nil)
	mkvH264AAC := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 8, true)}, []probe.AudioStream{audio(1, "aac", 2, true)}, nil)
	mkvH264DTS := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 8, true)}, []probe.AudioStream{audio(1, "dts", 6, true)}, nil)
	mkvHevc10 := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{hevc10(3840, 2160)}, []probe.AudioStream{audio(1, "eac3", 6, true)}, nil)
	webmVp9 := movie("/m/a.webm", "matroska,webm",
		[]probe.VideoStream{{Index: 0, Codec: "vp9", Width: 1280, Height: 720, BitDepth: 8, Default: true}},
		[]probe.AudioStream{audio(1, "opus", 2, true)}, nil)
	mkvVp9 := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{{Index: 0, Codec: "vp9", Width: 1280, Height: 720, BitDepth: 8, Default: true}},
		[]probe.AudioStream{audio(1, "opus", 2, true)}, nil)
	mp4NoAudio := movie("/m/a.mp4", "mov,mp4,m4a,3gp,3g2,mj2",
		[]probe.VideoStream{h264(1280, 720, 8, true)}, nil, nil)

	tests := []struct {
		name         string
		req          Request
		mode         string
		playable     bool
		segFormat    string
		videoAction  string
		audioAction  string
		subAction    string
		downmix      bool
		wantContains string // 理由链里必须出现
	}{
		{
			name: "mp4 + h264/aac 直出",
			req:  Request{Profile: BrowserProfile(), Files: []File{mp4h264}},
			mode: ModeDirect, playable: true,
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "直接播放原文件",
		},
		{
			name: "mkv + h264/aac 转封装（浏览器不认 mkv 容器）",
			req:  Request{Profile: BrowserProfile(), Files: []File{mkvH264AAC}},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "转封装成 fmp4",
		},
		{
			name: "mkv + DTS 6 声道：音频转 AAC 立体声，视频仍复制",
			req:  Request{Profile: BrowserProfile(), Files: []File{mkvH264DTS}},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionConvert, subAction: ActionNone, downmix: true,
			wantContains: "转成 AAC 立体声",
		},
		{
			name: "mkv + 10bit HEVC：浏览器档需要转码，但机器不能转 → 放不了",
			req:  Request{Profile: BrowserProfile(), Files: []File{mkvHevc10}},
			mode: ModeTranscode, playable: false,
			videoAction: ActionTranscode, audioAction: ActionConvert, subAction: ActionNone,
			wantContains: "没有可用的编码器",
		},
		{
			name: "mkv + 10bit HEVC：Safari 档可以转封装直出",
			req:  Request{Profile: SafariProfile(), Files: []File{mkvHevc10}},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "原样复制",
		},
		{
			name: "webm + vp9/opus 直出（扩展名 .webm 才算 webm）",
			req:  Request{Profile: BrowserProfile(), Files: []File{webmVp9}},
			mode: ModeDirect, playable: true,
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "直接播放原文件",
		},
		{
			name: "mkv + vp9/opus 转封装",
			req:  Request{Profile: BrowserProfile(), Files: []File{mkvVp9}},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "转封装成 fmp4",
		},
		{
			name: "只能吃 TS 分片的客户端 + vp9：放不了（TS 装不下 vp9）",
			req: Request{Profile: Profile{
				Containers: []string{ContainerMP4}, VideoCodecs: []string{"vp9"},
				AudioCodecs: []string{"opus"}, SupportsHLS: true, SupportsTS: true,
			}, Files: []File{mkvVp9}},
			mode: ModeTranscode, playable: false,
			videoAction: ActionTranscode, audioAction: ActionConvert, subAction: ActionNone,
			wantContains: "无法放进",
		},
		{
			name: "mp4 + 手动指定 SRT 字幕：字幕转 WebVTT，视频仍直出",
			req: Request{Profile: BrowserProfile(),
				Files: []File{movie("/m/a.mp4", "mov,mp4,m4a,3gp,3g2,mj2",
					[]probe.VideoStream{h264(1920, 1080, 8, true)},
					[]probe.AudioStream{audio(1, "aac", 2, true)},
					[]probe.SubtitleStream{textSub(2, "subrip", true, false)})},
				SubtitleIndex: 2},
			mode: ModeDirect, playable: true,
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionConvert,
			wantContains: "转成 WebVTT",
		},
		{
			name: "强制字幕轨自动选中（foreign 片段）",
			req: Request{Profile: BrowserProfile(),
				Files: []File{movie("/m/a.mkv", "matroska,webm",
					[]probe.VideoStream{h264(1920, 1080, 8, true)},
					[]probe.AudioStream{audio(1, "aac", 2, true)},
					[]probe.SubtitleStream{textSub(2, "ass", true, true)})}},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionConvert,
			wantContains: "libass",
		},
		{
			name: "PGS 图形字幕：不烧录，明确告知本次不显示",
			req: Request{Profile: BrowserProfile(),
				Files: []File{movie("/m/a.mkv", "matroska,webm",
					[]probe.VideoStream{h264(1920, 1080, 8, true)},
					[]probe.AudioStream{audio(1, "aac", 2, true)},
					[]probe.SubtitleStream{imageSub(2)})},
				SubtitleIndex: 2},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionDrop,
			wantContains: "烧进画面",
		},
		{
			name: "显式关闭字幕",
			req: Request{Profile: BrowserProfile(),
				Files: []File{movie("/m/a.mkv", "matroska,webm",
					[]probe.VideoStream{h264(1920, 1080, 8, true)},
					[]probe.AudioStream{audio(1, "aac", 2, true)},
					[]probe.SubtitleStream{textSub(2, "ass", true, true)})},
				SubtitleIndex: -1},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "关闭字幕",
		},
		{
			name: "没有音轨也能直出",
			req:  Request{Profile: BrowserProfile(), Files: []File{mp4NoAudio}},
			mode: ModeDirect, playable: true,
			videoAction: ActionCopy, audioAction: ActionNone, subAction: ActionNone,
			wantContains: "没有音轨",
		},
		{
			// 同一部片子同时有 4K 10bit HEVC 与 1080p H264：先出现的（大的）需要转码，
			// 但旁边那个能直出 —— 必须挑能直出的，而不是“第一个容器能放的”
			// （两者容器都是 mp4，这是实测踩到过的坑）。
			name: "多版本：挑能直出的那个，而不是体积大的那个",
			req:  Request{Profile: BrowserProfile(), Files: []File{mkvHevc10, mp4h264}},
			mode: ModeDirect, playable: true,
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "按「能直出 > 转封装 > 转码」",
		},
		{
			name: "多版本：都不支持时挑最好的（转封装优于转码）",
			req:  Request{Profile: BrowserProfile(), Files: []File{mkvHevc10, mkvH264AAC}},
			mode: ModeRemux, playable: true, segFormat: "fmp4",
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "选了 .mkv",
		},
		{
			name:         "没有文件时给明确理由，而不是静默失败",
			req:          Request{Profile: BrowserProfile()},
			mode:         "",
			playable:     false,
			videoAction:  ActionNone,
			audioAction:  ActionNone,
			subAction:    ActionNone,
			wantContains: "没有可播放的视频文件",
		},
		{
			name: "探测不出视频流的文件被跳过",
			req: Request{Profile: BrowserProfile(), Files: []File{
				movie("/m/broken.mkv", "matroska,webm", nil, []probe.AudioStream{audio(1, "aac", 2, true)}, nil),
				mp4h264,
			}},
			mode: ModeDirect, playable: true,
			videoAction: ActionCopy, audioAction: ActionCopy, subAction: ActionNone,
			wantContains: "直接播放原文件",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.req)
			if got.Mode != tc.mode {
				t.Errorf("mode = %q，期望 %q", got.Mode, tc.mode)
			}
			if got.Playable != tc.playable {
				t.Errorf("playable = %v，期望 %v", got.Playable, tc.playable)
			}
			if tc.segFormat != "" && got.SegmentFormat != tc.segFormat {
				t.Errorf("segmentFormat = %q，期望 %q", got.SegmentFormat, tc.segFormat)
			}
			if got.Video.Action != tc.videoAction {
				t.Errorf("视频动作 = %q，期望 %q（理由：%s）", got.Video.Action, tc.videoAction, got.Video.Reason)
			}
			if got.Audio.Action != tc.audioAction {
				t.Errorf("音频动作 = %q，期望 %q（理由：%s）", got.Audio.Action, tc.audioAction, got.Audio.Reason)
			}
			if got.Subtitle.Action != tc.subAction {
				t.Errorf("字幕动作 = %q，期望 %q（理由：%s）", got.Subtitle.Action, tc.subAction, got.Subtitle.Reason)
			}
			if got.Audio.Downmix != tc.downmix {
				t.Errorf("downmix = %v，期望 %v", got.Audio.Downmix, tc.downmix)
			}
			if tc.wantContains != "" {
				all := strings.Join(got.Reasons, " | ")
				if !strings.Contains(all, tc.wantContains) {
					t.Errorf("理由链里没有 %q：\n%s", tc.wantContains, all)
				}
			}
			if len(got.Reasons) == 0 {
				t.Error("理由链为空：界面就没法解释「为什么这么播」了")
			}
		})
	}
}

func TestDecidePreservesStartPosition(t *testing.T) {
	req := Request{
		Profile: BrowserProfile(),
		Files: []File{movie("/m/a.mp4", "mov,mp4,m4a,3gp,3g2,mj2",
			[]probe.VideoStream{h264(1920, 1080, 8, true)},
			[]probe.AudioStream{audio(1, "aac", 2, true)}, nil)},
		StartTicks: 1234 * 10_000_000,
	}
	got := Decide(req)
	if got.StartTicks != req.StartTicks {
		t.Errorf("startTicks 被改动了：%d", got.StartTicks)
	}
}

func TestPlanAudioExplicitIndex(t *testing.T) {
	f := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 8, true)},
		[]probe.AudioStream{audio(1, "aac", 2, true), audio(2, "flac", 2, false)}, nil)
	got := Decide(Request{Profile: BrowserProfile(), Files: []File{f}, AudioIndex: 2})
	if got.Audio.Index != 2 {
		t.Fatalf("没有按用户指定的音轨挑：#%d", got.Audio.Index)
	}
	if got.Audio.Action != ActionCopy {
		t.Errorf("flac 立体声应当能原样复制，实际 %q", got.Audio.Action)
	}

	got = Decide(Request{Profile: BrowserProfile(), Files: []File{f}, AudioIndex: 99})
	if got.Audio.Index != 1 {
		t.Errorf("指定不存在的音轨应当回退到默认轨，实际 #%d", got.Audio.Index)
	}
}

func TestSubtitleDelivery(t *testing.T) {
	// ASS/SSA 交给前端 libass（保留定位/动画/样式）：转成 WebVTT 这些全丢。
	ass := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 8, true)},
		[]probe.AudioStream{audio(1, "aac", 2, true)},
		[]probe.SubtitleStream{textSub(2, "ass", true, false)})
	got := Decide(Request{Profile: BrowserProfile(), Files: []File{ass}, SubtitleIndex: 2})
	if got.Subtitle.DeliverAs != DeliverLibass {
		t.Errorf("ASS 应当交给 libass，得到 %q（理由：%s）", got.Subtitle.DeliverAs, got.Subtitle.Reason)
	}

	// SRT 这类纯对白字幕转 WebVTT 更轻（浏览器原生轨道，起播快）。
	srt := movie("/m/b.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 8, true)},
		[]probe.AudioStream{audio(1, "aac", 2, true)},
		[]probe.SubtitleStream{textSub(3, "subrip", true, false)})
	got = Decide(Request{Profile: BrowserProfile(), Files: []File{srt}, SubtitleIndex: 3})
	if got.Subtitle.DeliverAs != DeliverWebVTT {
		t.Errorf("SRT 应当转 WebVTT，得到 %q", got.Subtitle.DeliverAs)
	}

	// SSA 与 ASS 同样处理（同一套语法）。
	ssa := movie("/m/c.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 8, true)},
		[]probe.AudioStream{audio(1, "aac", 2, true)},
		[]probe.SubtitleStream{textSub(4, "ssa", true, false)})
	got = Decide(Request{Profile: BrowserProfile(), Files: []File{ssa}, SubtitleIndex: 4})
	if got.Subtitle.DeliverAs != DeliverLibass {
		t.Errorf("SSA 应当交给 libass，得到 %q", got.Subtitle.DeliverAs)
	}
}

// TestSubtitleAutoSelect 盖「自动」怎么选字幕。
//
// 回归点：以前要求 Default && Forced，于是普通内封字幕（Default=true / forced=false）
// 永远选不出来 —— 用户看到的是「字幕明明有却什么都不显示」。
func TestSubtitleAutoSelect(t *testing.T) {
	chiDefault := probe.SubtitleStream{Index: 2, Codec: "subrip", Default: true, Language: "chi"}
	engPlain := probe.SubtitleStream{Index: 3, Codec: "subrip", Language: "eng"}
	chiForced := probe.SubtitleStream{Index: 4, Codec: "subrip", Default: true, Forced: true, Language: "chi"}
	jpnDefault := probe.SubtitleStream{Index: 5, Codec: "ass", Default: true, Language: "jpn"}

	cases := []struct {
		name  string
		subs  []probe.SubtitleStream
		want  int
		langs []string
		pick  int
		how   string
	}{
		{
			name: "回归：Default 但不 forced 的字幕轨，自动也要挂上",
			subs: []probe.SubtitleStream{chiDefault, engPlain},
			pick: 2, how: "default",
		},
		{
			name: "语言偏好命中",
			subs: []probe.SubtitleStream{chiDefault, engPlain}, langs: []string{"zh-CN"},
			pick: 2, how: "language",
		},
		{
			name: "语言偏好压过默认轨",
			subs: []probe.SubtitleStream{chiDefault, engPlain}, langs: []string{"en-US"},
			pick: 3, how: "language",
		},
		{
			name: "偏好没命中就回落默认轨",
			subs: []probe.SubtitleStream{jpnDefault, engPlain}, langs: []string{"ko"},
			pick: 5, how: "default",
		},
		{
			name: "同一语言有强制轨与完整轨时优先完整轨",
			subs: []probe.SubtitleStream{chiForced, chiDefault}, langs: []string{"zh"},
			pick: 2, how: "language",
		},
		{
			name: "没有默认轨也没有命中 → 不挂字幕",
			subs: []probe.SubtitleStream{engPlain},
			pick: -1,
		},
		{
			name: "显式指定优先于语言偏好",
			subs: []probe.SubtitleStream{chiDefault, engPlain}, want: 3, langs: []string{"zh"},
			pick: 3, how: "explicit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := movie("/m/a.mkv", "matroska,webm",
				[]probe.VideoStream{h264(1920, 1080, 8, true)},
				[]probe.AudioStream{audio(1, "aac", 2, true)}, tc.subs)
			got, how, ok := pickSubtitle(&f, tc.want, tc.langs)
			idx := -1
			if ok {
				idx = got.Index
			}
			if idx != tc.pick {
				t.Fatalf("选中 #%d，期望 #%d", idx, tc.pick)
			}
			if ok && how != tc.how {
				t.Errorf("选中方式 = %q，期望 %q", how, tc.how)
			}
		})
	}
}

// TestSubtitleAutoSelectReason 盖「为什么这么播」里的自动选择说明。
func TestSubtitleAutoSelectReason(t *testing.T) {
	f := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 8, true)},
		[]probe.AudioStream{audio(1, "aac", 2, true)},
		[]probe.SubtitleStream{{Index: 2, Codec: "subrip", Default: true, Language: "chi"}})

	hit := Decide(Request{Profile: BrowserProfile(), Files: []File{f}, SubtitleLanguages: []string{"zh-CN"}})
	if !strings.Contains(hit.Subtitle.Reason, "命中语言偏好") {
		t.Errorf("语言命中时理由应说明原因，得到 %q", hit.Subtitle.Reason)
	}
	miss := Decide(Request{Profile: BrowserProfile(), Files: []File{f}, SubtitleLanguages: []string{"de"}})
	if !strings.Contains(miss.Subtitle.Reason, "文件默认字幕轨") {
		t.Errorf("回落默认轨时理由应说明原因，得到 %q", miss.Subtitle.Reason)
	}
	if hit.Subtitle.Index != 2 || miss.Subtitle.Index != 2 {
		t.Errorf("两种情况都应选 #2，得到 %d / %d", hit.Subtitle.Index, miss.Subtitle.Index)
	}
}
