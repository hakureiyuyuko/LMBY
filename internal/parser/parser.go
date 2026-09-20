// Package parser 从文件名与目录结构推断媒体条目信息。
//
// 设计原则：
//   - **纯函数**，不碰文件系统也不碰数据库，因此可以用真实语料做表驱动单测；
//   - 规则按优先级串行尝试，先命中先返回，并把命中理由记进 Reasons 便于排查；
//   - 兼容 Emby/Jellyfin 的目录约定（`名称 (年份)/Season N/SxxExx.ext`），
//     同时支持本媒体库特有的命名规范
//     `编码 分辨率 色彩 帧率 码率《标题》.ext`（见 docs/LIBRARY-NOTES.md）。
//
// 不负责的事：读 nfo、探测流信息、决定刮削 —— 那些在 metadata / probe 包里。
package parser

import (
	"path/filepath"
	"strings"
)

// Kind 是解析出的条目类型。
type Kind string

const (
	KindMovie   Kind = "movie"
	KindEpisode Kind = "episode"
	KindExtra   Kind = "extra"
	KindUnknown Kind = "unknown"
)

// NoSeason 表示「季号未知」。
//
// 注意：0 是合法的季号（特典 / Season 00），所以不能用 0 当哨兵值。
const NoSeason = -1

// 扩展名分类。集合故意写死而不是配置化：它们是解析语义的一部分，
// 不是用户偏好，配置化只会让行为变得不可预测。
var (
	videoExts = map[string]bool{
		".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".rmvb": true,
		".rm": true, ".wmv": true, ".flv": true, ".mov": true, ".mpg": true,
		".mpeg": true, ".ts": true, ".m2ts": true, ".webm": true, ".vob": true,
		".iso": true, ".divx": true, ".ogm": true, ".ogv": true, ".asf": true,
		".mts": true, ".m2t": true, ".3gp": true,
	}
	subtitleExts = map[string]bool{
		".ass": true, ".ssa": true, ".srt": true, ".sub": true, ".idx": true,
		".sup": true, ".vtt": true, ".smi": true, ".sami": true, ".ttml": true,
		".sbv": true,
	}
	imageExts = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".bmp": true,
		".gif": true, ".tbn": true, ".avif": true,
	}
	audioExts = map[string]bool{
		".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".wav": true,
		".ogg": true, ".opus": true, ".ape": true, ".wma": true, ".dsf": true,
		".dff": true,
	}
	// 明确忽略：不产生任何条目，也不产生「未识别」噪音
	ignoreExts = map[string]bool{
		".nfo": true, ".db": true, ".ini": true, ".inf": true, ".ico": true,
		".exe": true, ".lnk": true, ".url": true, ".txt": true, ".log": true,
		".bak": true, ".tmp": true, ".part": true, ".crdownload": true,
		".torrent": true, ".sfv": true, ".md5": true, ".ds_store": true,
	}
)

// IsVideo 判断是否为可播放的正片容器。
func IsVideo(name string) bool { return videoExts[extOf(name)] }

// IsSubtitle 判断是否为外挂字幕。
func IsSubtitle(name string) bool { return subtitleExts[extOf(name)] }

// IsImage 判断是否为图片。
func IsImage(name string) bool { return imageExts[extOf(name)] }

// IsAudio 判断是否为音频（M1 不做音乐库，但扫描器要能识别并跳过）。
func IsAudio(name string) bool { return audioExts[extOf(name)] }

// ShouldIgnore 判断是否应当直接跳过、连「未识别」记录都不产生。
func ShouldIgnore(name string) bool {
	base := filepath.Base(name)
	if base == "" {
		return true
	}
	if strings.HasPrefix(base, ".") {
		return true
	}
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".!qb") || strings.HasSuffix(lower, ".part") {
		return true
	}
	return ignoreExts[extOf(name)]
}

// Result 是一次文件级解析的结果。
type Result struct {
	Kind Kind

	// Title 是作品名（电影名 / 剧集名）。Emby 风格的 `S01E01.mkv` 本身不含标题，
	// 此时为空字符串，由上层用父目录名补齐。
	Title string

	Year int

	Season     int // NoSeason 表示未知
	Episode    int
	EpisodeEnd int // 双集连播的结束集号，0 表示不是连播

	ExtraType string
	Edition   string

	// Tech 是从文件名里识别出的技术标记（本媒体库的命名规范）。
	Tech Tech

	// Confidence 是解析可信度 0~1，供上层决定是否需要人工确认。
	Confidence float64

	// Reasons 记录命中的规则，便于排查「为什么解析成这样」。
	Reasons []string
}

// Hint 是解析时可用的外部线索（来自目录结构与库配置）。
//
// 零值表示「什么都不知道」：ParentSeason 为 0 即未知，
// 真正的特典季（Season 00）要用 ParentIsSpecials 表达。
type Hint struct {
	// LibraryKind 是库类型：movie / tv / homevideo / mixed。
	LibraryKind string

	// ParentTitle / ParentYear 是父目录解析出的作品名与年份。
	ParentTitle string
	ParentYear  int

	// ParentSeason 是父目录给出的季号（>0 才有意义）。
	ParentSeason int

	// ParentIsSpecials 表示父目录是特典季目录（Season 00 / Specials）。
	ParentIsSpecials bool

	// IsExtraDir 表示当前文件所在目录已被判定为花絮目录。
	IsExtraDir bool
}

// inSeasonDir 判断是否处在确定的季目录里。
func (h Hint) inSeasonDir() bool { return h.ParentSeason > 0 || h.ParentIsSpecials }

// hintSeason 返回父目录给出的季号。
func (h Hint) hintSeason() int {
	if h.ParentIsSpecials {
		return 0
	}
	if h.ParentSeason > 0 {
		return h.ParentSeason
	}
	return NoSeason
}

// ParseVideo 解析一个视频文件。
//
// path 可以是绝对或相对路径；只有文件名与 Hint 参与判断，
// 目录层级的信息由扫描器预先解析成 Hint 传进来。
func ParseVideo(path string, hint Hint) Result {
	res := Result{Kind: KindUnknown, Season: NoSeason, Confidence: 0.2}
	if hint.inSeasonDir() {
		res.Season = hint.hintSeason()
		res.Reasons = append(res.Reasons, "季号取自父目录")
	}

	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))

	res = parseVideoName(res, name, base, path, hint)
	return res
}

func parseVideoName(res Result, name, base, path string, hint Hint) Result {
	// ---- 规则 1：书名号标题（本媒体库规范：`编码 …《标题》.ext`）
	if mt := reBookTitle.FindStringSubmatchIndex(name); mt != nil {
		rawTitle := name[mt[2]:mt[3]]
		outside := name[:mt[0]] + " " + name[mt[1]:]

		res.Title = cleanBookTitle(rawTitle)
		res.Year = yearOf(rawTitle)
		if res.Year == 0 {
			res.Year = hint.ParentYear
		}
		// 技术标记只从书名号之外提取，避免标题里的 HDR/4K 被当成规格
		res.Tech = ParseTech(outside)
		res.Kind = KindMovie
		res.Confidence = 0.9
		res.Reasons = append(res.Reasons, "书名号标题")

		// 季集标记也只看书名号之外：《…第3话插入歌》里的「第3话」是标题的一部分
		if em, ok := findEpisodeMarker(outside); ok {
			res.Kind = KindEpisode
			res.Season, res.Episode, res.EpisodeEnd = em.Season, em.Episode, em.End
			if res.Season == NoSeason {
				res.Season = hint.hintSeason()
			}
			res.Reasons = append(res.Reasons, "书名号外含季集标记")
		}
		applyEdition(&res, name)
		markExtra(&res, hint, base)
		return res
	}

	// ---- 规则 2：显式季集标记（Emby 主流形态）
	if em, ok := findEpisodeMarker(name); ok {
		res.Kind = KindEpisode
		res.Season, res.Episode, res.EpisodeEnd = em.Season, em.Episode, em.End
		if res.Season == NoSeason {
			res.Season = hint.hintSeason()
		}
		res.Title = titleAfterMarker(name, em)
		res.Tech = ParseTech(name)
		res.Confidence = 0.95
		res.Reasons = append(res.Reasons, "季集标记")
		applyEdition(&res, name)
		if res.Title == "" && hint.ParentTitle != "" {
			res.Title = hint.ParentTitle
			res.Reasons = append(res.Reasons, "标题取自父目录")
		}
		if res.Year == 0 {
			res.Year = hint.ParentYear
		}
		markExtra(&res, hint, base)
		return res
	}

	// ---- 规则 3：处在季目录里
	if hint.inSeasonDir() {
		res.Kind = KindEpisode
		res.Year = hint.ParentYear
		res.Tech = ParseTech(name)
		if n, rest, ok := leadingEpisodeNumber(name); ok {
			res.Episode = n
			res.Title = cleanTitle(rest)
			res.Confidence = 0.8
			res.Reasons = append(res.Reasons, "季目录内的前导集号")
		} else {
			res.Title = cleanTitle(name)
			res.Confidence = 0.5
			res.Reasons = append(res.Reasons, "季目录内但无集号")
		}
		if res.Title == "" && hint.ParentTitle != "" {
			res.Title = hint.ParentTitle
		}
		markExtra(&res, hint, base)
		return res
	}

	// ---- 规则 4：花絮目录
	if hint.IsExtraDir {
		res.Kind = KindExtra
		res.Title = cleanTitle(name)
		res.ExtraType = ExtraTypeOf(path)
		res.Confidence = 0.7
		res.Reasons = append(res.Reasons, "父目录为花絮目录")
		return res
	}

	// ---- 规则 5：电影
	title := cleanTitle(name)
	junk := looksLikeJunkName(name)
	if (title == "" || junk) && hint.ParentTitle != "" {
		res.Kind = KindMovie
		res.Title = hint.ParentTitle
		res.Year = hint.ParentYear
		res.Tech = ParseTech(name)
		res.Confidence = 0.7
		res.Reasons = append(res.Reasons, "文件名不可靠，标题取自父目录")
		return res
	}
	if title != "" && !junk {
		res.Kind = KindMovie
		res.Title = title
		res.Year = yearOf(name)
		if res.Year == 0 {
			res.Year = hint.ParentYear
		}
		res.Tech = ParseTech(name)
		res.Confidence = 0.75
		res.Reasons = append(res.Reasons, "按电影处理")
		applyEdition(&res, name)
		return res
	}

	// ---- 兜底
	res.Kind = KindUnknown
	res.Title = title
	res.Tech = ParseTech(name)
	res.Reasons = append(res.Reasons, "无法判定")
	return res
}

// ---------------------------------------------------------------- 内部工具

// cleanBookTitle 清洗书名号内的标题。
//
// 与 cleanTitle 的区别：书名号内**按定义就是标题**，所以不能把 codec / HDR /
// 杜比视界之类的词当规格删掉 —— 「《杜比视界测试》」的标题就是它本身。
// 只去掉括号补充与纯分隔符。
func cleanBookTitle(raw string) string {
	s := stripBracketGroups(raw)
	// 括号年份是元数据，属于噪声
	s = reYear.ReplaceAllString(s, " ")
	// 注意：不剔独立的四位年份 —— 电影《1917》的标题就是 1917
	s = strings.NewReplacer("　", " ").Replace(s)
	s = strings.Trim(s, " -–—·、|~　\t")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func markExtra(res *Result, hint Hint, base string) {
	if hint.IsExtraDir || IsExtraName(base) {
		res.Kind = KindExtra
		res.ExtraType = ExtraTypeOf(base)
		res.Reasons = append(res.Reasons, "花絮")
	}
}

func applyEdition(res *Result, name string) {
	if res.Edition == "" {
		res.Edition = editionOf(name)
	}
}

func extOf(name string) string {
	return strings.ToLower(filepath.Ext(name))
}
