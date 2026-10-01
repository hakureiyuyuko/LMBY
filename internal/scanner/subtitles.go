package scanner

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/parser"
)

// 外挂字幕的归属。
//
// 和图片一样，归属推迟到「整轮遍历结束之后」：WalkDir 按字典序走，字幕可能排在
// 视频前面；而且判断挂给谁要等本目录的视频都建出条目。
//
// 和图片不一样的是匹配方式。图片靠「文件名主干 + 目录」猜，字幕必须靠
// **完整视频主干**做前缀匹配，因为视频名自己可能带点号（真实语料：
// 「SSSS.古立特宇宙 (2023).chs.ass」「攻壳机动队：S.A.C._SSS (2006).chs.ass」）。
// 同一目录下完全可能同时存在「SSSS.mkv」和「SSSS.古立特宇宙 (2023).mkv」，
// 所以候选视频必须按主干**从长到短**试，否则短名会把长名的字幕抢走。
//
// 归属不上的（同目录里没有对应视频、或视频太小被跳过）就当没有，不登记 ——
// 和图片的处理一致：登记一条挂不到条目的字幕没有意义，播放器也没法选。

// subtitleEntry 是遍历阶段攒下来的一个文本外挂字幕。
type subtitleEntry struct {
	path  string
	stem  string
	size  int64
	mtime int64
}

// videoCandidate 是本目录下本轮建出条目的视频，供字幕归属匹配。
type videoCandidate struct {
	itemID int64
	stem   string
}

// collectSubtitle 在遍历阶段把文本外挂字幕攒进 subsByDir。
func (w *walker) collectSubtitle(dir, path string, d fs.DirEntry) {
	info, err := d.Info()
	if err != nil {
		return
	}
	base := d.Name()
	w.subsByDir[dir] = append(w.subsByDir[dir], subtitleEntry{
		path:  path,
		stem:  strings.TrimSuffix(base, filepath.Ext(base)),
		size:  info.Size(),
		mtime: info.ModTime().UnixNano(),
	})
}

// linkSubtitles 把遍历阶段攒下来的外挂字幕归属到同名视频并落库。
//
// 在这一轮之前就已经登记过、而这次没再出现的记录由 cleanupSubtitles 收掉。
func (w *walker) linkSubtitles(ctx context.Context) {
	if len(w.subsByDir) == 0 {
		w.cleanupSubtitles(ctx)
		return
	}

	// 候选视频 = 本轮建出条目的全部视频（含未变的，见 itemByPath 的三处赋值），
	// 按目录分组，主干从长到短。
	byDir := map[string][]videoCandidate{}
	for path, itemID := range w.itemByPath {
		if itemID == 0 {
			continue
		}
		base := filepath.Base(path)
		dir := filepath.Dir(path)
		byDir[dir] = append(byDir[dir], videoCandidate{
			itemID: itemID,
			stem:   strings.TrimSuffix(base, filepath.Ext(base)),
		})
	}
	for dir := range byDir {
		cands := byDir[dir]
		sort.SliceStable(cands, func(i, j int) bool { return len(cands[i].stem) > len(cands[j].stem) })
	}

	seen := map[int64][]string{}
	for dir, subs := range w.subsByDir {
		cands := byDir[dir]
		if len(cands) == 0 {
			continue // 这个目录里没有任何建出条目的视频，字幕无处可挂
		}
		for _, sub := range subs {
			itemID, language, title, forced := matchSubtitleOwner(cands, sub.stem)
			if itemID == 0 {
				continue
			}
			format := parser.SubtitleFormat(sub.path)
			if err := w.st.UpsertSubtitle(ctx, itemID, sub.path, language, title, format,
				forced, sub.size, sub.mtime); err != nil {
				w.issue("warning", sub.path, "登记外挂字幕失败: "+err.Error())
				continue
			}
			seen[itemID] = append(seen[itemID], sub.path)
		}
	}
	w.subsSeen = seen
	w.cleanupSubtitles(ctx)
}

// matchSubtitleOwner 在候选视频里找 subStem 的归属，返回条目 id 与解析出的标记。
//
// 候选已按主干从长到短排序，所以第一个匹配上的就是最贴切的那个。
func matchSubtitleOwner(cands []videoCandidate, subStem string) (itemID int64, language, title string, forced bool) {
	for _, c := range cands {
		lang, t, f, ok := parser.ParseSubtitle(c.stem, subStem)
		if !ok {
			continue
		}
		return c.itemID, lang, t, f
	}
	return 0, "", "", false
}

// cleanupSubtitles 收掉这一轮不再出现的外挂字幕记录。
//
// 只清理「本轮真的扫到的条目」（itemByPath 里的 item）—— 不然扫 A 库会把 B 库的
// 字幕记录误删。库里已经有记录的条目通常只有几千个，够快；没有记录的条目直接跳过，
// 不需要为几万个条目各发一条 delete。
func (w *walker) cleanupSubtitles(ctx context.Context) {
	existing, err := w.st.ListSubtitleItemIDs(ctx)
	if err != nil {
		w.issue("warning", "", "读取外挂字幕记录失败: "+err.Error())
		return
	}
	if len(existing) == 0 {
		return
	}
	scanned := map[int64]bool{}
	for _, id := range w.itemByPath {
		if id != 0 {
			scanned[id] = true
		}
	}
	for _, itemID := range existing {
		if !scanned[itemID] {
			continue
		}
		if keep, ok := w.subsSeen[itemID]; ok {
			if _, err := w.st.DeleteSubtitlesExcept(ctx, itemID, keep); err != nil {
				w.issue("warning", "", "清理外挂字幕记录失败: "+err.Error())
			}
			continue
		}
		if _, err := w.st.DeleteSubtitlesExcept(ctx, itemID, nil); err != nil {
			w.issue("warning", "", "清理外挂字幕记录失败: "+err.Error())
		}
	}
}
