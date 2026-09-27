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
	"unicode/utf8"

	"github.com/hakureiyuyuko/lmby/internal/metadata"
	"github.com/hakureiyuyuko/lmby/internal/parser"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/textutil"
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
	// RevivedFiles 是本轮「复活」的文件数：以前被扫成消失（软删）过、这轮又见到了。
	RevivedFiles int `json:"revivedFiles"`
	// SkippedDeletions 是本轮**本该删但没删**的文件数（命中安全阀或目录读不到）。
	// 大于 0 说明这次结果不完整，界面上要看得到。
	SkippedDeletions int `json:"skippedDeletions"`
	// UnreadableRoots 是读不到的库根路径数（网盘掉线的典型症状）。
	UnreadableRoots int `json:"unreadableRoots"`
	ItemsNew        int `json:"itemsNew"`
	SeriesNew       int `json:"seriesNew"`
	SeasonsNew      int `json:"seasonsNew"`
	EpisodesNew     int `json:"episodesNew"`
	MoviesNew       int `json:"moviesNew"`
	NFORead         int `json:"nfoRead"`
	Images          int `json:"images"`
	Subtitles       int `json:"subtitles"`
	Unrecognized    int `json:"unrecognized"`
	Issues          int `json:"issues"`
	// ProbesEnqueued 是本轮扫描后入队的探测任务数。
	ProbesEnqueued int64 `json:"probesEnqueued"`
	ElapsedMS      int64 `json:"elapsedMs"`
}

// Options 控制一次扫描。
type Options struct {
	ScanRunID  int64
	Trigger    string
	OnProgress func(Progress)

	// RefreshMetadata 为真时，**文件没变也重读一遍同目录的 nfo**。
	//
	// 为什么需要它：nfo 在本项目里是「权威元数据」（人工整理的），
	// 用户手改 nfo 之后必须有个办法让它生效 —— 否则得去 touch 媒体文件，
	// 而网络盘上 touch 会连带把重新探测也触发一遍（几万个文件，得不偿失）。
	RefreshMetadata bool

	MinFileSize int64 // 小于该字节数的视频跳过；<=0 表示用默认值

	// MaxDeleteRatio 是「一次扫描最多能删掉存活文件的比例」（<=0 表示用内置默认；
	// 负数 = 不限制，见 config.ScanConfig.MaxDeleteRatio 的说明）。
	MaxDeleteRatio float64
}

const (
	// defaultMinFileSize 只用来挡真垃圾：缩略图、被改成 .mp4 的文本、下载残片。
	//
	// ⚠️ 它**不是画质/时长过滤器** —— 2026-09-22 用户真踩到：
	// 一个「连载动画」目录里 63 个文件全部被跳过，因为它们是**28 秒的 1080p h264 短片**
	// （ffprobe 确认是有效视频）而单文件只有 646 KB —— 旧默认 1 MiB 把正常片源当垃圾了。
	// 所以默认压到 64 KiB：比它小的东西几乎不可能是视频，而再短的正常片段也不会这么小。
	// 真要调：config.toml 的 `[scan] min_file_size = <字节>`（0 = 用这个默认值）。
	defaultMinFileSize = 64 * 1024
	// maxIssues 是单次扫描记录的问题数上限，防止畸形库把表写爆。
	maxIssues = 2000
	// defaultMaxDeleteRatio 是「一次扫描最多能删掉存活文件的比例」的默认安全阀。
	defaultMaxDeleteRatio = 0.2
	// minFilesForDeleteGuard 是安全阀生效的**最小规模**：要删的文件不到这么多时
	// 不看比例。小库（测试库、家庭照片库）本来是「删两个只就少一半」，
	// 按比例卡会把正常删文件也拦下来，不值当。
	minFilesForDeleteGuard = 20
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
		metaApplied:      map[int64]bool{},
		dirMetaChecked:   map[int64]bool{},
		unreadableRoots:  map[string]bool{},
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

	w.stats.UnreadableRoots = len(w.unreadableRoots)
	w.stats.Issues = len(w.issues)
	w.stats.ElapsedMS = time.Since(w.started).Milliseconds()

	if err := w.finish(ctx); err != nil {
		return w.stats, err
	}

	// 安全阀拦下了删除（比例闸）→ 扫描结果不可信，标成失败让界面说出来。
	if w.guardMsg != "" {
		return w.stats, errors.New(w.guardMsg)
	}
	// 全部库根都读不到 = 这次什么都没读到（网盘掉线的典型）：文件一个都没删
	//（闸一只会保护读不到的子树，闸二在这里不一定会触发，比如库里本来就没几个文件），
	// 但也绝不能说「成功」—— 那会让人以为库真的空了。
	if len(w.unreadableRoots) >= len(lib.Paths) {
		return w.stats, fmt.Errorf("全部 %d 个根路径都读不到（网络盘掉线或权限问题？）：本次没有删除任何文件，请检查挂载后重扫",
			len(lib.Paths))
	}
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
	// metaApplied 记录本轮已经写过元数据的条目。
	// 剧集目录的 tvshow.nfo 会被每一集各触发一次，没这个会重复读 130 遍。
	metaApplied map[int64]bool
	// dirMetaChecked 记录重扫时已补过目录级 nfo 的条目（同上，避免重复查库）。
	dirMetaChecked map[int64]bool

	// 本轮读不到的路径（网盘掉线时 WalkDir 会在根或子目录上报错）。
	//
	// 它存在的唯一理由：**「本轮没看见」不等于「文件没了」**。
	// 读不到的子树一律不参与删除判定，否则一次掉线就把人家的库清空了。
	unreadable []string
	// unreadableRoots 是「整棵读不到」的库根；全都读不到时本次扫描直接判失败。
	unreadableRoots map[string]bool

	// guardMsg 非空表示「扫描跑完了但结果不可信」（安全阀拦下了删除）：
	// 扫描状态会被标成失败，并把这句人话写进 error 里给界面看。
	guardMsg string

	processed  int
	lastReport time.Time
}

func (w *walker) issue(severity, path, msg string) {
	if len(w.issues) >= maxIssues {
		return
	}
	// 这里做一次净化：文本可能来自文件系统，也可能内嵌在系统调用的错误信息里
	// （`open /mnt/media/…: Host is down` 这种会把文件名原样带进来），而库是 UTF-8 的。
	// 集中在这一处，后面所有 w.issue(...) 的调用点就不用各自留神。
	w.issues = append(w.issues, store.ScanIssue{
		Severity: severity,
		Path:     textutil.Valid(path),
		Message:  textutil.Valid(msg),
	})
}

// noteUnreadable 记下「本轮读不到」的位置。
//
// 网盘掉线时它在库根上报错（`lstat /mnt/media/…: host is down`），掉权限时在某个
// 子目录上报错。两种情况都要记：它下面的文件本轮「没看见」，但那不等于文件没了。
func (w *walker) noteUnreadable(root, path string) {
	if path == root {
		w.unreadableRoots[root] = true
	}
	w.unreadable = append(w.unreadable, path)
}

// underUnreadable 判断某个已入库的路径是否落在本轮读不到的位置下。
//
// 前缀比较而不是精确匹配：读不到的是**整棵**目录，它下面的文件同样没被看到。
func (w *walker) underUnreadable(path string) bool {
	for _, p := range w.unreadable {
		if path == p || strings.HasPrefix(path, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// badNameReason 回答「这个目录项的名字能不能进数据库」，空串表示能。
//
// 为什么要单独判断：库是 UTF-8 的，路径里只要有非法字节就写不进去。而坏的不只是
// 这一个文件 —— 扫描问题是**整批**写的，2026-09-25 实测一条坏路径就让整批失败、
// 扫描被判成「失败」，后面几百条问题全部丢失（界面只留下失败前面写进去的 63 条）。
// 所以宁可在遍历时就把它挑出来跳过，连探测/播放都别去试。
//
// 坏名字的十六进制写进说明里：界面只能把非法字节显示成 U+FFFD，看不出到底是什么，
// 给了十六进制，用户可以照着 `ls | xxd` 找到那个文件（改名后重扫即可入库）。
func badNameReason(name string) string {
	if utf8.ValidString(name) {
		return ""
	}
	return fmt.Sprintf("文件名含非 UTF-8 字节（%s），已跳过：数据库是 UTF-8，存不下这个路径；把文件改名后重扫即可",
		textutil.FirstInvalid(name))
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
			w.noteUnreadable(root, path)
			w.issue("error", path, "访问失败: "+err.Error())
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}

		// 名字不是合法 UTF-8 的目录项：不入库（也跳过整棵目录）——
		// 详见 badNameReason 的注释，这是「一条坏名字搞挂整个扫描」的修复点。
		if reason := badNameReason(d.Name()); reason != "" {
			w.issue("warning", path, reason)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
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
		// 把「阈值」和「怎么改」写进问题描述：只说「文件过小」用户不知道是多小、
		// 也不知道能不能调（这是真踩过的坑：正常短片被当垃圾跳了，界面上看不出原因）。
		w.issue("info", path, fmt.Sprintf(
			"文件过小（%d 字节 < 阈值 %d），已跳过 —— 阈值可在 config.toml 的 [scan] min_file_size 调整",
			size, w.opts.MinFileSize))
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

	// ---- 快速路径：路径已存在
	if ex, ok := w.existingByPath[path]; ok {
		w.seen[ex.ID] = true
		w.itemByPath[path] = ex.ItemID

		// 这一行以前被扫成「消失」过（最典型的成因：网盘掉线时整库软删），
		// 现在文件又见到了 → 必须**复活**它。
		//
		// 不能当新文件去插：media_files.path 上有唯一约束（墓碑行也占着那个路径），
		// 插进去只会撞约束，换来一条「该路径已被其它条目占用」的假警告 —— 而文件
		// 永远回不来，每轮重扫都重报一次（2026-09-25 实测：38461 个文件被这么钉死）。
		if ex.DeletedAt != nil {
			if err := w.st.ReviveFile(ctx, ex.ID, size, mtime); err != nil {
				w.issue("error", path, "恢复已删除的文件记录失败: "+err.Error())
				return
			}
			w.stats.RevivedFiles++
			if ex.SizeBytes != size || ex.MtimeNS != mtime {
				// 内容也变了：按「变化」处理（探测状态已在 ReviveFile 里退回 pending）
				w.applyMetadata(ctx, path, ex.ItemID)
			}
			return
		}

		if ex.SizeBytes == size && ex.MtimeNS == mtime {
			w.stats.Unchanged++
			if w.opts.RefreshMetadata {
				// 「重新导入 nfo」：文件没变也重读一遍 nfo。
				// 这只多一次小文件读（实测 CIFS 上 ~11ms），且不会碰媒体文件本身。
				w.applyMetadata(ctx, path, ex.ItemID)
				// 目录级 nfo（tvshow.nfo / season.nfo）也要认领：
				// 它本来是在 ensureItem 里读的，而那条路只在新建/变更时走 ——
				// 重扫时文件没变，用户手改了 tvshow.nfo 就永远不生效。
				w.refreshDirMetadata(ctx, ex.ItemID, dir)
			}
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
	// 作品级元数据在剧集目录的 tvshow.nfo 里（同名 nfo 是文件级的）
	w.applyDirMetadata(ctx, w.seriesDirOf(dir), seriesID, seriesNFONames...)

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
		// 季目录里可能有 season.nfo（本地优先同样适用于季）
		w.applyDirMetadata(ctx, dir, seasonID, seasonNFONames(season)...)
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
		// 同名 nfo 找不到时还有 movie.nfo / folder.nfo 这两种目录级写法
		// （注意：同名 nfo 会在后面的 applyMetadata 里覆盖这里写入的值，优先级是对的）
		w.applyDirMetadata(ctx, dir, id, movieNFONames...)
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
		// 「格式不对」也归为「本地没东西可用」：记一个问题，不标 nfo 状态，
		// 那条就会回到可刮削的队列里（用户明确要求：本地没有/格式不对再去刮）。
		w.issue("warning", nfoPath, "解析 nfo 失败（已按「无本地元数据」处理）: "+err.Error())
		return
	}
	if md == nil {
		return
	}
	w.applyNFO(ctx, md, nfoPath, itemID)
}

// applyNFO 把一份解析好的 nfo 写进条目，并标上「元数据来自 nfo」。
func (w *walker) applyNFO(ctx context.Context, md *metadata.Metadata, nfoPath string, itemID int64) {
	w.stats.NFORead++

	meta := itemMetaFromNFO(md)
	if nfoHasMetadata(md) {
		// 有 nfo 就标上「元数据来自 nfo（人工整理）」：刮削默认不会碰它。
		// 这是用户明确拍板的策略 —— nfo 是花了大力气人工做的，TMDB 不许覆盖它，
		// 只有没 nfo 的条目才去刮（见 docs/REQUIREMENTS.md §0 的决策表）。
		meta.MatchState = store.MatchStateNFO
		meta.MetadataSource = store.MetadataSourceNFO
	}
	if err := w.st.ApplyItemMeta(ctx, itemID, meta); err != nil {
		w.issue("warning", nfoPath, "写入条目元数据失败: "+err.Error())
	}

	// 演职员单独写一张表（一对多 + 每人带角色）。
	//
	// **只在 nfo 里真有演职员时才写**：为空说明这份 nfo 没带演职员，
	// 不该把库里已有的清掉（比如将来从 TMDB 拉的，或者目录级 nfo 里的）。
	if people := peopleFromNFO(md); len(people) > 0 {
		if err := w.st.ReplaceItemPeople(ctx, itemID, people); err != nil {
			w.issue("warning", nfoPath, "写入演职员失败: "+err.Error())
		}
	}
}

// refreshDirMetadata 在「重扫但文件没变」时补读目录级 nfo。
//
// 为什么需要单独一条路：tvshow.nfo / season.nfo 是在 ensureItem 里读的，
// 而那条路只在「新建条目 / 文件变更」时才走。重扫时文件没变，
// 用户手改了 tvshow.nfo 就永远不生效（这个坑在实测里踩到过：
// 335 条电影/episode 都认领了 nfo，就 2 个剧集还是 TMDB 的标题）。
//
// 它靠「文件所属条目的 parent/series 指针」找剧集与季，不靠标题匹配 ——
// 元数据刮过之后标题可能与扫描时不一致，按标题查会查不到。
func (w *walker) refreshDirMetadata(ctx context.Context, itemID int64, dir string) {
	if w.dirMetaChecked[itemID] {
		return
	}
	w.dirMetaChecked[itemID] = true

	it, err := w.st.GetItem(ctx, itemID)
	if err != nil {
		return
	}

	switch it.Kind {
	case "movie":
		w.applyDirMetadata(ctx, dir, itemID, movieNFONames...)
	case "episode":
		if it.SeriesID != nil && *it.SeriesID != 0 {
			w.applyDirMetadata(ctx, w.seriesDirOf(dir), *it.SeriesID, seriesNFONames...)
		}
		if it.ParentID != nil && *it.ParentID != 0 {
			season := 0
			if it.SeasonNum != nil {
				season = int(*it.SeasonNum)
			}
			w.applyDirMetadata(ctx, dir, *it.ParentID, seasonNFONames(season)...)
		}
	}
}

// applyDirMetadata 读「目录级」的 nfo 并写进条目：tvshow.nfo / movie.nfo / season.nfo。
//
// 为什么需要：同名 nfo（`S01E01.nfo`）是「文件级」的，而作品级元数据
// （一部剧的标题/简介/流派、外部 id）在 Emby 的布局里存在剧集目录的 `tvshow.nfo`。
// 不读它的话「本地优先」在剧集层面就失效了 —— 每个剧集都会被 TMDB 覆盖元数据。
//
// names 按优先级从高到低试；同一个条目在一次扫描里只处理一次
// （否则一个剧集的 130 集会把同一个 tvshow.nfo 读 130 遍）。
func (w *walker) applyDirMetadata(ctx context.Context, dir string, itemID int64, names ...string) {
	if w.metaApplied[itemID] {
		return
	}
	w.metaApplied[itemID] = true

	// 目标条目已经有「同名 nfo」给的元数据时，目录级 nfo 不越权覆盖它
	// （同名 nfo 是文件级的，优先级更高）。注意判断的是**目标条目**：
	// 一集自己有 S01E01.nfo 不应当阻止它所属剧集去认领 tvshow.nfo。
	if it, err := w.st.GetItem(ctx, itemID); err == nil && it.MetadataSource == store.MetadataSourceNFO {
		return
	}

	for _, name := range names {
		p := filepath.Join(dir, name)
		md, err := metadata.TryReadNFO(p)
		if err != nil {
			w.issue("warning", p, "解析 nfo 失败（已按「无本地元数据」处理）: "+err.Error())
			return
		}
		if md == nil {
			continue
		}
		w.applyNFO(ctx, md, p, itemID)
		return
	}
}

// 目录级 nfo 的候选文件名（按优先级）。
var (
	seriesNFONames = []string{"tvshow.nfo", "show.nfo"}
	movieNFONames  = []string{"movie.nfo", "folder.nfo"}
)

// seasonNFONames 返回季目录里可能存在的 nfo 名（season.nfo、season01.nfo …）。
func seasonNFONames(season int) []string {
	return []string{
		"season.nfo",
		fmt.Sprintf("season%02d.nfo", season),
		fmt.Sprintf("season%d.nfo", season),
	}
}

// itemMetaFromNFO 把解析出来的 nfo 转成要落库的元数据。
func itemMetaFromNFO(md *metadata.Metadata) store.ItemMeta {
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
	}
	if md.RuntimeMinutes > 0 {
		t := int64(md.RuntimeMinutes) * metadata.TicksPerMinute
		meta.RuntimeTicks = &t
	}
	return meta
}

// peopleFromNFO 把 nfo 里的演职员转成要落库的形状。
//
// nfo 解析器早就把 <actor>（含 role/type/tmdbid…）、<director>、<credits> 读出来了，
// 但一直没落库；M6 的详情页要用，这里把它接上（**本地优先**：不联网、不占 API 配额）。
func peopleFromNFO(md *metadata.Metadata) []store.ItemPerson {
	out := make([]store.ItemPerson, 0, len(md.People))
	for i, p := range md.People {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		out = append(out, store.ItemPerson{
			Name:        name,
			Role:        strings.TrimSpace(p.Kind),
			Character:   strings.TrimSpace(p.Role),
			Order:       i,
			ProviderIDs: p.ProviderIDs,
		})
	}
	return out
}

// nfoHasMetadata 判断这份 nfo 是否真的带了人工整理的元数据。
//
// 为什么要有这个判断：空的 nfo（或只写了技术字段的）不该把条目钉成
// 「nfo 元数据」—— 那样它就永远不会被刮削，而用户其实什么也没写。
func nfoHasMetadata(md *metadata.Metadata) bool {
	return md.Title != "" || md.OriginalTitle != "" || md.Overview != "" ||
		md.Year != nil || md.Rating != nil ||
		len(md.Genres) > 0 || len(md.Studios) > 0 || len(md.ProviderIDs) > 0
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
			if err := w.st.UpsertImage(ctx, itemID, kind, img.path, store.ImageSourceLocal, 0, 0, img.size, img.mtime); err != nil {
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

// deletePlan 是「本轮该删哪些文件」的结论。
//
// 单独拎成纯函数（只读 walker 状态、不碰数据库）是为了能直接单测：这两道闸关系到
// 「一次掉盘会不会把整库清空」，不能只靠真盘上试。
type deletePlan struct {
	// gone 是真要软删的文件行 id（guard 非空时一定是空）。
	gone []int64
	// skippedUnreadable 是因「所在目录本轮读不到」而没参与判定的文件数。
	skippedUnreadable int
	// wouldDelete 是本轮「本该删」的总数（guard 非空时这就是被拦下的量）。
	wouldDelete int
	// alive 是本库本轮开头的存活文件数（比例闸的分母）。
	alive int
	// guard 非空表示比例闸拦下：一个都不删，扫描要被判失败。
	guard string
}

func (w *walker) planDeletions() deletePlan {
	var p deletePlan
	for _, ex := range w.existingByPath {
		if ex.DeletedAt != nil {
			continue // 已经是墓碑了，不用再删；这行要留着给「复活」用
		}
		p.alive++
		if w.seen[ex.ID] {
			continue
		}
		// 闸一：读不到的子树下的文件不参与删除判定。
		if w.underUnreadable(ex.Path) {
			p.skippedUnreadable++
			continue
		}
		p.gone = append(p.gone, ex.ID)
	}
	sort.Slice(p.gone, func(i, j int) bool { return p.gone[i] < p.gone[j] })

	// 闸二：比例闸。整棵挂载掉线（目录读成空的）就是「要删掉接近 100%」这种形态。
	// 只在规模够大时生效 —— 小库本来就是「删两个就少一半」，按比例卡会把正常删除也拦下。
	//
	// 阀值语义：0 = 用内置默认，负数 = 不限制（真要删掉大部分文件时的出路），
	// 正数 = 自定义比例。
	limit := w.opts.MaxDeleteRatio
	if limit == 0 {
		limit = defaultMaxDeleteRatio
	}
	if n := len(p.gone); n >= minFilesForDeleteGuard && p.alive > 0 && limit > 0 {
		if ratio := float64(n) / float64(p.alive); ratio > limit {
			p.guard = fmt.Sprintf(
				"本次要删 %d 个文件（占存活 %d 个的 %.0f%%），超过 [scan] max_delete_ratio=%.2f 的安全阀，已全部跳过：通常意味着网盘/共享掉线或挂载点变空。确认文件真没了，再把该值调大（负数 = 不限制）后重扫",
				n, p.alive, ratio*100, limit)
			// 统计仍要报出「本该删多少」；但 gone 清空 —— 它是一个「可以安全交给
			// MarkFilesDeleted」的承诺，不能留半个需要调用方自己记得别用。
			p.wouldDelete = n
			p.gone = nil
		}
	}
	return p
}

func (w *walker) finish(ctx context.Context) error {
	// 1) 未在本轮出现的文件 → 软删除。
	//
	// 两道闸（见 planDeletions），任何一道拦下就**一个都不删**。这不是保守，是必须：
	// 网络盘挂载会掉线，而掉线时「本轮没看见」根本不等于「文件没了」
	//（2026-09-25 实测：一次掉线把 38437 个文件标成删除，扫描还报成功）。
	plan := w.planDeletions()

	if plan.guard != "" {
		w.guardMsg = plan.guard
		// 被拦下的量也要报出来，否则界面上看不出「本来要删多少」
		w.stats.SkippedDeletions = plan.wouldDelete + plan.skippedUnreadable
		w.issue("error", "", plan.guard)
		return w.writeIssues(ctx)
	}
	w.stats.SkippedDeletions = plan.skippedUnreadable
	if plan.skippedUnreadable > 0 {
		w.issue("warning", "", fmt.Sprintf(
			"有 %d 个文件位于本轮读不到的目录下（挂载掉线/权限？），已跳过删除判定 —— 读不到不等于文件没了；检查挂载后重扫",
			plan.skippedUnreadable))
	}

	deleted, err := w.st.MarkFilesDeleted(ctx, plan.gone)
	if err != nil {
		return err
	}
	w.stats.DeletedFiles = int(deleted)

	return w.writeIssues(ctx)
}

// writeIssues 把本轮问题清单写回数据库（finish 的两条路径共用）。
func (w *walker) writeIssues(ctx context.Context) error {
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
