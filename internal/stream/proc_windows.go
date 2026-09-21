//go:build windows

package stream

import "os/exec"

// Windows 上 Go 的 Process.Signal 不支持暂停进程（没有 SIGSTOP 的等价物），
// 所以节流在这条路径上退化为「不节流」：报一个明确的错误，调用方记一次日志就不再
// 重试（不影响播放，只是转码会把窗口一口气生成完 —— 老行为）。
func pauseProcess(*exec.Cmd) error  { return errThrottleUnsupported }
func resumeProcess(*exec.Cmd) error { return errThrottleUnsupported }
