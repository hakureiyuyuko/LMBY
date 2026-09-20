package scanner

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/parser"
)

// 本文件负责「一张图片属于哪个条目、是哪一类图」的判定。
//
// 覆盖 Emby/Kodi 的实际命名约定（不是猜的，是对着真实媒体库验证过的）：
//
//	剧集根：   poster.jpg  fanart.jpg  banner.jpg  clearlogo.png  thumb.jpg
//	          landscape.jpg  disc.png  tvshow.nfo
//	季目录：   season01-poster.jpg  season-specials-poster.jpg
//	集目录：   S01E01-thumb.jpg  S01E01.jpg  S01E01-poster.jpg
//	图片目录： Backdrops/（整棵跳过，由扫描器跳过目录）

// reSeasonPoster 匹配 season01-poster / season-specials-poster。
var reSeasonPoster = regexp.MustCompile(`^season[\s._-]*((?:specials|0|\d{1,2}))[\s._-]*poster$`)

// dirLevelImageKind 判断「目录级」图片（名字不带具体视频名）。
func dirLevelImageKind(stem string) (string, bool) {
	switch stem {
	case "poster", "folder", "cover", "movie", "show", "tvshow", "default":
		return "poster", true
	case "fanart", "backdrop", "background", "art", "extrafanart":
		return "fanart", true
	case "banner", "banners":
		return "banner", true
	case "logo", "clearlogo", "clearart":
		return "logo", true
	case "disc", "discart", "cdart":
		return "disc", true
	case "thumb", "landscape", "screenshot":
		return "thumb", true
	}
	return "", false
}

// suffixImageKind 判断 `<视频名>-poster.jpg` 这类后缀。
func suffixImageKind(suffix string) (string, bool) {
	switch suffix {
	case "poster", "folder", "cover":
		return "poster", true
	case "fanart", "backdrop", "art":
		return "fanart", true
	case "banner":
		return "banner", true
	case "logo", "clearlogo":
		return "logo", true
	case "disc":
		return "disc", true
	case "thumb", "landscape":
		return "thumb", true
	}
	return "", false
}

func seasonFromPosterToken(tok string) int {
	if tok == "" || strings.EqualFold(tok, "specials") {
		return 0
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return 0
	}
	return n
}

// resolveImageOwner 决定图片归属与类别。返回 ok=false 表示这张图不认领（静默跳过）。
func (w *walker) resolveImageOwner(ctx context.Context, dir string, img imageEntry) (int64, string, bool) {
	base := strings.ToLower(img.base)
	stem := strings.TrimSuffix(base, extOf(base))

	// 1) 季海报：seasonNN-poster.jpg
	if m := reSeasonPoster.FindStringSubmatch(stem); m != nil {
		seriesDir := w.seriesDirOf(dir)
		if seriesID, ok := w.seriesItemByDir[seriesDir]; ok {
			season := seasonFromPosterToken(m[1])
			if id, err := w.st.FindSeasonID(ctx, seriesID, int32(season)); err == nil {
				return id, "poster", true
			}
		}
		return 0, "", false
	}

	// 2) 目录级图片：先给剧集，再给电影，最后给「本目录唯一的视频」
	if kind, ok := dirLevelImageKind(stem); ok {
		if id, ok := w.seriesItemByDir[w.seriesDirOf(dir)]; ok {
			return id, kind, true
		}
		if id, ok := w.movieItemByDir[dir]; ok {
			return id, kind, true
		}
		if id, ok := w.singleItemInDir(dir); ok {
			return id, kind, true
		}
		return 0, "", false
	}

	// 3) `<视频名>-poster.jpg` / `-thumb.jpg` / `-fanart.jpg`
	if idx := strings.LastIndex(stem, "-"); idx > 0 {
		if kind, ok := suffixImageKind(stem[idx+1:]); ok {
			if id, ok := w.itemForVideoStem(dir, stem[:idx]); ok {
				return id, kind, true
			}
		}
	}

	// 4) 与视频同名的图片：Emby 当作缩略图
	if id, ok := w.itemForVideoStem(dir, stem); ok {
		return id, "thumb", true
	}

	return 0, "", false
}

// itemForVideoStem 在当前目录里找「文件名主干」等于 stem 的视频条目。
//
// 用扫描时顺手收集的目录清单，而不是每次 readdir ——
// 一个 3 万文件的库里图片有近万张，逐张 readdir 在 CIFS 上是纯浪费。
func (w *walker) itemForVideoStem(dir, stem string) (int64, bool) {
	want := strings.ToLower(stem)
	for _, name := range w.dirEntries[dir] {
		if !parser.IsVideo(name) {
			continue
		}
		if strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) != want {
			continue
		}
		if id, ok := w.itemByPath[filepath.Join(dir, name)]; ok {
			return id, true
		}
	}
	return 0, false
}

// singleItemInDir 当目录下只有一个视频条目时返回它（用于没有视频名后缀的目录级图片）。
func (w *walker) singleItemInDir(dir string) (int64, bool) {
	var found int64
	n := 0
	for _, name := range w.dirEntries[dir] {
		if !parser.IsVideo(name) {
			continue
		}
		if id, ok := w.itemByPath[filepath.Join(dir, name)]; ok {
			found = id
			n++
		}
	}
	if n == 1 {
		return found, true
	}
	return 0, false
}

// osOpenDir 留作测试替身注入点（当前直接使用 os.Open）。
var osOpenDir = os.Open
