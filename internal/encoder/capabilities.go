// Package encoder 回答一个问题：**这台机器**能把什么转成什么。
//
// 「ffmpeg 里有 h264_vaapi 这个编码器」不等于「这台机器能用它」——编码器列表是
// 编译期就定好的，跟有没有显卡、驱动装没装、设备节点在不在完全无关。
// 实测就踩过：这台机器 `-encoders` 里 h264_qsv / h264_nvenc 都在，
// 真跑立刻报 -22 Invalid argument（一个没有 QSV 硬件、一个没有 N 卡）。
//
// 所以这里的探测分两层：
//  1. 静态层：`-version` / `-hwaccels` / `-encoders` / `-filters` / `/dev/dri`
//  2. **真跑层**：每种后端用 lavfi 生成 1 秒小样真编一遍，还要真解一段 h264/hevc
// 只有真跑通过的后端才会被决策引擎使用。
package encoder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Kind 是转码后端种类（与 ffmpeg 的 -hwaccels 命名对齐）。
type Kind string

const (
	KindSoftware     Kind = "software"     // libx264 / libx265
	KindVAAPI        Kind = "vaapi"        // Linux 通用硬件接口（Intel/AMD）
	KindQSV          Kind = "qsv"          // Intel Quick Sync
	KindNVENC        Kind = "nvenc"        // NVIDIA
	KindVideoToolbox Kind = "videotoolbox" // macOS
	KindAMF          Kind = "amf"          // AMD Windows
)

// 关心哪些编码器（其它不记，省得能力表噪声太大）。
var interestingEncoders = []string{
	"h264_vaapi", "hevc_vaapi", "av1_vaapi", "vp9_vaapi", "mpeg2_vaapi",
	"h264_qsv", "hevc_qsv", "av1_qsv",
	"h264_nvenc", "hevc_nvenc", "av1_nvenc",
	"h264_videotoolbox", "hevc_videotoolbox",
	"h264_amf", "hevc_amf",
	"libx264", "libx265", "libsvtav1", "libvpx-vp9",
	"aac", "ac3", "eac3", "libopus", "libmp3lame", "flac",
}

// 关心哪些滤镜（转码链路上会用到的）。
var interestingFilters = []string{
	"scale_vaapi", "deinterlace_vaapi", "tonemap_vaapi", "denoise_vaapi",
	"scale_qsv", "deinterlace_qsv", "vpp_qsv",
	"scale_cuda", "tonemap_opencl", "tonemap_cuda",
	"scale", "zscale", "tonemap", "yadif", "bwdif", "subtitles", "overlay",
	"hwupload", "hwdownload", "hwmap", "format",
}

// Backend 是某个后端在**这台机器上**的真实能力。
type Backend struct {
	Kind      Kind            `json:"kind"`
	Name      string          `json:"name"`
	Device    string          `json:"device,omitempty"`
	Available bool            `json:"available"`
	Encode    map[string]bool `json:"encode,omitempty"` // 编码："h264"/"hevc"/"av1"
	Decode    map[string]bool `json:"decode,omitempty"` // 解码："h264"/"hevc"
	Quality   []string        `json:"quality,omitempty"`
	Filters   []string        `json:"filters,omitempty"`
	Notes     []string        `json:"notes,omitempty"`
}

// Usable 表示「至少能编出一个浏览器能播的编码」。
func (b Backend) Usable() bool {
	return b.Available && (b.Encode["h264"] || b.Encode["hevc"])
}

// Capabilities 是一次探测的完整结果（会缓存到数据目录，供界面与决策引擎读）。
type Capabilities struct {
	ProbedAt   time.Time `json:"probedAt"`
	ElapsedMS  int64     `json:"elapsedMs"`
	FFmpeg     string    `json:"ffmpeg"`
	Version    string    `json:"version"`
	HWAccels   []string  `json:"hwaccels"` // 声明支持的（≠ 能用）
	Encoders   []string  `json:"encoders"`
	Filters    []string  `json:"filters"`
	Devices    []string  `json:"devices"` // /dev/dri/* 之类
	Software   Backend   `json:"software"`
	Backends   []Backend `json:"backends"`
	Warnings   []string  `json:"warnings,omitempty"`
	SamplePath string    `json:"samplePath,omitempty"` // 真跑用的样本（有的话）
	Speeds     map[string]float64 `json:"speeds,omitempty"` // "vaapi/h264" → 实测倍速
}

// Best 按优先级返回第一个可用后端（硬件优先，最后兜底软件）。
//
// 顺序参考 Jellyfin 的心智模型：先挑「离硬件最近、实测能跑」的那个。
// 注意 QSV 排在 VAAPI 前面只是**顺序**：本机 QSV 探测不过，自然会被跳过。
func (c *Capabilities) Best() Backend {
	order := []Kind{KindNVENC, KindVideoToolbox, KindAMF, KindQSV, KindVAAPI}
	for _, k := range order {
		for _, b := range c.Backends {
			if b.Kind == k && b.Usable() {
				return b
			}
		}
	}
	return c.Software
}

// DeviceFor 返回该后端应该用的设备节点。
func (c *Capabilities) DeviceFor(k Kind) string {
	for _, b := range c.Backends {
		if b.Kind == k && b.Device != "" {
			return b.Device
		}
	}
	return ""
}

// ProbeOptions 控制探测范围。
type ProbeOptions struct {
	// WorkDir 放小样文件与临时输出（默认 os.TempDir()）。
	WorkDir string
	// Timeout 单个小样的超时（默认 25 秒）。
	Timeout time.Duration
	// SkipDecode 跳过解码探测（很快，一般不用跳）。
	SkipDecode bool
}

// Probe 真跑一遍能力探测。耗时通常 1~3 秒（lavfi 小样 + 两个真解测试）。
func Probe(ctx context.Context, ffmpeg string, opts ProbeOptions) (*Capabilities, error) {
	start := time.Now()
	if opts.Timeout <= 0 {
		opts.Timeout = 25 * time.Second
	}
	if opts.WorkDir == "" {
		opts.WorkDir = os.TempDir()
	}
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建探测工作目录失败: %w", err)
	}

	p := &prober{ffmpeg: ffmpeg, dir: opts.WorkDir, timeout: opts.Timeout, ctx: ctx}
	caps := &Capabilities{
		FFmpeg:   ffmpeg,
		Speeds:   map[string]float64{},
		Software: Backend{Kind: KindSoftware, Name: "CPU（libx264 / libx265）", Encode: map[string]bool{}, Decode: map[string]bool{}},
	}

	// ---- 静态层 ----
	out, err := p.run("-version")
	if err != nil {
		return nil, fmt.Errorf("跑不了 ffmpeg（%s）: %w", ffmpeg, err)
	}
	caps.Version = firstLine(out)

	if out, err := p.run("-hwaccels"); err == nil {
		caps.HWAccels = parseHWAccels(out)
	}
	encoderList, filterList := []string{}, []string{}
	if out, err := p.run("-encoders"); err == nil {
		encoderList = parseNamedList(out)
		caps.Encoders = intersect(encoderList, interestingEncoders)
	}
	if out, err := p.run("-filters"); err == nil {
		filterList = parseNamedList(out)
		caps.Filters = intersect(filterList, interestingFilters)
	}
	caps.Devices = listDevices()
	hasEncoder := func(name string) bool { return contains(encoderList, name) }
	hasFilter := func(name string) bool { return contains(filterList, name) }

	// ---- 真跑层：软件基线 ----
	for _, enc := range []string{"libx264", "libx265"} {
		codec := "h264"
		if enc == "libx265" {
			codec = "hevc"
		}
		if !hasEncoder(enc) {
			caps.Software.Notes = append(caps.Software.Notes, enc+" 不存在")
			continue
		}
		ok, reason := p.smoke("-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25", "-t", "1",
			"-c:v", enc, "-preset", "ultrafast", "-f", "null", "-")
		caps.Software.Encode[codec] = ok
		if !ok {
			caps.Software.Notes = append(caps.Software.Notes, enc+" 跑失败："+reason)
		}
	}
	caps.Software.Available = caps.Software.Encode["h264"]
	if !caps.Software.Available {
		caps.Warnings = append(caps.Warnings, "连 CPU 基线 libx264 都跑不起来，转码不可用")
	}

	// ---- 真跑层：硬件后端 ----
	type hwSpec struct {
		kind     Kind
		name     string
		encoders map[string]string // codec → 编码器名
		filters  []string          // 该后端用到的滤镜
		device   string
	}
	var hw []hwSpec
	if len(caps.Devices) > 0 {
		dev := ""
		for _, d := range caps.Devices {
			if strings.Contains(d, "renderD") {
				dev = d
				break
			}
			if dev == "" {
				dev = d
			}
		}
		hw = append(hw, hwSpec{kind: KindVAAPI, name: "VAAPI（Linux 通用）", device: dev,
			encoders: map[string]string{"h264": "h264_vaapi", "hevc": "hevc_vaapi", "av1": "av1_vaapi"},
			filters:  []string{"scale_vaapi", "deinterlace_vaapi", "tonemap_vaapi"},
		})
	}
	hw = append(hw,
		hwSpec{kind: KindQSV, name: "Intel Quick Sync", encoders: map[string]string{"h264": "h264_qsv", "hevc": "hevc_qsv", "av1": "av1_qsv"}, filters: []string{"scale_qsv", "vpp_qsv"}},
		hwSpec{kind: KindNVENC, name: "NVIDIA NVENC", encoders: map[string]string{"h264": "h264_nvenc", "hevc": "hevc_nvenc", "av1": "av1_nvenc"}, filters: []string{"scale_cuda"}},
	)

	// 解码测试要真文件：用 libx264/libx265 各生成 1 秒小样。
	testFiles := map[string]string{}
	if !opts.SkipDecode && caps.Software.Available {
		for codec, enc := range map[string]string{"h264": "libx264", "hevc": "libx265"} {
			if !caps.Software.Encode[codec] {
				continue
			}
			path := filepath.Join(opts.WorkDir, "probe-"+codec+".mkv")
			if ok, _ := p.smoke("-y", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25", "-t", "1",
				"-c:v", enc, "-preset", "ultrafast", path); ok {
				testFiles[codec] = path
			}
			defer func() { _ = os.Remove(path) }()
		}
	}

	for _, spec := range hw {
		b := Backend{Kind: spec.kind, Name: spec.name, Device: spec.device,
			Encode: map[string]bool{}, Decode: map[string]bool{}}
		for _, f := range spec.filters {
			if hasFilter(f) {
				b.Filters = append(b.Filters, f)
			}
		}
		anyEncoder := false
		for codec, encName := range spec.encoders {
			if !hasEncoder(encName) {
				continue
			}
			anyEncoder = true
			args := []string{"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25", "-t", "1"}
			if spec.device != "" {
				args = append([]string{"-vaapi_device", spec.device}, args...)
			}
			args = append(args, "-vf", "format=nv12,hwupload", "-c:v", encName)
			if spec.kind == KindVAAPI {
				args = append(args, "-rc_mode", "CQP", "-qp", "23")
			} else {
				args = append(args, "-b:v", "4M")
			}
			args = append(args, "-f", "null", "-")
			if ok, reason := p.smoke(args...); ok {
				b.Encode[codec] = true
				if spec.kind == KindVAAPI {
					// 码率模式挨个试：本机实测 CQP 与 CBR 都能用，但不同驱动差别很大，
					// 所以不靠猜，直接各跑一次。
					if okCBR, _ := p.smoke("-vaapi_device", spec.device, "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25",
						"-t", "1", "-vf", "format=nv12,hwupload", "-c:v", encName,
						"-rc_mode", "CBR", "-b:v", "4M", "-maxrate", "4M", "-bufsize", "8M", "-f", "null", "-"); okCBR {
						b.Quality = appendUnique(b.Quality, "cbr")
					}
					b.Quality = appendUnique(b.Quality, "cqp")
				}
			} else {
				b.Notes = append(b.Notes, encName+" 真跑失败："+reason)
			}
		}
		if !anyEncoder {
			b.Notes = append(b.Notes, "ffmpeg 里没有对应的硬件编码器")
		}
		// 解码：拿真文件试
		if spec.device != "" && len(testFiles) > 0 {
			for codec, path := range testFiles {
				ok, reason := p.smoke("-vaapi_device", spec.device, "-hwaccel", string(spec.kind),
					"-hwaccel_output_format", string(spec.kind), "-i", path, "-f", "null", "-")
				b.Decode[codec] = ok
				if !ok {
					b.Notes = append(b.Notes, "解 "+codec+" 失败："+reason)
				}
			}
		}
		b.Available = len(b.Encode) > 0 && anyEncoder
		caps.Backends = append(caps.Backends, b)
	}

	for _, b := range caps.Backends {
		if !b.Available && len(b.Encode) == 0 {
			caps.Warnings = append(caps.Warnings,
				fmt.Sprintf("%s 不可用（编码器在列表里但真跑不过，通常是没硬件或驱动没装）", b.Name))
		}
	}
	caps.ElapsedMS = time.Since(start).Milliseconds()
	caps.ProbedAt = time.Now()
	return caps, nil
}

// ---------------------------------------------------------------- 内部

type prober struct {
	ffmpeg  string
	dir     string
	timeout time.Duration
	ctx     context.Context
}

// run 跑一个只读查询（-version / -encoders 之类），把 stdout 返回。
func (p *prober) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.ffmpeg, append([]string{"-hide_banner", "-nostdin"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// smoke 真跑一次，返回是否成功，以及失败时最后一行 stderr（给人看的理由）。
//
// 失败理由一定要留：能力表上写「QSV 不可用」没人知道为什么，
// 写「h264_qsv 真跑失败：[vost#0:0] Terminating thread with return code -22」
// 才说得清是驱动问题还是设备问题。
func (p *prober) smoke(args ...string) (bool, string) {
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.ffmpeg, append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, ""
	}
	msg := lastLine(stderr.String())
	if msg == "" {
		msg = err.Error()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg = "超时（" + p.timeout.String() + "）"
	}
	return false, truncate(msg, 160)
}

// parseHWAccels 解析 `ffmpeg -hwaccels`。
func parseHWAccels(out string) []string {
	var list []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Hardware acceleration") {
			continue
		}
		list = append(list, line)
	}
	return list
}

// parseNamedList 解析 `-encoders` / `-filters` 那种表格：取每行的名字列。
//
// 两种表格长得不一样：
//
//	 V....D h264_vaapi    H.264/AVC (VAAPI) (codec h264)     ← 编码器
//	 T.. scale_vaapi      V->V    Scale VAAPI surface       ← 滤镜
//
// 共同点是「名字是这一行里第一个不含箭头、也不像标记位的词」，用正则取比按列切稳。
var nameField = regexp.MustCompile(`(?m)^\s*[A-Z.]{2,6}\s+(\S+)\s`)

func parseNamedList(out string) []string {
	var list []string
	for _, m := range nameField.FindAllStringSubmatch(out, -1) {
		if len(m) < 2 {
			continue
		}
		name := m[1]
		// 表格里有一行是分隔线（`V..... ==================` / `T.. =  =->=`），
		// 它的第二列是等号，会被当成名字解出来 —— 排除掉。
		if strings.HasPrefix(name, "=") {
			continue
		}
		list = append(list, name)
	}
	return list
}

func listDevices() []string {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, filepath.Join("/dev/dri", e.Name()))
	}
	return out
}

func intersect(hay []string, needles []string) []string {
	var out []string
	for _, n := range needles {
		if contains(hay, n) {
			out = append(out, n)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	if contains(list, v) {
		return list
	}
	return append(list, v)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
