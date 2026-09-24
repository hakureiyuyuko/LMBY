package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/version"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
		ok   bool
	}{
		{"v1.0.0", []int{1, 0, 0}, true},
		{"1.2.3", []int{1, 2, 3}, true},
		{"V2.10", []int{2, 10}, true},
		{" 1.0.1 ", []int{1, 0, 1}, true},
		{"v1.0.0-rc1", []int{1, 0, 0}, true}, // 后缀丢掉，主体仍可比
		{"dev-4f5f0cf", nil, false},          // 开发构建：不给结论
		{"", nil, false},
		{"latest", nil, false},
		{"v1.x.0", nil, false},
	}
	for _, c := range cases {
		got, ok := parseVersion(c.in)
		if ok != c.ok {
			t.Fatalf("parseVersion(%q) ok=%v，想要 %v", c.in, ok, c.ok)
		}
		if !ok {
			continue
		}
		if len(got) != len(c.want) {
			t.Fatalf("parseVersion(%q)=%v，想要 %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseVersion(%q)=%v，想要 %v", c.in, got, c.want)
			}
		}
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0", "1.0.0", 0},  // 缺的段按 0
		{"v1.2", "1.10", -1}, // 数字比较，不是字符串比较
		{"0.10.0", "0.9.0", 1},
	}
	for _, c := range cases {
		av, _ := parseVersion(c.a)
		bv, _ := parseVersion(c.b)
		if got := compareVersion(av, bv); got != c.want {
			t.Errorf("compare(%s, %s)=%d，想要 %d", c.a, c.b, got, c.want)
		}
	}
}

// releaseJSON 造一份上游响应。
func releaseJSON(tag, notes string, assets ...string) string {
	a := ""
	for i, n := range assets {
		if i > 0 {
			a += ","
		}
		a += `{"name":"` + n + `"}`
	}
	return `{"tag_name":"` + tag + `","html_url":"https://example.test/releases/tag/` + tag + `",
	  "published_at":"2026-09-24T00:00:00Z","body":"` + notes + `","assets":[` + a + `]}`
}

func TestFetchUpdate(t *testing.T) {
	// fetchUpdate 用的是包里的 version.Version，测试里临时改一下再还原。
	orig := version.Version
	defer func() { version.Version = orig }()

	t.Run("有新版本", func(t *testing.T) {
		version.Version = "v1.0.0"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("User-Agent") == "" {
				t.Error("请求没带 User-Agent（GitHub 会拒）")
			}
			_, _ = w.Write([]byte(releaseJSON("v1.0.1", `修了几个字幕问题\n\n- a\n- b`, "lmby-linux-amd64", "SHA256SUMS.txt")))
		}))
		defer srv.Close()

		res := fetchUpdate(context.Background(), srv.URL, 5)
		if !res.OK || res.State != "update-available" {
			t.Fatalf("想要 update-available，得到 ok=%v state=%s err=%s", res.OK, res.State, res.Error)
		}
		if res.Latest != "v1.0.1" || res.ReleaseURL == "" {
			t.Errorf("最新版/链接不对：%+v", res)
		}
		if strings.Contains(res.Notes, "\n") {
			t.Errorf("说明应当压成一行：%q", res.Notes)
		}
		if len(res.Assets) != 2 {
			t.Errorf("资产列表不对：%v", res.Assets)
		}
	})

	t.Run("已是最新", func(t *testing.T) {
		version.Version = "v1.0.1"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(releaseJSON("v1.0.1", "x")))
		}))
		defer srv.Close()
		if res := fetchUpdate(context.Background(), srv.URL, 5); res.State != "up-to-date" {
			t.Fatalf("想要 up-to-date，得到 %s（%s）", res.State, res.Error)
		}
	})

	t.Run("本地更超前也算最新", func(t *testing.T) {
		version.Version = "v1.2.0"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(releaseJSON("v1.0.1", "x")))
		}))
		defer srv.Close()
		if res := fetchUpdate(context.Background(), srv.URL, 5); res.State != "up-to-date" {
			t.Fatalf("想要 up-to-date，得到 %s", res.State)
		}
	})

	t.Run("开发构建不下结论", func(t *testing.T) {
		version.Version = "dev-4f5f0cf"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(releaseJSON("v1.0.1", "x")))
		}))
		defer srv.Close()
		res := fetchUpdate(context.Background(), srv.URL, 5)
		if !res.OK || res.State != "dev" {
			t.Fatalf("想要 ok + dev，得到 ok=%v state=%s", res.OK, res.State)
		}
		if res.Latest != "v1.0.1" {
			t.Errorf("开发构建也应当把上游最新版带回来，得到 %q", res.Latest)
		}
	})

	t.Run("没有发布（404）", func(t *testing.T) {
		version.Version = "v1.0.0"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		res := fetchUpdate(context.Background(), srv.URL, 5)
		if res.OK || res.Error == "" {
			t.Fatalf("想要失败 + 人话原因，得到 ok=%v err=%q", res.OK, res.Error)
		}
	})

	t.Run("格式不对", func(t *testing.T) {
		version.Version = "v1.0.0"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<!doctype html><html>这是首页不是 API</html>"))
		}))
		defer srv.Close()
		res := fetchUpdate(context.Background(), srv.URL, 5)
		if res.OK || !strings.Contains(res.Error, "预期格式") {
			t.Fatalf("想要「格式不对」，得到 ok=%v err=%q", res.OK, res.Error)
		}
	})

	t.Run("连不上（连接被拒）", func(t *testing.T) {
		version.Version = "v1.0.0"
		// 关掉的端口：连接被拒。
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()
		res := fetchUpdate(context.Background(), url, 2)
		if res.OK || res.Error == "" {
			t.Fatalf("想要失败 + 人话原因，得到 ok=%v err=%q", res.OK, res.Error)
		}
		if !strings.Contains(res.Error, "连接被拒绝") {
			t.Errorf("连接被拒应当说人话，得到 %q", res.Error)
		}
	})

	t.Run("连不上（超时）", func(t *testing.T) {
		version.Version = "v1.0.0"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(1500 * time.Millisecond)
		}))
		defer srv.Close()
		res := fetchUpdate(context.Background(), srv.URL, 1) // 1 秒超时
		if res.OK || !strings.Contains(res.Error, "超时") {
			t.Fatalf("想要超时 + 人话，得到 ok=%v err=%q", res.OK, res.Error)
		}
	})
}

func TestCheckUpdateCaches(t *testing.T) {
	orig := version.Version
	defer func() { version.Version = orig }()
	version.Version = "v1.0.0"

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(releaseJSON("v1.0.1", "x")))
	}))
	defer srv.Close()

	s := &Server{cfg: &config.Config{Update: config.UpdateConfig{SourceURL: srv.URL, TimeoutSeconds: 3}}}

	first := s.checkUpdate(context.Background(), srv.URL, true)
	if first.Cached {
		t.Error("第一次不该是缓存")
	}
	second := s.checkUpdate(context.Background(), srv.URL, true)
	if !second.Cached {
		t.Error("最小间隔内的第二次应当回缓存")
	}
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Errorf("上游只该被访问 1 次，实际 %d 次", n)
	}
	if second.Latest != first.Latest || second.State != first.State {
		t.Errorf("缓存内容应与首次一致：%+v vs %+v", second, first)
	}

	// 把「上次访问时间」往前拨过最小间隔 → 应当真的重查。
	s.update.mu.Lock()
	s.update.at = time.Now().Add(-updateCheckMinInterval - time.Minute)
	s.update.mu.Unlock()
	third := s.checkUpdate(context.Background(), srv.URL, true)
	if third.Cached {
		t.Error("过了最小间隔应当重查")
	}
	if n := atomic.LoadInt64(&hits); n != 2 {
		t.Errorf("上游应当被访问 2 次，实际 %d 次", n)
	}
}
