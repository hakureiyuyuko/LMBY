package stream

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Stats 是 ffmpeg 进度里的实时指标（监控页显示「转得动吗、多快」）。
type Stats struct {
	Frame   int64
	FPS     float64
	Speed   float64 // 相对实时的倍数：1.0 = 刚好实时
	Bitrate string  // 形如 "169.9kbits/s"，原样给界面
	Time    string  // 形如 "00:00:49.360000"

	// 给 bitrate 兼底用：HLS 输出下 ffmpeg 常常报 N/A，就用「已写字节 / 已编时长」算平均。
	TotalBytes int64
	OutTimeUS  int64
}

// BitrateText 返回可显示的码率：优先用 ffmpeg 报的，它报 N/A 时回退到平均值。
func (s Stats) BitrateText() string {
	if s.Bitrate != "" && s.Bitrate != "N/A" {
		return s.Bitrate
	}
	if s.TotalBytes > 0 && s.OutTimeUS > 0 {
		kbps := float64(s.TotalBytes) * 8 / (float64(s.OutTimeUS) / 1e6) / 1000
		if kbps >= 1000 {
			return fmt.Sprintf("%.1f Mbps", kbps/1000)
		}
		return fmt.Sprintf("%.0f kbps", kbps)
	}
	return ""
}

// ParseStats 从日志尾巴里解析 ffmpeg 的进度（我们用 `-progress pipe:2` 让它写 stderr）。
//
// 为什么不用默认的 stats 行：那一行是 av_log(AV_LOG_INFO) 打出来的，而我们把
// loglevel 压到 warning（不让 info 噪声淹掉真正的报错）—— 结果 stats 一起没了。
// `-progress` 是机器可读的 key=value 流，不受日志级别影响。
//
// 输出的每个块长这样（每 stats_period 一块）：
//
//	frame=123
//	fps=25.00
//	bitrate= 169.9kbits/s
//	out_time=00:00:49.360000
//	speed=0.987x
//	progress=continue
//
// 取「每个键最后出现的值」：日志里会累积多块，最后一块就是最新状态。
func ParseStats(log string) (Stats, bool) {
	var st Stats
	seen := false
	for _, line := range strings.Split(log, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "frame":
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				st.Frame, seen = n, true
			}
		case "fps":
			st.FPS = parseNum(v)
		case "speed":
			st.Speed = parseNum(strings.TrimSuffix(strings.TrimSpace(v), "x"))
		case "bitrate":
			st.Bitrate = strings.TrimSpace(v)
		case "total_size":
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				st.TotalBytes = n
			}
		case "out_time_us":
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				st.OutTimeUS = n
			}
		case "out_time":
			st.Time = strings.TrimSpace(v)
		}
	}
	return st, seen
}

// MediaSeconds 把 out_time（形如 "00:00:45.587208"）换算成秒。
func (s Stats) MediaSeconds() float64 {
	parts := strings.Split(strings.TrimSpace(s.Time), ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.ParseFloat(parts[0], 64)
	m, _ := strconv.ParseFloat(parts[1], 64)
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return h*3600 + m*60 + sec
}

// HumanBitrate 把「字节数 / 秒数」写成给人看的码率。
//
// 带一个「≈」：这是平均值，不是瞬时值 —— HLS 输出下 ffmpeg 不报
// total_size/bitrate（全是 N/A），只能靠分片文件大小自己量。
func HumanBitrate(bytes int64, seconds float64) string {
	if bytes <= 0 || seconds <= 0 {
		return ""
	}
	kbps := float64(bytes) * 8 / seconds / 1000
	if kbps >= 1000 {
		return fmt.Sprintf("≈ %.1f Mbps", kbps/1000)
	}
	return fmt.Sprintf("≈ %.0f kbps", kbps)
}

// readTail 读文件尾部最多 n 字节。
//
// 进度文件是逐块**追加**写的，最新数据在末尾；取尾部就不必关心它长了多少。
func readTail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > n {
		if _, err := f.Seek(info.Size()-n, io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseNum 解析数字，遇到 ffmpeg 的 N/A 一律当 0（刚起播时 fps/speed 都是 N/A）。
func parseNum(v string) float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}
