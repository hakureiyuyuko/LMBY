package livetv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadSample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sample.m3u"))
	if err != nil {
		t.Fatalf("读取样例播放列表失败: %v", err)
	}
	return string(b)
}

func TestParseSample(t *testing.T) {
	got := Parse(loadSample(t))
	if len(got) != 9 {
		t.Fatalf("条目数 = %d，期望 9（其中一条重复地址应被去重）\n%+v", len(got), got)
	}

	// 逐条核对前八个字段（顺序 = 文件顺序）
	type want struct {
		name, url, group, logo, tvgID, tvgName, headers string
	}
	wants := []want{
		{"CCTV1 综合", "rtsp://10.10.0.1:554/live/cctv1", "央视", "", "cctv1", "CCTV1", ""},
		{"CCTV2 财经", "rtsp://10.10.0.1:554/live/cctv2", "", "", "cctv2", "", ""},
		{"CCTV5 体育", "rtsp://10.10.0.1:554/live/cctv5", "", "", "", "", ""},
		{"湖南卫视", "http://10.10.0.2/hls/hunan.m3u8", "卫视,地方", "", "", "",
			"User-Agent: Lavf/1\r\nReferer: http://10.10.0.2/\r\n"},
		{"重庆卫视", "udp://@239.1.1.1:1234", "", "", "", "", ""},
		{"某频道名里带,逗号", "rtsp://10.10.0.3:554/odd", "", "", "", "", ""},
		{"CCTV15 音乐", "rtsp://10.10.0.1:554/live/cctv15", "音乐戏曲", "", "", "", ""},
		{"http://10.10.0.5/bare.ts", "http://10.10.0.5/bare.ts", "", "", "", "", ""},
		{"节目单文件", "ftp://10.10.0.6/epg.xml", "", "", "", "", ""},
	}
	for i, w := range wants {
		g := got[i]
		if g.Name != w.name || g.URL != w.url || g.Group != w.group || g.Logo != w.logo ||
			g.TvgID != w.tvgID || g.TvgName != w.tvgName || g.Headers != w.headers {
			t.Errorf("第 %d 条不符\n实际: %+v\n期望: name=%q url=%q group=%q logo=%q tvgID=%q tvgName=%q headers=%q",
				i, g, w.name, w.url, w.group, w.logo, w.tvgID, w.tvgName, w.headers)
		}
	}
}

func TestParseBOMAndCRLF(t *testing.T) {
	content := "\ufeff#EXTM3U\r\n#EXTINF:-1 ,A台\r\nrtsp://10.0.0.1/a\r\n"
	got := Parse(content)
	if len(got) != 1 || got[0].Name != "A台" || got[0].URL != "rtsp://10.0.0.1/a" {
		t.Fatalf("BOM/CRLF 处理不对: %+v", got)
	}
}

func TestParseSkipsBrokenEntries(t *testing.T) {
	// 只有 #EXTINF 没有地址 → 丢弃；空行与未知指令 → 忽略
	content := "#EXTM3U\n#EXTINF:-1 ,没地址的台\n\n#EXTVLCOPT:network-caching=500\n#EXTINF:-1 ,有地址\nrtsp://10.0.0.9/x\n"
	got := Parse(content)
	if len(got) != 1 || got[0].Name != "有地址" {
		t.Fatalf("畸形条目处理不对: %+v", got)
	}
}

func TestParseDedupeByURL(t *testing.T) {
	content := "#EXTM3U\n#EXTINF:-1 ,甲\nrtsp://10.0.0.1/a\n#EXTINF:-1 ,乙\nrtsp://10.0.0.1/a\n"
	got := Parse(content)
	if len(got) != 1 {
		t.Fatalf("按地址去重失败: %+v", got)
	}
	if got[0].Name != "甲" {
		t.Errorf("去重应保留最先出现的一条，实际保留 %q", got[0].Name)
	}
}

func TestFilterStreams(t *testing.T) {
	got := FilterStreams(Parse(loadSample(t)))
	if len(got) != 8 {
		t.Fatalf("过滤后条目数 = %d，期望 8（ftp 那条应被丢掉）", len(got))
	}
	for _, e := range got {
		if e.URL == "ftp://10.10.0.6/epg.xml" {
			t.Errorf("非流地址没被过滤掉")
		}
	}
}

func TestGuessGroup(t *testing.T) {
	cases := map[string]string{
		"CCTV1 综合":    "央视",
		"CCTV5+ 体育赛事": "央视",
		"湖南卫视":        "卫视",
		"重庆卫视":        "卫视", // 规则里「卫视」优先于「重庆」：它确实是卫视
		"重庆影视":        "重庆",
		"东方卫视":        "卫视",
		"CCTV15 音乐":   "央视",
		"金鹰卡通":        "少儿教育",
		"CGTN":        "央视",
		"某台":          "其他",
		"":            "其他",
	}
	for name, want := range cases {
		if got := GuessGroup(name); got != want {
			t.Errorf("GuessGroup(%q) = %q，期望 %q", name, got, want)
		}
	}
}

func TestApplyGroupsKeepsExisting(t *testing.T) {
	in := []Entry{
		{Name: "CCTV1 综合", URL: "rtsp://a"},
		{Name: "湖南卫视", URL: "rtsp://b", Group: "自建分组"},
	}
	got := ApplyGroups(in)
	if got[0].Group != "央视" {
		t.Errorf("没分组的应被推断出来，实际 %q", got[0].Group)
	}
	if got[1].Group != "自建分组" {
		t.Errorf("已有的分组不该被覆盖，实际 %q", got[1].Group)
	}
}

func TestKind(t *testing.T) {
	cases := map[string]string{
		"rtsp://1.2.3.4:554/x":  "rtsp",
		"rtsps://1.2.3.4/x":     "rtsp",
		"rtmp://1.2.3.4/live":   "rtmp",
		"udp://@239.1.1.1:1234": "udp",
		"rtp://239.1.1.1:1234":  "udp",
		"http://1.2.3.4/a.m3u8": "http",
		"https://1.2.3.4/a":     "http",
		"ftp://1.2.3.4/a":       "other",
		"":                      "other",
	}
	for url, want := range cases {
		if got := Kind(url); got != want {
			t.Errorf("Kind(%q) = %q，期望 %q", url, got, want)
		}
	}
}

func TestRenderRoundTrip(t *testing.T) {
	orig := ApplyGroups(FilterStreams(Parse(loadSample(t))))
	text := Render(orig)
	// 生成出来的文本再解析一次，应当与原条目等价（分组/名字/地址/请求头）
	again := Parse(text)
	if len(again) != len(orig) {
		t.Fatalf("往返后条目数变了: %d → %d\n%s", len(orig), len(again), text)
	}
	for i := range orig {
		a, b := orig[i], again[i]
		if a.Name != b.Name || a.URL != b.URL || a.Group != b.Group || a.Headers != b.Headers {
			t.Errorf("第 %d 条往返不一致\n原: %+v\n新: %+v", i, a, b)
		}
	}
	if !strings.HasPrefix(text, "#EXTM3U\n") {
		t.Errorf("生成结果缺少 #EXTM3U 头")
	}
}
