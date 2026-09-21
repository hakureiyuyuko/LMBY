package playback

import (
	"strings"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/probe"
)

// 这一组钉住「图形字幕烧录」的**决策层**。
//
// 执行层（ffmpeg 滤镜图）在 internal/encoder 的单测里；这里只管：
// 什么时候判成要烧、烧录会不会把片子拉进转码、编不了时理由是否如实。

// burnable 是一个「本来能直出」的 mp4：视频 h264 8bit、音频 aac 立体声。
func burnable() File {
	return movie("/m/a.mp4", "mov,mp4,m4a,3gp,3g2,mj2",
		[]probe.VideoStream{h264(1920, 1080, 8, true)},
		[]probe.AudioStream{audio(1, "aac", 2, true)},
		[]probe.SubtitleStream{imageSub(2)})
}

func TestBurnGraphicalSubtitle(t *testing.T) {
	f := burnable()
	base := Request{Profile: BrowserProfile(), Files: []File{f}, SubtitleIndex: 2}

	// 没选烧录：图形字幕如实标成「本次不显示」，不能假装有字幕。
	got := Decide(base)
	if got.Subtitle.Action != ActionDrop {
		t.Fatalf("没选烧录时图形字幕应当 drop，实际 %q（理由：%s）", got.Subtitle.Action, got.Subtitle.Reason)
	}
	if !strings.Contains(got.Subtitle.Reason, "烧进画面") {
		t.Errorf("理由里要说清楚怎么才能看到它：%s", got.Subtitle.Reason)
	}
	if got.Mode != ModeDirect {
		t.Errorf("不烧字幕的话这个文件本来能直出，实际 %q", got.Mode)
	}

	// 选了烧录：**本来能直出也必须转码** —— 位图只能烧进像素。
	req := base
	req.Machine = vaapiMachine()
	req.BurnSubtitle = true
	got = Decide(req)
	if got.Subtitle.Action != ActionBurn {
		t.Fatalf("选了烧录应当 burn，实际 %q（理由：%s）", got.Subtitle.Action, got.Subtitle.Reason)
	}
	if got.Video.Action != ActionTranscode {
		t.Fatalf("烧录必须重新编码视频，实际 %q", got.Video.Action)
	}
	if got.Mode != ModeTranscode || !got.Playable {
		t.Fatalf("应当判成可转码播放，实际 mode=%q playable=%v", got.Mode, got.Playable)
	}
	if !strings.Contains(strings.Join(got.Reasons, " | "), "烧") {
		t.Errorf("理由链里要出现「烧录」，否则用户不知道为什么忽然要转码：%v", got.Reasons)
	}
	// 烧录是用户的选择，理由不能写成「你选了 720p」那种画质原因。
	if !strings.Contains(got.Video.Reason, "烧录") {
		t.Errorf("视频理由里要说明是烧录导致的转码：%s", got.Video.Reason)
	}
}

func TestBurnWithoutEncoderIsHonest(t *testing.T) {
	// 机器编不了：不能假装能烧，也不能悄悄把字幕丢掉。
	got := Decide(Request{
		Profile: BrowserProfile(), Files: []File{burnable()},
		SubtitleIndex: 2, BurnSubtitle: true,
	})
	if got.Playable {
		t.Error("这台机器没有可用编码器时不该判成能放")
	}
	if got.Mode != ModeTranscode {
		t.Errorf("应当如实报成「需要转码」，实际 %q", got.Mode)
	}
	if !strings.Contains(strings.Join(got.Reasons, " | "), "编码器") {
		t.Errorf("理由里要说清是缺编码器：%v", got.Reasons)
	}
}

func TestBurnDoesNotForceTranscodeForTextSubtitles(t *testing.T) {
	// 文本字幕走 WebVTT / libass 旁路，不需要重编画面 ——
	// 选了「烧录」也不该把本来能直出的片子拉进转码。
	f := movie("/m/a.mp4", "mov,mp4,m4a,3gp,3g2,mj2",
		[]probe.VideoStream{h264(1920, 1080, 8, true)},
		[]probe.AudioStream{audio(1, "aac", 2, true)},
		[]probe.SubtitleStream{textSub(3, "subrip", true, false)})
	got := Decide(Request{
		Profile: BrowserProfile(), Files: []File{f}, Machine: vaapiMachine(),
		SubtitleIndex: 3, BurnSubtitle: true,
	})
	if got.Mode != ModeDirect {
		t.Errorf("文本字幕不该因为「烧录」被拉进转码，实际 %q", got.Mode)
	}
	if got.Subtitle.Action != ActionConvert || got.Subtitle.DeliverAs != DeliverWebVTT {
		t.Errorf("文本字幕照旧走 WebVTT，实际 action=%q deliverAs=%q",
			got.Subtitle.Action, got.Subtitle.DeliverAs)
	}
}
