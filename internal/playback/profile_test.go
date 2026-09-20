package playback

import "testing"

func TestContainerKind(t *testing.T) {
	// ffprobe 对 .mkv 与 .webm 报同一个 format_name，必须靠扩展名分开
	tests := []struct{ format, path, want string }{
		{"matroska,webm", "/m/a.mkv", ContainerMKV},
		{"matroska,webm", "/m/a.webm", ContainerWebM},
		{"matroska,webm", "/m/A.WEBM", ContainerWebM},
		{"mov,mp4,m4a,3gp,3g2,mj2", "/m/a.mp4", ContainerMP4},
		{"mov,mp4,m4a,3gp,3g2,mj2", "/m/a.m4v", ContainerMP4},
		{"mpegts", "/m/a.ts", ContainerTS},
		{"avi", "/m/a.avi", ContainerAVI},
		{"", "/m/a.mp4", ContainerMP4},
		{"flv", "/m/a.flv", ContainerOther},
		{"", "/m/a", ContainerOther},
	}
	for _, tc := range tests {
		if got := ContainerKind(tc.format, tc.path); got != tc.want {
			t.Errorf("ContainerKind(%q, %q) = %q，期望 %q", tc.format, tc.path, got, tc.want)
		}
	}
}

func TestProfileNormalizeDropsJunk(t *testing.T) {
	p := Profile{
		Name:        "  My Browser!! ",
		Containers:  []string{"MP4", "mp4", "exe", "mkv"},
		VideoCodecs: []string{"H264", "h264", "h264; rm -rf /", ""},
		AudioCodecs: []string{"aac"},
		MaxWidth:    -5,
		MaxHeight:   100000,
		MaxBitDepth: 12,
		// MaxAudioChannels 留 0 → 应当回落到 2
	}.Normalize()

	if p.Name != "MyBrowser" {
		t.Errorf("name = %q，期望 MyBrowser（去掉空格与非法字符）", p.Name)
	}
	if len(p.Containers) != 2 || p.Containers[0] != "mkv" || p.Containers[1] != "mp4" {
		t.Errorf("containers = %v，期望只留下 mkv/mp4", p.Containers)
	}
	if len(p.VideoCodecs) != 1 || p.VideoCodecs[0] != "h264" {
		t.Errorf("videoCodecs = %v，期望只留下 h264", p.VideoCodecs)
	}
	if p.MaxWidth != 1280 || p.MaxHeight != maxMaxDimension {
		t.Errorf("分辨率上限清洗错误：%d x %d", p.MaxWidth, p.MaxHeight)
	}
	if p.MaxBitDepth != 8 {
		t.Errorf("非法色深应当回落到 8，实际 %d", p.MaxBitDepth)
	}
	if p.MaxAudioChannels != 2 {
		t.Errorf("声道上限应当回落到 2，实际 %d", p.MaxAudioChannels)
	}
}

func TestProfileSupportsVideo(t *testing.T) {
	p := BrowserProfile()
	cases := []struct {
		name     string
		codec    string
		w, h, bd int
		want     bool
	}{
		{"1080p h264 8bit", "h264", 1920, 1080, 8, true},
		{"色深未知按 8bit 算", "h264", 1920, 1080, 0, true},
		{"4K 超出上限", "h264", 4096, 2160, 8, false},
		{"10bit h264 超色深", "h264", 1920, 1080, 10, false},
		{"hevc 默认档不支持", "hevc", 1920, 1080, 8, false},
		{"mpeg4 不支持", "mpeg4", 720, 480, 8, false},
	}
	for _, tc := range cases {
		if got := p.SupportsVideo(tc.codec, tc.w, tc.h, tc.bd); got != tc.want {
			t.Errorf("%s：SupportsVideo = %v，期望 %v", tc.name, got, tc.want)
		}
	}

	if !SafariProfile().SupportsVideo("hevc", 3840, 2160, 10) {
		t.Error("Safari 档应当能解码 4K 10bit HEVC")
	}
	if !SafariProfile().SupportsAudio("eac3", 6) {
		t.Error("Safari 档应当支持 6 声道 E-AC3")
	}
	if BrowserProfile().SupportsAudio("dts", 6) {
		t.Error("默认档不应当假称支持 DTS")
	}
	if BrowserProfile().SupportsAudio("aac", 6) {
		t.Error("默认档只保证立体声，6 声道 aac 应当判为需要 downmix")
	}
}
