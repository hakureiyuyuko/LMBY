// Package scanner 把媒体库目录同步进 PostgreSQL。
//
// 职责边界：
//   - 遍历文件系统、算指纹、决定「新增/变化/移动/删除」；
//   - 借助 parser 推断条目层级，借助 metadata 读取本地 nfo；
//   - 登记图片路径（二进制永不入库，详见 images.go）。
//
// 刻意不做的事：不探测流信息（交给 probe 包 + tasks 队列），不刮削（M2）。
//
// 关于「删除」：本轮未出现的文件只做**软删除**。网络盘掉线时一次扫描会把整库
// 判定为消失，硬删会造成灾难性数据丢失，真正的清理留给后续按策略执行的任务。
package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/metadata"
	"github.com/hakureiyuyuko/lmby/internal/parser"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// Progress 是扫描进度快照，会通过 SSE 推给前端。
type Progress struct {
	Phase        string    `json:"phase"` // walking | linking | done
	ScanRunID    int64     `json:"scanRunId"`
	LibraryID    int64     `json:"libraryId"`
	Videos       int       `json:"videos"`
	NewFiles     int       `json:"newFiles"`
	ChangedFiles int       `json:"changedFiles"`
	MovedFiles   int       `json:"movedFiles"`
	DeletedFiles int       `json:"deletedFiles"`
	Unchanged    int       `json:"unchanged"`
	ItemsNew     int       `json:"itemsNew"`
	Images       int       `json:"images"`
	Issues       int       `json:"issues"`
	CurrentPath  string    `json:"currentPath"`
	ElapsedMS    int64     `json:"elapsedMs"`
	At           time.Time `json:"at"`
}

// Stats 是扫描结束时的统计。
type Stats struct {
	Dirs         int `json:"dirs"`
	Videos       int `json:"videos"`
	NewFiles     int `json:"newFiles"`
	ChangedFiles int `json:"changedFiles"`
	MovedFiles   int `json:"movedFiles"`
	DeletedFiles int `json:"deletedFiles"`
	Unchanged    int `json:"unchanged"`
	ItemsNew     int `json:"itemsNew"`
	SeriesNew    int `json:"seriesNew"`
	SeasonsNew   int `json:"seasonsNew"`
	EpisodesNew  int `json:"episodesNew"`
	MoviesNew    int `json:"moviesNew"`
	NFORead      int `json:"nfoRead"`
	Images       int `json:"images"`
	Subtitles    int `json:"subtitles"`
	Unrecognized int `json:"unrecognized"`
	Issues       int `json:"issues"`
	// ProbesEnqueued 是本轮扫描后入队的探测任务数。
	ProbesEnqueued int64 `json:"probesEnqueued"`
	ElapsedMS      int64 `json:"elapsedMs"`
}

// Options 控制一次扫描。
type Options struct {
	ScanRunID   int64
	Trigger     string
	OnProgress  func(Progress)
	MinFileSize int64 // 小于该字节数的视频跳过；<=0 表示用默认值
}

const (
	// defaultMinFileSize 挡掉样片碎片与下载残片。
	defaultMinFileSize = 1024 * 1024
	// maxIssues 是单次扫描记录的问题数上限，防止畸形库把表写爆。
	maxIssues = 2000
)

var skipDirNames = map[string]bool{
	"@eadir": true, "#recycle": true, "@recycle": true, "$recycle.bin": true,
	"system volume information": true, ".git": true, "node_modules": true,
	"lost+found": true, ".trash": true, ".trashes": true, "#snapshot": true,
	".snapshot": true, "@snapshot": true, "recycler": true, "thumbs.db": true,
	".temporaryitems": true, "found.000": true,
}

// Scan 执行一次扫描。
func Scan(ctx context.Context, st *store.Store, lib store.Library, opts Options) (Stats, error) {
	if opts.MinFileSize <= 0 {
		opts.MinFileSize = defaultMinFileSize
	}
	if len(lib.Paths) == 0 {
		return Stats{}, errors.New("媒体库没有配置根路径")
	}

	w := &walker{
		st:               st,
		lib:              lib,
		opts:             opts,
		started:          time.Now(),
		dirCache:         map[string]parser.DirInfo{},
		ctxCache:         map[string]dirCtx{},
		dirEntries:       map[string][]string{},
		existingByPath:   map[string]store.LibraryFile{},
		existingByFinger: map[fingerprint][]store.LibraryFile{},
		seen:             map[int64]bool{},
		seriesCache:      map[string]int64{},
		seasonCache:      map[string]int64{},
		episodeCache:     map[string]int64{},
		movieCache:       map[string]int64{},
		extraCache:       map[string]int64{},
		itemByPath:       map[string]int64{},
		seriesItemByDir:  map[string]int64{},
		movieItemByDir:   map[string]int64{},
		imagesByDir:      map[string][]imageEntry{},
		imagesSeen:       map[int64]map[string]bool{},
	}

	if err := w.loadExisting(ctx); err != nil {
		return w.stats, err
	}

	for _, p := range lib.Paths {
		if err := w.walkPath(ctx, strings.TrimSuffix(p.Path, "/")); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return w.stats, err
			}
			w.issue("error", p.Path, "遍历根路径失败: "+err.Error())
		}
	}

	w.linkImages(ctx)

	// 扫描结束后统一入队探测任务。
	//
	// 放在这里而不是「每插一个文件就入队一次」有两个原因：
	//   1. 一条 insert ... select 就能把整个库待探测的文件全排上，避免几万次往返；
	//   2. 上一次扫描中途被中断时残留的 pending 文件也会被自然补上。
	if n, err := st.EnqueueProbesForLibrary(ctx, lib.ID); err != nil {
		w.issue("warning", "", "入队探测任务失败: "+err.Error())
	} else {
		w.stats.ProbesEnqueued = n
	}

	if err := w.finish(ctx); err != nil {
		return w.stats, err
	}

	w.stats.Issues = len(w.issues)
	w.stats.ElapsedMS = time.Since(w.started).Milliseconds()
	return w.stats, nil
}

// ---------------------------------------------------------------- 内部状态

type fingerprint struct {
	size    int64
	mtimeNS int64
}

type imageEntry struct {
	path  string
	base  string
	size  int64
	mtime int64
}

type dirCtx struct {
	category  string
	title     string
	year      int
	season    int
	isSeason  bool
	isExtra   bool
	extraType string
}

func (c dirCtx) hint(libraryKind string) parser.Hint {
	h := parser.Hint{
		LibraryKind: libraryKind,
		ParentTitle: c.title,
		ParentYear:  c.year,
		IsExtraDir:  c.isExtra,
	}
	if c.isSeason {
		h.ParentSeason = c.season
		h.ParentIsSpecials = c.season == 0
	}
	return h
}

type walker struct {
	st      *store.Store
	lib     store.Library
	opts    Options
	started time.Time
	stats   Stats
	issues  []store.ScanIssue

	dirCache   map[string]parser.DirInfo
	ctxCache   map[string]dirCtx
	dirEntries map[string][]string

	// 增量比对用的既有状态
	existingByPath   map[string]store.LibraryFile
	existingByFinger map[fingerprint][]store.LibraryFile
	seen             map[int64]bool

	// 条目去重缓存（key 见各 ensure* 方法）
	seriesCache  map[string]int64
	seasonCache  map[string]int64
	episodeCache map[string]int64
	movieCache   map[string]int64
	extraCache   map[string]int64

	// 本轮产生的映射，供图片归属使用
	itemByPath      map[string]int64
	seriesItemByDir map[string]int64
	movieItemByDir  map[string]int64

	imagesByDir map[string][]imageEntry
	imagesSeen  map[int64]map[string]bool

	processed  int
	lastReport time.Time
}

func (w *walker) issue(severity, path, msg string) {
	if len(w.issues) >= maxIssues {
		return
	}
	w.issues = append(w.issues, store.ScanIssue{Severity: severity, Path: path, Message: msg})
}

func (w *walker) report(phase, current string) {
	if w.opts.OnProgress == nil {
		return
	}
	w.opts.OnProgress(Progress{
		Phase:        phase,
		ScanRunID:    w.opts.ScanRunID,
		LibraryID:    w.lib.ID,
		Videos:       w.stats.Videos,
		NewFiles:     w.stats.NewFiles,
		ChangedFiles: w.stats.ChangedFiles,
		MovedFiles:   w.stats.MovedFiles,
		DeletedFiles: w.stats.DeletedFiles,
		Unchanged:    w.stats.Unchanged,
		ItemsNew:     w.stats.ItemsNew,
		Images:       w.stats.Images,
		Issues:       len(w.issues),
		CurrentPath:  current,
		ElapsedMS:    time.Since(w.started).Milliseconds(),
		At:           time.Now(),
	})
}

// ---------------------------------------------------------------- 增量准备

func (w *walker) loadExisting(ctx context.Context) error {
	files, err := w.st.ListLibraryFiles(ctx, w.lib.ID)
	if err != nil {
		return err
	}
	for _, f := range files {
		w.existingByPath[f.Path] = f
		fp := fingerprint{size: f.SizeBytes, mtimeNS: f.MtimeNS}
		w.existingByFinger[fp] = append(w.existingByFinger[fp], f)
	}
	return nil
}

// takeMoveCandidate 在「本轮尚未见到、指纹完全一致」的旧文件里认领一个，
// 把「删除 + 新增」还原成「移动」，从而保住条目归属与播放进度。
func (w *walker) takeMoveCandidate(size, mtimeNS int64) (store.LibraryFile, bool) {
	cands := w.existingByFinger[fingerprint{size: size, mtimeNS: mtimeNS}]
	for _, c := range cands {
		if w.seen[c.ID] {
			continue
		}
		return c, true
	}
	return store.LibraryFile{}, false
}

// ---------------------------------------------------------------- 遍历

func (w *walker) walkPath(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			w.issue("error", path, "访问失败: "+err.Error())
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}

		dir := filepath.Dir(path)
		w.dirEntries[dir] = append(w.dirEntries[dir], d.Name())

		if d.IsDir() {
			if path == root {
				return nil
			}
			if skipDirNames[strings.ToLower(d.Name())] || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			if w.dirInfo(path).IsImageDir {
				// Backdrops / Screenshots 之类整棵跳过（图片在目录级规则里登记）
				return fs.SkipDir
			}
			w.stats.Dirs++
			return nil
		}

		switch {
		case parser.IsVideo(path):
			w.handleVideo(ctx, dir, path, d)
		case parser.IsImage(path):
			if info, ierr := d.Info(); ierr == nil {
				w.imagesByDir[dir] = append(w.imagesByDir[dir], imageEntry{
					path: path, base: d.Name(), size: info.Size(), mtime: info.ModTime().UnixNano(),
				})
			}
		case parser.IsSubtitle(path):
			w.stats.Subtitles++
		case parser.IsAudio(path):
			// 音乐库已砍：识别但跳过
		case parser.ShouldIgnore(path):
			// 静默跳过
		default:
			w.stats.Unrecognized++
		}
		return nil
	})
}

func (w *walker) dirInfo(path string) parser.DirInfo {
	if di, ok := w.dirCache[path]; ok {
		return di
	}
	di := parser.ParseDir(filepath.Base(path))
	w.dirCache[path] = di
	return di
}

// resolve 依据从库根到目标目录的逐级目录名推断上下文（按目录缓存）。
//
// 关键细节：**库根本身也算一层**。因为用户很可能把库根直接指向一个作品目录
// （例如 `/media/TV/钢之炼金术师 (2009)`），此时剧集名只能从这个根目录得到，
// 漏掉它就会出现「剧集名变成 Season 1」这类错误。
func (w *walker) resolve(root, dir string) dirCtx {
	if ctx, ok := w.ctxCache[dir]; ok {
		return ctx
	}

	out := dirCtx{season: parser.NoSeason}

	chain := []string{root}
	if rel, err := filepath.Rel(root, dir); err == nil && rel != "." && rel != "" {
		cur := root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if part == "" || part == "." || part == ".." {
				continue
			}
			cur = filepath.Join(cur, part)
			chain = append(chain, cur)
		}
	}

	for i, p := range chain {
		di := w.dirInfo(p)
		if di.IsCategory {
			out.category = di.Category
		}
		if di.IsImageDir {
			continue
		}
		if di.IsExtra {
			out.isExtra = true
			out.extraType = di.ExtraType
		}
		if di.IsSeason {
			out.isSeason = true
			out.season = di.Season
		}
		// 只有「作品目录」能提供标题，且不能是分类目录 / 季目录 / 花絮目录。
		// 库根没有年份时也不当作品目录 —— 否则 `/media/Movies` 会被当成作品名。
		if di.IsCategory || di.IsSeason || di.IsExtra || di.Title == "" {
			continue
		}
		if di.Year > 0 || i > 0 {
			out.title = di.Title
			out.year = di.Year
		}
	}

	w.ctxCache[dir] = out
	return out
}

// ---------------------------------------------------------------- 视频文件

func (w *walker) handleVideo(ctx context.Context, dir, path string, d fs.DirEntry) {
	info, err := d.Info()
	if err != nil {
		w.issue("warning", path, "读取文件信息失败: "+err.Error())
		return
	}
	size, mtime := info.Size(), info.ModTime().UnixNano()
	if size < w.opts.MinFileSize {
		w.issue("info", path, fmt.Sprintf("文件过小（%d 字节），已跳过", size))
		return
	}

	w.stats.Videos++
	w.processed++
	// 进度上报：既看文件数也看时间，避免在慢速存储上「前 200 个文件之前界面完全没反应」
	if w.processed%50 == 0 || time.Since(w.lastReport) > 2*time.Second {
		w.report("walking", path)
		w.lastReport = time.Now()
	}

	root := w.rootOf(path)

	// ---- 快速路径：路径已存在且指纹未变
	if ex, ok := w.existingByPath[path]; ok {
		w.seen[ex.ID] = true
		w.itemByPath[path] = ex.ItemID
		if ex.SizeBytes == size && ex.MtimeNS == mtime {
			w.stats.Unchanged++
			return
		}
		if err := w.st.UpdateFileChanged(ctx, ex.ID, size, mtime); err != nil {
			w.issue("error", path, "更新文件失败: "+err.Error())
			return
		}
		w.stats.ChangedFiles++
		w.applyMetadata(ctx, path, ex.ItemID)
		return
	}

	// ---- 移动识别：指纹一致说明只是换了路径
	if moved, ok := w.takeMoveCandidate(size, mtime); ok {
		w.seen[moved.ID] = true
		if err := w.st.MoveFile(ctx, moved.ID, path); err != nil {
			w.issue("error", path, "迁移文件路径失败: "+err.Error())
			return
		}
		w.itemByPath[path] = moved.ItemID
		w.stats.MovedFiles++
		return
	}

	// ---- 新文件：解析 → 建/找条目 → 落文件行
	res := parser.ParseVideo(path, w.resolve(root, dir).hint(w.lib.Kind))

	itemID, created, err := w.ensureItem(ctx, path, dir, res)
	if err != nil {
		w.issue("error", path, "建立条目失败: "+err.Error())
		return
	}
	if _, err := w.st.InsertFile(ctx, itemID, path, size, mtime, containerOf(path)); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			w.issue("warning", path, "该路径已被其它条目占用，已跳过")
			return
		}
		w.issue("error", path, "写入文件失败: "+err.Error())
		return
	}

	w.stats.NewFiles++
	w.itemByPath[path] = itemID
	if created {
		w.stats.ItemsNew++
	}
	w.applyMetadata(ctx, path, itemID)
}

// rootOf 找出路径属于哪个库根（支持多根路径）。
func (w *walker) rootOf(path string) string {
	best := ""
	for _, p := range w.lib.Paths {
		base := strings.TrimSuffix(p.Path, "/")
		if path == base || strings.HasPrefix(path, base+string(filepath.Separator)) {
			if len(base) > len(best) {
				best = base
			}
		}
	}
	return best
}

// ---------------------------------------------------------------- 条目归属

func (w *walker) ensureItem(ctx context.Context, path, dir string, res parser.Result) (int64, bool, error) {
	switch res.Kind {
	case parser.KindEpisode:
		return w.ensureEpisodeItem(ctx, dir, res)
	case parser.KindMovie:
		return w.ensureMovieItem(ctx, dir, res)
	case parser.KindExtra:
		return w.ensureExtraItem(ctx, path, dir, res)
	default:
		// 判定不了也收下，标题用文件名 —— 至少界面上能看到，而不是被静默丢弃。
		title := res.Title
		if title == "" {
			title = filepath.Base(path)
		}
		id, created, err := w.ensureDedup(ctx, w.movieCache, "movie", title, res.Year)
		if err == nil {
			w.movieItemByDir[dir] = id
		}
		return id, created, err
	}
}

func (w *walker) ensureEpisodeItem(ctx context.Context, dir string, res parser.Result) (int64, bool, error) {
	seriesTitle := res.Title
	if seriesTitle == "" {
		seriesTitle = filepath.Base(dir)
	}

	seriesID, _, err := w.ensureDedup(ctx, w.seriesCache, "series", seriesTitle, res.Year)
	if err != nil {
		return 0, false, err
	}
	// 注意：新建剧集的计数已经在 ensureDedup 里做了，这里不能重复加，否则
	// 「新建条目」的统计会比实际条目数多。
	seriesCreated := false
	w.seriesItemByDir[w.seriesDirOf(dir)] = seriesID

	// 没写季号的剧集按第 1 季收，符合 Emby 习惯
	season := res.Season
	if season == parser.NoSeason {
		season = 1
	}

	seasonKey := fmt.Sprintf("%d:%d", seriesID, season)
	seasonID, ok := w.seasonCache[seasonKey]
	if !ok {
		seasonID, err = w.st.FindSeasonID(ctx, seriesID, int32(season))
		if errors.Is(err, store.ErrNotFound) {
			seasonID, err = w.st.InsertItem(ctx, store.NewItem{
				LibraryID: w.lib.ID, Kind: "season", ParentID: &seriesID, SeriesID: &seriesID,
				SeasonNum: seasonInt32(season), Title: parser.SeasonLabel(season),
			})
			if err == nil {
				w.stats.SeasonsNew++
			}
		}
		if err != nil {
			return 0, false, err
		}
		w.seasonCache[seasonKey] = seasonID
	}

	// 解析器拿到的标题若等于剧集名，说明文件名里没有集标题
	episodeTitle := res.Title
	if episodeTitle == seriesTitle {
		episodeTitle = ""
	}

	// 集号未知时不按（季+集）去重：同一季里可能有多个无法编号的文件，
	// 合并成一个条目是错的。重复扫描不会造成重复条目 ——
	// 因为已存在的文件走快速路径，根本不会再建条目。
	if res.Episode > 0 {
		epKey := fmt.Sprintf("%d:%d:%d", seriesID, season, res.Episode)
		if id, cached := w.episodeCache[epKey]; cached {
			return id, seriesCreated, nil
		}
		id, err := w.st.FindEpisodeID(ctx, seriesID, int32(season), int32(res.Episode))
		if errors.Is(err, store.ErrNotFound) {
			id, err = w.st.InsertItem(ctx, store.NewItem{
				LibraryID: w.lib.ID, Kind: "episode", ParentID: &seasonID, SeriesID: &seriesID,
				SeasonNum: seasonInt32(season), EpisodeNum: int32Ptr(res.Episode),
				EpisodeEnd: int32Ptr(res.EpisodeEnd), Title: episodeTitle,
			})
			if err == nil {
				w.stats.EpisodesNew++
			}
		}
		if err != nil {
			return 0, false, err
		}
		w.episodeCache[epKey] = id
		return id, seriesCreated, nil
	}

	id, err := w.st.InsertItem(ctx, store.NewItem{
		LibraryID: w.lib.ID, Kind: "episode", ParentID: &seasonID, SeriesID: &seriesID,
		SeasonNum: seasonInt32(season), Title: episodeTitle,
	})
	if err != nil {
		return 0, false, err
	}
	w.stats.EpisodesNew++
	return id, seriesCreated, nil
}

func (w *walker) ensureMovieItem(ctx context.Context, dir string, res parser.Result) (int64, bool, error) {
	title := res.Title
	if title == "" {
		title = filepath.Base(dir)
	}
	id, created, err := w.ensureDedup(ctx, w.movieCache, "movie", title, res.Year)
	if err == nil {
		w.movieItemByDir[dir] = id
	}
	return id, created, err
}

func (w *walker) ensureExtraItem(ctx context.Context, path, dir string, res parser.Result) (int64, bool, error) {
	title := res.Title
	if title == "" {
		title = filepath.Base(path)
	}

	// 花絮挂在同级内容下：优先用本轮已建的条目，找不到再按目录名查库
	parentID := int64(0)
	if id, ok := w.seriesItemByDir[w.seriesDirOf(dir)]; ok {
		parentID = id
	} else if id, ok := w.movieItemByDir[dir]; ok {
		parentID = id
	} else {
		name := filepath.Base(w.seriesDirOf(dir))
		if id, err := w.st.FindSeriesID(ctx, w.lib.ID, name, nil); err == nil {
			parentID = id
		} else if id, err := w.st.FindMovieID(ctx, w.lib.ID, name, nil); err == nil {
			parentID = id
		}
	}

	if parentID == 0 {
		// 找不到父作品时不要把文件丢掉：当作电影收下，至少用户能在库里看到它，
		// 而不是变成一条「找不到花絮所属的作品条目」的扫描错误。
		id, created, err := w.ensureDedup(ctx, w.movieCache, "movie", title, 0)
		if err == nil {
			w.movieItemByDir[dir] = id
			w.issue("info", path, "被判为花絮但找不到所属作品，已按电影收录")
		}
		return id, created, err
	}

	key := fmt.Sprintf("%d:%s", parentID, strings.ToLower(title))
	if id, ok := w.extraCache[key]; ok {
		return id, false, nil
	}
	id, err := w.st.FindExtraID(ctx, parentID, title)
	if errors.Is(err, store.ErrNotFound) {
		id, err = w.st.InsertItem(ctx, store.NewItem{
			LibraryID: w.lib.ID, Kind: "extra", ParentID: &parentID,
			ExtraType: res.ExtraType, Title: title,
		})
	}
	if err != nil {
		return 0, false, err
	}
	w.extraCache[key] = id
	return id, false, nil
}

// ensureDedup 按（库, 标题, 年份）找或建 series/movie 条目。
func (w *walker) ensureDedup(ctx context.Context, cache map[string]int64, kind, title string, year int) (int64, bool, error) {
	key := strings.ToLower(title) + "|" + itoa(year)
	if id, ok := cache[key]; ok {
		return id, false, nil
	}

	var (
		id  int64
		err error
	)
	if kind == "movie" {
		id, err = w.st.FindMovieID(ctx, w.lib.ID, title, int32Ptr(year))
	} else {
		id, err = w.st.FindSeriesID(ctx, w.lib.ID, title, int32Ptr(year))
	}

	created := false
	if errors.Is(err, store.ErrNotFound) {
		id, err = w.st.InsertItem(ctx, store.NewItem{
			LibraryID: w.lib.ID, Kind: kind, Title: title, Year: int32Ptr(year),
		})
		if err == nil {
			created = true
			switch kind {
			case "movie":
				w.stats.MoviesNew++
			case "series":
				w.stats.SeriesNew++
			}
		}
	}
	if err != nil {
		return 0, false, err
	}
	cache[key] = id
	return id, created, nil
}

// seriesDirOf 找剧集目录（季目录的父目录），用于把目录级图片挂到剧集上。
func (w *walker) seriesDirOf(dir string) string {
	if w.dirInfo(dir).IsSeason {
		return filepath.Dir(dir)
	}
	return dir
}

// ---------------------------------------------------------------- 元数据

// applyMetadata 读取同名 nfo 并写入条目；nfo 不存在时只记录文件技术标记。
func (w *walker) applyMetadata(ctx context.Context, path string, itemID int64) {
	res := parser.ParseVideo(path, parser.Hint{})
	if !res.Tech.IsZero() {
		tech := map[string]any{}
		if res.Tech.Codec != "" {
			tech["codec"] = res.Tech.Codec
		}
		if res.Tech.Resolution != "" {
			tech["resolution"] = res.Tech.Resolution
		}
		if res.Tech.Chroma != "" {
			tech["chroma"] = res.Tech.Chroma
		}
		if res.Tech.FrameRate != "" {
			tech["frameRate"] = res.Tech.FrameRate
		}
		if res.Tech.Bitrate != "" {
			tech["bitrate"] = res.Tech.Bitrate
		}
		if len(res.Tech.Features) > 0 {
			tech["features"] = res.Tech.Features
		}
		if err := w.st.SetItemFileTech(ctx, itemID, tech); err != nil {
			w.issue("warning", path, "写入文件技术标记失败: "+err.Error())
		}
	}

	nfoPath := filepath.Join(filepath.Dir(path), metadata.NFOFileName(filepath.Base(path)))
	md, err := metadata.TryReadNFO(nfoPath)
	if err != nil {
		w.issue("warning", nfoPath, "解析 nfo 失败: "+err.Error())
		return
	}
	if md == nil {
		return
	}
	w.stats.NFORead++

	meta := store.ItemMeta{
		Title:          md.Title,
		SortTitle:      md.SortTitle,
		OriginalTitle:  md.OriginalTitle,
		Year:           md.Year,
		Overview:       md.Overview,
		Tagline:        md.Tagline,
		Rating:         md.Rating,
		OfficialRating: md.OfficialRating,
		Genres:         md.Genres,
		Tags:           md.Tags,
		Studios:        md.Studios,
		ProviderIDs:    md.ProviderIDs,
		PremiereDate:   md.PremiereDate,
		MatchState:     "local",
	}
	if md.RuntimeMinutes > 0 {
		t := int64(md.RuntimeMinutes) * metadata.TicksPerMinute
		meta.RuntimeTicks = &t
	}
	if err := w.st.ApplyItemMeta(ctx, itemID, meta); err != nil {
		w.issue("warning", nfoPath, "写入条目元数据失败: "+err.Error())
	}
}

// ---------------------------------------------------------------- 图片归属

// linkImages 在所有文件处理完后统一解析图片归属。
//
// 放在最后是因为要可靠知道「同目录下哪个视频对应哪个条目」，
// 而 WalkDir 按文件名字典序访问，图片完全可能先于视频出现。
//
// 这里的失败都是逐条 issue（图片登记不了不该让整轮扫描失败），所以没有返回值。
func (w *walker) linkImages(ctx context.Context) {
	w.report("linking", "")

	dirs := make([]string, 0, len(w.imagesByDir))
	for d := range w.imagesByDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		for _, img := range w.imagesByDir[dir] {
			itemID, kind, ok := w.resolveImageOwner(ctx, dir, img)
			if !ok {
				continue
			}
			if err := w.st.UpsertImage(ctx, itemID, kind, img.path, img.size, img.mtime); err != nil {
				w.issue("warning", img.path, "登记图片失败: "+err.Error())
				continue
			}
			if w.imagesSeen[itemID] == nil {
				w.imagesSeen[itemID] = map[string]bool{}
			}
			w.imagesSeen[itemID][img.path] = true
			w.stats.Images++
		}
	}

	// 清理本条目下已不存在的本地图片记录（保留远程图与用户上传图）
	itemIDs := make([]int64, 0, len(w.imagesSeen))
	for id := range w.imagesSeen {
		itemIDs = append(itemIDs, id)
	}
	sort.Slice(itemIDs, func(i, j int) bool { return itemIDs[i] < itemIDs[j] })

	for _, itemID := range itemIDs {
		keep := make([]string, 0, len(w.imagesSeen[itemID]))
		for p := range w.imagesSeen[itemID] {
			keep = append(keep, p)
		}
		if _, err := w.st.DeleteImagesExcept(ctx, itemID, keep); err != nil {
			w.issue("warning", "", "清理图片记录失败: "+err.Error())
		}
	}
}

// ---------------------------------------------------------------- 收尾

func (w *walker) finish(ctx context.Context) error {
	// 1) 未在本轮出现的文件 → 软删除
	gone := make([]int64, 0)
	for _, ex := range w.existingByPath {
		if !w.seen[ex.ID] {
			gone = append(gone, ex.ID)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })

	deleted, err := w.st.MarkFilesDeleted(ctx, gone)
	if err != nil {
		return err
	}
	w.stats.DeletedFiles = int(deleted)

	// 2) 写回问题清单
	if err := w.st.AddScanIssues(ctx, w.opts.ScanRunID, w.lib.ID, w.issues); err != nil {
		return err
	}
	w.report("done", "")
	return nil
}

// ---------------------------------------------------------------- 小工具

// int32Ptr 把年份之类「0 表示未知」的整数转成可空列的值。
func int32Ptr(v int) *int32 {
	if v == 0 {
		return nil
	}
	n := int32(v)
	return &n
}

// seasonInt32 用季号构造非空值：0 是合法的特典季，不能变成 NULL，
// 否则 (parent_id, season_number) 的唯一约束会失效。
func seasonInt32(v int) *int32 {
	n := int32(v)
	return &n
}

func containerOf(path string) string {
	return strings.TrimPrefix(extOf(path), ".")
}

func extOf(name string) string {
	return strings.ToLower(filepath.Ext(name))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
