package encoder

import "testing"

// 真实机器上的输出片段（ffmpeg 7.1.5 / Intel iHD）。解析器要用真实格式钉住 ——
// 这张表每个 ffmpeg 版本都会动一点，靠肉眼看「大概对」是靠不住的。
const encodersFixture = `Encoders:
 V..... ==================
 V....D h264_vaapi           H.264/AVC (VAAPI) (codec h264)
 V....D hevc_vaapi           H.265/HEVC (VAAPI) (codec hevc)
 V....D av1_vaapi            AV1 (VAAPI) (codec av1)
 V....D h264_qsv             H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (Intel Quick Sync Video acceleration) (codec h264)
 V....D h264_nvenc           NVIDIA NVENC H.264 encoder (codec h264)
 V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 V....D libx265              libx265 H.265 / HEVC (codec hevc)
 A....D aac                  AAC (Advanced Audio Coding)
 A....D ac3                  ATSC A/52A (AC-3)
`

const filtersFixture = `Filters:
  T.. =             =->=
  T.. abench        A->A       Benchmark part of a filtergraph.
  ... hwupload      V->V       Upload a normal frame to a hw frame
  ... hwdownload    V->V       Download a hw frame to normal memory
  ... scale         V->V       Scale the input video size and/or convert the image format.
  ... scale_vaapi   V->V       Scale VAAPI surface
  ... tonemap_vaapi V->V       Perform HDR to SDR tone mapping
  ... subtitles     V->V       Render text subtitles onto input video using the libass library.
`

func TestParseNamedList(t *testing.T) {
	got := parseNamedList(encodersFixture)
	for _, want := range []string{"h264_vaapi", "hevc_vaapi", "libx264", "libx265", "aac", "ac3"} {
		if !contains(got, want) {
			t.Errorf("编码器表里没解出 %q（得到 %v）", want, got)
		}
	}
	// 分隔行「V..... ==================」与「T.. =  =->=」不能被当成编码器名
	if contains(got, "=") || contains(got, "==================") {
		t.Errorf("把分隔行当成了名字：%v", got)
	}

	got = parseNamedList(filtersFixture)
	for _, want := range []string{"scale_vaapi", "tonemap_vaapi", "hwupload", "subtitles", "scale"} {
		if !contains(got, want) {
			t.Errorf("滤镜表里没解出 %q（得到 %v）", want, got)
		}
	}
}

func TestParseHWAccels(t *testing.T) {
	out := "Hardware acceleration methods:\nvdpau\ncuda\nvaapi\nqsv\n"
	got := parseHWAccels(out)
	want := []string{"vdpau", "cuda", "vaapi", "qsv"}
	if len(got) != len(want) {
		t.Fatalf("hwaccels 解析数量不对：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项：得到 %q 想要 %q", i, got[i], want[i])
		}
	}
}

func TestBackendUsableAndBest(t *testing.T) {
	caps := &Capabilities{
		Software: Backend{Kind: KindSoftware, Available: true, Encode: map[string]bool{"h264": true}},
		Backends: []Backend{
			// 编码器在列表里、但真跑不过 —— 就是这台机器上 QSV 的样子
			{Kind: KindQSV, Name: "Intel Quick Sync", Available: false, Encode: map[string]bool{}, Notes: []string{"h264_qsv 真跑失败：-22"}},
			{Kind: KindVAAPI, Name: "VAAPI（Linux 通用）", Device: "/dev/dri/renderD128", Available: true,
				Encode: map[string]bool{"h264": true, "hevc": true}, Decode: map[string]bool{"h264": true, "hevc": true},
				Quality: []string{"cqp", "cbr"}},
			{Kind: KindNVENC, Name: "NVIDIA NVENC", Available: false, Encode: map[string]bool{}},
		},
	}
	if !caps.Backends[1].Usable() {
		t.Fatal("VAAPI 编得出 h264，应当算可用")
	}
	if caps.Backends[0].Usable() || caps.Backends[2].Usable() {
		t.Fatal("真跑失败的后端不该算可用")
	}
	if got := caps.Best(); got.Kind != KindVAAPI {
		t.Errorf("首选应当是 VAAPI，得到 %s", got.Kind)
	}
	if got := caps.DeviceFor(KindVAAPI); got != "/dev/dri/renderD128" {
		t.Errorf("设备节点取错：%q", got)
	}

	// 硬件全不可用时要兜底到软件编码，而不是返回「放不了」
	for i := range caps.Backends {
		caps.Backends[i].Available = false
		caps.Backends[i].Encode = map[string]bool{}
	}
	if got := caps.Best(); got.Kind != KindSoftware {
		t.Errorf("硬件全挂时应当兜底软件，得到 %s", got.Kind)
	}
}

func TestCapabilitiesDeviceForMissing(t *testing.T) {
	caps := &Capabilities{}
	if got := caps.DeviceFor(KindVAAPI); got != "" {
		t.Errorf("没有该后端时应当返回空串，得到 %q", got)
	}
}
