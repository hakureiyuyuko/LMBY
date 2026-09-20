// Package ffmpeg 封装对外部 ffmpeg / ffprobe 可执行文件的探测与调用。
//
// M0 只做「能不能用」的探测；M4 会扩展成完整的能力矩阵
// （encoders / filters / 真跑小样验证）。
package ffmpeg

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Info 描述本机 ffmpeg 的可用性，供 /healthz 与转码模块使用。
type Info struct {
	Path      string   `json:"path"`
	Available bool     `json:"available"`
	Version   string   `json:"version"`
	HWAccels  []string `json:"hw_accels"`
	Error     string   `json:"error,omitempty"`
}

// Detect 运行 ffmpeg 探测版本与硬件加速后端。
//
// 注意：这里只是「列出来有什么」，并不能证明硬件后端真的能用 ——
// 真正的可用性验证（跑一小段转码）属于 M4。
func Detect(ctx context.Context, path string) Info {
	if strings.TrimSpace(path) == "" {
		path = "ffmpeg"
	}
	info := Info{Path: path}

	resolved, err := exec.LookPath(path)
	if err != nil {
		info.Error = "找不到 ffmpeg 可执行文件: " + err.Error()
		return info
	}
	info.Path = resolved

	verCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(verCtx, resolved, "-hide_banner", "-version").Output()
	if err != nil {
		info.Error = "执行 ffmpeg -version 失败: " + err.Error()
		return info
	}
	info.Available = true
	info.Version = parseVersion(string(out))

	hwCtx, cancelHW := context.WithTimeout(ctx, 10*time.Second)
	defer cancelHW()

	hwOut, err := exec.CommandContext(hwCtx, resolved, "-hide_banner", "-hwaccels").Output()
	if err != nil {
		// 版本能拿到就算可用，硬件列表拿不到不算致命。
		return info
	}
	info.HWAccels = parseHWAccels(string(hwOut))
	return info
}

// parseVersion 从 `ffmpeg -version` 输出里取第一行的版本号。
func parseVersion(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	fields := strings.Fields(line)
	if len(fields) >= 3 && fields[0] == "ffmpeg" && fields[1] == "version" {
		return fields[2]
	}
	return strings.TrimSpace(line)
}

// parseHWAccels 从 `ffmpeg -hwaccels` 输出里取后端名列表。
//
// 输出形如：
//
//	Hardware acceleration methods:
//	vdpau
//	cuda
//	vaapi
//	qsv
func parseHWAccels(out string) []string {
	var accels []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		accels = append(accels, line)
	}
	return accels
}
