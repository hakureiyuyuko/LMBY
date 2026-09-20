package parser

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------- 正则表

var (
	// 季集标记。兼容 S01E02 / s1e2 / S01.E02 / S01E02E03 / 01E02 / E02。
	// 前缀用 (?:^|[^a-z0-9]) 而不是 \b，避免把 "Season01E02" 这类粘在一起的写法漏掉。
	reEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\s?(\d{1,2}))?[\s._-]*(?:e|ep|episode)\s?(\d{1,4})(?:[\s._-]*(?:e|ep)\s?(\d{1,4}))?`)

	// 1x02 形式。刻意限制前导 1~2 位数字且要求前一个字符不是数字，
	// 这样 "1920x1080" / "1280x720" 这类分辨率不会被误判成第 20 集。
	// 注意：Go 的 regexp 不支持 lookahead，所以用「消费一个非数字字符」代替。
	reAltEpisode = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,2})x(\d{1,3})(?:[^0-9x]|$)`)

	// 发布标签：属于噪音，不应混进标题
	reReleaseTag = regexp.MustCompile(`(?i)\b(WEB[\s._-]?DL|WEB[\s._-]?Rip|WEB|Blu[\s._-]?Ray|BDRip|BRRip|HDRip|DVDRip|DVDScr|HDTV|PDTV|REMUX|REPACK|PROPER|AAC|AC3|EAC3|DD[P+]?[\s._-]?5[\s._-]?1|DTS[\s._-]?HD|DTS|TrueHD|FLAC|MP3|OPUS|Atmos|CHS|CHT|BIG5)\b`)

	// 第 N 集 / 第 N 话 / 第 N 期
	reChineseEpisode = regexp.MustCompile(`第\s*(\d{1,4})\s*[集话話期]`)

	// 纯季目录标记
	reSeasonOnly = regexp.MustCompile(`(?i)^\s*(?:s|season|第)\s*(\d{1,2})\s*(?:季)?\s*$`)

	// 出现在季集标记之前的季信息。必须严格：「…30fps 15Mbps…」里的 "s 15"
	// 绝不能被当成 S15，所以只认 Season 3 / 第2季 / S03 三种明确写法。
	reSeasonWord  = regexp.MustCompile(`(?i)(?:season|第)\s*(\d{1,2})\s*季?`)
	reSeasonShort = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})(?:[^0-9]|$)`)

	// 前导集号：`02 - 标题.mkv`、`7.mkv`（仅在季目录里启用）
	reLeadingNumber = regexp.MustCompile(`^\s*(\d{1,3})\s*(?:[\s._\-–—:：]+(.*))?$`)

	// 独立的四位年份 token：某电影.2024.2160p.mkv
	reBareYear = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)

	// 年份：(2020) （2020） [2020]
	reYear = regexp.MustCompile(`[\(\[（]\s*(\d{4})\s*[\)\]）]`)

	// 书名号标题：《...》
	reBookTitle = regexp.MustCompile(`《\s*([^》]+?)\s*》`)

	// 版本修饰
	reEdition = regexp.MustCompile(`(?i)(director'?s?[\s._-]?cut|extended[\s._-]?(?:cut|edition)?|unrated|remastered|theatrical[\s._-]?cut|imax|special[\s._-]?edition|导演剪辑版|加长版|未删减版|修复版|最终版)`)

	// 分片标记 cd1 / disc2 / part3
	rePart = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:cd|disc|disk|part|pt)\s?(\d{1,2})(?:[^0-9]|$)`)

	// 技术标记
	reCodec     = regexp.MustCompile(`(?i)\b(HEVC|AVC|AV1|VP9|VP8|VCC|RV40|RV30|X26[45]|H\.?26[45]|MPEG-?2|MPEG-?4|VC-?1|DIVX|XVID|PRORES|THEORA)\b`)
	reRes       = regexp.MustCompile(`(?i)\b(480[pi]|576[pi]|720[pi]|1080[pi]|1440[pi]|2160[pi]|2K|4K|8K|SD|HD|FHD|UHD)\b`)
	reChroma    = regexp.MustCompile(`(?i)\bYUV\s?(\d{3})\s?P\s?(\d{1,2})\b`)
	reFrameRate = regexp.MustCompile(`(?i)\b(\d{2,3}(?:\.\d+)?)\s?fps\b`)
	reBitrate   = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s?(Mbps|Mb/s|Mbit|kbps|Kb/s)\b`)
)

// Tech 是从文件名里识别出的技术标记。
//
// 这些字段只作为「线索」存库；真正可信的流信息来自 ffprobe，两者都保留，
// 便于排查「为什么这个文件被判定为 10bit」之类的问题。
type Tech struct {
	Codec      string   `json:"codec,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	Chroma     string   `json:"chroma,omitempty"`
	FrameRate  string   `json:"frameRate,omitempty"`
	Bitrate    string   `json:"bitrate,omitempty"`
	Features   []string `json:"features,omitempty"`
}

// IsZero 判断是否没有识别出任何技术标记。
func (t Tech) IsZero() bool {
	return t.Codec == "" && t.Resolution == "" && t.Chroma == "" &&
		t.FrameRate == "" && t.Bitrate == "" && len(t.Features) == 0
}

// featureTokens 是「类型特征」关键词，来自媒体库的命名规范文档。
var featureTokens = []struct {
	Token    string
	Patterns []string
}{
	{"3D", []string{"3d", "3dtv", "hsbs", "sbs3d"}},
	{"HDR", []string{"hdr", "hdr10", "hdr10+", "hlg"}},
	{"杜比视界", []string{"杜比视界", "dolbyvision", "dolby.vision", "dv"}},
	{"杜比全景声", []string{"杜比全景声", "atmos", "dolbyatmos"}},
	{"10bit", []string{"10bit", "10-bit"}},
	{"12bit", []string{"12bit", "12-bit"}},
}

// ParseTech 从文件名里提取技术标记。
func ParseTech(name string) Tech {
	var t Tech

	if m := reCodec.FindString(name); m != "" {
		t.Codec = normalizeCodec(m)
	}
	if m := reRes.FindStringSubmatch(name); m != nil {
		t.Resolution = strings.ToUpper(m[1])
	}
	if m := reChroma.FindStringSubmatch(name); m != nil {
		t.Chroma = "YUV" + m[1] + "P" + m[2]
	}
	if m := reFrameRate.FindStringSubmatch(name); m != nil {
		t.FrameRate = m[1] + "fps"
	}
	if m := reBitrate.FindStringSubmatch(name); m != nil {
		unit := "Mbps"
		if strings.HasPrefix(strings.ToLower(m[2]), "k") {
			unit = "kbps"
		}
		t.Bitrate = m[1] + unit
	}

	lower := strings.ToLower(name)
	for _, f := range featureTokens {
		for _, p := range f.Patterns {
			if matchesWord(lower, p) {
				t.Features = append(t.Features, f.Token)
				break
			}
		}
	}
	return t
}

func normalizeCodec(s string) string {
	up := strings.ToUpper(strings.ReplaceAll(s, ".", ""))
	switch up {
	case "H264", "X264":
		return "AVC"
	case "H265", "X265":
		return "HEVC"
	case "MPEG4":
		return "MPEG-4"
	case "MPEG2":
		return "MPEG-2"
	case "VC1":
		return "VC-1"
	}
	return up
}

// matchesWord 做「词边界」包含判断，避免 "dv" 命中 "advance"。
func matchesWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	// 含非 ASCII 的关键词（中文）直接子串匹配
	if !isASCII(needle) {
		return strings.Contains(haystack, needle)
	}
	idx := 0
	for {
		i := strings.Index(haystack[idx:], needle)
		if i < 0 {
			return false
		}
		start := idx + i
		end := start + len(needle)
		beforeOK := start == 0 || !isAlnum(haystack[start-1])
		afterOK := end == len(haystack) || !isAlnum(haystack[end])
		if beforeOK && afterOK {
			return true
		}
		idx = start + 1
	}
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- 季集

type episodeMatch struct {
	Season  int
	Episode int
	End     int
	Start   int // 标记在字符串中的起始下标
	Stop    int // 标记结束下标（不含）
}

// findEpisodeMarker 依次尝试三种季集写法。
func findEpisodeMarker(name string) (episodeMatch, bool) {
	if m := reEpisode.FindStringSubmatchIndex(name); m != nil {
		em := episodeMatch{Season: NoSeason, Start: m[0], Stop: m[1]}
		if m[2] >= 0 {
			em.Season, _ = strconv.Atoi(name[m[2]:m[3]])
		}
		em.Episode, _ = strconv.Atoi(name[m[4]:m[5]])
		if m[6] >= 0 {
			em.End, _ = strconv.Atoi(name[m[6]:m[7]])
		}
		if em.Season == NoSeason {
			em.Season = seasonFromPrefix(name[:m[0]])
		}
		return em, true
	}
	if m := reAltEpisode.FindStringSubmatchIndex(name); m != nil {
		em := episodeMatch{Season: NoSeason, Start: m[0], Stop: m[1]}
		em.Season, _ = strconv.Atoi(name[m[2]:m[3]])
		em.Episode, _ = strconv.Atoi(name[m[4]:m[5]])
		return em, true
	}
	if m := reChineseEpisode.FindStringSubmatchIndex(name); m != nil {
		em := episodeMatch{Season: NoSeason, Start: m[0], Stop: m[1]}
		em.Episode, _ = strconv.Atoi(name[m[2]:m[3]])
		if em.Season == NoSeason {
			em.Season = seasonFromPrefix(name[:m[0]])
		}
		return em, true
	}
	return episodeMatch{}, false
}

// seasonFromPrefix 从季集标记之前的部分里找季号，处理
// 「Season 1 Episode 5」「第2季 第3集」这类写法。
func seasonFromPrefix(prefix string) int {
	if m := reSeasonWord.FindStringSubmatch(prefix); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 0 && n <= 99 {
			return n
		}
	}
	if m := reSeasonShort.FindStringSubmatch(prefix); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 0 && n <= 99 {
			return n
		}
	}
	return NoSeason
}

// titleAfterMarker 取季集标记之后的文字作为标题。
//
// `S01E02 - 某集标题.mkv` → 某集标题；`S01E01.mkv` → 空（标题由父目录补）。
func titleAfterMarker(name string, em episodeMatch) string {
	rest := name[em.Stop:]
	rest = strings.TrimLeft(rest, " ._-–—:：·、|")
	return cleanTitle(rest)
}

// leadingEpisodeNumber 处理 `02 - 标题.mkv` 这种「前导集号」写法，
// 只在已经确定处于季目录时才使用，否则会把 `2024.mkv` 误判成第 2024 集。
func leadingEpisodeNumber(name string) (int, string, bool) {
	m := reLeadingNumber.FindStringSubmatch(name)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 || n > 999 {
		return 0, "", false
	}
	return n, m[2], true
}

// ---------------------------------------------------------------- 标题清洗

// cleanTitle 去掉技术标记、括号补充与分隔符，得到可能的中文/原文标题。
func cleanTitle(s string) string {
	if s == "" {
		return ""
	}

	// 书名号只保留内容
	if m := reBookTitle.FindStringSubmatch(s); m != nil {
		s = m[1]
	}

	// 去掉纯技术性的方括号/圆括号分组，如 [1080p][HEVC]、（双语字幕）
	s = stripBracketGroups(s)

	// 去掉技术 token
	s = reReleaseTag.ReplaceAllString(s, " ")
	s = reCodec.ReplaceAllString(s, " ")
	s = reChroma.ReplaceAllString(s, " ")
	s = reFrameRate.ReplaceAllString(s, " ")
	s = reBitrate.ReplaceAllString(s, " ")
	s = reRes.ReplaceAllString(s, " ")
	s = reYear.ReplaceAllString(s, " ")
	s = reBareYear.ReplaceAllString(s, " ")
	s = rePart.ReplaceAllString(s, " ")
	s = reEdition.ReplaceAllString(s, " ")
	for _, f := range featureTokens {
		for _, p := range f.Patterns {
			s = replaceWord(s, p)
		}
	}

	// 归一分隔符
	s = strings.NewReplacer(
		".", " ", "_", " ", "　", " ",
	).Replace(s)
	s = strings.Trim(s, " -–—·、|~！!　\t")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

// stripBracketGroups 删除内容为纯技术信息的分组，保留像 (1998) 之外的有意义括号内容。
//
// 说明：不能无脑删括号 —— `LoveLive! 虹之咲学园偶像同好会「四格漫」 (2023)` 里的
// 年份要删，但 `大欺诈师／GREAT PRETENDER` 这种没有括号的不能动。
func stripBracketGroups(s string) string {
	var out strings.Builder
	var stack, openEnd []int // 每层括号的起始字节偏移，与「开括号之后」的偏移
	last := 0

	for i, r := range s {
		switch r {
		case '[', '【', '(', '（':
			stack = append(stack, i)
			openEnd = append(openEnd, i+utf8.RuneLen(r))
		case ']', '】', ')', '）':
			if len(stack) == 0 {
				// 孤立的右括号：原样保留
				out.WriteString(s[last:i])
				out.WriteRune(r)
				last = i + utf8.RuneLen(r)
				continue
			}
			start := stack[len(stack)-1]
			inner := s[openEnd[len(stack)-1]:i]
			stack = stack[:len(stack)-1]
			openEnd = openEnd[:len(openEnd)-1]
			if len(stack) > 0 {
				// 内层分组：等最外层一起处理
				continue
			}
			end := i + utf8.RuneLen(r)
			if isTechGroup(inner) {
				out.WriteString(s[last:start])
				out.WriteString(" ")
			} else {
				out.WriteString(s[last:end])
			}
			last = end
		}
	}
	out.WriteString(s[last:])
	return out.String()
}

func isTechGroup(inner string) bool {
	t := strings.TrimSpace(inner)
	if t == "" {
		return true
	}
	if reYear.MatchString("(" + t + ")") {
		return true
	}
	if reCodec.MatchString(t) || reRes.MatchString(t) || reChroma.MatchString(t) ||
		reFrameRate.MatchString(t) || reBitrate.MatchString(t) {
		return true
	}
	lower := strings.ToLower(t)
	for _, kw := range []string{
		"1080", "720", "2160", "480", "576", "hdr", "bluray", "blu-ray", "bdrip",
		"web-dl", "webrip", "hdrip", "dvdrip", "hdtv", "remux", "x264", "x265",
		"aac", "ac3", "dts", "flac", "sub", "chs", "cht", "简体", "繁体", "双语",
		"内封", "字幕", "无字幕", "gb", "国配", "国语", "日语", "粤语",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// yearOf 从名字里提取年份。
//
// 优先取括号形式 `(2020)`（Emby 约定），退而求其次取独立的四位数字 token
// （如 `某电影.2024.2160p.mkv`）。只接受 1900~2099，避免把集号之类当成年份。
func yearOf(s string) int {
	for _, m := range reYear.FindAllStringSubmatch(s, -1) {
		y, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if y >= 1900 && y <= 2099 {
			return y
		}
	}
	for _, m := range reBareYear.FindAllString(s, -1) {
		y, err := strconv.Atoi(m)
		if err != nil {
			continue
		}
		if y >= 1900 && y <= 2099 {
			return y
		}
	}
	return 0
}

func editionOf(s string) string {
	if m := reEdition.FindString(s); m != "" {
		return m
	}
	return ""
}

// looksLikeJunkName 判断文件名是否不可靠（应由父目录提供标题），
// 例如 `cd1.mkv`、`01.mkv`、`A.mkv`。
func looksLikeJunkName(name string) bool {
	n := strings.TrimSpace(strings.TrimSuffix(name, filepathExt(name)))
	if n == "" {
		return true
	}
	if rePart.MatchString(n) {
		return true
	}
	// 全是数字或极短
	onlyDigits := true
	for _, r := range n {
		if r < '0' || r > '9' {
			onlyDigits = false
			break
		}
	}
	return onlyDigits || len([]rune(n)) <= 2
}

func filepathExt(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}

// replaceWord 删除按词边界匹配的关键词（用于清理 10bit 之类标记）。
func replaceWord(s, word string) string {
	if word == "" {
		return s
	}
	if !isASCII(word) {
		return strings.ReplaceAll(s, word, " ")
	}
	lower := strings.ToLower(s)
	var out strings.Builder
	last := 0
	for i := 0; i+len(word) <= len(lower); {
		j := strings.Index(lower[i:], word)
		if j < 0 {
			break
		}
		start := i + j
		end := start + len(word)
		beforeOK := start == 0 || !isAlnum(lower[start-1])
		afterOK := end == len(lower) || !isAlnum(lower[end])
		if beforeOK && afterOK {
			out.WriteString(s[last:start])
			out.WriteString(" ")
			last = end
			i = end
			continue
		}
		i = start + 1
	}
	out.WriteString(s[last:])
	return out.String()
}
