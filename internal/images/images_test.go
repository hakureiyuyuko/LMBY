package images

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writePNG 造一张纯色 png 当测试图。
func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x60, A: 0xFF})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("造测试图失败: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("编码测试图失败: %v", err)
	}
}

func TestFit(t *testing.T) {
	cases := []struct {
		sw, sh, mw, mh int
		wantW, wantH   int
	}{
		{800, 1200, 200, 0, 200, 300},   // 只给宽
		{800, 1200, 0, 300, 200, 300},   // 只给高
		{800, 1200, 400, 400, 267, 400}, // 框住等比（800×0.3333=266.67 → 267）
		{800, 1200, 1600, 0, 800, 1200}, // 不放大
		{800, 1200, 0, 0, 800, 1200},    // 不做任何缩放
		{100, 100, 4096, 4096, 100, 100},
	}
	for _, c := range cases {
		w, h := fit(c.sw, c.sh, c.mw, c.mh)
		if w != c.wantW || h != c.wantH {
			t.Errorf("fit(%d,%d,%d,%d) = %dx%d, 期望 %dx%d",
				c.sw, c.sh, c.mw, c.mh, w, h, c.wantW, c.wantH)
		}
	}
}

func TestBestOfKindPriority(t *testing.T) {
	rows := []store.Image{
		{ID: 3, Kind: "poster", Path: "/m/whatever.jpg", Source: store.ImageSourceLocal},
		{ID: 1, Kind: "poster", Path: "/m/poster.jpg", Source: store.ImageSourceLocal},
		{ID: 2, Kind: "poster", Path: "/m/cache/abc.jpg", Source: store.ImageSourceRemote},
		{ID: 4, Kind: "fanart", Path: "/m/backdrop.jpg", Source: store.ImageSourceLocal},
		{ID: 5, Kind: "poster", Path: "/m/picked.jpg", Source: store.ImageSourceUploaded},
	}

	// 本地优先；同类里标准命名（poster.jpg）优先于随便一个名字
	if got := bestOfKind(rows, "poster"); got == nil || got.ID != 1 {
		t.Errorf("poster 应当选本地 poster.jpg（id=1），实际 %+v", got)
	}
	if got := bestOfKind(rows, "fanart"); got == nil || got.ID != 4 {
		t.Errorf("fanart 应当选 id=4，实际 %+v", got)
	}
	// 本地没有时，远程缓存也能用
	remoteOnly := []store.Image{{ID: 9, Kind: "poster", Path: "/cache/x.jpg", Source: store.ImageSourceRemote}}
	if got := bestOfKind(remoteOnly, "poster"); got == nil || got.ID != 9 {
		t.Errorf("没有本地图时应当用远程缓存，实际 %+v", got)
	}
	// 本地 > 手选 > 远程：用户手选存在但本地也有本地图时，按项目约定仍以本地为准
	preferLocal := []store.Image{
		{ID: 10, Kind: "poster", Path: "/m/picked.jpg", Source: store.ImageSourceUploaded},
		{ID: 11, Kind: "poster", Path: "/m/poster.jpg", Source: store.ImageSourceLocal},
	}
	if got := bestOfKind(preferLocal, "poster"); got == nil || got.ID != 11 {
		t.Errorf("本地图优先于手选图，实际 %+v", got)
	}
	if got := bestOfKind(rows, "logo"); got != nil {
		t.Errorf("没有的 kind 应当返回 nil，实际 %+v", got)
	}
}

func TestRenderScalesAndCaches(t *testing.T) {
	dir := t.TempDir()
	pipe, err := newPipeline(dir, 32, testLogger())
	if err != nil {
		t.Fatalf("newPipeline: %v", err)
	}
	src := filepath.Join(dir, "poster.png")
	writePNG(t, src, 800, 1200)
	img := &store.Image{ID: 1, Kind: "poster", Path: src, Source: store.ImageSourceLocal}

	r1, err := pipe.Render(img, Request{Width: 200})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if r1.Width != 200 || r1.Height != 300 {
		t.Errorf("缩放结果 = %dx%d, 期望 200x300", r1.Width, r1.Height)
	}
	if r1.ContentType != "image/png" {
		t.Errorf("源是 png，缩放后应当还是 png，实际 %s", r1.ContentType)
	}
	if !r1.Cached || r1.Path == src {
		t.Errorf("应当落了缩放缓存: cached=%v path=%s", r1.Cached, r1.Path)
	}
	if w, h, _, err := probeImage(r1.Path); err != nil || w != 200 || h != 300 {
		t.Errorf("缓存文件本身应当是 200x300: %dx%d err=%v", w, h, err)
	}

	// 第二次同样的请求命中缓存（同一个文件、同一个 ETag）
	r2, err := pipe.Render(img, Request{Width: 200})
	if err != nil {
		t.Fatalf("Render 第二次: %v", err)
	}
	if r2.Path != r1.Path || r2.ETag != r1.ETag || !r2.Cached {
		t.Errorf("第二次应当命中同一个缓存文件: %+v vs %+v", r2, r1)
	}

	// 要求比原图大 → 不放大，直接给原文件
	r3, err := pipe.Render(img, Request{Width: 5000})
	if err != nil {
		t.Fatalf("Render 放大请求: %v", err)
	}
	if r3.Cached || r3.Path != src || r3.Width != 800 {
		t.Errorf("不该放大、应当直给原图: %+v", r3)
	}

	// 显式要 jpeg → 另一份缓存，格式也变了
	r4, err := pipe.Render(img, Request{Width: 200, Format: "jpeg"})
	if err != nil {
		t.Fatalf("Render jpeg: %v", err)
	}
	if r4.ContentType != "image/jpeg" || r4.Path == r1.Path {
		t.Errorf("jpeg 请求应当另存一份: %+v", r4)
	}
}

func TestSourceStemRank(t *testing.T) {
	cases := []struct {
		kind, path string
		wantFirst  bool // 是否比 "whatever.jpg" 更优先
	}{
		{"poster", "/m/poster.jpg", true},
		{"poster", "/m/folder.jpg", true},
		{"poster", "/m/whatever.jpg", false},
		{"fanart", "/m/backdrop.jpg", true},
		{"fanart", "/m/fanart.jpg", true},
		{"poster", "/m/season01-poster.jpg", true}, // 带季号后缀也算标准命名
	}
	for _, c := range cases {
		got := stemRank(c.kind, c.path) < stemRank(c.kind, "/m/whatever.jpg")
		if got != c.wantFirst {
			t.Errorf("stemRank(%s, %s) 应当%s优先", c.kind, c.path, map[bool]string{true: "", false: "不"}[c.wantFirst])
		}
	}
}

// —— 回源 ——

type fakeStore struct {
	item   *store.Item
	images []store.Image
}

func (f *fakeStore) GetItem(context.Context, int64) (*store.Item, error) {
	return f.item, nil
}

func (f *fakeStore) ListImages(context.Context, int64) ([]store.Image, error) {
	return f.images, nil
}

func (f *fakeStore) UpsertImage(_ context.Context, itemID int64, kind, path, source string, w, h int, size, mtimeNS int64) error {
	f.images = append(f.images, store.Image{
		ID: int64(len(f.images) + 1), ItemID: itemID, Kind: kind, Path: path,
		Source: source, Width: w, Height: h, SizeBytes: size,
	})
	return nil
}

func (f *fakeStore) SetImageDimensions(context.Context, int64, int, int) error { return nil }

type fakeProvider struct {
	posterPath string
	imageBase  string
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) SearchMovie(context.Context, string, provider.SearchOptions) ([]provider.SearchResult, error) {
	return nil, nil
}
func (f *fakeProvider) SearchSeries(context.Context, string, provider.SearchOptions) ([]provider.SearchResult, error) {
	return nil, nil
}
func (f *fakeProvider) Movie(context.Context, int, string) (*provider.Movie, error) {
	return &provider.Movie{PosterPath: f.posterPath}, nil
}
func (f *fakeProvider) Series(context.Context, int, string) (*provider.Series, error) {
	return &provider.Series{PosterPath: f.posterPath}, nil
}
func (f *fakeProvider) Season(context.Context, int, int, string) (*provider.Season, error) {
	return &provider.Season{}, nil
}
func (f *fakeProvider) Episode(context.Context, int, int, int, string) (*provider.Episode, error) {
	return &provider.Episode{}, nil
}
func (f *fakeProvider) Images(context.Context, string, int, string) ([]provider.Image, error) {
	return nil, nil
}
func (f *fakeProvider) Credits(context.Context, string, int) (*provider.Credits, error) {
	return nil, nil
}
func (f *fakeProvider) ImageURL(path, size string) string {
	return f.imageBase + "/" + size + path
}

// TestServicePrefersLocalOverRemote 本地有图时不回源。
func TestServicePrefersLocalOverRemote(t *testing.T) {
	cacheDir := t.TempDir()
	local := filepath.Join(cacheDir, "poster.png")
	writePNG(t, local, 400, 600)

	fetched := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched++
		http.Error(w, "不该被调用", http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := &fakeStore{
		item:   &store.Item{ID: 7, Kind: "movie", ProviderIDs: map[string]string{"tmdb": "198375"}},
		images: []store.Image{{ID: 1, ItemID: 7, Kind: "poster", Path: local, Source: store.ImageSourceLocal}},
	}
	svc, err := NewService(st, &fakeProvider{posterPath: "/p.png", imageBase: srv.URL}, filepath.Join(cacheDir, "images"), 32, testLogger())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	out, err := svc.Open(context.Background(), st.item, "poster", Request{Width: 100})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if out.Width != 100 || out.Height != 150 {
		t.Errorf("本地图缩放 = %dx%d, 期望 100x150", out.Width, out.Height)
	}
	if fetched != 0 {
		t.Errorf("本地有图就不该回源，实际请求了 %d 次", fetched)
	}
}

// TestServiceFetchesWhenNoLocalImage 本地没有时才回源，并把下载结果登记成 remote。
func TestServiceFetchesWhenNoLocalImage(t *testing.T) {
	remoteDir := t.TempDir()
	remotePNG := filepath.Join(remoteDir, "src.png")
	writePNG(t, remotePNG, 500, 750)
	body, err := os.ReadFile(remotePNG)
	if err != nil {
		t.Fatalf("读取测试图: %v", err)
	}

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	st := &fakeStore{item: &store.Item{ID: 7, Kind: "movie", ProviderIDs: map[string]string{"tmdb": "198375"}}}
	svc, err := NewService(st, &fakeProvider{posterPath: "/p.png", imageBase: srv.URL}, filepath.Join(remoteDir, "images"), 32, testLogger())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	out, err := svc.Open(context.Background(), st.item, "poster", Request{Width: 250})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if out.Width != 250 || out.Height != 375 {
		t.Errorf("回源图缩放 = %dx%d, 期望 250x375", out.Width, out.Height)
	}
	if hits != 1 {
		t.Errorf("应当只下载一次，实际 %d 次", hits)
	}
	if len(st.images) != 1 || st.images[0].Source != store.ImageSourceRemote {
		t.Fatalf("应当登记一条 remote 图: %+v", st.images)
	}
	if st.images[0].Width != 500 || st.images[0].Height != 750 {
		t.Errorf("登记尺寸应当来自下载到的图: %+v", st.images[0])
	}

	// 再请求一次：应当直接用已登记的 remote 图，不再下载
	if _, err := svc.Open(context.Background(), st.item, "poster", Request{Width: 120}); err != nil {
		t.Fatalf("Open 第二次: %v", err)
	}
	if hits != 1 {
		t.Errorf("第二次不该再下载，实际总共 %d 次", hits)
	}
}

// TestServiceNoImage 什么都没有时返回 ErrNoImage（而不是空指针）。
func TestServiceNoImage(t *testing.T) {
	st := &fakeStore{item: &store.Item{ID: 7, Kind: "movie"}}
	svc, err := NewService(st, nil, t.TempDir(), 32, testLogger())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.Open(context.Background(), st.item, "poster", Request{}); !errors.Is(err, ErrNoImage) {
		t.Errorf("期望 ErrNoImage，实际 %v", err)
	}
}
