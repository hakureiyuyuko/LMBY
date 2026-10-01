// Package subtitle 处理外挂字幕的**内容**（命名归 internal/parser 管）。
//
// 这里刻意只放纯函数：内容转换最容易出错，也最值得离线测 ——
// 跟 internal/parser 一样的思路，跑单测不需要 ffmpeg、不需要网络。
package subtitle

import (
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/textutil"
)

// IsUTF8 判断字节流是不是合法 UTF-8。
//
// 老外挂字幕大量是 GB18030（也有 Big5），而浏览器和前端 libass 都只吃 UTF-8，
// 所以下发前必须过这一关，否则界面上就是一片乱码。
//
// 这里复用 internal/textutil 的判定（FirstInvalid 返回空串就说明没有坏字节），
// 和入库前净化用的是同一套标准 —— 免得两处对「什么算非法字节」理解不一致。
func IsUTF8(b []byte) bool { return textutil.FirstInvalid(string(b)) == "" }

// DeliverySuffix 返回这种格式该以什么形态下发：
//
//	ass / ssa → "ass"（原样给前端 libass 渲染，保留特效与排版）
//	srt / vtt → "vtt"（转成 WebVTT 给浏览器原生轨道）
//
// 位图字幕（sup/sub/idx）不在本版范围内，调用方不该走到这里。
func DeliverySuffix(format string) string {
	switch format {
	case "ass", "ssa":
		return "ass"
	default:
		return "vtt"
	}
}

// SRTToVTT 把 SRT 文本转成 WebVTT。
//
// 只动两处：加 WEBVTT 头、把**时间行**里的逗号换成点。
// 不能全局替换逗号 —— 那会把正文里的「你好，世界」一起改掉。
// 序号行 WebVTT 是允许保留的，所以不删。
func SRTToVTT(b []byte) []byte {
	s := strings.TrimPrefix(string(b), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, "-->") {
			lines[i] = strings.ReplaceAll(ln, ",", ".")
		}
	}
	return []byte("WEBVTT\n\n" + strings.Join(lines, "\n"))
}
