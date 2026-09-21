//go:build !windows

package stream

import (
	"errors"
	"os/exec"
	"syscall"
)

// pauseProcess / resumeProcess 让 ffmpeg 歇一会儿（节流用）。
//
// 为什么不是 Jellyfin 那套「往 stdin 写 p / u 按键」：**实测无效**。
// Debian 的 ffmpeg 7.1.5 在 stdin 是管道时完全不处理按键（显式加 `-stdin` 也一样：
// 帧数一路涨，我们连测了三种写法）。而 SIGSTOP 唯一的顾虑是「暂停时会留下
// 半写分片」—— 这一点我们用 `-hls_flags temp_file`（分片先写临时文件、写完才改名）
// 已经堵住了：客户端只会看到完整分片，暂停期间不会出现截断的字节。
//
// 进程被 SIGSTOP 后 /proc/<pid>/status 的 State 是 T —— 验收脚本就是靠这个
// 确认「真的停了」，而不只是我们单方面记了个状态位。
func pauseProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("ffmpeg 还没启动")
	}
	return cmd.Process.Signal(syscall.SIGSTOP)
}

func resumeProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("ffmpeg 还没启动")
	}
	return cmd.Process.Signal(syscall.SIGCONT)
}
