package parser

import (
	"encoding/json"
	"os"
	"testing"
)

// 语料格式说明见 testdata/cases.json 顶部的 _comment。
//
// 三种用例（视频 / 目录 / 文件分类）共用同一个 want 结构，
// 用指针区分「没写」和「写成零值」：没写的字段不参与断言。

type jsonHint struct {
	LibraryKind      string `json:"libraryKind"`
	ParentTitle      string `json:"parentTitle"`
	ParentYear       int    `json:"parentYear"`
	ParentSeason     int    `json:"parentSeason"`
	ParentIsSpecials bool   `json:"parentIsSpecials"`
	IsExtraDir       bool   `json:"isExtraDir"`
}

type jsonWant struct {
	// 视频
	Kind       *string `json:"kind"`
	Title      *string `json:"title"`
	Year       *int    `json:"year"`
	Season     *int    `json:"season"`
	Episode    *int    `json:"episode"`
	EpisodeEnd *int    `json:"episodeEnd"`
	ExtraType  *string `json:"extraType"`
	Edition    *string `json:"edition"`
	Codec      *string `json:"codec"`
	Resolution *string `json:"resolution"`
	Chroma     *string `json:"chroma"`
	FrameRate  *string `json:"frameRate"`
	Bitrate    *string `json:"bitrate"`

	// 目录
	IsCategory *bool   `json:"isCategory"`
	Category   *string `json:"category"`
	IsSeason   *bool   `json:"isSeason"`
	IsExtra    *bool   `json:"isExtra"`
	IsImageDir *bool   `json:"isImageDir"`

	// 文件分类
	IsVideo    *bool `json:"isVideo"`
	IsSubtitle *bool `json:"isSubtitle"`
	IsAudio    *bool `json:"isAudio"`
	IsImage    *bool `json:"isImage"`
	Ignore     *bool `json:"ignore"`
}

type videoCase struct {
	Name string   `json:"name"`
	Path string   `json:"path"`
	Hint jsonHint `json:"hint"`
	Want jsonWant `json:"want"`
}

type dirCase struct {
	Name string   `json:"name"`
	Dir  string   `json:"dir"`
	Want jsonWant `json:"want"`
}

type fileCase struct {
	Name string   `json:"name"`
	File string   `json:"file"`
	Want jsonWant `json:"want"`
}

type corpus struct {
	Comment string      `json:"_comment"`
	Videos  []videoCase `json:"videos"`
	Dirs    []dirCase   `json:"dirs"`
	Files   []fileCase  `json:"files"`
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatalf("读取语料失败: %v", err)
	}
	var c corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("解析语料失败: %v", err)
	}
	if len(c.Videos) == 0 || len(c.Dirs) == 0 || len(c.Files) == 0 {
		t.Fatal("语料为空，检查 testdata/cases.json")
	}
	return c
}

func TestParseVideoCases(t *testing.T) {
	c := loadCorpus(t)

	for _, tc := range c.Videos {
		t.Run(tc.Name, func(t *testing.T) {
			got := ParseVideo(tc.Path, Hint{
				LibraryKind:      tc.Hint.LibraryKind,
				ParentTitle:      tc.Hint.ParentTitle,
				ParentYear:       tc.Hint.ParentYear,
				ParentSeason:     tc.Hint.ParentSeason,
				ParentIsSpecials: tc.Hint.ParentIsSpecials,
				IsExtraDir:       tc.Hint.IsExtraDir,
			})
			w := tc.Want

			eqStr(t, "Kind", string(got.Kind), w.Kind)
			eqStr(t, "Title", got.Title, w.Title)
			eqInt(t, "Year", got.Year, w.Year)
			eqInt(t, "Season", got.Season, w.Season)
			eqInt(t, "Episode", got.Episode, w.Episode)
			eqInt(t, "EpisodeEnd", got.EpisodeEnd, w.EpisodeEnd)
			eqStr(t, "ExtraType", got.ExtraType, w.ExtraType)
			eqStr(t, "Edition", got.Edition, w.Edition)
			eqStr(t, "Tech.Codec", got.Tech.Codec, w.Codec)
			eqStr(t, "Tech.Resolution", got.Tech.Resolution, w.Resolution)
			eqStr(t, "Tech.Chroma", got.Tech.Chroma, w.Chroma)
			eqStr(t, "Tech.FrameRate", got.Tech.FrameRate, w.FrameRate)
			eqStr(t, "Tech.Bitrate", got.Tech.Bitrate, w.Bitrate)

			if t.Failed() {
				t.Logf("解析理由: %v", got.Reasons)
				t.Logf("完整结果: %+v", got)
			}
		})
	}
}

func TestParseDirCases(t *testing.T) {
	c := loadCorpus(t)

	for _, tc := range c.Dirs {
		t.Run(tc.Name, func(t *testing.T) {
			got := ParseDir(tc.Dir)
			w := tc.Want

			eqStr(t, "Title", got.Title, w.Title)
			eqStr(t, "Category", got.Category, w.Category)
			eqStr(t, "ExtraType", got.ExtraType, w.ExtraType)
			eqInt(t, "Year", got.Year, w.Year)
			eqInt(t, "Season", got.Season, w.Season)
			eqBool(t, "IsCategory", got.IsCategory, w.IsCategory)
			eqBool(t, "IsSeason", got.IsSeason, w.IsSeason)
			eqBool(t, "IsExtra", got.IsExtra, w.IsExtra)
			eqBool(t, "IsImageDir", got.IsImageDir, w.IsImageDir)

			if t.Failed() {
				t.Logf("完整结果: %+v", got)
			}
		})
	}
}

func TestFileClassificationCases(t *testing.T) {
	c := loadCorpus(t)

	for _, tc := range c.Files {
		t.Run(tc.Name, func(t *testing.T) {
			w := tc.Want
			eqBool(t, "IsVideo", IsVideo(tc.File), w.IsVideo)
			eqBool(t, "IsSubtitle", IsSubtitle(tc.File), w.IsSubtitle)
			eqBool(t, "IsAudio", IsAudio(tc.File), w.IsAudio)
			eqBool(t, "IsImage", IsImage(tc.File), w.IsImage)
			eqBool(t, "ShouldIgnore", ShouldIgnore(tc.File), w.Ignore)
		})
	}
}

// TestNoSeasonSentinel 固定住一个容易踩的约定：
// 0 是合法季号（特典），未知必须用 NoSeason(-1)。
func TestNoSeasonSentinel(t *testing.T) {
	r := ParseVideo("/m/whatever/movie (2020).mkv", Hint{})
	if r.Season != NoSeason {
		t.Fatalf("无季信息时应为 NoSeason(%d)，实际 %d", NoSeason, r.Season)
	}
	if NoSeason != -1 {
		t.Fatalf("NoSeason 必须是 -1，实际 %d", NoSeason)
	}
}

// TestParseVideoIsPure 确认解析器不依赖真实文件系统。
func TestParseVideoIsPure(t *testing.T) {
	const p = "/完全/不存在的/路径/某剧 (2021)/Season 3/S03E07.mkv"
	r := ParseVideo(p, Hint{ParentTitle: "某剧", ParentYear: 2021, ParentSeason: 3})
	if r.Kind != KindEpisode || r.Season != 3 || r.Episode != 7 || r.Title != "某剧" {
		t.Fatalf("期望依赖路径字符串而非真实文件，实际: %+v", r)
	}
}

// ---------------------------------------------------------------- 断言小工具

func eqStr(t *testing.T, field, got string, want *string) {
	t.Helper()
	if want == nil {
		return
	}
	if got != *want {
		t.Errorf("%s 不符：期望 %q，实际 %q", field, *want, got)
	}
}

func eqInt(t *testing.T, field string, got int, want *int) {
	t.Helper()
	if want == nil {
		return
	}
	if got != *want {
		t.Errorf("%s 不符：期望 %d，实际 %d", field, *want, got)
	}
}

func eqBool(t *testing.T, field string, got bool, want *bool) {
	t.Helper()
	if want == nil {
		return
	}
	if got != *want {
		t.Errorf("%s 不符：期望 %v，实际 %v", field, *want, got)
	}
}
