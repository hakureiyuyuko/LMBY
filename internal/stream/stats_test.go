package stream

import "testing"

func TestBitrateText(t *testing.T) {
	// ffmpeg 报了就原样用
	if got := (Stats{Bitrate: "169.9kbits/s"}).BitrateText(); got != "169.9kbits/s" {
		t.Errorf("应当原样用 ffmpeg 的码率：%q", got)
	}
	// HLS 输出下常常报 N/A → 用 total_size / 已编时长算平均
	// 12_000_000 字节 / 10 秒 = 9.6 Mbps
	got := (Stats{Bitrate: "N/A", TotalBytes: 12_000_000, OutTimeUS: 10_000_000}).BitrateText()
	if got != "9.6 Mbps" {
		t.Errorf("平均码率算得不对：%q", got)
	}
	// 数据不全就不编一个数字出来
	if got := (Stats{Bitrate: "N/A"}).BitrateText(); got != "" {
		t.Errorf("没有数据时应当返回空：%q", got)
	}
	if got := (Stats{}).BitrateText(); got != "" {
		t.Errorf("空 stats 应当返回空：%q", got)
	}
}

func TestParseStats(t *testing.T) {
	// `-progress pipe:2` 的输出：每个键一行，会和 warning 日志交织在一起。
	// 日志里会累积多块，要取**最后**一块的值。
	log := `[mp4 @ 0x1] pts has no value
frame=120
fps=24.50
bitrate= 169.9kbits/s
out_time=00:00:49.360000
speed=0.987x
progress=continue
frame=456
fps=26.00
bitrate= 182.3kbits/s
out_time=00:01:32.100000
speed=1.02x
progress=continue`
	st, ok := ParseStats(log)
	if !ok {
		t.Fatal("应当解析出进度")
	}
	if st.Frame != 456 || st.FPS != 26 || st.Speed != 1.02 {
		t.Errorf("数字字段不对：%+v", st)
	}
	if st.Bitrate != "182.3kbits/s" {
		t.Errorf("码率应原样带单位：%q", st.Bitrate)
	}
	if st.Time != "00:01:32.100000" {
		t.Errorf("媒体时间不对：%q", st.Time)
	}

	// 刚起播时 ffmpeg 会打 N/A：不能当解析失败，也不能把 N/A 塞进数字字段
	early := "frame=0\nfps=0.00\nbitrate=N/A\nout_time=00:00:00.000000\nspeed=N/A\nprogress=continue"
	st, ok = ParseStats(early)
	if !ok {
		t.Fatal("带 N/A 的进度也应算解析成功")
	}
	if st.Speed != 0 || st.FPS != 0 {
		t.Errorf("N/A 应当解析成 0：%+v", st)
	}
	if st.Bitrate != "N/A" {
		t.Errorf("码率字符串应原样保留：%q", st.Bitrate)
	}

	// 完全没有进度输出：返回 false，调用方据此显示「—」
	if _, ok := ParseStats("just a warning\nanother line\n"); ok {
		t.Error("普通日志不该被当成进度")
	}
	if _, ok := ParseStats(""); ok {
		t.Error("空日志不该算进度")
	}
}

func TestMediaSecondsAndBitrate(t *testing.T) {
	if got := (Stats{Time: "00:00:45.587208"}).MediaSeconds(); got < 45.58 || got > 45.59 {
		t.Errorf("MediaSeconds = %v，想要 ≈45.587", got)
	}
	if got := (Stats{Time: "01:02:03.500000"}).MediaSeconds(); got < 3723.4 || got > 3723.6 {
		t.Errorf("带小时的换算不对：%v", got)
	}
	if got := (Stats{Time: "N/A"}).MediaSeconds(); got != 0 {
		t.Errorf("N/A 应当当 0：%v", got)
	}

	// 12_000_000 字节 / 10 秒 = 9.6 Mbps（带「≈」表示这是平均值，不是瞬时值）
	if got := HumanBitrate(12_000_000, 10); got != "≈ 9.6 Mbps" {
		t.Errorf("平均码率 = %q", got)
	}
	if got := HumanBitrate(1_000_000, 10); got != "≈ 800 kbps" {
		t.Errorf("低码率应当用 kbps：%q", got)
	}
	if got := HumanBitrate(0, 10); got != "" {
		t.Errorf("没有数据时应当返回空：%q", got)
	}
}
