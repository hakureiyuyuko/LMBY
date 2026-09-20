package match

import (
	"math"
	"strings"
)

// Similarity 返回两个标题的相似度（0~1），以及胜出的判定方式。
//
// 为什么是「多种简单指标取最大值」而不是单一算法：中文标题、英文标题、
// 中英混排（《钢之炼金术师 FULLMETAL ALCHEMIST》）在同一个算法下很难都表现好。
// 取最大值让每种形态都有自己擅长的路子；「哪种方式胜出」本身也是解释材料，
// 会原样显示在匹配结果里。
//
// 方式取值：equal（完全相同）、bigram（二元组 Dice）、token（分词集合 Jaccard）、
// contain（包含关系）、script（按文字体系分段加权）。
func Similarity(a, b string) (float64, string) {
	na, nb := Normalize(a), Normalize(b)
	if na == "" || nb == "" {
		return 0, "empty"
	}
	if na == nb {
		return 1, "equal"
	}

	ra, rb := compactOf(na), compactOf(nb)
	best, how := dice(ra, rb), "bigram"

	if t := jaccard(tokensOf(na), tokensOf(nb)); t > best {
		best, how = t, "token"
	}
	if c := containScore(ra, rb); c > best {
		best, how = c, "contain"
	}
	if s := scriptScore(na, nb); s > best {
		best, how = s, "script"
	}
	return best, how
}

// dice 是二元组（bigram）Dice 系数：2|A∩B| / (|A|+|B|)。
//
// 对中文特别合适 —— 中文没有词边界，但字与字的搭配很稳定：
// 「钢之炼金术师」与「鋼の錬金術師」折完字形后共享大部分二元组。
func dice(a, b []rune) float64 {
	if len(a) < 2 || len(b) < 2 {
		// 单字标题没有二元组：交给 token / contain 判，别在这里猜
		return 0
	}
	ga, gb := bigrams(a), bigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	var inter, totalA, totalB int
	for g, n := range ga {
		totalA += n
		if m, ok := gb[g]; ok {
			if m < n {
				inter += m
			} else {
				inter += n
			}
		}
	}
	for _, n := range gb {
		totalB += n
	}
	return 2 * float64(inter) / float64(totalA+totalB)
}

func bigrams(rs []rune) map[string]int {
	m := make(map[string]int, len(rs))
	for i := 0; i+1 < len(rs); i++ {
		m[string(rs[i:i+2])]++
	}
	return m
}

// jaccard 是集合 Jaccard 系数：|A∩B| / |A∪B|（先去掉重复 token）。
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	sa := make(map[string]struct{}, len(a))
	for _, t := range a {
		sa[t] = struct{}{}
	}
	sb := make(map[string]struct{}, len(b))
	for _, t := range b {
		sb[t] = struct{}{}
	}
	var inter int
	for t := range sa {
		if _, ok := sb[t]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// containScore 处理「一个是另一个的子串」：本地《冰菓》对 TMDB《冰菓 第二季》。
//
// 有下限（短的至少占长的 1/3）才给分 —— 否则《东京》会匹配上任何
// 带「东京」的标题，这种误报比漏配更糟。
func containScore(a, b []rune) float64 {
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) < 2 || len(short)*3 < len(long) {
		return 0
	}
	if !runesContains(long, short) {
		return 0
	}
	ratio := float64(len(short)) / float64(len(long))
	return 0.65 + 0.35*ratio
}

func runesContains(haystack, needle []rune) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}

// scriptScore 按文字体系分段比较，再按两边的公共长度加权。
//
// 解决的正是最难的那类：《钢之炼金术师 FULLMETAL ALCHEMIST》对原名的
// 《鋼の錬金術師 FULLMETAL ALCHEMIST》—— 汉字段有轻微用字差异，拉丁段完全一致。
// 整体二元组会被汉字差异拖低，分体系之后各算各的，总分就合理了。
//
// 关键细节：**某一体系只有一边有，要按「这边有、那边没有」计一笔 0 分**，
// 不能直接跳过。否则《钢之炼金术师》对《钢之炼金术师 FA》会因为
// 「汉字段完全相同、拉丁段被无视」拿到 1.0 —— 那就把 FA 与本体当成同一部了。
// 加了这条之后这一对降到 0.9 以下，正好交给年份与领先幅度去判。
func scriptScore(na, nb string) float64 {
	alat, acjk := splitScript(na)
	blat, bcjk := splitScript(nb)

	var sum, weight float64
	add := func(x, y []rune) {
		switch {
		case len(x) == 0 && len(y) == 0:
			return
		case len(x) == 0 || len(y) == 0:
			// 只有一边有这个体系：算一笔长度为 0 分的项，不能白送
			weight += float64(maxInt(len(x), len(y)))
			return
		}
		w := math.Min(float64(len(x)), float64(len(y)))
		s := math.Max(dice(x, y), jaccard(tokensOf(string(x)), tokensOf(string(y))))
		sum += w * s
		weight += w
	}
	add(alat, blat)
	add(acjk, bcjk)

	if weight == 0 {
		return 0
	}
	return sum / weight
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// splitScript 把归一标题按文字体系拆成「拉丁/数字」与「中日韩」两段（各自去空格）。
func splitScript(normalized string) (latin, cjk []rune) {
	for _, r := range normalized {
		switch {
		case r == ' ':
		case isCJKRune(r):
			cjk = append(cjk, r)
		default:
			latin = append(latin, r)
		}
	}
	return latin, cjk
}

// cleanupTitle 把字符串折成单行，用于日志与说明文字（避免把换行带进 JSON）。
func cleanupTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
