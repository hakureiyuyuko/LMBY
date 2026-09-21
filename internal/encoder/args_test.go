package encoder

import (
	"strings"
	"testing"
)

// 这一组钉住「烧录图形字幕」的滤镜图装配。
//
// 为什么值得单测：滤镜图拼错的表现是「整路转码起不来」（用户看到的是放不了），
// 而不是画质差一点 —— 从界面上根本看不出哪里错了。而这些参数又是纯函数，
// 正好可以在没有显卡、没有 ffmpeg 的机器上钉死。
//
// 图里的形式（实测真跑过，见 docs/TRANSCODING.md）：
//
//	[0:字幕序号]缩放位图[sub];[0:视频序号]主链[main];[main][sub]overlay,收尾[vout]

// softBackend 是只有软编的机器（没显卡时的兜底）。
func softBackend() Backend {
	return Backend{Kind: KindSoftware, Name: "CPU", Available: true,
		Encode: map[string]bool{"h264": true}, Decode: map[string]bool{}}
}

// vaapiBackend 是本机（Intel iHD）那样能硬编、按需硬解的机器。
func vaapiBackend(hwDecode bool) Backend {
	return Backend{
		Kind: KindVAAPI, Name: "VAAPI", Device: "/dev/dri/renderD128", Available: true,
		Encode:  map[string]bool{"h264": true},
		Decode:  map[string]bool{"h264": hwDecode, "hevc": hwDecode},
		Quality: []string{"cqp"}, Prefer: "cqp",
	}
}

// burnOfH264 是「把 #2 这条图形字幕烧进画面、输出 h264」的常见请求。
func burnReq(videoIndex int) ArgsRequest {
	return ArgsRequest{
		SourceCodec: "hevc", Width: 1920, Height: 1080, BitDepth: 10,
		TargetCodec: "h264", Quality: QualityByName("medium"), KeyframeSeconds: 4,
		VideoIndex:   videoIndex,
		BurnSubtitle: &SubtitleBurn{Index: 2},
	}
}

func TestVideoArgsWithoutBurnUsesVF(t *testing.T) {
	// 不烧字幕：照旧是一条 -vf，不该冒出 -filter_complex（否则 stream 层会
	// 按 MapLabel 走，而它其实是空的）。
	req := burnReq(0)
	req.BurnSubtitle = nil
	req.TargetWidth, req.TargetHeight = 1280, 720

	got := softBackend().VideoArgs(req)
	if got.ComplexFilter != "" || got.MapLabel != "" {
		t.Fatalf("不烧字幕不该有 -filter_complex：%q / %q", got.ComplexFilter, got.MapLabel)
	}
	if len(got.FilterArgs) != 2 || got.FilterArgs[0] != "-vf" {
		t.Fatalf("应当有一条 -vf，实际 %v", got.FilterArgs)
	}
	if !strings.Contains(got.FilterArgs[1], "scale=w=1280:h=720") {
		t.Errorf("-vf 里应当有缩放：%v", got.FilterArgs)
	}
}

func TestVideoArgsBurnSoftware(t *testing.T) {
	req := burnReq(0)
	req.TargetWidth, req.TargetHeight = 1280, 720
	got := softBackend().VideoArgs(req)

	if got.MapLabel != "[vout]" {
		t.Fatalf("应当 map 滤镜图产出的标签，实际 %q", got.MapLabel)
	}
	if len(got.FilterArgs) != 0 {
		t.Errorf("-vf 与 -filter_complex 互斥，实际同时给了 -vf：%v", got.FilterArgs)
	}
	for _, want := range []string{
		"[0:2]scale=w=1280:h=720", // 位图按输出尺寸缩放
		"pad=w=1280:h=720",        // 再居中补齐到整框
		"color=black@0",           // 填充必须透明，否则画面糊一条黑边
		"format=yuva420p[sub]",    // overlay 要求带 alpha
		"[0:0]scale=w=1280:h=720", // 主链：软件缩放
		"[main][sub]overlay=eof_action=pass:repeatlast=0[vout]",
	} {
		if !strings.Contains(got.ComplexFilter, want) {
			t.Errorf("滤镜图里缺少 %q：\n%s", want, got.ComplexFilter)
		}
	}
	// 软件路径的帧本来就在内存里，不该出现上传/下载（多余且会失败）。
	for _, bad := range []string{"hwupload", "hwdownload"} {
		if strings.Contains(got.ComplexFilter, bad) {
			t.Errorf("软件路径不该出现 %s：\n%s", bad, got.ComplexFilter)
		}
	}
}

func TestVideoArgsBurnVAAPIHardware(t *testing.T) {
	req := burnReq(0)
	got := vaapiBackend(true).VideoArgs(req)

	if !strings.Contains(strings.Join(got.InputArgs, " "), "-hwaccel vaapi") {
		t.Fatalf("硬解没接上，帧不会在显存里：%v", got.InputArgs)
	}
	// 位图缩放用的是软件 scale（帧要在内存里才能 overlay）。
	if !strings.Contains(got.ComplexFilter, "[0:2]scale=w=1920:h=1080") {
		t.Errorf("位图应当缩放到源尺寸（本例没有降分辨率）：\n%s", got.ComplexFilter)
	}
	// 主链必须在 overlay **之前**把帧取回内存，overlay **之后**再传回显存。
	dl := strings.Index(got.ComplexFilter, "hwdownload")
	ov := strings.Index(got.ComplexFilter, "overlay=eof_action=pass")
	ul := strings.LastIndex(got.ComplexFilter, "hwupload")
	if dl < 0 || ov < 0 || ul < 0 {
		t.Fatalf("硬解烧录必须有 hwdownload→overlay→hwupload：\n%s", got.ComplexFilter)
	}
	if dl > ov || ov > ul {
		t.Errorf("顺序必须是 hwdownload → overlay → hwupload：\n%s", got.ComplexFilter)
	}
	if got.MapLabel != "[vout]" {
		t.Errorf("应当 map 滤镜图产出的标签，实际 %q", got.MapLabel)
	}
}

func TestVideoArgsBurnVAAPISoftwareDecode(t *testing.T) {
	// 硬编 + 软解：帧从一开始就在内存里，不该有 hwdownload；
	// 更不能出现 scale_vaapi —— 硬件滤镜作用在软件帧上必然失败（实测报
	// "Impossible to convert between the formats supported by…"）。
	req := burnReq(0)
	req.TargetWidth, req.TargetHeight = 1280, 720
	got := vaapiBackend(false).VideoArgs(req)

	if strings.Contains(strings.Join(got.InputArgs, " "), "-hwaccel") {
		t.Errorf("软解不该带 -hwaccel：%v", got.InputArgs)
	}
	if strings.Contains(got.ComplexFilter, "hwdownload") {
		t.Errorf("软解的帧本来就在内存里，不该 hwdownload：\n%s", got.ComplexFilter)
	}
	if strings.Contains(got.ComplexFilter, "scale_vaapi") {
		t.Errorf("软解不能用 scale_vaapi（必然失败）：\n%s", got.ComplexFilter)
	}
	if !strings.Contains(got.ComplexFilter, "scale=w=1280:h=720:force_original_aspect_ratio=decrease") {
		t.Errorf("软解应当用软件缩放：\n%s", got.ComplexFilter)
	}
	// 叠加之后要把帧传回显存给硬编码器。
	if !strings.HasSuffix(got.ComplexFilter, "format=nv12,hwupload[vout]") {
		t.Errorf("叠加之后应当上传回显存：\n%s", got.ComplexFilter)
	}
}

func TestVideoArgsNoFilterWhenNothingToDo(t *testing.T) {
	// 8bit 源、不缩放、不烧字幕：什么都不用加 —— 帧一路留在显存里直通编码器。
	req := ArgsRequest{SourceCodec: "h264", Width: 1920, Height: 1080, BitDepth: 8,
		TargetCodec: "h264", Quality: QualityByName("medium"), KeyframeSeconds: 4}
	got := vaapiBackend(true).VideoArgs(req)
	if len(got.FilterArgs) != 0 || got.ComplexFilter != "" {
		t.Errorf("没有要处理的东西时不该加滤镜：%v / %q", got.FilterArgs, got.ComplexFilter)
	}
}

func TestVideoArgsNoHWDecodeFallsBackToSoftwareDecode(t *testing.T) {
	// 硬解这路起不来时的降级：换成软件解码，编码器不变。
	// 触发场景是真实的：本机 iHD 解得了 8bit H.264，却解不了 H.264 High 10。
	req := ArgsRequest{SourceCodec: "h264", Width: 1920, Height: 1080, BitDepth: 10,
		TargetCodec: "h264", Quality: QualityByName("medium"), KeyframeSeconds: 4, NoHWDecode: true}
	got := vaapiBackend(true).VideoArgs(req)

	if strings.Contains(strings.Join(got.InputArgs, " "), "-hwaccel") {
		t.Errorf("降级后不该带 -hwaccel：%v", got.InputArgs)
	}
	if !strings.Contains(strings.Join(got.CodecArgs, " "), "h264_vaapi") {
		t.Errorf("降级只换解码，编码器不该变：%v", got.CodecArgs)
	}
	// 10bit 源降位深 + 上传（软解时帧在内存里，必须自己传上去）
	if !strings.Contains(strings.Join(got.FilterArgs, " "), "format=nv12,hwupload") {
		t.Errorf("软解后应当用软件链 + 上传：%v", got.FilterArgs)
	}
	if strings.Contains(strings.Join(got.FilterArgs, " "), "scale_vaapi") {
		t.Errorf("软解时不能用 scale_vaapi：%v", got.FilterArgs)
	}
}

func TestSubtitleBitmapFilters(t *testing.T) {
	// 有目标尺寸：等比缩放 + 居中补齐。
	got := subtitleBitmapFilters(ArgsRequest{TargetWidth: 1280, TargetHeight: 720})
	for _, want := range []string{"scale=w=1280:h=720", "pad=w=1280:h=720", "format=yuva420p"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q：%s", want, got)
		}
	}

	// 不缩放输出：用**源**尺寸（位图本来就是按源尺寸画的）。
	got = subtitleBitmapFilters(ArgsRequest{Width: 1920, Height: 1080})
	if !strings.Contains(got, "scale=w=1920:h=1080") {
		t.Errorf("没有目标尺寸时应当退回源尺寸：%s", got)
	}

	// 连源尺寸都不知道：只能原样送进去，交给 ffmpeg 自己协商。
	got = subtitleBitmapFilters(ArgsRequest{})
	if got != "format=yuva420p" {
		t.Errorf("尺寸未知时只该统一像素格式，实际 %q", got)
	}
}
