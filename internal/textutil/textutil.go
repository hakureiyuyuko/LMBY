// Package textutil 收敛「不受控文本在交给数据库 / 界面之前」必须做的两件事。
//
// 背景（2026-09-25 在验证实例上踩到的真事故）：数据库是 **UTF-8** 编码的
// PostgreSQL，写进去的字符串里有非法字节就直接 `22021 invalid byte sequence for
// encoding "UTF8"`。文本有两条来路会带坏字节：
//
//  1. **文件系统**：SMB / NTFS / 网盘共享上的文件名可以不满足 UTF-8
//     （客户端用 `iocharset=utf8` 挂载时是原样透传字节的）。路径入库失败时，
//     伤的不只是那个文件 —— `AddScanIssues` 是整批写的，一条坏路径会让**整批**
//     扫描问题写不进去，扫描被判定为「失败」。
//  2. **按字节截断**：`s[:n]` 会从多字节字符中间切开，切坏了同样是非法字节。
//     `ffprobe` 的 stderr 尾巴、HTTP 响应体、User-Agent 都属于这一类。
//
// 因此这里的两个函数是**成对使用**的：
//
//	Truncate —— 生产短摘要（保证 rune 对齐 + 输出合法 UTF-8）
//	Valid    —— 入库前兜底（把非法字节换成 U+FFFD）
//
// 纪律：**任何**来自文件系统或外部进程的字符串，进数据库之前都要过一遍 Valid；
// 任何「截短」都不要手写 `s[:n]`，用 Truncate。
package textutil

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// replacement 是非法字节的替身。用 U+FFFD（替换字符）而不是删掉：
// 界面上能看出「这里有个字符是坏的」，而不是让两个词诡异地粘在一起。
const replacement = "\uFFFD"

// invalidBytesShown 是 FirstInvalid 最多展示的字节数：首字节 + 让它不成立的那一个。
const invalidBytesShown = 2

// Valid 把 s 里所有非法 UTF-8 字节换成 U+FFFD，保证结果是合法 UTF-8。
//
// 已经是合法 UTF-8 时原样返回（不分配新字符串），所以可以放心地放在热路径上。
func Valid(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, replacement)
}

// Truncate 把 s 截到最多 n 字节，并且保证：
//   - 不切断多字节字符（在 rune 边界上切）；
//   - 返回的一定是合法 UTF-8（非法字节会被换成 U+FFFD）；
//   - 真截了才追加一个省略号「…」。
//
// n <= 0 表示不截断。注意返回值可能比 n 多 3 个字节（省略号本身）。
//
// 为什么不做 `s[:n]`：`s[:n]` 会把 UTF-8 从中间切开，得到非法字节 —— 写进库就是
// `22021`，写进界面就是乱码。这个函数存在的全部理由就是别让任何人再手写 `s[:n]`。
func Truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return Valid(s)
	}
	cut := n
	// 回退到「下一字节是某个 rune 的首字节」的位置：s[:cut] 才是完整的序列边界。
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return Valid(s[:cut]) + "…"
}

// FirstInvalid 返回 s 里第一段非法 UTF-8 字节的十六进制写法（如 `0xde 0x20`），
// 没有非法字节时返回空串。
//
// 只取「首字节 + 紧跟的 1 个字节」：前者是那个不该出现的 lead 字节，后者是让这个序列
// 不成立的那个字节 —— 恰好是 PostgreSQL 在 `22021` 里报的那两个（实测 `0xde 0x20`），
// 照抄进问题描述里，用户要么能对上服务器的日志，要么能 `ls | xxd` 找到那个文件。
// 多取字节没意义：后面往往是正常的文件名文本（"y.mkv" 这种），只会干扰判断。
func FirstInvalid(s string) string {
	if utf8.ValidString(s) {
		return ""
	}
	// 找到第一个非法字节的位置：逐 rune 前进，遇到 RuneError 且宽度为 1 就是它。
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			end := i + invalidBytesShown
			if end > len(s) {
				end = len(s)
			}
			var b strings.Builder
			for j, c := range []byte(s[i:end]) {
				if j > 0 {
					b.WriteByte(' ')
				}
				fmt.Fprintf(&b, "0x%02x", c)
			}
			return b.String()
		}
		i += size
	}
	return ""
}
