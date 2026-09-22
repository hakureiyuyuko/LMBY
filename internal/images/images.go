// Package images 负责条目的图片：列出、按尺寸输出、必要时回源下载。
//
// **覆盖顺序（用户定的）：媒体目录里的本地图 > 用户手选 > provider 下载缓存。**
//
// 本地图是权威 —— 与 nfo 完全同一个模型：媒体目录里的东西优先，LMBY 只读它。
// 「以后图片也像 nfo 一样存在视频文件旁边」只是让这个模型更完整；
// 写回媒体目录是以后的可选能力，现在一个字节也不往媒体目录写。
//
// 二进制永不入库：库表里只存路径、来源、尺寸。缩放结果按内容寻址落在
// 数据目录（DataDir/images）下，磁盘不够时按最旧优先清理。
package images

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/overlay"
	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// Store 是本包需要的存储能力（窄接口，便于注入测试替身）。
type Store interface {
	GetItem(ctx context.Context, id int64) (*store.Item, error)
	ListImages(ctx context.Context, itemID int64) ([]store.Image, error)
	UpsertImage(ctx context.Context, itemID int64, kind, path, source string, width, height int, size, mtimeNS int64) error
	SetImageDimensions(ctx context.Context, imageID int64, width, height int) error
}

// Service 是图片对外的门面。
type Service struct {
	st     Store
	client provider.Client // 可为 nil：没配元数据源时不回源
	pipe   *Pipeline
	log    *slog.Logger
	hc     *http.Client

	// fetchMu/fetching 保证同一张远程图只下载一次：
	// 一个详情页会同时请求多张图，并发重复下载既慢又浪费。
	fetchMu  sync.Mutex
	fetching map[string]*sync.Mutex

	// overlay 是只读媒体库的写入层：可写库的图走缓存，
	// 只读库的图另外存一份进叠加层（算数据，不参与缓存淘汰）。可为 nil。
	overlay *overlay.Service
}

// SetOverlay 接入只读库的叠加层。
func (s *Service) SetOverlay(o *overlay.Service) { s.overlay = o }

// NewService 创建图片服务。cacheDir 是缩放结果与下载图的落盘目录。
func NewService(st Store, client provider.Client, cacheDir string, maxCacheMB int, log *slog.Logger) (*Service, error) {
	pipe, err := newPipeline(cacheDir, maxCacheMB, log)
	if err != nil {
		return nil, err
	}
	return &Service{
		st:       st,
		client:   client,
		pipe:     pipe,
		log:      log,
		hc:       &http.Client{Timeout: 60 * time.Second},
		fetching: map[string]*sync.Mutex{},
	}, nil
}

// Info 是列给界面看的图片信息（不含本地路径这类实现细节）。
type Info struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Lang   string `json:"lang,omitempty"`
	// URL 是取这张图的地址（尺寸参数由界面按需附加）。
	URL string `json:"url"`
}

// List 列出条目现有的图片。这一步**不回源**：只是让界面知道有什么。
func (s *Service) List(ctx context.Context, item *store.Item) ([]Info, error) {
	rows, err := s.st.ListImages(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(rows))
	for _, img := range rows {
		out = append(out, Info{
			Kind:   img.Kind,
			Source: img.Source,
			Width:  img.Width,
			Height: img.Height,
			Lang:   img.Lang,
			URL:    imageURL(item.ID, img.Kind),
		})
	}
	return out, nil
}

// Open 按尺寸要求输出某类图片；本地没有时会尝试回源下载一次。
func (s *Service) Open(ctx context.Context, item *store.Item, kind string, req Request) (*Rendered, error) {
	img, err := s.pick(ctx, item, kind)
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, ErrNoImage
	}

	// 只读库：把**回源**到的图补一份进该库的叠加层。
	//
	// 放在这里（取图的唯一漏斗）而不是只放在下载处：打开只读开关之前
	// 就缓存下来的图也要补 —— 否则叠加层只能拿到「开关之后新下的那几张」。
	// 本地图不补：它们本来就在媒体目录里，属于只读输入。
	if s.overlay != nil && img.Source == store.ImageSourceRemote {
		if p, err := s.overlay.PutImageIfMissing(ctx, item.ID, kind, img.Path); err != nil {
			s.log.Warn("只读库：补写叠加层图片失败（不影响本次取图）",
				"itemId", item.ID, "kind", kind, "err", err.Error())
		} else if p != "" {
			s.log.Info("只读库：图片已进叠加层", "itemId", item.ID, "kind", kind, "path", p)
		}
	}

	// 尺寸未知时顺手补齐（首次输出这张图时才知道它多大）
	if img.Width == 0 || img.Height == 0 {
		if w, h, err := probeSize(img.Path); err == nil {
			_ = s.st.SetImageDimensions(ctx, img.ID, w, h)
			img.Width, img.Height = w, h
		}
	}

	return s.pipe.Render(img, req)
}

// pick 按覆盖顺序挑一张：本地 > 用户手选 > 远程缓存；都没有就回源。
func (s *Service) pick(ctx context.Context, item *store.Item, kind string) (*store.Image, error) {
	rows, err := s.st.ListImages(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	if img := bestOfKind(rows, kind); img != nil {
		return img, nil
	}

	// 本地没有这一类图：向 provider 要一张（拿到就直接可用）
	fetched, err := s.fetchOnce(ctx, item, kind)
	if err != nil {
		s.log.Warn("回源取图片失败", "itemId", item.ID, "kind", kind, "err", err)
		return nil, nil
	}
	return fetched, nil
}

// imageURL 拼出取图地址。
func imageURL(itemID int64, kind string) string {
	return "/api/v1/items/" + strconv.FormatInt(itemID, 10) + "/images/" + kind
}

// sourceRank 是覆盖顺序：数字越小越优先。
func sourceRank(source string) int {
	switch source {
	case store.ImageSourceLocal:
		return 0
	case store.ImageSourceUploaded:
		return 1
	case store.ImageSourceRemote:
		return 2
	default:
		return 3
	}
}

// preferredStems 是同一来源下优先选哪个文件名。
//
// Emby 的库里一个目录常常有好几张同类图（poster.jpg 与 folder.jpg、fanart.jpg 与 backdrop.jpg），
// 按标准命名的那个优先，比按插入顺序随机挑一张更符合预期。
var preferredStems = map[string][]string{
	"poster":   {"poster", "folder", "cover", "show", "tvshow", "movie"},
	"fanart":   {"fanart", "backdrop", "background", "art"},
	"backdrop": {"backdrop", "fanart", "background", "art"},
	"banner":   {"banner"},
	"logo":     {"logo", "clearlogo"},
	"disc":     {"disc", "discart", "cdart"},
	"thumb":    {"thumb", "landscape", "screenshot"},
	"art":      {"clearart", "art"},
}

func stemRank(kind, path string) int {
	stem := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	// 去掉季号之类的后缀（season01-poster → poster）
	if i := strings.LastIndexAny(stem, "-_. "); i >= 0 {
		if tail := stem[i+1:]; tail != "" {
			if _, ok := rankOf(preferredStems[kind], tail); ok {
				stem = tail
			}
		}
	}
	if r, ok := rankOf(preferredStems[kind], stem); ok {
		return r
	}
	return len(preferredStems[kind]) + 1
}

func rankOf(list []string, name string) (int, bool) {
	for i, s := range list {
		if s == name {
			return i, true
		}
	}
	return 0, false
}

// bestOfKind 在同一类图里挑最合适的一张。
func bestOfKind(rows []store.Image, kind string) *store.Image {
	var best *store.Image
	bestKey := [3]int{0, 0, 0}
	for i := range rows {
		img := &rows[i]
		if img.Kind != kind {
			continue
		}
		key := [3]int{sourceRank(img.Source), stemRank(kind, img.Path), int(img.ID)}
		if best == nil || less(key, bestKey) {
			best, bestKey = img, key
		}
	}
	return best
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// lockKey 拿到某个键上的互斥锁（用于「同一张图只下一次」）。
func (s *Service) lockKey(key string) func() {
	s.fetchMu.Lock()
	mu, ok := s.fetching[key]
	if !ok {
		mu = &sync.Mutex{}
		s.fetching[key] = mu
	}
	s.fetchMu.Unlock()

	mu.Lock()
	return func() { mu.Unlock() }
}

// numOr 取可空整数的值，为空时返回兜底值。
func numOr(v *int32, def int32) int32 {
	if v == nil {
		return def
	}
	return *v
}
