package subtitle

import "testing"

func TestIsUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"中文 UTF-8", []byte("你好，世界"), true},
		{"带 BOM 的 UTF-8", append([]byte{0xEF, 0xBB, 0xBF}, []byte("你好")...), true},
		{"空", nil, true},
		// 「你好」的 GB18030 编码
		{"GB18030", []byte{0xC4, 0xE3, 0xBA, 0xC3}, false},
		// 前面合法、后面一个坏字节
		{"UTF-8 里混坏字节", []byte{'a', 0xE4, 0xBD, 0xA0, 0xDE}, false},
	}
	for _, c := range cases {
		if got := IsUTF8(c.in); got != c.want {
			t.Errorf("%s: IsUTF8 = %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestDeliverySuffix(t *testing.T) {
	cases := map[string]string{
		"ass": "ass", "ssa": "ass", "srt": "vtt", "vtt": "vtt",
	}
	for format, want := range cases {
		if got := DeliverySuffix(format); got != want {
			t.Errorf("DeliverySuffix(%q) = %q，期望 %q", format, got, want)
		}
	}
}

func TestSRTToVTT(t *testing.T) {
	in := "\ufeff1\r\n00:00:01,000 --> 00:00:03,500\r\n你好，世界\r\n\r\n2\r\n00:00:04,000 --> 00:00:06,000\r\n第二句\r\n"
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.500\n你好，世界\n\n2\n00:00:04.000 --> 00:00:06.000\n第二句\n"
	if got := string(SRTToVTT([]byte(in))); got != want {
		t.Errorf("SRTToVTT 结果不对\n得到: %q\n期望: %q", got, want)
	}
}

// 正文里的逗号绝不能被当成时间戳分隔符改掉。
func TestSRTToVTTKeepsBodyCommas(t *testing.T) {
	got := string(SRTToVTT([]byte("1\n00:00:01,000 --> 00:00:02,000\n你好，世界，再见\n")))
	if want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\n你好，世界，再见\n"; got != want {
		t.Errorf("正文逗号被改了\n得到: %q\n期望: %q", got, want)
	}
}
