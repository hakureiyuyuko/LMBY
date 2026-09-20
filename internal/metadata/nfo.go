// Package metadata 处理「本地元数据文件」的读取，目前只有 xbmc 风格的 .nfo。
//
// 说明：本包**只读**。LMBY 不生成也不导出 XML —— PostgreSQL 是元数据的唯一数据源。
// 读 nfo 的目的有两个：
//  1. 兼容已经用 Emby/Jellyfin/Kodi 刮过的库，零重刮接管；
//  2. 给将来的 Emby 一次性导入器提供基础。
package metadata

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// TicksPerMinute 是 .NET tick 换算：1 tick = 100ns，1 分钟 = 600,000,000 ticks。
const TicksPerMinute = 600_000_000

// Metadata 是从 nfo 读出的元数据。
type Metadata struct {
	Kind           string // movie | series | season | episode
	Title          string
	SortTitle      string
	OriginalTitle  string
	Overview       string
	Tagline        string
	Year           *int32
	PremiereDate   *time.Time
	Rating         *float64
	RuntimeMinutes int
	OfficialRating string
	Genres         []string
	Tags           []string
	Studios        []string
	ProviderIDs    map[string]string
	People         []Person

	Season  int
	Episode int

	// Path 是来源文件路径，便于排查。
	Path string
}

// Person 是演员/导演/编剧。
type Person struct {
	Name        string
	Role        string
	Kind        string // Actor | Director | Writer
	ProviderIDs map[string]string
	Order       int
}

// ---------------------------------------------------------------- XML 结构

type xmlRoot struct {
	XMLName xml.Name

	Title         string `xml:"title"`
	SortTitle     string `xml:"sorttitle"`
	OriginalTitle string `xml:"originaltitle"`
	Plot          string `xml:"plot"`
	Outline       string `xml:"outline"`
	Tagline       string `xml:"tagline"`
	Year          string `xml:"year"`
	Premiered     string `xml:"premiered"`
	Rating        string `xml:"rating"`
	Runtime       string `xml:"runtime"`
	MPAA          string `xml:"mpaa"`
	LockData      string `xml:"lockdata"`

	Genres    []string `xml:"genre"`
	Tags      []string `xml:"tag"`
	Studios   []string `xml:"studio"`
	Directors []string `xml:"director"`
	Credits   []string `xml:"credits"`
	Countries []string `xml:"country"`

	Actors    []xmlActor    `xml:"actor"`
	UniqueIDs []xmlUniqueID `xml:"uniqueid"`

	Season         string `xml:"season"`
	Episode        string `xml:"episode"`
	Aired          string `xml:"aired"`
	DisplaySeason  string `xml:"displayseason"`
	DisplayEpisode string `xml:"displayepisode"`
}

type xmlActor struct {
	Name      string `xml:"name"`
	Role      string `xml:"role"`
	Type      string `xml:"type"`
	SortOrder string `xml:"sortorder"`
	TMDBID    string `xml:"tmdbid"`
	IMDBID    string `xml:"imdbid"`
	TVDBID    string `xml:"tvdbid"`
}

type xmlUniqueID struct {
	Type    string `xml:"type,attr"`
	Default string `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

// ---------------------------------------------------------------- 读取

// NFOFileName 返回给定媒体文件名对应的 nfo 文件名。
//
// 约定：`S01E01.mkv` → `S01E01.nfo`；无扩展名的目录级元数据用 movie.nfo / tvshow.nfo。
func NFOFileName(mediaFile string) string {
	return strings.TrimSuffix(mediaFile, filepath.Ext(mediaFile)) + ".nfo"
}

// ReadNFO 解析一个 nfo 文件。
//
// 需要处理编码现实：Emby 写出的 nfo 是 UTF-8（常见带 BOM），
// 而从别处拷来的 nfo 可能是 UTF-16。两者都要能吃下。
func ReadNFO(path string) (*Metadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := decodeXMLBytes(raw)

	var root xmlRoot
	if err := xml.Unmarshal([]byte(text), &root); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	md := &Metadata{
		Kind:           strings.ToLower(root.XMLName.Local),
		Path:           path,
		Title:          strings.TrimSpace(root.Title),
		SortTitle:      strings.TrimSpace(root.SortTitle),
		OriginalTitle:  strings.TrimSpace(root.OriginalTitle),
		Overview:       pick(root.Plot, root.Outline),
		Tagline:        strings.TrimSpace(root.Tagline),
		OfficialRating: strings.TrimSpace(root.MPAA),
		Genres:         cleanList(root.Genres),
		Tags:           cleanList(root.Tags),
		Studios:        cleanList(root.Studios),
	}

	md.Year = parseYear(root.Year)
	md.PremiereDate = parseDate(root.Premiered)
	md.Rating = parseRating(root.Rating)
	md.RuntimeMinutes = parseInt(root.Runtime)
	md.Season = parseInt(pick(root.DisplaySeason, root.Season))
	md.Episode = parseInt(pick(root.DisplayEpisode, root.Episode))

	md.ProviderIDs = map[string]string{}
	for _, u := range root.UniqueIDs {
		t := strings.ToLower(strings.TrimSpace(u.Type))
		v := strings.TrimSpace(u.Value)
		if t != "" && v != "" {
			md.ProviderIDs[t] = v
		}
	}

	for i, a := range root.Actors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		p := Person{
			Name:        name,
			Role:        strings.TrimSpace(a.Role),
			Kind:        strings.TrimSpace(a.Type),
			Order:       i,
			ProviderIDs: map[string]string{},
		}
		if p.Kind == "" {
			p.Kind = "Actor"
		}
		if a.SortOrder != "" {
			if n, err := strconv.Atoi(a.SortOrder); err == nil {
				p.Order = n
			}
		}
		addID(p.ProviderIDs, "tmdb", a.TMDBID)
		addID(p.ProviderIDs, "imdb", a.IMDBID)
		addID(p.ProviderIDs, "tvdb", a.TVDBID)
		md.People = append(md.People, p)
	}
	for _, d := range cleanList(root.Directors) {
		md.People = append(md.People, Person{Name: d, Kind: "Director", ProviderIDs: map[string]string{}})
	}
	for _, c := range cleanList(root.Credits) {
		md.People = append(md.People, Person{Name: c, Kind: "Writer", ProviderIDs: map[string]string{}})
	}

	// 标题缺失时回退到 originaltitle
	if md.Title == "" {
		md.Title = md.OriginalTitle
	}
	return md, nil
}

// TryReadNFO 读取 nfo；文件不存在时返回 (nil, nil)，方便调用方直接忽略。
func TryReadNFO(path string) (*Metadata, error) {
	md, err := ReadNFO(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return md, err
}

// ---------------------------------------------------------------- 编码处理

// decodeXMLBytes 处理 BOM 与 UTF-16。
// 任何字节序列都能解出字符串（识别不了就按 UTF-8 原样处理），所以只有返回值。
func decodeXMLBytes(raw []byte) string {
	switch {
	case len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF:
		return string(raw[3:])
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return decodeUTF16(raw[2:], false)
	case len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF:
		return decodeUTF16(raw[2:], true)
	}
	// 没有 BOM 时也要识别 UTF-16（无 BOM 的 UTF-16 很常见）
	if len(raw) >= 4 && raw[1] == 0x00 && raw[3] == 0x00 {
		return decodeUTF16(raw, false)
	}
	if len(raw) >= 4 && raw[0] == 0x00 && raw[2] == 0x00 {
		return decodeUTF16(raw, true)
	}
	return string(raw)
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return string(utf16.Decode(u))
}

// ---------------------------------------------------------------- 解析工具

func pick(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func addID(m map[string]string, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		m[key] = v
	}
}

func parseYear(s string) *int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1800 || n > 2200 {
		return nil
	}
	v := int32(n)
	return &v
}

func parseInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func parseRating(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

// parseDate 支持 `2020-01-02` 与 `2020-01-02 00:00:00`（Emby 会写后者）。
func parseDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
