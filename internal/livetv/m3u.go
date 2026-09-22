// Package livetv 负责直播电视：M3U 播放列表解析/生成、订阅源拉取、
// 以及「频道 → 直播转封装会话」的编排。
//
// 本包的 M3U 解析与分组推断源自同作者的 tvhub 项目（同样是 AGPL-3.0-only，
// 已验证过大量真实 IPTV 播放列表），按 LMBY 的数据模型重写：
// 这里只做「文本 → 条目」，落库与增量更新在 internal/store/livetv.go。
package livetv

import (
	"fmt"
	"regexp"
	"strings"
)

// Entry 是播放列表里的一条频道。
type Entry struct {
	Name    string // 频道名
	URL     string // 播放地址（已剥离 "|" 之后的附加请求头）
	Group   string // 分组（group-title）
	Logo    string // tvg-logo
	TvgID   string // tvg-id
	TvgName string // tvg-name
	Headers string // 附加请求头，形如 "User-Agent: xxx\r\nReferer: yyy\r\n"
}

// attrRe 匹配 #EXTINF 行里的属性（key="value"）。
var attrRe = regexp.MustCompile(`([A-Za-z0-9_.\-]+)\s*=\s*"([^"]*)"`)

// Parse 解析 m3u/m3u8 文本。
//
// 兼容真实播放列表里常见的几种写法：
//   - `#EXTINF:-1 tvg-id="x" group-title="y",频道名`（标准写法）
//   - `#EXTINF:-1 ,频道名`（没有属性的写法）
//   - `#EXTGRP:分组`（独立成行的分组指令）
//   - `地址|User-Agent=xxx&Referer=yyy`（附加请求头内联在地址后面）
//   - 没有 #EXTINF 的裸地址（用地址本身当名字）
//
// 解析失败的条目（例如只有 #EXTINF 却没有地址）直接跳过，不报错：
// 上百台的播放列表里有一两条畸形是常态，不该让整次导入失败。
func Parse(content string) []Entry {
	content = strings.TrimPrefix(content, "\ufeff") // 有的源带 BOM
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	var (
		out   []Entry
		cur   *Entry
		group string
	)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "#EXTINF"):
				e := parseEXTINF(line)
				cur, group = &e, ""
			case strings.HasPrefix(upper, "#EXTGRP:"):
				group = strings.TrimSpace(line[len("#EXTGRP:"):])
				if cur != nil {
					cur.Group = group
				}
			default:
				// #EXTM3U / #KODIPROP / #EXTVLCOPT / #EXT-X-* 等指令与 LMBY 无关
			}
			continue
		}

		url, headers := splitHeaders(line)
		if cur == nil {
			// 裸地址：没有 #EXTINF，拿地址当名字
			if isStreamURL(url) {
				out = append(out, Entry{Name: url, URL: url, Headers: headers})
			}
			continue
		}
		if url == "" {
			cur = nil
			continue
		}
		e := *cur
		e.URL, e.Headers = url, headers
		if e.Group == "" {
			e.Group = group
		}
		if e.Name == "" {
			e.Name = e.TvgName
		}
		if e.Name == "" {
			e.Name = url
		}
		out = append(out, e)
		cur, group = nil, ""
	}
	return Dedupe(out)
}

// parseEXTINF 解析 `#EXTINF:-1 tvg-id="x" group-title="y",频道名`。
func parseEXTINF(line string) Entry {
	var e Entry
	rest := line[len("#EXTINF"):] // 前缀长度固定，与大小写无关
	attrs, name := rest, ""
	// 频道名从**引号外**的第一个逗号开始，否则 `group-title="a,b"` 会被切错
	if i := indexUnquoted(rest, ','); i >= 0 {
		attrs, name = rest[:i], rest[i+1:]
	}
	for _, m := range attrRe.FindAllStringSubmatch(attrs, -1) {
		key, val := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		switch key {
		case "tvg-id":
			e.TvgID = val
		case "tvg-name":
			e.TvgName = val
		case "tvg-logo":
			e.Logo = val
		case "group-title", "group":
			e.Group = val
		}
	}
	e.Name = strings.TrimSpace(name)
	return e
}

// indexUnquoted 返回 s 中第一个不在双引号内的 c 的下标，找不到返回 -1。
func indexUnquoted(s string, c byte) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case c:
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

// splitHeaders 把 `地址|User-Agent=foo&Referer=bar` 拆成地址与请求头文本。
func splitHeaders(line string) (url, headers string) {
	i := strings.Index(line, "|")
	if i < 0 {
		return strings.TrimSpace(line), ""
	}
	url = strings.TrimSpace(line[:i])
	var sb strings.Builder
	for _, part := range strings.FieldsFunc(line[i+1:], func(r rune) bool { return r == '|' || r == '&' }) {
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			continue
		}
		sb.WriteString(k + ": " + v + "\r\n")
	}
	return url, sb.String()
}

// isStreamURL 判断是否是我们认得的流地址。
func isStreamURL(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"rtsp://", "rtsps://", "rtmp://", "rtmps://", "http://", "https://", "udp://", "rtp://"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// Kind 返回地址的协议类型，用来决定 ffmpeg 要带哪些输入参数。
func Kind(rawurl string) string {
	l := strings.ToLower(strings.TrimSpace(rawurl))
	switch {
	case strings.HasPrefix(l, "rtsp://"), strings.HasPrefix(l, "rtsps://"):
		return "rtsp"
	case strings.HasPrefix(l, "rtmp://"), strings.HasPrefix(l, "rtmps://"):
		return "rtmp"
	case strings.HasPrefix(l, "udp://"), strings.HasPrefix(l, "rtp://"):
		return "udp"
	case strings.HasPrefix(l, "http://"), strings.HasPrefix(l, "https://"):
		return "http"
	}
	return "other"
}

// KindLabel 返回协议的中文标签（界面上显示用）。
func KindLabel(rawurl string) string {
	switch Kind(rawurl) {
	case "rtsp":
		return "RTSP"
	case "rtmp":
		return "RTMP"
	case "udp":
		return "UDP"
	case "http":
		return "HTTP"
	}
	return "其他"
}

// Dedupe 按地址去重（保留最先出现的一条）。
func Dedupe(in []Entry) []Entry {
	seen := make(map[string]bool, len(in))
	out := make([]Entry, 0, len(in))
	for _, e := range in {
		if e.URL == "" || seen[e.URL] {
			continue
		}
		seen[e.URL] = true
		out = append(out, e)
	}
	return out
}

// FilterStreams 丢掉不是流地址的条目（播放列表里混进网页链接/注释是常态）。
func FilterStreams(in []Entry) []Entry {
	out := make([]Entry, 0, len(in))
	for _, e := range in {
		if isStreamURL(e.URL) {
			out = append(out, e)
		}
	}
	return out
}

// groupRule 是一组「命中关键字即归入该分组」的规则，按顺序匹配，先命中者优先。
type groupRule struct {
	Group string
	Keys  []string
}

// defaultGroupRules 仅在播放列表**没有** group-title 时兜底使用。
//
// 为什么需要：重庆联通的单播源列表就是清一色的 `#EXTINF:-1 ,CCTV1 综合`，
// 149 台平铺在一页里没法看。这里按名字粗分十几组，用户在界面上还能改。
var defaultGroupRules = []groupRule{
	{"央视", []string{"cctv", "央视", "cgtn", "中央新影"}},
	{"IPTV专区", []string{"iptv"}},
	{"卫视", []string{"卫视"}},
	{"重庆", []string{"重庆"}},
	{"上海", []string{"上海"}},
	{"影视剧场", []string{"剧场", "影院", "电影", "影视", "影迷", "佳片", "曲艺"}},
	{"体育", []string{"体育", "足球", "台球", "网球", "高尔夫", "垂钓", "武术", "赛事", "球"}},
	{"少儿教育", []string{"少儿", "卡通", "动漫", "动画", "早教", "教育", "学生", "学堂", "宝贝", "启蒙"}},
	{"纪录科教", []string{"纪录", "纪实", "地理", "科教", "解密", "文化", "精品", "国学", "探索", "发现", "求索"}},
	{"音乐戏曲", []string{"音乐", "戏曲", "相声", "小品", "文艺", "娱乐", "时尚", "美妆", "美人", "欢乐"}},
	{"生活服务", []string{"生活", "交通", "农业", "农村", "汽摩", "红岩", "红叶", "购物", "健康", "法治", "社会"}},
}

// GuessGroup 依据频道名推断分组，判断不出来归入「其他」。
func GuessGroup(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return "其他"
	}
	for _, r := range defaultGroupRules {
		for _, k := range r.Keys {
			if strings.Contains(n, strings.ToLower(k)) {
				return r.Group
			}
		}
	}
	return "其他"
}

// ApplyGroups 给没有分组的条目补上推断出来的分组（原有分组原样保留）。
func ApplyGroups(in []Entry) []Entry {
	out := make([]Entry, len(in))
	for i, e := range in {
		e.Group = strings.TrimSpace(e.Group)
		if e.Group == "" {
			e.Group = GuessGroup(e.Name)
		}
		out[i] = e
	}
	return out
}

// Render 把条目还原成 m3u 文本（导出口播放在别的播放器里用）。
func Render(in []Entry) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, e := range in {
		sb.WriteString("#EXTINF:-1")
		if e.TvgID != "" {
			fmt.Fprintf(&sb, ` tvg-id="%s"`, e.TvgID)
		}
		if e.TvgName != "" {
			fmt.Fprintf(&sb, ` tvg-name="%s"`, e.TvgName)
		}
		if e.Logo != "" {
			fmt.Fprintf(&sb, ` tvg-logo="%s"`, e.Logo)
		}
		if e.Group != "" {
			fmt.Fprintf(&sb, ` group-title="%s"`, e.Group)
		}
		sb.WriteString("," + strings.TrimSpace(e.Name) + "\n")
		sb.WriteString(e.URL)
		for _, line := range strings.Split(strings.TrimSpace(e.Headers), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
			if !ok {
				continue
			}
			sb.WriteString("|" + strings.TrimSpace(k) + "=" + strings.TrimSpace(v))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
