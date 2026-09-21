package playback

import (
	"strings"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/probe"
)

// 这一组专钉 M4 的「转码」决策：机器能不能编、编成什么、要不要缩放/色调映射。
//
// 与 TestDecide 的区别：那边用零值 Machine（= 不能转码），所以需要转码的内容
// 会被判成放不了；这边给出「真跑探测过」的机器能力，验证转码路径真的接上了。

// vaapiMachine 是「能硬件转码」的机器（照本机实测填）。
func vaapiMachine() Machine {
	return Machine{
		EncodeCodecs: []string{"h264", "hevc"},
		DecodeHW:     []string{"h264", "hevc"},
		Hardware:     true,
		Tonemap:      true,
		Name:         "VAAPI（Linux 通用）",
	}
}

// cappedProfile 是「客户端只吃到 1080p」的档。
//
// 默认的 BrowserProfile 上限是 3840×2160 —— 4K H.264 浏览器确实能直出，
// 硬降成 1080p 只会白烧 CPU，所以「超限才缩」这条路径要显式给一个上限更低的档。
func cappedProfile(w, h int) Profile {
	p := BrowserProfile()
	p.MaxWidth, p.MaxHeight = w, h
	return p
}

// softwareMachine 是只探测到软编的机器（没显卡时的兜底）。
func softwareMachine() Machine {
	return Machine{EncodeCodecs: []string{"h264"}, Tonemap: true, Name: "CPU（libx264 / libx265）"}
}

func hdrHevc10(w, h int) probe.VideoStream {
	vs := hevc10(w, h)
	vs.ColorTransfer = "smpte2084"
	vs.ColorPrimaries = "bt2020"
	return vs
}

func TestTranscodeWithMachine(t *testing.T) {
	mkvHevc := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{hevc10(1920, 1080)}, []probe.AudioStream{audio(1, "eac3", 6, true)}, nil)
	mkvHevcHDR := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{hdrHevc10(3840, 2160)}, []probe.AudioStream{audio(1, "truehd", 8, true)}, nil)
	mkv4K := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{h264(3840, 2160, 8, true)}, []probe.AudioStream{audio(1, "aac", 2, true)}, nil)
	mkvHi10P := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{h264(1920, 1080, 10, true)}, []probe.AudioStream{audio(1, "flac", 2, true)}, nil)

	tests := []struct {
		name        string
		req         Request
		mode        string
		playable    bool
		targetCodec string
		wantWidth   int
		wantHeight  int
		wantTonemap bool
		wantTenBit  bool
		wantContain string
	}{
		{
			name:        "10bit HEVC + 硬件转码 → 转成 h264，能播了",
			req:         Request{Profile: BrowserProfile(), Machine: vaapiMachine(), Files: []File{mkvHevc}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantTenBit:  true,
			wantContain: "转码为 h264",
		},
		{
			name:        "HDR 内容 → 标记色调映射（不做映射画面会发灰）",
			req:         Request{Profile: cappedProfile(1920, 1080), Machine: vaapiMachine(), Files: []File{mkvHevcHDR}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantWidth:   1920,
			wantHeight:  1080,
			wantTonemap: true,
			wantTenBit:  true,
			wantContain: "色调映射",
		},
		{
			name:        "HDR 但机器没有映射能力 → 照样转，理由里说清画面会发灰",
			req:         Request{Profile: BrowserProfile(), Machine: Machine{EncodeCodecs: []string{"h264"}, Name: "CPU"}, Files: []File{mkvHevcHDR}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantTenBit:  true,
			wantContain: "发灰",
		},
		{
			name:        "4K 超出客户端上限 → 转码 + 等比缩到 1080p",
			req:         Request{Profile: cappedProfile(1920, 1080), Machine: vaapiMachine(), Files: []File{mkv4K}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantWidth:   1920,
			wantHeight:  1080,
			wantContain: "缩放到 1920×1080",
		},
		{
			// 客户端能吃 4K，但转码本身跑不了那么快：上限由配置给（默认 1080）。
			name:        "转码上限：客户端允许 4K，转码仍然压到 1080p",
			req:         Request{Profile: BrowserProfile(), Machine: vaapiMachine(), TranscodeMaxHeight: 1080, Files: []File{mkvHevcHDR}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantWidth:   1920,
			wantHeight:  1080,
			wantTonemap: true,
			wantTenBit:  true,
			wantContain: "缩放到 1920×1080",
		},
		{
			name:        "转码上限写 0 = 不限：4K 源不缩放（机器够强时的选择）",
			req:         Request{Profile: BrowserProfile(), Machine: vaapiMachine(), TranscodeMaxHeight: 0, Files: []File{mkvHevcHDR}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantTonemap: true,
			wantTenBit:  true,
		},
		{
			name:        "10bit H.264（Hi10P）→ 必须转码（浏览器一律解不了）",
			req:         Request{Profile: BrowserProfile(), Machine: vaapiMachine(), Files: []File{mkvHi10P}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantTenBit:  true,
			wantContain: "10bit 超出客户端上限",
		},
		{
			name:        "只有软编可用也照转（没硬件不等于放不了）",
			req:         Request{Profile: BrowserProfile(), Machine: softwareMachine(), Files: []File{mkvHevc}},
			mode:        ModeTranscode,
			playable:    true,
			targetCodec: "h264",
			wantTenBit:  true,
		},
		{
			name:        "机器什么也编不了 → 如实说放不了，而不是黑屏",
			req:         Request{Profile: BrowserProfile(), Machine: Machine{}, Files: []File{mkvHevc}},
			mode:        ModeTranscode,
			playable:    false,
			wantContain: "没有可用的编码器",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.req)
			if got.Mode != tt.mode {
				t.Fatalf("模式：得到 %q 想要 %q（理由 %v）", got.Mode, tt.mode, got.Reasons)
			}
			if got.Playable != tt.playable {
				t.Fatalf("可播性：得到 %v 想要 %v（理由 %v）", got.Playable, tt.playable, got.Reasons)
			}
			if got.Video.Action != ActionTranscode {
				t.Errorf("视频动作：得到 %q 想要 %q", got.Video.Action, ActionTranscode)
			}
			if tt.targetCodec != "" && got.Video.TargetCodec != tt.targetCodec {
				t.Errorf("目标编码：得到 %q 想要 %q", got.Video.TargetCodec, tt.targetCodec)
			}
			if tt.wantWidth != 0 && got.Video.TargetWidth != tt.wantWidth {
				t.Errorf("目标宽度：得到 %d 想要 %d", got.Video.TargetWidth, tt.wantWidth)
			}
			if tt.wantHeight != 0 && got.Video.TargetHeight != tt.wantHeight {
				t.Errorf("目标高度：得到 %d 想要 %d", got.Video.TargetHeight, tt.wantHeight)
			}
			if got.Video.Tonemap != tt.wantTonemap {
				t.Errorf("色调映射：得到 %v 想要 %v", got.Video.Tonemap, tt.wantTonemap)
			}
			if got.Video.TenBit != tt.wantTenBit {
				t.Errorf("10bit 降位深：得到 %v 想要 %v", got.Video.TenBit, tt.wantTenBit)
			}
			// 执行层要用的源信息必须一起带出来，否则拼参数时还得回头查
			if got.Video.SourceCodec == "" || got.Video.SourceWidth == 0 {
				t.Errorf("转码计划里应当带上源信息：%+v", got.Video)
			}
			if tt.wantContain != "" {
				joined := strings.Join(got.Reasons, " | ")
				if !strings.Contains(joined, tt.wantContain) {
					t.Errorf("理由链里应当有 %q，实际：%s", tt.wantContain, joined)
				}
			}
		})
	}
}

func TestTranscodeKeepsAudioReasons(t *testing.T) {
	// 视频转码时音频顺带一起转，理由要说清楚（否则用户以为音频坏了）
	file := movie("/m/a.mkv", "matroska,webm",
		[]probe.VideoStream{hevc10(1920, 1080)}, []probe.AudioStream{audio(1, "dts", 6, true)}, nil)
	got := Decide(Request{Profile: BrowserProfile(), Machine: vaapiMachine(), Files: []File{file}})
	if got.Audio.Action != ActionConvert {
		t.Errorf("音频应当跟着转成 AAC，得到 %q", got.Audio.Action)
	}
	if !got.Audio.Downmix {
		t.Errorf("6 声道超过浏览器上限（2），应当降混：%+v", got.Audio)
	}
	joined := strings.Join(got.Reasons, " | ")
	if !strings.Contains(joined, "转成 AAC") {
		t.Errorf("理由链里应当说明音频也转了：%s", joined)
	}
}

func TestFitWithin(t *testing.T) {
	if w, h := fitWithin(3840, 2160, 1920, 1080); w != 1920 || h != 1080 {
		t.Errorf("3840x2160 → 1920x1080，得到 %dx%d", w, h)
	}
	// 非 16:9：等比缩，且宽高必须是偶数（编码器不吃奇数尺寸）
	w, h := fitWithin(1920, 800, 1280, 720)
	if w != 1280 || h%2 != 0 || h <= 0 {
		t.Errorf("1920x800 缩到宽 1280 时应给偶数高，得到 %dx%d", w, h)
	}
	// 上限比源大：不放大（放大只会白费码率）
	if w, h := fitWithin(1280, 720, 3840, 2160); w != 1280 || h != 720 {
		t.Errorf("上限比源大时不应放大，得到 %dx%d", w, h)
	}
	if w, h := fitWithin(0, 0, 1920, 1080); w != 0 || h != 0 {
		t.Errorf("源尺寸未知时应当返回 0，得到 %dx%d", w, h)
	}
}
