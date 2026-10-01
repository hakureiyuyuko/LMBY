package parser

import "testing"

// 语料都来自真实媒体库（2026-10-01 从 /mnt/111 抓了 13278 个外挂字幕文件名，
// 其中 13267 个是 <集名>.chs.ass，其余是 cht / chi / zh / zh-CN 和几个带中文标题的测试文件）。
func TestParseSubtitle(t *testing.T) {
	cases := []struct {
		video, sub string
		language   string
		title      string
		forced     bool
		ok         bool
	}{
		// 主流写法：<集名>.chs.ass
		{"S01E01", "S01E01.chs", "chs", "", false, true},
		{"S01E01", "S01E01.cht", "cht", "", false, true},
		{"S01E01", "S01E01.chi", "chi", "", false, true},
		{"S01E01", "S01E01.zh", "zh", "", false, true},
		{"S01E01", "S01E01.zh-CN", "zh-CN", "", false, true},
		{"S01E01", "S01E01.eng", "eng", "", false, true},
		{"S01E01", "S01E01.chs&eng", "chs&eng", "", false, true},

		// 带标题：<集名>.<语言>.<标题>
		{"S01E01", "S01E01.zh-CN.简体&繁体", "zh-CN", "简体&繁体", false, true},
		{"S01E01", "S01E01.chs.第二版", "chs", "第二版", false, true},

		// forced
		{"S01E01", "S01E01.forced", "", "", true, true},
		{"S01E01", "S01E01.chs.forced", "chs", "", true, true},

		// 只有位置性标记，既不是语言也不是标题
		{"S01E01", "S01E01.sdh", "", "", false, true},

		// 和视频同名，没有标记
		{"S01E01", "S01E01", "", "", false, true},

		// 认不出来的标记进标题，不硬当语言
		{"S01E01", "S01E01.外部特效字幕渲染测试", "", "外部特效字幕渲染测试", false, true},

		// 视频名自己带点号（真实语料）
		{"攻壳机动队：S.A.C._SSS (2006)", "攻壳机动队：S.A.C._SSS (2006).chs", "chs", "", false, true},
		{"SSSS.古立特宇宙 (2023)", "SSSS.古立特宇宙 (2023).chs", "chs", "", false, true},
		{"攻壳机动队2.0 (2008)", "攻壳机动队2.0 (2008).chs", "chs", "", false, true},

		// 前缀包含陷阱：S01E01 不能吃掉 S01E010
		{"S01E01", "S01E010.chs", "", "", false, false},
		{"S01E01", "S01E01x.chs", "", "", false, false},

		// 根本不是同一个视频
		{"S01E01", "S01E02.chs", "", "", false, false},
		{"S01E01", "完全不同的东西.chs", "", "", false, false},

		// 空入参
		{"", "S01E01.chs", "", "", false, false},
		{"S01E01", "", "", "", false, false},
	}

	for _, c := range cases {
		lang, title, forced, ok := ParseSubtitle(c.video, c.sub)
		if ok != c.ok {
			t.Errorf("ParseSubtitle(%q, %q) ok=%v，期望 %v", c.video, c.sub, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if lang != c.language || title != c.title || forced != c.forced {
			t.Errorf("ParseSubtitle(%q, %q) = (语言 %q, 标题 %q, forced %v)，期望 (%q, %q, %v)",
				c.video, c.sub, lang, title, forced, c.language, c.title, c.forced)
		}
	}
}

func TestSubtitleFormat(t *testing.T) {
	cases := map[string]string{
		"S01E01.chs.ass":      "ass",
		"S01E01.CHS.ASS":      "ass",
		"movie.zh.srt":        "srt",
		"movie.zh-Hant.ssa":   "ssa",
		"movie.webvtt":        "webvtt",
		"movie.sup":           "sup",
		"movie.zh.forced.sub": "sub",
	}
	for name, want := range cases {
		if got := SubtitleFormat(name); got != want {
			t.Errorf("SubtitleFormat(%q) = %q，期望 %q", name, got, want)
		}
	}
}

func TestIsTextSubtitle(t *testing.T) {
	yes := []string{"S01E01.chs.ass", "a.srt", "b.ssa", "c.vtt"}
	no := []string{"a.sup", "a.sub", "a.idx", "a.smi", "a.ttml", "a.mkv", "a.ass.bak"}

	for _, name := range yes {
		if !IsTextSubtitle(name) {
			t.Errorf("IsTextSubtitle(%q) = false，期望 true", name)
		}
	}
	for _, name := range no {
		if IsTextSubtitle(name) {
			t.Errorf("IsTextSubtitle(%q) = true，期望 false", name)
		}
	}
}
