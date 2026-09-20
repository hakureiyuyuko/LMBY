package probe

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/worker"
)

// 编译期断言：Handler 必须实现 worker.Handler。
var _ worker.Handler = (*Handler)(nil)

// Payload 是 probe 任务的载荷。
type Payload struct {
	FileID int64 `json:"fileId"`
}

// Handler 处理 probe 任务：读文件的流信息并落库。
type Handler struct {
	st        *store.Store
	probePath string
	log       *slog.Logger
}

// NewHandler 构造探测处理器。
func NewHandler(st *store.Store, probePath string, log *slog.Logger) *Handler {
	if probePath == "" {
		probePath = "ffprobe"
	}
	return &Handler{st: st, probePath: probePath, log: log}
}

// Kind 实现 worker.Handler。
func (h *Handler) Kind() string { return store.TaskKindProbe }

// probeTimeout 是单个文件的探测上限。
//
// 容错考虑两件事：
//  1. 网络存储上单文件可能要十几秒（随机寻道），这里给足余量；
//  2. 并发探测多个文件时，存储的随机读会被互相拖累 ——
//     实测同一文件单跑 10 秒，8 路并发时能超过 2 分钟。
// 所以上限取 5 分钟，并建议网络存储上把 `[tasks] workers` 降到 2~4。
// 超时后标记失败且不重试（重试也是同样结果，只会白占 worker）。
const probeTimeout = 5 * time.Minute

// Handle 实现 worker.Handler。
//
// 四种结局要分清：
//   - 文件已被删除 / 载荷非法 → 任务已无意义，返回 nil（丢弃，不重试）
//   - 文件本身无法解析       → 标记 probe_state=failed 并返回 nil（不重试）
//   - 单个文件探测超时       → 同上，标记失败且不重试（否则会无休止重试）
//   - 环境问题（ffprobe 缺失、网络盘掉线、整个任务被取消）→ 返回错误，交给队列退避重试
func (h *Handler) Handle(ctx context.Context, t store.Task) error {
	var payload Payload
	if err := t.Decode(&payload); err != nil {
		h.log.Error("探测任务载荷非法，丢弃任务", "taskId", t.ID, "err", err)
		return nil
	}
	if payload.FileID == 0 {
		h.log.Error("探测任务缺少 fileId，丢弃任务", "taskId", t.ID)
		return nil
	}

	f, err := h.st.GetMediaFile(ctx, payload.FileID)
	if errors.Is(err, store.ErrNotFound) {
		h.log.Debug("文件已不存在，跳过探测", "fileId", payload.FileID)
		return nil
	}
	if err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	info, err := Run(probeCtx, h.probePath, f.Path)
	if err != nil {
		// 整个任务被取消（进程退出、队列回收）→ 交给队列处理
		if ctx.Err() != nil {
			return err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			h.log.Warn("探测超时，标记失败", "fileId", f.ID, "path", f.Path)
			return h.st.MarkFileProbeFailed(ctx, f.ID, "探测超时（超过 5 分钟）")
		}
		if errors.Is(err, ErrUnsupported) {
			h.log.Warn("文件无法解析，标记探测失败",
				"fileId", f.ID, "path", f.Path, "err", err.Error())
			return h.st.MarkFileProbeFailed(ctx, f.ID, err.Error())
		}
		return err
	}

	rec := store.ProbeRecord{
		Container:       info.Container,
		DurationTicks:   info.DurationTicks,
		SizeBytes:       info.SizeBytes,
		VideoStreams:    info.Video,
		AudioStreams:    info.Audio,
		SubtitleStreams: info.Subtitles,
		Chapters:        info.Chapters,
		HDR:             info.HDR,
	}
	if err := h.st.SaveFileProbe(ctx, f.ID, rec); err != nil {
		return err
	}

	h.log.Debug("探测完成",
		"fileId", f.ID, "container", info.Container,
		"video", len(info.Video), "audio", len(info.Audio), "subtitle", len(info.Subtitles))
	return nil
}
