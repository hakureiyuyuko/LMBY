package overlay

// 「清理叠加层孤儿」的规则用真目录 + 假 store 钉住。
//
// 这里最要紧的一条不是「删掉孤儿」，而是**什么情况下不能删**：
// 查库出错（数据库有问题）时，「查不到」并不等于「不存在」——
// 那种时候照删就会把好数据抹掉。所以下面专门有一个 case 验它。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// errStore 让「查库」永远报错（模拟数据库故障）。
type errStore struct{ fakeStore }

func (e *errStore) GetLibrary(context.Context, int64) (*store.Library, error) {
	return nil, errors.New("数据库连接断了")
}

func TestPurgeOrphans(t *testing.T) {
	root := filepath.Join(t.TempDir(), "overlay")
	st := &fakeStore{
		items: map[int64]*store.Item{
			10: {ID: 10}, // 活着
		},
		libs: map[int64]*store.Library{
			1: {ID: 1}, // 活着
		},
		readOnly: map[int64]bool{},
	}
	svc, err := NewService(root, st)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// 造四种目录：活着 / 条目已删 / 库已删 / 坟场与垃圾名（都不该动）
	mk := func(parts ...string) string {
		dir := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	live := mk("1", "10")
	orphanItem := mk("1", "11")
	orphanLib := mk("2", "20")
	graveyard := mk(".clearing-1-123")
	junk := mk("not-a-number")

	files, bytes, err := svc.OrphanStats(context.Background())
	if err != nil {
		t.Fatalf("OrphanStats: %v", err)
	}
	if files != 2 || bytes != 2 {
		t.Fatalf("孤儿统计应为 2 个文件 / 2 字节，实际 %d / %d", files, bytes)
	}

	if _, _, err := svc.PurgeOrphans(context.Background()); err != nil {
		t.Fatalf("PurgeOrphans: %v", err)
	}
	for _, gone := range []string{orphanItem, orphanLib} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("孤儿目录应被删掉：%s", gone)
		}
	}
	for _, kept := range []string{live, graveyard, junk} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("不该动的东西被删了：%s（%v）", kept, err)
		}
	}
}

func TestPurgeOrphansKeepsEverythingOnDBError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "overlay")
	sb := &errStore{fakeStore{
		items:    map[int64]*store.Item{},
		libs:     map[int64]*store.Library{},
		readOnly: map[int64]bool{},
	}}
	svc, err := NewService(root, sb)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	dir := filepath.Join(root, "1", "10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	files, _, err := svc.PurgeOrphans(context.Background())
	if err != nil {
		t.Fatalf("PurgeOrphans 不该因为查库出错而报错（应跳过）：%v", err)
	}
	if files != 0 {
		t.Fatalf("查库出错时不该删任何东西，却报告删了 %d 个文件", files)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("查库出错时目录应原样保留：%v", err)
	}
}
