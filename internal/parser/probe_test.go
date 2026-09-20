package parser

import (
	"fmt"
	"os"
	"testing"
)

// TestProbe 不是断言测试，而是一个「人工核对工具」：
// 它把真实媒体库里的路径喂给解析器并打印结果，用来发现规则问题，
// 核对无误后再把结论固化到 cases.json（TestCases 会真正断言）。
//
// 用法：LMBY_PARSER_PROBE=1 go test ./internal/parser -run TestProbe -v
func TestProbe(t *testing.T) {
	if os.Getenv("LMBY_PARSER_PROBE") == "" {
		t.Skip("设置 LMBY_PARSER_PROBE=1 才会运行（人工核对工具）")
	}

	type probe struct {
		path string
		hint Hint
	}

	seriesHint := func(title string, year, season int) Hint {
		return Hint{ParentTitle: title, ParentYear: year, ParentSeason: season}
	}

	probes := []probe{
		// ---- Emby 风格：SxxExx 纯编号，标题靠父目录
		{"/m/「Z」折纸Se丶/「完结动画」/钢之炼金术师 FULLMETAL ALCHEMIST (2009)/Season 1/S01E01.mkv",
			seriesHint("钢之炼金术师 FULLMETAL ALCHEMIST", 2009, 1)},
		{"/m/「Z」折纸Se丶/「完结动画」/黑色五叶草 (2017)/Season 1/S01E129.mkv",
			seriesHint("黑色五叶草", 2017, 1)},
		{"/m/「Z」折纸Se丶/「完结动画」/数码宝贝大冒险 (2020)/Season 1/S01E50.mp4",
			seriesHint("数码宝贝大冒险", 2020, 1)},
		{"/m/「Z」折纸Se丶/「完结动画」/我的英雄学院 (2016)/Season 7/S07E05.mkv",
			seriesHint("我的英雄学院", 2016, 7)},
		{"/m/「Z」折纸Se丶/「完结动画」/大欺诈师／GREAT PRETENDER (2020)/Season 1/S01E13.mkv",
			seriesHint("大欺诈师／GREAT PRETENDER", 2020, 1)},
		{"/m/「Z」折纸Se丶/「完结动画」/银河机攻队：庄严王子 (2013)/Season 1/S01E19.mkv",
			seriesHint("银河机攻队：庄严王子", 2013, 1)},

		// ---- 带集标题
		{"/m/TV/Show (2020)/Season 01/S01E02 - 某集标题.mkv", seriesHint("Show", 2020, 1)},
		{"/m/TV/Show (2020)/Season 01/02 - 另一种写法.mkv", seriesHint("Show", 2020, 1)},
		{"/m/TV/Show (2020)/Season 01/Show.S01E03.1080p.WEB-DL.AAC.mkv", seriesHint("Show", 2020, 1)},
		{"/m/TV/Show (2020)/Season 02/第3集.mkv", seriesHint("Show", 2020, 2)},
		{"/m/TV/Show (2020)/Season 01/Show - 1x05 - Title.mkv", seriesHint("Show", 2020, 1)},
		{"/m/TV/Show/Season 1 Episode 5.mkv", Hint{}},
		{"/m/TV/Show (2020)/Season 01/S01E01E02 连播.mkv", seriesHint("Show", 2020, 1)},
		{"/m/TV/Show (2020)/Specials/S00E01 特别篇.mkv", seriesHint("Show", 2020, 0)},

		// ---- 本项目媒体库规范：技术标记 + 书名号
		{"/m/「W」未来Se丶/「基准测试」/AVC 演示片/AVC 1080P YUV420P8 30fps 45Mbps《东芝 - 炫Dazzle》.mkv", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/AVC 演示片/AVC 4K YUV420P8 120fps 15Mbps《LoveLive!Superstar!! - 第3话插入歌》.mp4", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/HEVC 演示片/HEVC 1080P YUV420P10 24fps 20Mbps《天晴烂漫》.mkv", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/RM 演示片/RV40 720P YUV420P8 25fps 1.5Mbps《满汉全席》.rmvb", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/AV1 演示片/AV1 4K YUV420P10 60fps 30Mbps《某科学的超电磁炮OP2》.mkv", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/HDR 演示片/HEVC 4K HDR YUV420P10 60fps 50Mbps《杜比视界测试》.mkv", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/※问题专项/内封PGS字幕测试《黑侠 (1996)》16：9.mkv", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/※问题专项/电视／盒子CPU性能测试片.mkv", Hint{}},

		// ---- 电影
		{"/m/「Z」折纸Se丶/「高清电影」/言叶之庭 (2013)/言叶之庭 (2013).mkv", Hint{ParentTitle: "言叶之庭", ParentYear: 2013}},
		{"/m/「Z」折纸Se丶/「高清电影」/某电影 (2024)/某电影.2024.2160p.HEVC.mkv", Hint{ParentTitle: "某电影", ParentYear: 2024}},
		{"/m/Movies/Arrival (2016)/Arrival (2016) - 2160p.mkv", Hint{ParentTitle: "Arrival", ParentYear: 2016}},
		{"/m/Movies/Arrival (2016)/Arrival (2016).Director's Cut.mkv", Hint{ParentTitle: "Arrival", ParentYear: 2016}},
		{"/m/Movies/Blade Runner 2049 (2017)/cd1.mkv", Hint{ParentTitle: "Blade Runner 2049", ParentYear: 2017}},

		// ---- 花絮
		{"/m/TV/Show (2020)/Extras/behind the scenes.mkv", Hint{IsExtraDir: true}},
		{"/m/TV/Show (2020)/Season 01/S01E01 - trailer.mkv", seriesHint("Show", 2020, 1)},
		{"/m/电影 (2020)/预告片/正式预告.mkv", Hint{ParentTitle: "电影", ParentYear: 2020, IsExtraDir: true}},

		// ---- 疑难
		{"/m/「W」未来Se丶/「基准测试」/AVI 演示片/122509_740 122509- 740model Collection选择…81圣诞节.avi", Hint{}},
		{"/m/「W」未来Se丶/「基准测试」/AVI 演示片/021114_001 021114-001未公开影像～仅凭口吻就能让人发狂～朝比奈舞.avi", Hint{}},
		{"/m/「Z」折纸Se丶/「完结动画」/86-不存在的战区- (2021)/Season 1/S01E01.mkv", seriesHint("86-不存在的战区-", 2021, 1)},
		{"/m/「Z」折纸Se丶/「完结动画」/ENDRO～！ (2019)/Season 1/S01E01.mkv", seriesHint("ENDRO～！", 2019, 1)},
		{"/m/「Z」折纸Se丶/「完结动画」/LoveLive! 虹之咲学园偶像同好会「四格漫」 (2023)/Season 1/S01E01.mkv",
			seriesHint("LoveLive! 虹之咲学园偶像同好会「四格漫」", 2023, 1)},
	}

	fmt.Printf("%-58s | %-8s | %-5s | %s\n", "文件名", "类型", "S/E", "标题 / 标记")
	fmt.Println("---------------------------------------------------------------------------------------------------")
	for _, p := range probes {
		r := ParseVideo(p.path, p.hint)
		se := "-"
		if r.Season != NoSeason {
			se = fmt.Sprintf("%d/%d", r.Season, r.Episode)
		} else if r.Episode > 0 {
			se = fmt.Sprintf("-/%d", r.Episode)
		}
		extra := ""
		if r.Edition != "" {
			extra += " edition=" + r.Edition
		}
		if r.ExtraType != "" {
			extra += " extra=" + r.ExtraType
		}
		fmt.Printf("%-58s | %-8s | %-5s | %s (y=%d %s)%s\n",
			trunc(p.path[2:], 56), r.Kind, se, r.Title, r.Year, r.Tech.String(), extra)
	}
}

func (t Tech) String() string {
	s := ""
	add := func(prefix, v string) {
		if v != "" {
			s += prefix + v + " "
		}
	}
	add("codec=", t.Codec)
	add("res=", t.Resolution)
	add("chroma=", t.Chroma)
	add("fps=", t.FrameRate)
	add("br=", t.Bitrate)
	if len(t.Features) > 0 {
		s += fmt.Sprintf("feat=%v ", t.Features)
	}
	if s == "" {
		return ""
	}
	return "[" + trimSpace(s) + "]"
}

func trimSpace(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
