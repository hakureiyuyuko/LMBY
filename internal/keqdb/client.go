package keqdb

// KeqDB 的**写**接口（贡献上传）。
//
// 读接口（拿元数据）走的是 internal/provider 里那套 TMDB v3 客户端；这里只管写：
//   1. 先 POST /api/ingest/image 把图片换成 KeqDB 侧的 path；
//   2. 再 POST /api/ingest/contribution 提交「作品包」；
//   3. GET /api/ingest/me 查本实例的贡献数。
//
// 详见 KeqDB 仓库的 docs/INTEGRATION-LMBY.md §5。
//
// 实例 token（keq_…）由 KeqDB 后台签发，加密存在本机 settings 表里，
// 只在进程内存里解密后使用；**不落日志**。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/version"
)

// ErrUnauthorized：实例 token 失效 / 被禁用。调用方应停止上传并提示用户。
var ErrUnauthorized = errors.New("KeqDB 实例 token 无效或已被禁用")

// APIError 是 KeqDB 返回的非 2xx（4xx 多是数据问题，重试无用）。
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("KeqDB 返回 %d: %s", e.Status, e.Body)
}

// Retryable 报告这个错误是否值得退避重试（网络/5xx 值得，4xx 不值得）。
func (e *APIError) Retryable() bool { return e.Status >= 500 }

// Client 是 KeqDB 的写客户端。
type Client struct {
	baseURL string // 形如 https://keqdb.kyarucloud.moe（无结尾斜杠）
	token   string
	http    *http.Client
	log     *slog.Logger
}

// New 构造。token 为空时也能建（但写接口会 401）。
func New(baseURL, token string, log *slog.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 60 * time.Second},
		log:     log,
	}
}

// Configured 表示地址与 token 都有（能跑贡献）。
func (c *Client) Configured() bool { return c.baseURL != "" && c.token != "" }

// ImageResult 是 POST /api/ingest/image 的响应。
type ImageResult struct {
	Path      string `json:"path"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	BytesSize int64  `json:"bytesSize"`
	MimeType  string `json:"mimeType"`
}

// ContributionResult 是 POST /api/ingest/contribution 的响应。
type ContributionResult struct {
	ID      int64  `json:"id"`
	State   string `json:"state"`
	Deduped bool   `json:"deduped"`
	Message string `json:"message"`
}

// InstanceStatus 是 GET /api/ingest/me 里的实例信息。
type InstanceStatus struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	Uploads  int64  `json:"uploads"`
	Approved int64  `json:"approved"`
	Rejected int64  `json:"rejected"`
}

// Me 是 GET /api/ingest/me 的响应。
type Me struct {
	Instance    InstanceStatus `json:"instance"`
	SitePending int64          `json:"sitePending"`
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	// 固定 UA：默认的 Go-http-client/1.1 会被 KeqDB 前面的 Cloudflare 挑战成 403。
	req.Header.Set("User-Agent", "LMBY/"+version.String())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 KeqDB 失败: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))

	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &APIError{Status: res.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析 KeqDB 响应失败: %w", err)
		}
	}
	return nil
}

// UploadImage 上传一张图片，返回 KeqDB 侧的 path。
//
// path 是内容寻址的：同一张图重复上传返回同一个 path，不会重复占空间。
func (c *Client) UploadImage(ctx context.Context, data []byte, mime string) (*ImageResult, error) {
	if strings.TrimSpace(mime) == "" {
		mime = "application/octet-stream"
	}
	var out ImageResult
	if err := c.do(ctx, http.MethodPost, "/api/ingest/image", bytes.NewReader(data), mime, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Contribute 提交一个作品包（字段都可选，见对接文档 §5.2）。
func (c *Client) Contribute(ctx context.Context, payload map[string]any) (*ContributionResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var out ContributionResult
	if err := c.do(ctx, http.MethodPost, "/api/ingest/contribution", bytes.NewReader(body), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Me 查本实例的贡献状态（已上传 / 已采纳 / 被驳回）。
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	if err := c.do(ctx, http.MethodGet, "/api/ingest/me", nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
