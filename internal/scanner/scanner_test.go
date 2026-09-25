package scanner

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hakureiyuyuko/lmby/internal/metadata"
	"github.com/hakureiyuyuko/lmby/internal/parser"
)

// TestBadNameReason 是 2026-09-25 那次事故的回归测试。
//
// 背景：库是 UTF-8 的，而 SMB / 网盘共享上的文件名不保证是 UTF-8。以前这种名字
// 会一路带进数据库：先让那个文件插不进 media_files，再让**整批扫描问题**写失败 ——
// 扫描于是被判成「失败」，后面几百条问题全丢（实测只留下失败前写进去的 63 条）。
// 现在在遍历时就把它们挑出来跳过，所以这个判断必须可靠。
func TestBadNameReason(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantNote string // 空串 = 应当被判为「没问题」
	}{
		{"中文目录名", "「Z」折纸Se丶", ""},
		{"带年份的剧集目录", "致不灭的你 (2021)", ""},
		{"emoji", "🎬 特典", ""},
		{"组合字符", "バクロ", ""},
		{"空名字", "", ""},
		{"CIFS 上真实存在的坏字节", "x\xde y.mkv", "0xde 0x20"},
		{"被切断的三字节汉字", "\xe3\x80", "0xe3 0x80"},
		{"合法名字后面跟着坏字节", "白色相簿\xff", "0xff"},
	}
	for _, c := range cases {
		got := badNameReason(c.in)
		if c.wantNote == "" {
			if got != "" {
				t.Errorf("%s：合法名字不该被判为坏名字，实际 %q", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.wantNote) {
			t.Errorf("%s：说明里应带上坏字节的十六进制 %s，实际 %q", c.name, c.wantNote, got)
		}
		// 说明本身要写进数据库，所以必须是合法 UTF-8 —— 否则「提示」本身就会把扫描搞挂。
		if !utf8.ValidString(got) {
			t.Errorf("%s：说明本身不是合法 UTF-8：%q", c.name, got)
		}
	}
}

// TestIssueSanitizes 守住「问题清单是入库前的最后一道」：
// 即使某个调用点忘了净化，写进 w.issues 的文本也必须是合法 UTF-8。
func TestIssueSanitizes(t *testing.T) {
	w := &walker{}
	// 真实的错误信息会把文件名原样带进来（内核/ffprobe 都可能）
	w.issue("warning", "/mnt/media/x\xde y.mkv", "访问失败: open /mnt/media/x\xde y.mkv: Host is down")

	if len(w.issues) != 1 {
		t.Fatalf("应当记录 1 条问题，实际 %d", len(w.issues))
	}
	got := w.issues[0]
	if got.Severity != "warning" {
		t.Errorf("严重级别应原样保留，实际 %q", got.Severity)
	}
	if !utf8.ValidString(got.Path) || !utf8.ValidString(got.Message) {
		t.Errorf("入库前必须净化：path=%q message=%q", got.Path, got.Message)
	}
	if !strings.Contains(got.Path, "\uFFFD") {
		t.Errorf("坏字节应换成 U+FFFD（看得出这里坏过），实际 %q", got.Path)
	}
}

// 空 nfo 不能把条目钉成 nfo 状态，否则那条就永远不会被刮削。
// TestNFOHasMetadata 守住「什么样的 nfo 算人工元数据」：
// 空 nfo 不能把条目钉成 nfo 状态，否则那条就永远不会被刮削。
func TestNFOHasMetadata(t *testing.T) {
	year := int32(2013)
	rating := 8.2

	cases := []struct {
		name string
		md   metadata.Metadata
		want bool
	}{
		{"空 nfo", metadata.Metadata{}, false},
		{"只有标题", metadata.Metadata{Title: "言叶之庭"}, true},
		{"只有原名", metadata.Metadata{OriginalTitle: "言の葉の庭"}, true},
		{"只有简介", metadata.Metadata{Overview: "下雨天的庭园…"}, true},
		{"只有年份", metadata.Metadata{Year: &year}, true},
		{"只有评分", metadata.Metadata{Rating: &rating}, true},
		{"只有流派", metadata.Metadata{Genres: []string{"动画"}}, true},
		{"只有外部 id", metadata.Metadata{ProviderIDs: map[string]string{"tmdb": "198375"}}, true},
	}

	for _, c := range cases {
		if got := nfoHasMetadata(&c.md); got != c.want {
			t.Errorf("%s: nfoHasMetadata = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// newTestWalker 构造一个只带解析缓存的 walker。
//
// resolve 只用 dirCache / ctxCache 与 parser.ParseDir，不碰数据库与文件系统，
// 所以可以脱离 store 直接单测 —— 这正是把它设计成纯逻辑的价值。
func newTestWalker() *walker {
	return &walker{
		dirCache: map[string]parser.DirInfo{},
		ctxCache: map[string]dirCtx{},
	}
}

// TestResolveLibraryRootIsSeriesDir 是回归测试：
//
// 用户很可能把库根直接指向一个作品目录（`/media/TV/某剧 (2021)`）。
// 早期实现从「库根的子目录」才开始算层级，于是剧集名丢失，
// 集会被挂到一个叫 "Season 1" 的假剧集上。
func TestResolveLibraryRootIsSeriesDir(t *testing.T) {
	w := newTestWalker()
	root := "/media/TV/钢之炼金术师 FULLMETAL ALCHEMIST (2009)"

	got := w.resolve(root, root+"/Season 1")
	if got.title != "钢之炼金术师 FULLMETAL ALCHEMIST" {
		t.Errorf("剧集名应来自库根目录，实际 %q", got.title)
	}
	if got.year != 2009 {
		t.Errorf("年份应为 2009，实际 %d", got.year)
	}
	if !got.isSeason || got.season != 1 {
		t.Errorf("应识别为第 1 季，实际 isSeason=%v season=%d", got.isSeason, got.season)
	}

	special := w.resolve(root, root+"/Specials")
	if !special.isSeason || special.season != 0 {
		t.Errorf("Specials 应识别为第 0 季，实际 isSeason=%v season=%d", special.isSeason, special.season)
	}
	if special.title != "钢之炼金术师 FULLMETAL ALCHEMIST" {
		t.Errorf("特典季的剧集名也应正确，实际 %q", special.title)
	}
}

// TestResolveIgnoresRootWithoutYear 确认没有年份的库根不会被当成作品名：
// 否则 `/media/Movies` 会被当成一部叫 Movies 的电影。
func TestResolveIgnoresRootWithoutYear(t *testing.T) {
	w := newTestWalker()

	for _, root := range []string{"/media/Movies", "/media/TV", "/mnt/media"} {
		if got := w.resolve(root, root); got.title != "" {
			t.Errorf("库根 %s 没有年份，不应提供标题，实际 %q", root, got.title)
		}
	}
}

// TestResolveEmbyNestedChain 覆盖标准 Emby 层级：
// 库根 →「分类」→ 作品 (年份) → Season N。
func TestResolveEmbyNestedChain(t *testing.T) {
	w := newTestWalker()
	root := "/mnt/media"
	dir := "/mnt/media/「Z」折纸Se丶/「完结动画」/某剧 （2021）/Season 2"

	got := w.resolve(root, dir)
	if got.category != "完结动画" {
		t.Errorf("分类应为 完结动画，实际 %q", got.category)
	}
	if got.title != "某剧" {
		t.Errorf("作品名应为 某剧，实际 %q", got.title)
	}
	if got.year != 2021 {
		t.Errorf("年份应为 2021，实际 %d", got.year)
	}
	if !got.isSeason || got.season != 2 {
		t.Errorf("应识别为第 2 季，实际 isSeason=%v season=%d", got.isSeason, got.season)
	}
}

// TestResolveMovieDir 覆盖电影目录：作品 (年份) 直接位于库根下。
func TestResolveMovieDir(t *testing.T) {
	w := newTestWalker()
	root := "/mnt/media/「Z」折纸Se丶/「剧场动画」"
	dir := root + "/乔西的虎与鱼"

	got := w.resolve(root, dir)
	if got.title != "乔西的虎与鱼" {
		t.Errorf("作品名应为 乔西的虎与鱼，实际 %q", got.title)
	}
	if got.isSeason {
		t.Error("电影目录不应被识别为季目录")
	}
}

// TestResolveExtraDir 覆盖花絮目录的传递。
func TestResolveExtraDir(t *testing.T) {
	w := newTestWalker()
	root := "/media/TV"
	dir := "/media/TV/某剧 (2018)/Extras"

	got := w.resolve(root, dir)
	if got.title != "某剧" {
		t.Errorf("花絮目录仍应继承作品名，实际 %q", got.title)
	}
	if !got.isExtra || got.extraType != "other" {
		t.Errorf("应识别为花絮目录，实际 isExtra=%v type=%q", got.isExtra, got.extraType)
	}
}

// TestHintSeasonMapping 确认 0 季（特典）通过 ParentIsSpecials 传递，
// 不会被当成「未指定季」。
func TestHintSeasonMapping(t *testing.T) {
	w := newTestWalker()
	root := "/media/TV/某剧 (2020)"

	specials := w.resolve(root, root+"/Specials").hint("tv")
	if specials.ParentSeason != 0 || !specials.ParentIsSpecials {
		t.Errorf("特典季应表达为 ParentIsSpecials，实际 %+v", specials)
	}

	normal := w.resolve(root, root+"/Season 3").hint("tv")
	if normal.ParentSeason != 3 || normal.ParentIsSpecials {
		t.Errorf("第 3 季映射错误: %+v", normal)
	}

	// 电影目录：没有季信息
	movie := w.resolve("/media/Movies/某电影 (2019)", "/media/Movies/某电影 (2019)").hint("movie")
	if movie.ParentSeason != 0 || movie.ParentIsSpecials {
		t.Errorf("电影目录不应带季信息，实际 %+v", movie)
	}
	if movie.ParentYear != 2019 {
		t.Errorf("电影目录的年份应为 2019，实际 %d", movie.ParentYear)
	}
}
