package images

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // 能读 webp（Emby 有时写 webp 图）

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// ErrNoImage 表示这个条目没有这一类图。
var ErrNoImage = errors.New("images: 没有这张图")

// Request 是一次取图的尺寸/格式要求。
type Request struct {
	// Width/Height 是期望的最大尺寸，等比缩放进这个框（不放大）。
	// 两个都为 0 表示原图直接给。
	Width  int
	Height int
	// Format 取 "" | jpeg | png；空表示「尽量保持原格式」。
	//
	// 没做 webp 编码：纯 Go 只有无损 webp 编码器，照片用它体积反而更大，
	// 为它引 cgo 或第三方库不划算。要 webp 再单独议（见 docs/ROADMAP.md）。
	Format  string
	Quality int // jpeg 质量，0 取默认 85
}

// Rendered 是一次输出结果。Path 可能是原图（直接给）或缓存里的缩放结果。
type Rendered struct {
	Path        string
	ContentType string
	ETag        string
	Width       int
	Height      int
	Size        int64
	// Cached 为真表示走的是缩放缓存（原图直给时为 false）。
	Cached bool
}

const (
	defaultQuality = 85
	// pruneInterval 是两次缓存清理之间的最小间隔（清理要遍历目录，别每次都做）。
	pruneInterval = 5 * time.Minute
)

// Pipeline 负责缩放与落缓存。
//
// 缓存分两块，清理策略不同：
//   - cache/ 放缩放结果，纯缓存，超上限就按最旧优先删；
//   - remote/ 放从 provider 下载的原图，那是**数据**（库里有记录指着它），不参与清理。
type Pipeline struct {
	dir      string
	maxBytes int64
	log      *slog.Logger

	mu        sync.Mutex
	lastPrune time.Time
}

func newPipeline(dir string, maxCacheMB int, log *slog.Logger) (*Pipeline, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("images: 缓存目录不能为空")
	}
	for _, sub := range []string{"cache", "remote"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("创建图片缓存目录失败: %w", err)
		}
	}
	if maxCacheMB <= 0 {
		maxCacheMB = 512
	}
	return &Pipeline{dir: dir, maxBytes: int64(maxCacheMB) * 1024 * 1024, log: log}, nil
}

// Render 输出一张图：能直接用的就直给原文件（零拷贝、零磁盘占用），
// 需要缩放/转格式的才落缓存。
func (p *Pipeline) Render(img *store.Image, req Request) (*Rendered, error) {
	srcW, srcH, format, err := probeImage(img.Path)
	if err != nil {
		return nil, fmt.Errorf("读取图片失败: %w", err)
	}

	targetW, targetH := fit(srcW, srcH, req.Width, req.Height)
	outFormat := chooseFormat(format, req.Format)
	quality := req.Quality
	if quality <= 0 {
		quality = defaultQuality
	}

	scaleNeeded := targetW != srcW || targetH != srcH
	formatChanged := outFormat != format
	if !scaleNeeded && !formatChanged && servableFormat(format) {
		st, err := os.Stat(img.Path)
		if err != nil {
			return nil, fmt.Errorf("读取图片失败: %w", err)
		}
		return &Rendered{
			Path:        img.Path,
			ContentType: contentTypeOf(format),
			ETag:        etagOf(img, 0, 0, format, 0),
			Width:       srcW,
			Height:      srcH,
			Size:        st.Size(),
		}, nil
	}

	key := cacheKey(img, targetW, targetH, outFormat, quality)
	target := filepath.Join(p.dir, "cache", key[:2], key+"."+extOf(outFormat))
	if st, err := os.Stat(target); err == nil && st.Size() > 0 {
		return &Rendered{
			Path:        target,
			ContentType: contentTypeOf(outFormat),
			ETag:        key,
			Width:       targetW,
			Height:      targetH,
			Size:        st.Size(),
			Cached:      true,
		}, nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("创建缓存子目录失败: %w", err)
	}
	src, _, err := decodeImage(img.Path)
	if err != nil {
		return nil, fmt.Errorf("解码图片失败: %w", err)
	}
	if err := writeScaled(target, src, targetW, targetH, outFormat, quality); err != nil {
		return nil, err
	}
	st, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("写入图片缓存后读取失败: %w", err)
	}

	p.maybePrune()
	return &Rendered{
		Path:        target,
		ContentType: contentTypeOf(outFormat),
		ETag:        key,
		Width:       targetW,
		Height:      targetH,
		Size:        st.Size(),
		Cached:      true,
	}, nil
}

// fit 把原图等比缩放进 maxW×maxH（只给一个维度就按那个维度算），永不放大。
func fit(srcW, srcH, maxW, maxH int) (int, int) {
	if maxW <= 0 && maxH <= 0 {
		return srcW, srcH
	}
	if maxW <= 0 {
		maxW = int(math.Round(float64(srcW) * float64(maxH) / float64(srcH)))
	}
	if maxH <= 0 {
		maxH = int(math.Round(float64(srcH) * float64(maxW) / float64(srcW)))
	}
	ratio := math.Min(float64(maxW)/float64(srcW), float64(maxH)/float64(srcH))
	if ratio >= 1 {
		return srcW, srcH
	}
	w := int(math.Round(float64(srcW) * ratio))
	h := int(math.Round(float64(srcH) * ratio))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// chooseFormat 决定输出格式：显式要求优先，否则「原格式能用就用原格式」。
func chooseFormat(srcFormat, want string) string {
	switch strings.ToLower(strings.TrimSpace(want)) {
	case "jpeg", "jpg":
		return "jpeg"
	case "png":
		return "png"
	}
	if srcFormat == "png" {
		// png 常见于 logo/透明图，转 jpeg 会丢透明通道
		return "png"
	}
	return "jpeg"
}

// servableFormat 判断浏览器能不能直接渲染这种格式。
func servableFormat(format string) bool {
	switch format {
	case "jpeg", "png", "webp", "gif":
		return true
	}
	return false
}

func contentTypeOf(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

func extOf(format string) string {
	switch format {
	case "png":
		return "png"
	case "webp":
		return "webp"
	default:
		return "jpg"
	}
}

// cacheKey 由「源文件身份 + 变换参数」决定，源文件变了 key 就变（不会拿到旧图）。
func cacheKey(img *store.Image, w, h int, format string, quality int) string {
	st, err := os.Stat(img.Path)
	var size, mtime int64
	if err == nil {
		size, mtime = st.Size(), st.ModTime().UnixNano()
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		img.Path,
		strconv.FormatInt(size, 10),
		strconv.FormatInt(mtime, 10),
		strconv.Itoa(w), strconv.Itoa(h), format, strconv.Itoa(quality),
	}, "|")))
	return hex.EncodeToString(sum[:])
}

// etagOf 给「原图直给」的情况算一个 ETag。
func etagOf(img *store.Image, w, h int, format string, quality int) string {
	return cacheKey(img, w, h, format, quality)[:16]
}

// probeImage 只读文件头拿尺寸与格式（不解码整张图）。
func probeImage(path string) (int, int, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, "", err
	}
	defer func() { _ = f.Close() }()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, "", err
	}
	return cfg.Width, cfg.Height, format, nil
}

// probeSize 只拿尺寸（补齐库里的 width/height 用）。
func probeSize(path string) (int, int, error) {
	w, h, _, err := probeImage(path)
	return w, h, err
}

func decodeImage(path string) (image.Image, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	return image.Decode(f)
}

// writeScaled 缩放并编码，先写临时文件再改名（避免半截文件被当成缓存命中）。
func writeScaled(target string, src image.Image, w, h int, format string, quality int) error {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// CatmullRom 比双线性锐利，缩海报这类「细线条 + 文字」的图差别看得出来
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	tmp, err := os.CreateTemp(filepath.Dir(target), "tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	switch format {
	case "png":
		err = png.Encode(tmp, dst)
	default:
		err = jpeg.Encode(tmp, dst, &jpeg.Options{Quality: quality})
	}
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("编码图片失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入图片缓存失败: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("落盘图片缓存失败: %w", err)
	}
	return nil
}

// maybePrune 缓存超上限时按最旧优先清理（概率性执行，避免每次请求都遍历目录）。
func (p *Pipeline) maybePrune() {
	p.mu.Lock()
	if time.Since(p.lastPrune) < pruneInterval {
		p.mu.Unlock()
		return
	}
	p.lastPrune = time.Now()
	p.mu.Unlock()

	type entry struct {
		path  string
		mtime time.Time
		size  int64
	}
	var (
		files []entry
		total int64
	)
	cacheDir := filepath.Join(p.dir, "cache")
	_ = filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, entry{path, info.ModTime(), info.Size()})
		total += info.Size()
		return nil
	})
	if total <= p.maxBytes {
		return
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	removed := 0
	for _, f := range files {
		if total <= p.maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			removed++
		}
	}
	p.log.Info("图片缓存已清理", "dir", cacheDir, "删除文件", removed, "剩余字节", total)
}
