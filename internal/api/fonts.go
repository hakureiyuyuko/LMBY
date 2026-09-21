package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 内封字体（mkv 的 attachment 流）的抽取与缓存。
//
// 为什么需要：mkv 可以把字体当附件塞进容器，字幕里 Style 的 Fontname 与 \fn 引用的
// 正是它们。而前端 libass（subtitles-octopus）只认「字体文件」——不给就只能退化成
// 兜底字体，表现是特效标题/美术字糊成一团（真跑踩到：一条片子的特效字幕用了 6 个内封
// 字体、一个都没给，于是「保底字体又叠了一层」）。
//
// 一次性全抽：mkv 的附件通常在文件末尾，逐个抽会把整部片子读 N 遍。
// 实测（223MB 的片、6 个附件）：只抽 1 个 23.2s，一条命令里堆 6 个 19.1s —— 同一份读盘。
// 所以流程是：ffprobe 数一下有几个附件（只读容器头部，实测 0.12s）→ 一条 ffmpeg 一次抽完。
//
// 为什么不用 `-dump_attachment:t ""`（按容器里的文件名落地）：ffmpeg 认为非 ASCII
// 文件名「不安全」而**整体失败**（实测 `Filename 方正大雅宋_GBk.TTF is unsafe`），
// 而内封字体几乎都是中文名。所以这边自己起安全文件名（0.ttf、1.ttf…），
// 容器里的原名只出现在列表接口的 JSON 里，不参与文件系统路径。
const (
	// attFontWaitTimeout 是列表接口同步等抽完的上限，超了先回 202 让前端轮询。
	attFontWaitTimeout = 2 * time.Second
	// attFontExtractTimeout 是一次性抽全部附件的上限：整部片子要读一遍，
	// 网络盘上的大文件可能好几分钟。超时就放弃（前端退化成兜底字体，不影响播放）。
	attFontExtractTimeout = 30 * time.Minute
	// attFontMaxCount 是能接受的最大附件数（纯防御：正常片子最多几十个）。
	attFontMaxCount = 64
	// attFontName 是抽取落盘的文件名模板：序号 + .ttf（.ttf/.otf 由 fontconfig 按
	// 内容识别，扩展名只影响观感）。
	attFontName = "%d.ttf"
)

// attFont 是「这个文件里内封的一个字体」。URL 由列表接口按当前播放会话拼出来。
type attFont struct {
	// Index 是**附件内部的序号**（不是流的绝对序号）：对应 -dump_attachment:t:N 的 N。
	Index int `json:"index"`
	// Name 是容器里记的文件名（给界面看/排错用，不参与落盘路径）。
	Name string `json:"name"`
	// Size 是字节数（前端可据此提示「正在下载字体」）。
	Size int64 `json:"size"`
	// URL 是给前端 libass 取字体用的地址。
	URL string `json:"url,omitempty"`
}

func (s *Server) attFontDir(fileID int64) string {
	return filepath.Join(s.cfg.StreamsDirPath(), "attfonts", fmt.Sprintf("f%d", fileID))
}

// attFontDone 是「这个文件已经抽完」的标记（内容是清单 JSON，空清单也会写）。
func (s *Server) attFontDone(fileID int64) string {
	return filepath.Join(s.attFontDir(fileID), ".done")
}

func attFontPath(dir string, index int) string {
	return filepath.Join(dir, fmt.Sprintf(attFontName, index))
}

// readAttFonts 读已抽好的清单；第二个返回值表示「抽过了」（哪怕结果是空清单）。
func (s *Server) readAttFonts(fileID int64) ([]attFont, bool) {
	raw, err := os.ReadFile(s.attFontDone(fileID))
	if err != nil {
		return nil, false
	}
	var list []attFont
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false
	}
	return list, true
}

func (s *Server) writeAttFonts(fileID int64, list []attFont) error {
	if err := os.MkdirAll(s.attFontDir(fileID), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	// 先写临时文件再改名：并发轮询的请求不会读到写了一半的清单。
	tmp := s.attFontDone(fileID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.attFontDone(fileID))
}

// attachmentNames 用 ffprobe 取容器里的附件文件名，**顺序就是 -dump_attachment:t:N 的 N**。
// 只读容器头部，网络盘上也很快（实测 0.12s）。
func (s *Server) attachmentNames(ctx context.Context, path string) ([]string, error) {
	probe := s.cfg.FFmpeg.ProbePath
	if probe == "" {
		probe = "ffprobe"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, probe,
		"-v", "error",
		"-show_entries", "stream=codec_type:stream_tags=filename",
		"-of", "json", path).Output()
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Tags      struct {
				Filename string `json:"filename"`
			} `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	names := make([]string, 0, 8)
	for _, st := range parsed.Streams {
		if st.CodecType == "attachment" {
			names = append(names, st.Tags.Filename)
		}
	}
	return names, nil
}

// extractAttFonts 一次性把某个文件的全部附件抽到缓存目录，然后写下清单。
func (s *Server) extractAttFonts(ps *playSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), attFontExtractTimeout)
	defer cancel()

	names, err := s.attachmentNames(ctx, ps.FilePath)
	if err != nil {
		return fmt.Errorf("读附件清单失败：%w", err)
	}
	if len(names) > attFontMaxCount {
		return fmt.Errorf("附件太多（%d），不抽", len(names))
	}
	dir := s.attFontDir(ps.FileID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if len(names) == 0 {
		// 没有附件也要落标记：下次直接读缓存，不再 ffprobe。
		return s.writeAttFonts(ps.FileID, []attFont{})
	}

	ffmpeg := s.cfg.FFmpeg.Path
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	for i := range names {
		args = append(args, fmt.Sprintf("-dump_attachment:t:%d", i), attFontPath(dir, i))
	}
	// -f null -：只为把附件读出来，不需要任何输出流。
	args = append(args, "-i", ps.FilePath, "-f", "null", "-")
	if out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("抽附件字体失败：%w：%s", err, strings.TrimSpace(string(out)))
	}

	list := make([]attFont, 0, len(names))
	for i, name := range names {
		st, err := os.Stat(attFontPath(dir, i))
		if err != nil || st.Size() == 0 {
			// 某个附件没落盘就跳过它，别让整件事失败（其余字体还能用）。
			continue
		}
		list = append(list, attFont{Index: i, Name: name, Size: st.Size()})
	}
	return s.writeAttFonts(ps.FileID, list)
}

// handlePlayFonts 列出当前文件内封的字体（前端 libass 要按这份清单去取字体）。
//
// 抽取要读整部片子，所以这里最多同步等 attFontWaitTimeout，超了就回 202 让前端轮询
// （与字幕抽出同一套路）。没有附件的文件也会落缓存，第二次调用是纯读文件。
func (s *Server) handlePlayFonts(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	respond := func(list []attFont) {
		out := make([]attFont, 0, len(list))
		for _, f := range list {
			f.URL = fmt.Sprintf("/api/v1/play/%s/fonts/%d", ps.ID, f.Index)
			out = append(out, f)
		}
		writeJSON(w, http.StatusOK, map[string]any{"fonts": out})
	}

	if list, ok := s.readAttFonts(ps.FileID); ok {
		respond(list)
		return
	}

	// 同一个文件只抽一次；后续请求会拿到同一个 job。
	job := s.subs.start(fmt.Sprintf("attfonts:%d", ps.FileID), func() error {
		return s.extractAttFonts(ps)
	})
	select {
	case <-job.done:
		if job.err != nil {
			s.log.Warn("抽内封字体失败", "file", ps.FileID, "err", job.err)
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": job.err.Error()})
			return
		}
	case <-time.After(attFontWaitTimeout):
		writeJSON(w, http.StatusAccepted, map[string]any{"state": "extracting"})
		return
	case <-r.Context().Done():
		return
	}
	if list, ok := s.readAttFonts(ps.FileID); ok {
		respond(list)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"state": "extracting"})
}

// handlePlayFont 给出第 n 个内封字体的字节。
//
// 文件名是「序号.ttf」，序号必须是纯数字 —— 路径穿越在这一步就被挡住了
// （容器里的原始文件名不参与路径）。
func (s *Server) handlePlayFont(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= attFontMaxCount {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "字体序号不合法"})
		return
	}
	f, err := os.Open(attFontPath(s.attFontDir(ps.FileID), n))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "这个字体还没抽好"})
		return
	}
	defer func() { _ = f.Close() }()
	// 同一个文件的内封字体不会变，让浏览器缓存住（下次播放就不用再传一遍）。
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	http.ServeContent(w, r, filepath.Base(f.Name()), time.Time{}, f)
}
