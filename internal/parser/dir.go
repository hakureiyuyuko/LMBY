package parser

import (
	"regexp"
	"strings"
)

// DirInfo 是从一个目录名推断出的信息。
type DirInfo struct {
	Raw string

	// Category 是被「」『』【】包裹的分类目录名（去掉括号）。
	// 本媒体库用它区分「连载动画 / 完结剧集 / 高清电影」等分类。
	Category   string
	IsCategory bool

	// IsSeason 表示这是季目录，Season 为季号（0 表示特典季）。
	IsSeason bool
	Season   int

	// Title / Year 来自 `名称 (年份)` 形式的目录。
	Title string
	Year  int

	// IsExtra 表示这是花絮目录，ExtraType 是归一化后的类型。
	IsExtra   bool
	ExtraType string

	// IsImageDir 表示这是 Backdrops / Screenshots 之类只放图的目录，整棵子树可跳过。
	IsImageDir bool
}

// 目录名里的分类括号
var reCategory = regexp.MustCompile(`^[「『【\[]\s*(.+?)\s*[」』】\]]$`)

// 花絮目录关键词 → 归一化类型
var extraDirPatterns = []struct {
	Type     string
	Keywords []string
}{
	{"trailer", []string{"trailer", "trailers", "预告", "预告片", "pv"}},
	{"behindthescenes", []string{"behind the scenes", "behindthescenes", "花絮", "制作特辑", "making of"}},
	{"deleted", []string{"deleted scenes", "deleted", "删减", "删减片段"}},
	{"interview", []string{"interview", "interviews", "访谈", "采访"}},
	{"featurette", []string{"featurette", "featurettes", "映像特典", "特典映像"}},
	{"short", []string{"short", "shorts", "短片"}},
	{"scene", []string{"scene", "scenes", "片段"}},
	{"sample", []string{"sample", "samples", "样片", "试看"}},
	{"other", []string{"extras", "extra", "附加", "其他"}},
}

// extraDirNames 是 Emby/Kodi 约定的花絮目录名（大小写不敏感）。
//
// 刻意**不含** specials / special / 特典 —— 在剧集层级它们是「特典季」，
// 是 Season 00，而不是花絮。「映像特典」「特典映像」才算花絮。
var extraDirNames = map[string]bool{
	"extras": true, "extra": true, "special features": true, "featurettes": true,
	"behind the scenes": true, "deleted scenes": true, "interviews": true,
	"scenes": true, "shorts": true, "trailers": true, "other": true,
	"花絮": true, "映像特典": true, "特典映像": true, "预告片": true,
}

// imageOnlyDirs 是只放图片的目录名，扫描时整棵跳过。
var imageOnlyDirs = map[string]bool{
	"backdrops": true, "backdrop": true, "screenshots": true, "screenshot": true,
	"artwork": true, "art": true, "extrafanart": true, "thumbs": true,
}

// ParseDir 解析单个目录名。
func ParseDir(name string) DirInfo {
	info := DirInfo{Raw: name, Season: NoSeason}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return info
	}

	// 分类目录： 「高清电影」
	if m := reCategory.FindStringSubmatch(trimmed); m != nil {
		info.Category = strings.TrimSpace(m[1])
		info.IsCategory = true
	}

	lower := strings.ToLower(trimmed)

	if imageOnlyDirs[lower] {
		info.IsImageDir = true
		return info
	}

	// ---- 顺序很重要：先判季，再判花絮。
	// 否则 "Specials" 会被 extraDirNames 抢走，Season 00 就丢了。
	switch {
	case lower == "specials" || lower == "special" || lower == "sp" ||
		trimmed == "特典" || trimmed == "特别篇" || trimmed == "特典篇":
		info.IsSeason = true
		info.Season = 0
		return info
	case strings.HasPrefix(lower, "season"):
		rest := strings.TrimSpace(strings.TrimPrefix(lower, "season"))
		rest = strings.Trim(rest, " ._-")
		if n, ok := atoiOr(rest); ok {
			info.IsSeason = true
			info.Season = n
			return info
		}
	}
	if m := reSeasonOnly.FindStringSubmatch(trimmed); m != nil {
		if n, ok := atoiOr(m[1]); ok {
			info.IsSeason = true
			info.Season = n
			return info
		}
	}

	// ---- 花絮目录
	if extraDirNames[lower] {
		info.IsExtra = true
		info.ExtraType = ExtraTypeOf(trimmed)
		return info
	}
	if et := ExtraTypeOf(trimmed); et != "" && !strings.Contains(trimmed, "(") {
		// 名字里含明确的花絮关键词，且不是「作品名 (年份)」形式
		info.IsExtra = true
		info.ExtraType = et
		return info
	}

	// ---- `名称 (年份)` 或普通目录名
	info.Year = yearOf(trimmed)
	info.Title = cleanDirTitle(trimmed)
	return info
}

// cleanDirTitle 清洗从目录名得到的标题。
//
// 与文件名的 cleanTitle 不同：目录名本身就是作品名，尾部的 `-` `～！` 往往是
// 名字的一部分（`86-不存在的战区-`、`ENDRO～！`），所以**不能裁剪标点**。
func cleanDirTitle(name string) string {
	s := stripBracketGroups(name)
	s = reYear.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "　", " ")
	return strings.Join(strings.Fields(s), " ")
}

// ExtraTypeOf 返回名字里的花絮类型；不是花絮则返回空串。
func ExtraTypeOf(name string) string {
	lower := strings.ToLower(name)
	for _, p := range extraDirPatterns {
		for _, kw := range p.Keywords {
			if kw == "pv" {
				// "pv" 太短容易误命中，要求按词边界出现
				if matchesWord(lower, "pv") {
					return p.Type
				}
				continue
			}
			if strings.Contains(lower, kw) {
				return p.Type
			}
		}
	}
	return ""
}

// IsExtraName 判断文件名或目录名是否表示花絮。
func IsExtraName(name string) bool {
	if ExtraTypeOf(name) != "" {
		return true
	}
	return extraDirNames[strings.ToLower(strings.TrimSpace(name))]
}

// SeasonLabel 返回季目录的规范展示名，用于创建 season 条目。
func SeasonLabel(season int) string {
	if season <= 0 {
		return "特典"
	}
	return "第 " + itoa(season) + " 季"
}

// ---------------------------------------------------------------- 小工具

func atoiOr(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 999 {
			return 0, false
		}
	}
	return n, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
