package api

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/probe"
	"github.com/hakureiyuyuko/lmby/internal/subtitle"
)

// subtitleExternalIndexBase 是外挂字幕的合成流序号起点。
//
// 内嵌字幕流的序号来自 ffprobe，实测都是个位数；1000 往上留给外挂文件。
// 这么做的好处是**播放器一行都不用改**：界面照样用 subtitleStreamIndex 选轨道、
// 照样按 /api/v1/play/{sid}/subtitles/{序号}.{后缀} 取字幕，只有服务端在取的时候
// 按序号分流 —— 小于基数是「从容器里抽内嵌流」，大于等于基数是「读外挂文件」。
const subtitleExternalIndexBase = 1000

// externalSubtitleStreams 把条目下的外挂字幕转成合成的 SubtitleStream，
// 由调用方接在内嵌流后面一起交给界面。
func (s *Server) externalSubtitleStreams(ctx context.Context, itemID int64) []probe.SubtitleStream {
	subs, err := s.store.ListSubtitles(ctx, itemID)
	if err != nil {
		s.log.Warn("读取外挂字幕失败", "err", err, "itemId", itemID)
		return nil
	}
	if len(subs) == 0 {
		return nil
	}
	out := make([]probe.SubtitleStream, 0, len(subs))
	for i, sub := range subs {
		out = append(out, probe.SubtitleStream{
			Index:    subtitleExternalIndexBase + i,
			Codec:    sub.Format,
			Language: sub.Language,
			Title:    sub.Title,
			Forced:   sub.Forced,
		})
	}
	return out
}

// externalSubtitleFile 准备一条外挂字幕并返回落盘路径。
//
// 和内嵌流不同，这里不需要从容器里抽流，只要两步：
// ① 非 UTF-8 的转成 UTF-8（老字幕大量是 GB18030）；
// ② srt/vtt 转成 WebVTT，ass/ssa 原样留着给前端 libass。
// 结果按 (记录, 序号, 后缀) 缓存，第二次直接命中。
//
// 同步做完再返回：外挂字幕就是本地一个小文件，代价远小于 ffmpeg 抽流，
// 没必要走 202 + 轮询那套。
func (s *Server) externalSubtitleFile(ctx context.Context, ps *playSession, streamIndex int, suffix string) (string, error) {
	subs, err := s.store.ListSubtitles(ctx, ps.ItemID)
	if err != nil {
		return "", err
	}
	i := streamIndex - subtitleExternalIndexBase
	if i < 0 || i >= len(subs) {
		return "", fmt.Errorf("没有这条外挂字幕")
	}
	sub := subs[i]

	dir := filepath.Join(s.cfg.StreamsDirPath(), "subs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, fmt.Sprintf("ext%d-s%d.%s", sub.ID, streamIndex, suffix))
	if info, err := os.Stat(out); err == nil && info.Size() > 0 {
		return out, nil
	}

	raw, err := os.ReadFile(sub.Path)
	if err != nil {
		return "", fmt.Errorf("读取外挂字幕失败: %w", err)
	}
	if !subtitle.IsUTF8(raw) {
		if raw, err = convertToUTF8(raw); err != nil {
			return "", err
		}
	}
	if suffix == "ass" {
		return writeSubtitleCache(out, raw)
	}
	return writeSubtitleCache(out, subtitle.SRTToVTT(raw))
}

// convertToUTF8 把 GB18030 的字节流转成 UTF-8。
//
// 为什么用 iconv 而不是 ffmpeg：ffmpeg 的 ass 解复用器**只认 UTF-8**，
// 喂它一个 GB18030 的 .ass 会直接在打开输入时失败（实测 exit 183 /
// Invalid data found when processing input），`-sub_charenc` 只对 srt 那类
// 纯文本字幕生效。iconv 来自 libc-bin，Debian 上是必装的，做纯文本转码又准又省事
// —— 实测把库里的 UTF-8 ass 转成 GB18030 再转回来，结果与源文件字节完全一致。
//
// 只认 GB18030：中文外挂字幕里它占绝大多数。Big5 之类的会转失败，
// 此时用户看到的是「这条字幕取不到」，比给一屏乱码强。
func convertToUTF8(raw []byte) ([]byte, error) {
	cmd := exec.Command("iconv", "-f", "GB18030", "-t", "UTF-8")
	cmd.Stdin = bytes.NewReader(raw)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("字幕转码失败（按 GB18030 处理）: %w（%s）",
			err, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

// writeSubtitleCache 先写临时文件再改名，避免两个并发请求读到写了一半的字幕。
func writeSubtitleCache(out string, data []byte) (string, error) {
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	return out, nil
}
