package overlay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// fakeStore 只需要三个方法：条目、库是否只读、只读根路径清单。
type fakeStore struct {
	items    map[int64]*store.Item
	readOnly map[int64]bool
	roots    []string
}

func (f *fakeStore) GetItem(_ context.Context, id int64) (*store.Item, error) {
	if it, ok := f.items[id]; ok {
		return it, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) LibraryReadOnly(_ context.Context, libraryID int64) (bool, error) {
	return f.readOnly[libraryID], nil
}

func (f *fakeStore) ReadOnlyRoots(context.Context) ([]string, error) { return f.roots, nil }

func newTestService(t *testing.T, st Store) *Service {
	t.Helper()
	svc, err := NewService(filepath.Join(t.TempDir(), "overlay"), st)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// 只读库：图片进叠加层，落在 <root>/<libraryId>/<itemId>/<kind><ext>。
func TestPutImageWritesForReadOnlyLibrary(t *testing.T) {
	st := &fakeStore{
		items:    map[int64]*store.Item{7: {ID: 7, LibraryID: 3}},
		readOnly: map[int64]bool{3: true},
	}
	svc := newTestService(t, st)

	src := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(src, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := svc.PutImage(context.Background(), 7, "poster", src)
	if err != nil {
		t.Fatalf("PutImage: %v", err)
	}
	want := filepath.Join(svc.Root(), "3", "7", "poster.jpg")
	if got != want {
		t.Fatalf("落点 = %s，想要 %s", got, want)
	}
	buf, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("读回叠加层图片: %v", err)
	}
	if string(buf) != "jpeg-bytes" {
		t.Fatalf("内容 = %q", string(buf))
	}
}

// 可写库：一个字也不写（那种库的图走 images 包的缓存）。
func TestPutImageSkipsWritableLibrary(t *testing.T) {
	st := &fakeStore{
		items:    map[int64]*store.Item{7: {ID: 7, LibraryID: 3}},
		readOnly: map[int64]bool{3: false},
	}
	svc := newTestService(t, st)

	src := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.PutImage(context.Background(), 7, "poster", src)
	if err != nil {
		t.Fatalf("PutImage: %v", err)
	}
	if got != "" {
		t.Fatalf("可写库不该写叠加层，却写了 %s", got)
	}
	if _, err := os.Stat(svc.LibraryDir(3)); !os.IsNotExist(err) {
		t.Fatalf("可写库不该建目录")
	}
}

// kind 里带路径分隔符要被消毒掉：不允许「kind」变成目录穿越。
func TestPutImageSanitizesKind(t *testing.T) {
	st := &fakeStore{
		items:    map[int64]*store.Item{7: {ID: 7, LibraryID: 3}},
		readOnly: map[int64]bool{3: true},
	}
	svc := newTestService(t, st)
	src := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.PutImage(context.Background(), 7, "../../etc", src)
	if err != nil {
		t.Fatalf("PutImage: %v", err)
	}
	// 消毒后是 "etc"，仍然落在条目目录里 —— 不能穿出去
	if want := filepath.Join(svc.ItemDir(3, 7), "etc.jpg"); got != want {
		t.Fatalf("消毒后的落点 = %s，想要 %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(svc.Root(), "etc.jpg")); !os.IsNotExist(err) {
		t.Fatal("不该在叠加层根目录留下文件（目录穿越）")
	}

	// 消毒后什么都不剩（"..."）→ 直接报错，而不是产生一个没名字的文件
	if _, err := svc.PutImage(context.Background(), 7, "...", src); err == nil {
		t.Fatal("空 kind 应当报错")
	}
}

// 元数据快照：只读库写 meta.json，可写库不写。
func TestWriteItemMeta(t *testing.T) {
	st := &fakeStore{
		items:    map[int64]*store.Item{7: {ID: 7, LibraryID: 3}, 8: {ID: 8, LibraryID: 4}},
		readOnly: map[int64]bool{3: true, 4: false},
	}
	svc := newTestService(t, st)
	ctx := context.Background()

	path, err := svc.WriteItemMeta(ctx, 7, map[string]any{"title": "迷雾中的她"})
	if err != nil {
		t.Fatalf("WriteItemMeta: %v", err)
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	if got["title"] != "迷雾中的她" {
		t.Fatalf("快照内容 = %v", got)
	}

	if p, err := svc.WriteItemMeta(ctx, 8, map[string]any{"title": "x"}); err != nil || p != "" {
		t.Fatalf("可写库不该写快照（path=%q err=%v）", p, err)
	}
}

// Stats：文件数与占用，目录不存在时是 0 而不是报错。
func TestStats(t *testing.T) {
	st := &fakeStore{items: map[int64]*store.Item{}, readOnly: map[int64]bool{}}
	svc := newTestService(t, st)

	if s, err := svc.Stats(9); err != nil || s.Files != 0 || s.Bytes != 0 {
		t.Fatalf("空库统计 = %+v err=%v", s, err)
	}
	dir := svc.ItemDir(9, 1)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := svc.Stats(9)
	if err != nil {
		t.Fatal(err)
	}
	if s.Files != 1 || s.Bytes != 5 {
		t.Fatalf("统计 = %+v，想要 1 个文件 5 字节", s)
	}
}

// AssertWritable：只读库根路径之下的目标一律拒绝（含根本身），之外放行。
func TestAssertWritable(t *testing.T) {
	root := t.TempDir()
	ro := filepath.Join(root, "网盘", "电影")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	st := &fakeStore{items: map[int64]*store.Item{}, readOnly: map[int64]bool{}, roots: []string{ro}}
	svc := newTestService(t, st)
	ctx := context.Background()

	for _, bad := range []string{
		ro,
		filepath.Join(ro, "某片", "poster.jpg"),
		filepath.Join(ro, "..", filepath.Base(ro), "a.jpg"), // 带 .. 也要认出来
	} {
		if err := svc.AssertWritable(ctx, bad); err == nil {
			t.Fatalf("%s 在只读库下，应当被拒", bad)
		} else if !strings.Contains(err.Error(), "只读") {
			t.Fatalf("错误信息要说清原因，得到 %v", err)
		}
	}

	ok := filepath.Join(root, "本地盘", "电影", "poster.jpg")
	if err := svc.AssertWritable(ctx, ok); err != nil {
		t.Fatalf("可写路径被误拒: %v", err)
	}
	// 前缀相似但不是子目录（/data/movies2 不等于 /data/movies）也要放行
	sibling := ro + "2"
	if err := svc.AssertWritable(ctx, filepath.Join(sibling, "a.jpg")); err != nil {
		t.Fatalf("同前缀的兄弟目录被误拒: %v", err)
	}
}

// Clear：只清指定库，别的库不受影响；返回删掉的文件数与字节数。
func TestClear(t *testing.T) {
	st := &fakeStore{items: map[int64]*store.Item{}, readOnly: map[int64]bool{}}
	svc := newTestService(t, st)

	write := func(lib, item int64, name, body string) string {
		t.Helper()
		dir := svc.ItemDir(lib, item)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(3, 1, "poster.jpg", "12345")
	write(3, 1, "meta.json", "12")
	write(4, 1, "poster.jpg", "999")

	files, bytes, err := svc.Clear(context.Background(), 3)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if files != 2 || bytes != 7 {
		t.Fatalf("Clear 返回 %d 个文件 / %d 字节，想要 2 / 7", files, bytes)
	}
	if st3, _ := svc.Stats(3); st3.Files != 0 {
		t.Fatalf("库 3 没清干净：%+v", st3)
	}
	if st4, _ := svc.Stats(4); st4.Files != 1 || st4.Bytes != 3 {
		t.Fatalf("库 4 被误删：%+v", st4)
	}
	// 清一个从来没写过的库：不算错，返回 0
	if f, b, err := svc.Clear(context.Background(), 9); err != nil || f != 0 || b != 0 {
		t.Fatalf("清空库 9 = (%d,%d,%v)，想要 (0,0,nil)", f, b, err)
	}
	// 清空后还能继续写（目录被重建）
	write(3, 1, "poster.jpg", "1")
	if st3, _ := svc.Stats(3); st3.Files != 1 {
		t.Fatalf("清空后写不进去了：%+v", st3)
	}
	// 清空用的临时目录（.clearing-*）不应该留在叠加层里
	if ents, _ := os.ReadDir(svc.Root()); len(ents) != 2 { // 只有 3 与 4
		t.Fatalf("叠加层根目录应只剩库目录，实际 %v", ents)
	}
}

// AllStats：按库列出占用，空库与非库目录不算。
func TestAllStats(t *testing.T) {
	st := &fakeStore{items: map[int64]*store.Item{}, readOnly: map[int64]bool{}}
	svc := newTestService(t, st)

	for _, lib := range []int64{7, 3} {
		dir := svc.ItemDir(lib, 1)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("123"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 空的库目录（开过只读但还没写东西）+ 人手放进去的目录
	if err := os.MkdirAll(svc.LibraryDir(5), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(svc.Root(), "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := svc.AllStats()
	if err != nil {
		t.Fatalf("AllStats: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("AllStats 应只报非空库，得到 %+v", got)
	}
	if got[0].LibraryID != 3 || got[1].LibraryID != 7 {
		t.Fatalf("AllStats 应按库 id 排序，得到 %+v", got)
	}
	if got[0].Files != 1 || got[0].Bytes != 3 {
		t.Fatalf("库 3 统计 = %+v", got[0])
	}
}

// 条目所属库查不到时，叠加层不该凭空创建目录。
func TestTargetMissingItem(t *testing.T) {
	st := &fakeStore{items: map[int64]*store.Item{}, readOnly: map[int64]bool{}}
	svc := newTestService(t, st)
	if _, err := svc.PutImage(context.Background(), 999, "poster", "/nope.jpg"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("想要 ErrNotFound，得到 %v", err)
	}
}
