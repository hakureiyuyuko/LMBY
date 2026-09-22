package livetv

import "strings"

// InputArgs 按源地址的协议给出 ffmpeg / ffprobe 的**输入参数**（放在 -i 之前）。
//
// 播放（internal/stream 的直播会话）与频道探测（internal/livetvsync）共用这一份：
// 两边各写一套的话，「播得起来但探不通」（或反过来）迟早变成常态，
// 而这类不一致最难查。
//
// 取值不是拍脑袋（IPTV 单播源实测）：
//   - rtsp 走 `-rtsp_transport tcp`：默认的 UDP 会偶发丢包花屏；
//   - rtsp 的 `-timeout`（微秒）是**读超时**：源站抽风时不给它，ffmpeg 会一直挂住
//     （注意 `-rw_timeout` 对 rtsp 无效，ffmpeg 会报 Option not found —— 踩过）；
//   - rtmp 用 `-rtmp_live live` 说明这是直播流，免得 ffmpeg 按点播去 seek。
//
// headers 是 m3u 里 `地址|User-Agent=xxx&Referer=yyy` 那种写法解析出来的自定义请求头，
// 原样透给 ffmpeg（有的源站按 UA/Referer 拦）。
func InputArgs(rawurl, headers string) []string {
	var args []string
	switch Kind(rawurl) {
	case "rtsp":
		args = append(args, "-rtsp_transport", "tcp", "-timeout", "15000000")
	case "rtmp":
		args = append(args, "-rtmp_live", "live")
	}
	if h := strings.TrimSpace(headers); h != "" {
		args = append(args, "-headers", h)
	}
	return args
}
