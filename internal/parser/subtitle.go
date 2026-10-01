package parser

import "strings"

// 外挂字幕的命名约定 —— 与 Emby/Jellyfin 一致：
//
//	<视频名>.ass                    没有标记
//	<视频名>.chs.ass                语言 chs
//	<视频名>.zh-CN.简体&繁体.ass     语言 zh-CN，标题「简体&繁体」
//	<视频名>.forced.ass             forced
//
// 关键点：视频名自己就可能带点号（本库实测有「攻壳机动队：S.A.C._SSS (2006).chs.ass」
// 和「SSSS.古立特宇宙 (2023).chs.ass」），所以不能按点号切完再猜，
// 必须先用**完整视频名**做前缀匹配，剩下的尾巴才是标记。
//
// 另外注意「S01E01」和「S01E010」这种前缀包含关系：视频名后面必须紧跟分隔符，
// 否则 S01E010.chs.ass 会被错配给 S01E01。

// subtitleLangTokens 是认作「语言标记」的写法（小写比较）。
//
// 只认表里的：认不出来的一律落进标题 —— 宁可把「外部特效字幕渲染测试」当标题，
// 也不要把随便一个词当成语言。本库 13278 个真实外挂字幕样本里 99.9% 是 chs，
// 其余出现过 cht / chi / zh / zh-CN，所以表的覆盖面够用；以后遇到新的再加。
var subtitleLangTokens = map[string]bool{
	// 中文（简体）
	"chs": true, "sc": true, "cn": true, "zh": true, "chi": true, "zho": true,
	"zh-cn": true, "zh_cn": true, "zh-sg": true, "zh-hans": true, "chs&eng": true,
	"简": true, "简中": true, "简体": true, "简体中文": true, "中文": true,
	// 中文（繁体）
	"cht": true, "tc": true, "tw": true, "hk": true, "zh-tw": true, "zh_tw": true,
	"zh-hk": true, "zh-hant": true,
	"繁": true, "繁中": true, "繁体": true, "繁體": true, "繁體中文": true,
	// 日语
	"jpn": true, "jp": true, "ja": true, "japanese": true, "日语": true, "日文": true, "日": true,
	// 英语
	"eng": true, "en": true, "english": true, "英语": true, "英文": true, "英": true,
	// 韩语
	"kor": true, "ko": true, "kr": true, "korean": true, "韩语": true, "韩文": true, "韩": true,
	// 其它常见语种
	"rus": true, "ru": true, "fra": true, "fre": true, "fr": true,
	"deu": true, "ger": true, "de": true, "spa": true, "es": true,
	"ita": true, "it": true, "por": true, "pt": true, "tha": true, "th": true,
	"vie": true, "vi": true, "ind": true, "msa": true, "ara": true, "mya": true,
}

// subtitleFlagTokens 是位置性标记：既不进语言也不进标题。
var subtitleFlagTokens = map[string]bool{
	"forced": true, "forcedonly": true, "forced-only": true, "强迫": true, "强制": true,
	"sdh": true, "cc": true, "hi": true, "default": true, "默认": true,
	"commentary": true, "评论": true,
}

// subtitleForcedTokens 是上面那些标记里代表 forced 的子集。
var subtitleForcedTokens = map[string]bool{
	"forced": true, "forcedonly": true, "forced-only": true, "强迫": true, "强制": true,
}

// textSubtitleFormats 是「能直接下发」的文本字幕格式。
//
// ass / ssa 交给前端 libass 渲染（保留特效与排版），
// srt 转成 WebVTT 给浏览器原生轨道。
// 位图字幕（sup / sub+idx / smi / ttml / sbv）要么得烧录、要么要额外转换，本版不做。
var textSubtitleFormats = map[string]bool{
	"ass": true, "ssa": true, "srt": true, "vtt": true,
}

// SubtitleFormat 返回归一化后的字幕格式名（小写、无点号），认不出来就返回扩展名本身。
func SubtitleFormat(name string) string {
	return strings.TrimPrefix(extOf(name), ".")
}

// IsTextSubtitle 判断这个外挂字幕是不是本版能直接下发的文本格式。
func IsTextSubtitle(name string) bool {
	return textSubtitleFormats[SubtitleFormat(name)]
}

// ParseSubtitle 判断 subStem 是不是 videoStem 的外挂字幕，并解析出语言 / 标题 / forced。
//
// 入参都是**不带扩展名**的文件名主干。ok=false 表示两者没有归属关系，
// 调用方应当跳过（比如同一目录下属于别的剧集的字幕）。
func ParseSubtitle(videoStem, subStem string) (language, title string, forced bool, ok bool) {
	if videoStem == "" || subStem == "" {
		return "", "", false, false
	}
	rest, ok := trimStemPrefix(videoStem, subStem)
	if !ok {
		return "", "", false, false
	}
	if rest == "" {
		// <视频名>.ass：整个主干就是视频名，没有标记
		return "", "", false, true
	}

	var titles []string
	for _, tok := range strings.Split(rest, ".") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		lower := strings.ToLower(tok)
		switch {
		case subtitleForcedTokens[lower]:
			forced = true
		case subtitleFlagTokens[lower]:
			// 其余标记（sdh / hi / default…）只是噪音
		case subtitleLangTokens[lower] && language == "":
			language = tok
		default:
			titles = append(titles, tok)
		}
	}
	return language, strings.Join(titles, "."), forced, true
}

// trimStemPrefix 去掉 subStem 前面的 videoStem，返回剩下带标记的尾巴。
//
// videoStem 后面必须紧跟分隔符（. - _ 空格），否则 S01E01 会吃掉 S01E010。
// 分隔符本身不保留。
func trimStemPrefix(videoStem, subStem string) (string, bool) {
	if !strings.HasPrefix(subStem, videoStem) {
		return "", false
	}
	rest := subStem[len(videoStem):]
	if rest == "" {
		return "", true
	}
	switch rest[0] {
	case '.', '-', '_', ' ':
		return strings.TrimLeft(rest[1:], "-_ "), true
	default:
		return "", false
	}
}
