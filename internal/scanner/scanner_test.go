package scanner

import (
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/parser"
)

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
