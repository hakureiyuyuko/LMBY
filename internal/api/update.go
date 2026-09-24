package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/version"
)

// updateCheckMinInterval 是两次「真去问上游」之间的最小间隔。
//
// 上游默认是 GitHub 的匿名 API（限流 60 次/小时/IP），而这个按钮是给人点的：
// 连点、开几个标签页、反复刷新都不该把配额打爆。间隔内的请求直接回缓存，
// 并在响应里标 cached=true，让界面能说「10 分钟内不再重复查」。
const updateCheckMinInterval = 10 * time.Minute

// updateCheckTimeoutDefault 是没配 [update] timeout_seconds 时的超时。
const updateCheckTimeoutDefault = 8 * time.Second

// updateNotesMaxRunes 是带回来的发布说明最多展示多少字（长了界面也放不下）。
const updateNotesMaxRunes = 600

// updateResult 既是一次检查的结果，也是缓存体与响应体。
//
// 为什么不复用统一的 {error, code} 错误格式：那个是给「请求本身有问题」用的
// （参数不对、没权限）。这里请求是合法的，只是**没查成**（服务器没有外网出口、
// 上游超时、上游返回看不懂的东西）——界面需要把原因显示给管理员看，
// 用 4xx/5xx 会被前端的通用错误处理吃掉，所以回 200 + ok:false + error。
type updateResult struct {
	// Enabled 表示配了更新源；false 时界面把按钮置灰并说明原因。
	Enabled bool `json:"enabled"`
	OK      bool `json:"ok"`
	// State 是给界面直接用的结论：up-to-date / update-available / dev / unknown。
	State string `json:"state"`
	Error string `json:"error,omitempty"`

	Current string `json:"current"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"`

	Latest      string   `json:"latest,omitempty"`
	PublishedAt string   `json:"publishedAt,omitempty"`
	ReleaseURL  string   `json:"releaseUrl,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	Assets      []string `json:"assets,omitempty"`

	CheckedAt time.Time `json:"checkedAt"`
	// Cached 表示这次是回合成的缓存、没有真的访问上游。
	Cached bool `json:"cached"`
}

// updateCache 缓存最近一次检查。零值即可用（没查过 = at 为零值）。
type updateCache struct {
	mu  sync.Mutex
	at  time.Time
	res updateResult
}

// handleCheckUpdate 查「有没有新版本」。
//
// 为什么由服务端去查、而不是让浏览器直接打 GitHub：
//  1. 这个服务的设计是「HTTP 是唯一出口、服务端是唯一联网方」，看片的人浏览器
//     在什么网络环境（内网 / 无外网）不该决定服务端能不能检查更新；
//  2. 服务器查得出问题、也说得清原因（连不上 / 超时 / 格式不对）；
//  3. 结果能缓存，天然保护上游限流。
//
// 默认**不自动查**：只有管理员点了按钮才会出网。这样既不会给每次打开设置页
// 都加一次外网请求，也不会在没有外网的环境里刷一堆失败日志。
func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	src := strings.TrimSpace(s.cfg.Update.SourceURL)
	if src == "" {
		writeJSON(w, http.StatusOK, updateResult{
			Enabled:   false,
			State:     "unknown",
			Error:     "未配置更新源（[update] source_url 为空）",
			Current:   version.Version,
			Commit:    version.Commit,
			Built:     version.BuildTime,
			CheckedAt: time.Now().UTC(),
		})
		return
	}
	force := r.URL.Query().Get("refresh") != ""
	writeJSON(w, http.StatusOK, s.checkUpdate(r.Context(), src, force))
}

// checkUpdate 先看缓存，必要时才真去问上游。
func (s *Server) checkUpdate(ctx context.Context, src string, force bool) updateResult {
	s.update.mu.Lock()
	defer s.update.mu.Unlock()

	fresh := !s.update.at.IsZero() && time.Since(s.update.at) < updateCheckMinInterval
	if !s.update.at.IsZero() && (fresh || !force) {
		cached := s.update.res
		cached.Cached = true
		return cached
	}

	res := fetchUpdate(ctx, src, s.cfg.Update.TimeoutSeconds)
	res.Enabled = true
	res.Current = version.Version
	res.Commit = version.Commit
	res.Built = version.BuildTime
	res.CheckedAt = time.Now().UTC()
	s.update.at, s.update.res = time.Now(), res
	return res
}

// ghRelease 是我们要从上游拿的字段（GitHub release JSON 的子集）。
type ghRelease struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	Assets      []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// fetchUpdate 真去问一次上游。任何失败都变成 {ok:false, error:"人话"}，不往上抛
// —— 检查更新失败不该是个 500，它只是一条信息。
func fetchUpdate(ctx context.Context, src string, timeoutSec int) updateResult {
	if timeoutSec <= 0 {
		timeoutSec = int(updateCheckTimeoutDefault / time.Second)
	}
	timeout := time.Duration(timeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res := updateResult{State: "unknown"}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		res.Error = "更新源地址不合法：" + err.Error()
		return res
	}
	// GitHub 要求带 User-Agent；Accept 用官方推荐的版本化媒体类型。
	req.Header.Set("User-Agent", "lmby/"+version.Version)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		res.Error = describeUpdateError(err, src, timeoutSec)
		return res
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		res.Error = "读取更新源响应失败：" + err.Error()
		return res
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		res.Error = "更新源上没有已发布的版本（404）"
		return res
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		res.Error = fmt.Sprintf("更新源拒绝了请求（%d）：多半是调用次数超限，过一会儿再试", resp.StatusCode)
		return res
	case resp.StatusCode != http.StatusOK:
		res.Error = fmt.Sprintf("更新源返回 %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		return res
	}

	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		res.Error = "更新源返回的不是预期格式（这里需要一个 GitHub release JSON）"
		return res
	}
	if strings.TrimSpace(rel.TagName) == "" {
		res.Error = "更新源没有给出 tag_name，认不出最新版本号"
		return res
	}

	res.OK = true
	res.Latest = rel.TagName
	res.PublishedAt = rel.PublishedAt
	res.ReleaseURL = rel.HTMLURL
	res.Notes = trimNotes(rel.Body, updateNotesMaxRunes)
	for _, a := range rel.Assets {
		res.Assets = append(res.Assets, a.Name)
	}

	cur, curOK := parseVersion(version.Version)
	lat, latOK := parseVersion(rel.TagName)
	switch {
	case !curOK:
		// dev-xxxx / 本地构建：没有可比的版本号，只把上游最新版展示出来，不下结论。
		res.State = "dev"
	case !latOK:
		res.State = "unknown"
		res.Error = "认不出上游的版本号：" + rel.TagName
		res.OK = false
	case compareVersion(cur, lat) < 0:
		res.State = "update-available"
	default:
		res.State = "up-to-date"
	}
	return res
}

// trimNotes 把发布说明压成一段能塞进界面的短文本。
func trimNotes(s string, max int) string {
	t := strings.Join(strings.Fields(s), " ")
	r := []rune(t)
	if len(r) <= max {
		return t
	}
	return strings.TrimRight(string(r[:max]), " ,，。;；") + "…"
}

// describeUpdateError 把网络层的报错翻成人话。
//
// 这件事值得单独写：检查更新最常见的失败就是「连不上」，而 Go 的原始报错
// （`dial tcp 127.0.0.1:1: connect: connection refused`）对管理员没有帮助。
// 分类之后每一句都能指向一个可操作的方向（地址错了 / 没外网 / 没 DNS / 证书）。
func describeUpdateError(err error, src string, timeoutSec int) string {
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return fmt.Sprintf("连不上更新源（%d 秒超时）：这台机器可能没有外网出口，或需要把 [update] source_url 指向可用镜像", timeoutSec)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "连不上更新源（连接被拒绝）：[update] source_url 的地址或端口对不上？当前指向 " + src
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "server misbehaving"):
		return "更新源的域名解析不了：这台机器可能没有可用的 DNS（当前指向 " + src + "）"
	case strings.Contains(msg, "certificate"):
		return "更新源的 HTTPS 证书验不过：自建镜像请检查证书链（当前指向 " + src + "）"
	}
	return "连不上更新源：" + msg
}

// parseVersion 解析 v1.2.3 / 1.2 这类版本号。
//
// 只认「点分数字」：dev-4f5f0cf、v1.0.0-rc1 这种带后缀的一律返回 ok=false
// ——拿不到可比的东西时，宁可不给结论，也不要拿字符串比大小骗人。
func parseVersion(s string) ([]int, bool) {
	v := strings.TrimSpace(s)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return nil, false
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil, false
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// compareVersion 比两个已解析的版本号（返回 -1 / 0 / 1）。缺的段按 0 算。
func compareVersion(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var ai, bi int
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}
