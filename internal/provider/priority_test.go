package provider

// Priority 的行为测试：开关关时只用备用源；开着时优先进主源、没命中才回退。
//
// 这里用「嵌 nil 接口」的假实现：只覆写用到的那几个方法，其余方法不实现
// （真调到会 nil panic，正好提醒测试别乱调）。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeClient struct {
	Client // 嵌 nil：未覆写的方法被调用会 panic，说明测试用错了

	name     string
	movie    *Movie
	movieErr error
	calls    int
}

func (f *fakeClient) Name() string { return f.name }

func (f *fakeClient) Movie(_ context.Context, _ int, _ string) (*Movie, error) {
	f.calls++
	return f.movie, f.movieErr
}

func (f *fakeClient) ImageURL(path, size string) string { return f.name + ":" + size + path }

func newPair(enabled *bool) (*Priority, *fakeClient, *fakeClient) {
	primary := &fakeClient{name: "keqdb", movie: &Movie{ID: 1, Title: "from-keqdb"}}
	backup := &fakeClient{name: "tmdb", movie: &Movie{ID: 2, Title: "from-tmdb"}}
	p := NewPriority(primary, backup, func() bool { return *enabled }, nil)
	return p, primary, backup
}

func TestPriorityPrimaryWhenEnabled(t *testing.T) {
	enabled := true
	p, primary, backup := newPair(&enabled)

	got, err := p.Movie(context.Background(), 1, "zh-CN")
	if err != nil {
		t.Fatalf("Movie: %v", err)
	}
	if got.Title != "from-keqdb" {
		t.Fatalf("启用时应走 KeqDB，得到 %q", got.Title)
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Fatalf("调用次数 primary=%d backup=%d（期望 1/0）", primary.calls, backup.calls)
	}
}

func TestPriorityFallsBackOnNotFound(t *testing.T) {
	enabled := true
	p, primary, backup := newPair(&enabled)
	primary.movieErr = ErrNotFound

	got, err := p.Movie(context.Background(), 1, "zh-CN")
	if err != nil {
		t.Fatalf("Movie: %v", err)
	}
	if got.Title != "from-tmdb" {
		t.Fatalf("KeqDB 未命中时应回退 TMDB，得到 %q", got.Title)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("调用次数 primary=%d backup=%d（期望 1/1）", primary.calls, backup.calls)
	}
}

func TestPriorityFallsBackOnOtherError(t *testing.T) {
	enabled := true
	p, primary, backup := newPair(&enabled)
	primary.movieErr = errors.New("boom") // 网络/5xx：同样回退，别让刮削整片失败

	got, err := p.Movie(context.Background(), 1, "zh-CN")
	if err != nil {
		t.Fatalf("Movie: %v", err)
	}
	if got.Title != "from-tmdb" || backup.calls != 1 {
		t.Fatalf("报错时也应回退，得到 %q（backup calls=%d）", got.Title, backup.calls)
	}
}

func TestPriorityDisabledUsesBackupOnly(t *testing.T) {
	enabled := false
	p, primary, backup := newPair(&enabled)

	got, err := p.Movie(context.Background(), 1, "zh-CN")
	if err != nil {
		t.Fatalf("Movie: %v", err)
	}
	if got.Title != "from-tmdb" {
		t.Fatalf("开关关时应只用 TMDB，得到 %q", got.Title)
	}
	if primary.calls != 0 {
		t.Fatalf("开关关时不该问主源（primary calls=%d）", primary.calls)
	}
	if backup.calls != 1 {
		t.Fatalf("开关关时应问备用源一次（backup calls=%d）", backup.calls)
	}
}

func TestPriorityImageURLByPathShape(t *testing.T) {
	// 开关关着也一样：只要路径长得像 KeqDB 的，就得用 KeqDB 的域名取。
	enabled := false
	p, _, _ := newPair(&enabled)

	keqPath := "/" + strings.Repeat("a", 64) + ".webp"
	if got := p.ImageURL(keqPath, "w500"); got != "keqdb:w500"+keqPath {
		t.Fatalf("KeqDB 形状的路径应走 KeqDB 域名（与开关无关），得到 %q", got)
	}
	if got := p.ImageURL("/abc123.jpg", "w500"); got != "tmdb:w500/abc123.jpg" {
		t.Fatalf("TMDB 形状的路径应走 TMDB，得到 %q", got)
	}
}
