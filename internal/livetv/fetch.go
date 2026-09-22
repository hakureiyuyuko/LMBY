package livetv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxSubscriptionBytes 是订阅源拉取的大小上限（8 MiB）。
//
// 一个 149 台的播放列表只有十几 KiB；上限存在的意义是防「地址填成了别的什么」
// ——比如误填一个大文件或一个无限流，否则会把内存吃光。
const MaxSubscriptionBytes = 8 << 20

// ErrEmptySubscription 表示订阅源拉回来是空的（内容为空或没有可用的流地址）。
//
// 单独定义这个错误，是为了让上层**不要**把它当成「这个源现在没有任何频道」
// 去清理频道表 —— 空结果更可能是网络截断/CDN 出错。
var ErrEmptySubscription = errors.New("订阅源没有返回任何频道")

// Fetcher 负责拉取订阅源。
type Fetcher struct {
	// Client 为 nil 时用默认客户端（20 秒超时）。
	Client *http.Client
	// UserAgent 是拉取订阅源时用的 UA（有些 IPTV 服务商按 UA 拦）。
	UserAgent string
}

// NewFetcher 构造拉取器。
func NewFetcher() *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: 20 * time.Second},
		UserAgent: "LMBY/live",
	}
}

// Fetch 拉取订阅源并解析成条目（已经过去重、过滤非流地址、补分组）。
//
// 返回 ErrEmptySubscription 表示拉到了内容但没有频道。
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]Entry, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("订阅地址为空")
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("订阅地址不合法: %w", err)
	}
	if f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取订阅源失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("拉取订阅源失败: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSubscriptionBytes))
	if err != nil {
		return nil, fmt.Errorf("读取订阅源失败: %w", err)
	}
	return ParseContent(string(body))
}

// ParseContent 解析播放列表文本：解析 → 去重 → 丢掉非流地址 → 补分组。
//
// 界面上的「粘贴文本」「上传文件」与订阅拉取走的是同一条路径，
// 免得三处各有一套清洗逻辑、行为还各不相同。
func ParseContent(content string) ([]Entry, error) {
	entries := ApplyGroups(FilterStreams(Parse(content)))
	if len(entries) == 0 {
		return nil, ErrEmptySubscription
	}
	return entries, nil
}
