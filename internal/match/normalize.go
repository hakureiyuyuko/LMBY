// Package match 负责把「本地媒体条目」与「元数据提供方（TMDB）候选」对上号。
//
// 三条设计约束：
//
//  1. **只用标准库、纯函数、可离线单测**。打分器不该依赖网络与数据库，
//     否则真实库里的那些「钢之炼金术师 FULLMETAL ALCHEMIST」根本没法回归测试。
//  2. **结果必须可解释**。每一项打分都带「名称 / 权重 / 得分 / 说明」，
//     界面才能回答用户「为什么自动匹配到了这一条」「为什么这条只进了人工队列」。
//  3. **阈值以上自动、以下进人工、两者之间靠领先幅度**。
//     TMDB 搜索经常返回同名重制版/续作（《钢之炼金术师》2003 与 2009 FA），
//     光看绝对分数会选错，所以还要求「明显领先第二名」才敢自动落锤。
package match

import (
	"strings"
	"unicode"
)

// 连接性助词：中日文标题互译时会自由增删（日文「言の葉の庭」↔ 中文「言叶之庭」），
// 所以当成噪声直接丢掉，两边都丢才是对称的。
//
// 丢掉之后，这一对会**完全相等**，而不是靠二元组勉强拿到 0.75 ——
// 这是实测踩出来的：原本只丢 の，结果《言叶之庭》对《言の葉の庭》只拿 0.75，
// 落到人工队列里去了。
const particleRunes = "のノ之的"

// Normalize 把标题折成便于比较的形式，顺序是：
//
//  1. 兼容字符归一（全角 ASCII → 半角、带圈数字 → 数字、罗马数字 → 拉丁字母）
//  2. 丢掉连接性助词「の / ノ / 之 / 的」（见 particleRunes）
//  3. 异体字折叠（繁体、日文旧字体 → 常用形，见 variants.go）
//  4. 大小写折叠，标点与空白都变成单个空格分隔符
//
// 结果保留空格作为分词边界（「FULLMETAL ALCHEMIST」是两个词，不能粘成一个）。
func Normalize(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false

	for _, r := range s {
		if strings.ContainsRune(particleRunes, r) {
			continue
		}
		for _, fr := range foldCompat(r) {
			if v, ok := variantFold[fr]; ok {
				fr = v
			}
			if isWordRune(fr) {
				if pendingSpace && b.Len() > 0 {
					b.WriteByte(' ')
				}
				pendingSpace = false
				b.WriteRune(unicode.ToLower(fr))
				continue
			}
			pendingSpace = true
		}
	}
	return b.String()
}

// compatFold 是 NFKC 里对影视标题有意义的那部分（标准库没有 NFKC）。
//
// 为什么不引 golang.org/x/text：这个包要能在任何机器上纯离线跑单测，
// 而且这点折叠手写只有二十来行，比多一个依赖划算（见 docs/ADR/0001-stdlib-first.md）。
var compatFold = map[rune]string{
	// 罗马数字：日本动画标题的续作常用 Ⅱ、Ⅳ（《ソードアート・オンラインⅡ》）
	'Ⅰ': "I", 'Ⅱ': "II", 'Ⅲ': "III", 'Ⅳ': "IV", 'Ⅴ': "V", 'Ⅵ': "VI",
	'Ⅶ': "VII", 'Ⅷ': "VIII", 'Ⅸ': "IX", 'Ⅹ': "X", 'Ⅺ': "XI", 'Ⅻ': "XII",
	'ⅰ': "i", 'ⅱ': "ii", 'ⅲ': "iii", 'ⅳ': "iv", 'ⅴ': "v", 'ⅵ': "vi",
	'ⅶ': "vii", 'ⅷ': "viii", 'ⅸ': "ix", 'ⅹ': "x", 'ⅺ': "xi", 'ⅻ': "xii",
	// 带圈数字：部分 BDRip 命名会用 ⑦ 之类标序号
	'⓪': "0", '①': "1", '②': "2", '③': "3", '④': "4",
	'⑤': "5", '⑥': "6", '⑦': "7", '⑧': "8", '⑨': "9", '⑩': "10",
}

// foldCompat 把单个字符展开成它的兼容形式（大多是一对一，罗马数字是一对多）。
func foldCompat(r rune) string {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E: // 全角 ASCII
		return string(r - 0xFEE0)
	case r == 0x3000: // 全角空格
		return " "
	case r == '・' || r == '･': // 中点（日文标题常见分隔符）
		return " "
	}
	if s, ok := compatFold[r]; ok {
		return s
	}
	return string(r)
}

// isWordRune 决定一个字符是「词内字符」还是分隔符。
// 中日文、假名、韩文、拉丁字母、数字都算词内字符；标点、括号、空格算分隔符。
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// Compact 返回去掉空格后的归一标题。
func Compact(s string) string {
	return strings.ReplaceAll(Normalize(s), " ", "")
}

// compactOf 取归一标题的紧凑形式，假定入参已经过 Normalize。
func compactOf(normalized string) []rune {
	out := make([]rune, 0, len(normalized))
	for _, r := range normalized {
		if r != ' ' {
			out = append(out, r)
		}
	}
	return out
}

// tokensOf 把归一标题切成 token 列表：
// 拉丁字母/数字的连续段算一个词，中日韩文字逐字算一个 token
// （中文没有词边界，逐字比乱切词稳）。
func tokensOf(normalized string) []string {
	var out []string
	var word []rune

	flush := func() {
		if len(word) > 0 {
			out = append(out, string(word))
			word = word[:0]
		}
	}
	for _, r := range normalized {
		switch {
		case r == ' ':
			flush()
		case isCJKRune(r):
			flush()
			out = append(out, string(r))
		default:
			word = append(word, r)
		}
	}
	flush()
	return out
}

// isCJKRune 判断是否中日韩文字（这类文字逐字成 token）。
func isCJKRune(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Bopomofo, unicode.Hangul)
}
