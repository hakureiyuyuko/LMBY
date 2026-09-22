package livetvsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// 本文件是频道探测（M5 的「失效源标记」）：真拉一次源，判定它出不出得来流。

// ProbeResult 是一次探测的结果。
//
// Summary 是给人看的一句话，会原样存进 tv_channels.probe：
// 通的时候形如「H.264 1920x1080 / MP2 立体声（1.1s）」，不通的时候
// 形如「连接被拒绝：...」—— 界面上直接把这句话显示出来就够了。
type ProbeResult struct {
	OK      bool
	Summary string
	// VideoCodec/VideoHeight：源视频编码与高度（判不出来就为空/0）——
	// 起播靠它决定「转封装就行」还是「浏览器吃不下，得转码」。
	VideoCodec  string
	VideoHeight int
	Elapsed     time.Duration
}

// 探测时「为了认出流」最多读多少数据（与点播探测同一个量级）。
//
// 不限制的话，某些源会让 ffprobe 一直读下去；限制了才能保证「探测」是秒级的。
const (
	probeAnalyzeMicros = 3_000_000 // 微秒，即最多分析 3 秒的内容
	probeSizeBytes     = 3 << 20   // 3 MiB
)

// Probe 真拉一次直播源，判定它能不能出流。
//
// 为什么必须真连：播放列表里的死源**不会**在 TCP 层露馅 —— 实测（IPTV 单播源）
// 死源照样能连上，是在 RTSP DESCRIBE 那一步 302 跳到一个从本网连不上的地址上。
// 所以只有真拉一次（ffprobe 会完整走完协议握手）才判得出来。
//
// 代价是每个频道要花一次连接的时间：可用的源实测约 1.1 秒返回流信息，
// 不可达的源只能等 timeout 到点（由调用方按机器与源站情况配置）。
//
// inputArgs 用 livetv.InputArgs 生成（与播放同一套参数），
// 这样「探通了的源播放也一定连得上」这件事才有保证。
func Probe(ctx context.Context, probePath, rawurl string, inputArgs []string, timeout time.Duration) ProbeResult {
	start := time.Now()
	if strings.TrimSpace(rawurl) == "" {
		return ProbeResult{OK: false, Summary: "没有播放地址", Elapsed: time.Since(start)}
	}
	if strings.TrimSpace(probePath) == "" {
		probePath = "ffprobe"
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := make([]string, 0, 12+len(inputArgs))
	args = append(args,
		"-v", "error",
		"-probesize", strconv.Itoa(probeSizeBytes),
		"-analyzeduration", strconv.Itoa(probeAnalyzeMicros),
		"-print_format", "json",
		"-show_streams",
		"-show_format",
	)
	args = append(args, inputArgs...)
	args = append(args, "-i", rawurl)

	cmd := exec.CommandContext(ctx, probePath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	elapsed := time.Since(start)

	if runErr != nil {
		summary := FailureSummary(stderr.String())
		switch {
		case ctx.Err() != nil:
			// 直播源最典型的失败模式：地址根本不应答。
			// （被上层取消也走这里，反正这一条已经不算数了。）
			summary = fmt.Sprintf("超时（%s 内没有应答）", timeout)
			if raw := lastMeaningfulLine(stderr.String()); raw != "" {
				summary += "：" + raw
			}
		case summary == "":
			summary = fmt.Sprintf("探测失败：%v", runErr)
		}
		return ProbeResult{OK: false, Summary: truncateSummary(summary), Elapsed: elapsed}
	}

	summary, info, ok := SummarizeStreams(stdout.Bytes())
	if !ok {
		// 连上了、也读到东西了，但认不出音视频流（比如地址指向的是网页）
		return ProbeResult{OK: false, Summary: "拉到了内容但认不出音视频流", Elapsed: elapsed}
	}
	return ProbeResult{
		OK:          true,
		Summary:     truncateSummary(fmt.Sprintf("%s（%.1fs）", summary, elapsed.Seconds())),
		VideoCodec:  info.VideoCodec,
		VideoHeight: info.VideoHeight,
		Elapsed:     elapsed,
	}
}

// SummarizeStreams 把 ffprobe 的 JSON 概括成一句人话；没有可用的音视频流时 ok=false。
//
// 抽成纯函数是为了能离线单测（不起进程、不连网络）。
func SummarizeStreams(data []byte) (string, StreamInfo, bool) {
	return summarizeStreams(data)
}

// StreamInfo 是探测里顺手拿到、**后面真的要用的**结构化信息。
//
// 只留这两项：起播要判断「浏览器吃不吃这份源」——吃不下就直接转码。
// 其余细节（采样率/声道/位深）对人有用，对决策没用，就别存了。
type StreamInfo struct {
	// VideoCodec 是 ffprobe 的 codec_name，小写原样（hevc / h264 / mpeg2video …）。
	VideoCodec string
	// VideoHeight 是源视频高度（0 = 不知道）：转码时决定要不要往下缩。
	VideoHeight int
}

func summarizeStreams(data []byte) (string, StreamInfo, bool) {
	var out struct {
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", StreamInfo{}, false
	}

	var video, audio string
	var info StreamInfo
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			// 封面图（mp3 里挂的 jpg）不算视频流
			if video != "" || isImageCodec(s.CodecName) {
				continue
			}
			video = codecLabel(s.CodecName)
			info.VideoCodec = strings.ToLower(strings.TrimSpace(s.CodecName))
			info.VideoHeight = s.Height
			if s.Width > 0 && s.Height > 0 {
				video += fmt.Sprintf(" %dx%d", s.Width, s.Height)
			}
		case "audio":
			if audio != "" {
				continue
			}
			audio = codecLabel(s.CodecName)
			if c := channelsLabel(s.Channels); c != "" {
				audio += " " + c
			}
			if khz := sampleRateLabel(s.SampleRate); khz != "" {
				audio += " " + khz
			}
		}
	}

	parts := make([]string, 0, 2)
	if video != "" {
		parts = append(parts, video)
	}
	if audio != "" {
		parts = append(parts, audio)
	}
	if len(parts) == 0 {
		return "", StreamInfo{}, false
	}
	summary := strings.Join(parts, " / ")
	if video != "" && audio == "" {
		// 有视频没音轨的直播源是真实存在的，标出来省得用户以为音量坏了
		summary += "（无音轨）"
	}
	return summary, info, true
}

// FailureSummary 把 ffprobe 的报错概括成一句人话；认不出时返回原始报错行（可能为空）。
//
// 直播源的失败形态其实就那么几种（连不上 / 不应答 / 源站回错码 / 数据不对），
// 翻译成人话比直接抛英文有用；原始那行一并留住，出问题时能对着查。
func FailureSummary(stderr string) string {
	raw := lastMeaningfulLine(stderr)
	lower := strings.ToLower(raw)

	var reason string
	switch {
	case strings.Contains(lower, "connection refused"):
		reason = "连接被拒绝"
	case strings.Contains(lower, "no route to host"):
		reason = "网络不可达"
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		reason = "连接超时"
	case strings.Contains(lower, "server returned 4"), strings.Contains(lower, "http error 4"):
		// 401/403/404 都落在这里（"server returned 4" 比逐个数码更耐改）
		reason = "源站拒绝"
	case strings.Contains(lower, "server returned 5"), strings.Contains(lower, "http error 5"):
		reason = "源站故障"
	case strings.Contains(lower, "describe failed"):
		reason = "RTSP DESCRIBE 失败"
	case strings.Contains(lower, "invalid data found"), strings.Contains(lower, "could not find codec parameters"):
		reason = "数据无法解析"
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"):
		reason = "源站要求鉴权"
	case strings.Contains(lower, "no such file or directory"):
		reason = "地址不存在"
	}
	if reason == "" {
		return truncateSummary(raw)
	}
	if raw == "" {
		return reason
	}
	return truncateSummary(reason + "：" + raw)
}

// lastMeaningfulLine 取最后一条非空行：ffmpeg 把人话放在最后
// （前面几行是协议层/解码器的细节，例如 `[tcp @ 0x...] Connection to ... failed`）。
func lastMeaningfulLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(strings.ReplaceAll(lines[i], "\r", "")); l != "" {
			return l
		}
	}
	return ""
}

func isImageCodec(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mjpeg", "png", "bmp", "gif", "webp", "tiff", "jpeg2000":
		return true
	}
	return false
}

func codecLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return "未知编码"
	case "h264":
		return "H.264"
	case "hevc", "h265":
		return "HEVC"
	case "mpeg2video":
		return "MPEG-2"
	case "mpeg4":
		return "MPEG-4"
	case "vc1":
		return "VC-1"
	case "av1":
		return "AV1"
	case "mp2":
		return "MP2"
	case "mp3":
		return "MP3"
	case "aac":
		return "AAC"
	case "aac_latm":
		return "AAC LATM"
	case "ac3":
		return "AC-3"
	case "eac3":
		return "E-AC-3"
	case "opus":
		return "Opus"
	case "flac":
		return "FLAC"
	case "pcm_mulaw":
		return "G.711 μ-law"
	case "pcm_alaw":
		return "G.711 A-law"
	}
	return strings.ToUpper(name)
}

func channelsLabel(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "单声道"
	case 2:
		return "立体声"
	}
	return fmt.Sprintf("%d 声道", n)
}

func sampleRateLabel(s string) string {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return ""
	}
	if n%1000 == 0 {
		return fmt.Sprintf("%dkHz", n/1000)
	}
	return fmt.Sprintf("%.1fkHz", float64(n)/1000)
}

// truncateSummary 截到 300 字（库里那列还会再截一次，这里只是别把界面撑爆）。
func truncateSummary(s string) string {
	const limit = 300
	r := []rune(strings.TrimSpace(s))
	if len(r) <= limit {
		return string(r)
	}
	return string(r[:limit]) + "…"
}
