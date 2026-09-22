package livetvsync

import (
	"testing"
)

// 这些夹具是 ffprobe 在真实 IPTV 单播源上的输出形状（字段名照抄，值做了通用化）。

func TestSummarizeStreams(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{
			name: "单播源：h264 + mp2",
			in: `{"streams":[
				{"index":0,"codec_name":"h264","codec_type":"video","width":1920,"height":1080},
				{"index":1,"codec_name":"mp2","codec_type":"audio","sample_rate":"48000","channels":2}
			],"format":{"format_name":"rtsp"}}`,
			want: "H.264 1920x1080 / MP2 立体声 48kHz",
			ok:   true,
		},
		{
			name: "只有视频（直播源没音轨是真实存在的）",
			in: `{"streams":[
				{"index":0,"codec_name":"hevc","codec_type":"video","width":3840,"height":2160}
			]}`,
			want: "HEVC 3840x2160（无音轨）",
			ok:   true,
		},
		{
			name: "只有音频",
			in: `{"streams":[
				{"index":0,"codec_name":"aac","codec_type":"audio","sample_rate":"44100","channels":1}
			]}`,
			want: "AAC 单声道 44.1kHz",
			ok:   true,
		},
		{
			name: "多声道与 5 声道标签",
			in: `{"streams":[
				{"index":0,"codec_name":"ac3","codec_type":"audio","sample_rate":"48000","channels":6}
			]}`,
			want: "AC-3 6 声道 48kHz",
			ok:   true,
		},
		{
			name: "未知编码原样大写",
			in: `{"streams":[
				{"index":0,"codec_name":"vp9","codec_type":"video","width":1280,"height":720}
			]}`,
			want: "VP9 1280x720（无音轨）",
			ok:   true,
		},
		{
			name: "封面图不算视频流（mp3 挂 jpg）",
			in: `{"streams":[
				{"index":0,"codec_name":"mjpeg","codec_type":"video","width":600,"height":600},
				{"index":1,"codec_name":"mp3","codec_type":"audio","sample_rate":"32000","channels":2}
			]}`,
			want: "MP3 立体声 32kHz",
			ok:   true,
		},
		{
			name: "同一类流只取第一条",
			in: `{"streams":[
				{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576},
				{"index":1,"codec_name":"h264","codec_type":"video","width":1920,"height":1080},
				{"index":2,"codec_name":"aac","codec_type":"audio","sample_rate":"48000","channels":2},
				{"index":3,"codec_name":"ac3","codec_type":"audio","sample_rate":"48000","channels":6}
			]}`,
			want: "H.264 720x576 / AAC 立体声 48kHz",
			ok:   true,
		},
		{
			name: "没有流（地址指向的是网页/空体）",
			in:   `{"streams":[],"format":{"format_name":"html"}}`,
			want: "",
			ok:   false,
		},
		{
			name: "输出不是 JSON",
			in:   `<html><body>404</body></html>`,
			want: "",
			ok:   false,
		},
		{
			name: "空输出",
			in:   "",
			want: "",
			ok:   false,
		},
		{
			name: "只有字幕与数据流（直播源不该出现，但别认成通）",
			in: `{"streams":[
				{"index":0,"codec_name":"subrip","codec_type":"subtitle"},
				{"index":1,"codec_name":"bin_data","codec_type":"data"}
			]}`,
			want: "",
			ok:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, ok := SummarizeStreams([]byte(c.in))
			if ok != c.ok {
				t.Fatalf("ok = %v，期望 %v（summary=%q）", ok, c.ok, got)
			}
			if got != c.want {
				t.Fatalf("summary = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestFailureSummary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空 stderr", "", ""},
		{
			// 实测形态：第一行是协议层细节，最后一行才是有用的话
			name: "连接被拒",
			in:   "[tcp @ 0x630ca4682c40] Connection to tcp://127.0.0.1:554?timeout=15000000 failed: Connection refused\nrtsp://127.0.0.1:554/nothing: Connection refused\n",
			want: "连接被拒绝：rtsp://127.0.0.1:554/nothing: Connection refused",
		},
		{
			name: "源站 404",
			in:   "[rtsp @ 0x1] method DESCRIBE failed: 404 Not Found\nrtsp://1.2.3.4/x: Server returned 404 Not Found",
			want: "源站拒绝：rtsp://1.2.3.4/x: Server returned 404 Not Found",
		},
		{
			name: "DESCRIBE 失败（没有 server returned 行）",
			in:   "[rtsp @ 0x1] method DESCRIBE failed: 453 Not Enough Bandwidth",
			want: "RTSP DESCRIBE 失败：[rtsp @ 0x1] method DESCRIBE failed: 453 Not Enough Bandwidth",
		},
		{
			name: "源站 5xx",
			in:   "http://a/b: Server returned 503 Service Unavailable",
			want: "源站故障：http://a/b: Server returned 503 Service Unavailable",
		},
		{
			name: "超时",
			in:   "[tcp @ 0x2] Connection to tcp://1.2.3.4:554?timeout=15000000 failed: Operation timed out",
			want: "连接超时：[tcp @ 0x2] Connection to tcp://1.2.3.4:554?timeout=15000000 failed: Operation timed out",
		},
		{
			name: "网络不可达",
			in:   "[tcp @ 0x3] Connection to tcp://10.0.0.1:554 failed: No route to host",
			want: "网络不可达：[tcp @ 0x3] Connection to tcp://10.0.0.1:554 failed: No route to host",
		},
		{
			name: "认不出的报错原样返回",
			in:   "Some completely unknown error\n",
			want: "Some completely unknown error",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FailureSummary(c.in); got != c.want {
				t.Fatalf("FailureSummary = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestLastMeaningfulLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"\n\n", ""},
		{"a\n", "a"},
		{"a\nb\n\n", "b"},
		{"a\r\nb\r\n", "b"},
		{"  a  \n", "a"},
	}
	for _, c := range cases {
		if got := lastMeaningfulLine(c.in); got != c.want {
			t.Fatalf("lastMeaningfulLine(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestSummarizeTruncatesLongOutput(t *testing.T) {
	// 一条超长的源站报错不能把界面撑爆（同时库里那列也会再截一次）
	long := make([]byte, 0, 800)
	long = append(long, []byte("http://a/b: Server returned 404 ")...)
	for i := 0; i < 40; i++ {
		long = append(long, []byte("xxxxxxxxxx")...)
	}
	got := FailureSummary(string(long))
	if len([]rune(got)) > 301 {
		t.Fatalf("摘要没有截断：%d 字", len([]rune(got)))
	}
}

// 起播要靠 VideoCodec/VideoHeight 判断「转封装够不够」，所以这两个字段必须真的被解析出来。
// 实测场景（2026-09-22）：IPTV 源里有 H.265，浏览器基本解不开 —— 有这些信息才能直接转码。
func TestSummarizeStreamsExtractsVideoCodec(t *testing.T) {
	const in = `{
		"streams": [
			{"index": 0, "codec_type": "video", "codec_name": "hevc", "width": 1920, "height": 1080},
			{"index": 1, "codec_type": "audio", "codec_name": "aac", "channels": 2}
		],
		"format": {"format_name": "mpegts"}
	}`
	summary, info, ok := SummarizeStreams([]byte(in))
	if !ok {
		t.Fatalf("应当能解析：%s", summary)
	}
	if info.VideoCodec != "hevc" {
		t.Fatalf("VideoCodec = %q，期望 hevc", info.VideoCodec)
	}
	if info.VideoHeight != 1080 {
		t.Fatalf("VideoHeight = %d，期望 1080", info.VideoHeight)
	}

	// 判不出来时不能瞎猜：只有音频流也算「探测通」（源站能出流），
	// 但视频编码必须留空 = 未知 —— 起播那时按转封装走，放不出来再由前端带 force 重试兜底。
	_, info2, ok2 := SummarizeStreams([]byte(`{"streams":[{"codec_type":"audio","codec_name":"mp2"}]}`))
	if !ok2 {
		t.Fatalf("只有音频流也算探测通（原语义），不该判失败")
	}
	if info2.VideoCodec != "" || info2.VideoHeight != 0 {
		t.Fatalf("没有视频流时不该给出编码信息：%+v", info2)
	}
}
