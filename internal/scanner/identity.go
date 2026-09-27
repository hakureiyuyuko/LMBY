package scanner

import (
	"strconv"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------- 扫描器身份
//
// 背景（2026-09-27 在验证实例上查清的真 bug）：条目身份原本是
// `(库, 类型, 标题, 年份)`，而**标题与年份都会被 nfo / 刮削改写**
//（internal/store/items.go 的 applyItemMetaSQL），扫描器却拿**目录名解析出来的标题**
// 去查 —— nfo 标题与目录名一旦不同（`公主连结！ReDive` vs `Re:Dive`、
// `战姬绝唱` vs `战姬绝唱Symphogear`），下次查身份就查不到自己，凭空再造一份。
//
// 解法：把「第一次见到它时用的身份」原样存进 media_items.scan_key，此后只由扫描器
// 读写，元数据碰不到。这里放的就是那个身份的计算与「认领老条目」的相似度判定。

// scanKey 是扫描器的身份串：与内存去重键同构（`lower(标题) + '|' + 年份`）。
//
// 空标题返回空串：那种条目不该有身份（交给「认领」或新建处理）。
func scanKey(title string, year int) string {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return ""
	}
	return title + "|" + strconv.Itoa(year)
}

// normalizeTitle 把标题归一化到「只留字母 / 数字 / 汉字假名」，用于相似度比较。
//
// 只做三件事，不做语义处理：
//  1. 去掉空白（含全角空格）；
//  2. 去掉一批**排版性**标点（半角与全角的冒号、惊叹号、问号、逗号、句号、中点、
//     波浪线、破折号、斜杠）—— 它们在不同来源里写法不同（`Re:Dive` / `Re：Dive`）；
//  3. 小写化。
//
// 之所以不能用 SQL 做：PostgreSQL 的 `[:alnum:]` 在 C.UTF-8 下不认 CJK，
// 按「非字母数字」一刀切会把中文标题全抹成空串。所以放在 Go 里，逐 rune 判。
func normalizeTitle(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	for _, r := range title {
		switch {
		case unicode.IsSpace(r):
			continue
		case r < 0x80:
			if strings.ContainsRune(asciiTitlePunct, r) {
				continue
			}
			b.WriteRune(unicode.ToLower(r))
		case strings.ContainsRune(cjkTitlePunct, r):
			continue
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

const (
	// asciiTitlePunct 是归一化时丢掉的半角排版标点。
	asciiTitlePunct = ":!?.,~-/·—'\"`()[]{}<>＋+&＊*"
	// cjkTitlePunct 是对应的全角 / CJK 版（冒号、惊叹号、问号、逗号、句号、中点、
	// 波浪线、破折号、斜杠、全角空格）。
	cjkTitlePunct = "：！？，。、・～〜－—／·　（）【】《》「」"
)

// identityCandidate 是「认领」判定用的候选（来自库里已有的剧集 / 电影）。
type identityCandidate struct {
	ID      int64
	Title   string
	Year    *int32
	ScanKey string
}

// pickClaim 在候选里挑出「就是同一部作品」的那一个，用于查找顺序的第③级。
//
// 只在**恰好一个**候选命中时返回它 —— 这一级是给「标题已被元数据改写、而且它还没有
// scan_key」的老条目兜底的，宁可漏认也不能错认（错认会把两部作品合并成一部）。
//
// 命中条件（两条都要）：
//  1. 归一化标题相同，或互为前缀 / 后缀；或**词集相同**（`ONE～辉之季节～` 与
//     `辉之季节／ONE` 这种词序不同的写法）；
//  2. 年份相容：任一方没有年份，或相差不超过 claimYearSlack 年
//     （年份会被 nfo 改，实测有 1~3 年的差；而完全不搭的年份说明是另一部作品）。
func pickClaim(cands []identityCandidate, title string, year int) (identityCandidate, bool) {
	want := normalizeTitle(title)
	if want == "" {
		return identityCandidate{}, false
	}
	var hit identityCandidate
	n := 0
	for _, c := range cands {
		if !titleSimilar(want, normalizeTitle(c.Title)) {
			continue
		}
		if !yearCompatible(year, c.Year) {
			continue
		}
		hit = c
		n++
	}
	if n != 1 {
		return identityCandidate{}, false
	}
	return hit, true
}

// claimYearSlack 是「认领」时允许的年份差（实测 nfo 改年份差 1~3 年）。
const claimYearSlack = 5

func yearCompatible(want int, have *int32) bool {
	if want == 0 || have == nil || *have == 0 {
		return true // 有一边不知道年份 → 不拿年份否决
	}
	diff := int(*have) - want
	if diff < 0 {
		diff = -diff
	}
	return diff <= claimYearSlack
}

// titleSimilar 判断两个**已归一化**的标题是不是同一部作品。
func titleSimilar(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// 互为前缀 / 后缀：`战姬绝唱` vs `战姬绝唱symphogear`、`buddycomplex` vs
	// `心灵盟友buddycomplex`。要求短的那个至少 2 个字，免得单字命中一大片。
	if len(a) >= 6 && strings.Contains(b, a) {
		return true
	}
	if len(b) >= 6 && strings.Contains(a, b) {
		return true
	}
	// 词集相同（顺序不同）：`one～辉之季节～` vs `辉之季节／one`。
	// 只在两边都是「多词」时才用，避免把单个长串误判成词集相同。
	at, bt := titleTokens(a), titleTokens(b)
	if len(at) >= 2 && len(at) == len(bt) && sameTokenSet(at, bt) {
		return true
	}
	return false
}

// titleTokens 把归一化标题切成「词」：CJK 每段算一个词，拉丁字母 / 数字连成一段。
//
// 目的只有一个：让 `one～辉之季节～` 与 `辉之季节／one` 得到相同的词集。
// 归一化已经把标点去掉了，所以这里只能按「字符类别切换」来切。
func titleTokens(s string) []string {
	var out []string
	var cur strings.Builder
	curLatin := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		latin := unicode.In(r, unicode.Latin, unicode.Digit)
		if cur.Len() > 0 && latin != curLatin {
			flush()
		}
		curLatin = latin
		cur.WriteRune(r)
	}
	flush()
	return out
}

func sameTokenSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, t := range a {
		seen[t]++
	}
	for _, t := range b {
		seen[t]--
		if seen[t] < 0 {
			return false
		}
	}
	return true
}
