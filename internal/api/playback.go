package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/encoder"
	"github.com/hakureiyuyuko/lmby/internal/playback"
	"github.com/hakureiyuyuko/lmby/internal/probe"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/stream"
)

// 播放会话的取值与阈值。
const (
	// playSessionIdleTTL 是播放会话在内存里保留多久（没有任何请求、也没有心跳）。
	//
	// 播放会话是内存态：它只是「这一次播放的上下文」（用了哪个文件、哪条流、
	// 从哪开始、转封装会话键），重启后失效是合理的 —— 真正要长期保留的是观看进度，
	// 那个在库里（playback_progress）。
	playSessionIdleTTL = 3 * time.Hour

	// playedRatio 是「算看完了」的比例。留一点余量：片尾曲/字幕还在放的时候
	// 用户就走开了，不能因此不计数。
	playedRatio = 0.92

	// playStartTimeout 是等 ffmpeg 产出第一个分片的超时。
	playStartTimeout = 20 * time.Second

	// subtitleWaitTimeout 是「等字幕抽好」的时长上限。
	//
	// 抽内嵌字幕要把整个文件读一遍（网络盘上 700MB 的片子冷读实测 ~60s），
	// 同步等到底会让播放器白屏很久，所以超过这个时间就回 202「正在准备」，
	// 后台继续抽（进程内单飞），前端轮询 —— 第二次播放就直接命中缓存。
	subtitleWaitTimeout = 15 * time.Second
	// subtitleExtractTimeout 是后台抽取任务自己的超时（大文件 + 慢盘要留足）。
	subtitleExtractTimeout = 20 * time.Minute
)

// ErrSubtitlePending 表示字幕还在抽取中（不是错误）。
var ErrSubtitlePending = errors.New("字幕正在抽取中")

// playSession 是一次播放的上下文。
type playSession struct {
	ID     string
	UserID int64
	ItemID int64
	Item   store.Item
	FileID int64
	// 文件路径在创建会话时就校验过（必须在媒体库根目录下），
	// 之后的请求一律用它，绝不接受客户端传来的路径。
	FilePath        string
	FileMTime       time.Time
	FileSize        int64
	Plan            playback.Plan
	StartSeconds    float64
	DurationSeconds float64
	// StreamKey 是转封装会话的键（direct 模式为空）。
	StreamKey string
	CreatedAt time.Time
	lastSeen  time.Time
	// Started 记录「是否已经上报过开始」（播放次数只在第一次 +1）。
	started bool
}

func (p *playSession) touch() { p.lastSeen = time.Now() }

// playRegistry 管理活跃播放会话。
type playRegistry struct {
	mu   sync.Mutex
	m    map[string]*playSession
	next int64
	last time.Time
}

func newPlayRegistry() *playRegistry {
	return &playRegistry{m: map[string]*playSession{}, last: time.Now()}
}

func (r *playRegistry) add(p *playSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	p.ID = "ps" + strconv.FormatInt(r.next, 36) + "-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 36)
	r.m[p.ID] = p
	r.reapLocked()
}

func (r *playRegistry) get(id string) *playSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[id]
	if !ok {
		return nil
	}
	p.touch()
	return p
}

func (r *playRegistry) remove(id string) *playSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[id]
	if ok {
		delete(r.m, id)
	}
	return p
}

// countForUser 统计某个用户当前活跃的播放会话数（并发限制用）。
func (r *playRegistry) countForUser(userID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.m {
		if p.UserID == userID {
			n++
		}
	}
	return n
}

// list 返回全部会话（诊断用）。
func (r *playRegistry) list() []*playSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*playSession, 0, len(r.m))
	for _, p := range r.m {
		out = append(out, p)
	}
	return out
}

// reapLocked 清掉过期会话。调用方持锁。
//
// 顺手做（每次新增会话时）而不是另起 ticker：播放会话的创建频率很低，
// 而且过期与否只在这时才有意义。
func (r *playRegistry) reapLocked() {
	now := time.Now()
	if now.Sub(r.last) < time.Minute {
		return
	}
	r.last = now
	for id, p := range r.m {
		if now.Sub(p.lastSeen) > playSessionIdleTTL {
			delete(r.m, id)
		}
	}
}

// subtitleJob 是一次字幕抽取任务。
type subtitleJob struct {
	done chan struct{}
	err  error
}

// subtitleJobs 保证「同一个文件 + 同一条字幕轨」只抽一次。
//
// 为什么不直接跑：一轮播放里前端可能并发请求同一条字幕（<track> 预加载 +
// 用户手动切换），两个 ffmpeg 同时读一个网络盘文件会把彼此拖慢一倍，
// 而且它们写的是同一个输出文件。
type subtitleJobs struct {
	mu   sync.Mutex
	jobs map[string]*subtitleJob
}

func newSubtitleJobs() *subtitleJobs { return &subtitleJobs{jobs: map[string]*subtitleJob{}} }

func (r *subtitleJobs) start(key string, run func() error) *subtitleJob {
	r.mu.Lock()
	if j, ok := r.jobs[key]; ok {
		r.mu.Unlock()
		return j
	}
	j := &subtitleJob{done: make(chan struct{})}
	r.jobs[key] = j
	r.mu.Unlock()

	go func() {
		j.err = run()
		close(j.done)
		// 跑完就摘掉：成功的话文件已经在缓存里（下次直接命中），
		// 失败的话下次请求应该重试，而不是永远返回同一个旧错误。
		r.mu.Lock()
		delete(r.jobs, key)
		r.mu.Unlock()
	}()
	return j
}

// CleanupPlaySessions 清理过期的播放会话，返回清理条数（janitor 用）。
func (s *Server) CleanupPlaySessions() int {
	s.plays.mu.Lock()
	defer s.plays.mu.Unlock()
	n := 0
	now := time.Now()
	for id, p := range s.plays.m {
		if now.Sub(p.lastSeen) > playSessionIdleTTL {
			delete(s.plays.m, id)
			n++
		}
	}
	s.plays.last = now
	return n
}

// ---------------------------------------------------------------- 请求 / 响应

// playRequest 是开始播放的请求体。
//
// 字段名（前端 JSON）与请求结构一一对应，接口开了 DisallowUnknownFields，
// 名字写错会直接报错而不是被静默忽略。
type playRequest struct {
	// Profile 是客户端上报的能力（前端用 MediaSource.isTypeSupported 实测）。
	// 不传则用服务端的保守默认档。
	Profile *playback.Profile `json:"profile,omitempty"`

	VideoStreamIndex    *int `json:"videoStreamIndex,omitempty"`
	AudioStreamIndex    *int `json:"audioStreamIndex,omitempty"`
	SubtitleStreamIndex *int `json:"subtitleStreamIndex,omitempty"`

	// StartPositionTicks 为 0 或省略时用库里的续播位置。
	StartPositionTicks int64 `json:"startPositionTicks,omitempty"`
	// Restart 为真时忽略续播位置（「从头播放」）。
	Restart bool `json:"restart,omitempty"`

	// MaxHeight 是用户在播放器里选的画质档（输出高度上限）：
	// 省略 = 没选（按配置的 transcode_max_height 走）；0 = 原生分辨率；> 0 = 上限。
	// 选了比源低的档就必须转码（否则等于没选）。
	MaxHeight *int `json:"maxHeight,omitempty"`
	// HighBitrate 为真 = 选了「Premium」档（同分辨率、更高码率）。
	HighBitrate bool `json:"highBitrate,omitempty"`
}

// playStateResponse 是播放状态响应（开始播放与查询状态共用）。
type playStateResponse struct {
	PlaySessionID string `json:"playSessionId,omitempty"`
	Mode          string `json:"mode"`
	Playable      bool   `json:"playable"`
	// State：direct（原文件直出）| starting | ready | error | stopped
	State           string        `json:"state"`
	Error           string        `json:"error,omitempty"`
	Log             string        `json:"log,omitempty"`
	Reasons         []string      `json:"reasons"`
	Plan            playback.Plan `json:"plan"`
	StartSeconds    float64       `json:"startSeconds"`
	DurationSeconds float64       `json:"durationSeconds"`
	DirectURL       string        `json:"directUrl,omitempty"`
	HLSURL          string        `json:"hlsUrl,omitempty"`
	SubtitleURL     string        `json:"subtitleUrl,omitempty"`
	// SubtitleFormat：vtt（浏览器原生轨道）| ass（前端 libass 渲染，保留特效）。
	SubtitleFormat string `json:"subtitleFormat,omitempty"`
	// SubtitleState：ready（已可挂上）/ preparing（内嵌字幕还在抽，前端轮询）。
	SubtitleState   string         `json:"subtitleState,omitempty"`
	// WindowEndSeconds：转封装模式下这一段预生成窗口的结束位置（秒）。
	//
	// 播放器靠它判断「该续下一段了」：不能用 video.seekable.end ——
	// hls.js 的可用区间只是「已加载的那几个分片」，播放中永远是「当前时间 + 几秒」，
	// 拿它做判据会一开播就疯狂续窗口。
	WindowEndSeconds float64 `json:"windowEndSeconds,omitempty"`
	ItemID          int64          `json:"itemId"`
	Title           string         `json:"title,omitempty"`
	Progress        *progressView  `json:"progress,omitempty"`
	Streams         map[string]any `json:"streams,omitempty"`
	PlaybackSeconds float64        `json:"playbackSeconds"`
}

// progressView 是给界面的进度视图。
type progressView struct {
	PositionTicks int64 `json:"positionTicks"`
	DurationTicks int64 `json:"durationTicks"`
	Played        bool  `json:"played"`
	PlayCount     int32 `json:"playCount"`
}

func viewProgress(p *store.PlaybackProgress) *progressView {
	if p == nil {
		return nil
	}
	return &progressView{
		PositionTicks: p.PositionTicks,
		DurationTicks: p.DurationTicks,
		Played:        p.Played,
		PlayCount:     p.PlayCount,
	}
}

// ---------------------------------------------------------------- 开始播放

// handleStartPlayback 创建（或复用）一次播放会话：做播放决策、必要时起 ffmpeg
// 转封装，并返回播放器要用的全部地址。
//
// 决策做不到的事情（需要视频转码）也返回 200 —— 让界面能把理由展示给用户，
// 而不是一个「播放失败」。
func (s *Server) handleStartPlayback(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	var req playRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	authCtx := currentAuth(r)
	userID := authCtx.User.ID

	// 每用户并发上限（users.max_concurrent_streams 为 0 表示不限）。
	if max := int(authCtx.User.MaxConcurrentStreams); max > 0 && s.plays.countForUser(userID) >= max {
		writeError(w, http.StatusConflict, "同时播放的数目已达上限")
		return
	}

	profile := playback.BrowserProfile()
	if req.Profile != nil {
		profile = req.Profile.Normalize()
	}

	files, err := s.store.ListPlayableFiles(r.Context(), item.ID)
	if err != nil {
		s.serverError(w, "读取条目文件失败", err)
		return
	}
	candidates := make([]playback.File, 0, len(files))
	for _, f := range files {
		candidates = append(candidates, playbackFile(f))
	}

	progress, err := s.store.GetPlaybackProgress(r.Context(), userID, item.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "读取播放进度失败", err)
		return
	}

	startTicks := req.StartPositionTicks
	if req.Restart {
		startTicks = 0
	} else if startTicks <= 0 && progress != nil && !progress.Played {
		startTicks = progress.PositionTicks
	}
	// 已经看完的条目再点进来，从头开始（否则会秒退回首页）。
	if progress != nil && progress.Played && !req.Restart {
		startTicks = 0
	}

	// 本机能力：拿不到就退化成「不能转码」—— 此时能直出/能转封装的内容照样能放，
	// 只是需要转码的文件会如实告知理由，而不是默默失败。
	machine := playback.Machine{}
	if caps, err := s.encoders.Get(r.Context()); err == nil && caps != nil {
		machine = machineForDecision(caps, s.encoders.Preferred(caps))
	} else if err != nil {
		s.log.Warn("播放时读不到编码能力，按“不能转码”处理", "err", err)
	}

	plan := playback.Decide(playback.Request{
		Profile:            profile,
		Files:              candidates,
		Machine:            machine,
		TranscodeMaxHeight: s.cfg.Playback.TranscodeMaxHeight,
		MaxHeight:          req.MaxHeight,
		HighBitrate:        req.HighBitrate,
		VideoIndex:         intValue(req.VideoStreamIndex),
		AudioIndex:         intValue(req.AudioStreamIndex),
		SubtitleIndex:      intValueOr(req.SubtitleStreamIndex, 0),
		StartTicks:         startTicks,
	})

	// 找到实际使用的文件行（决策只给了 id）。
	file, hasFile := findPlayableFile(files, plan.FileID)
	if plan.Playable && hasFile {
		// 起播点越界（源文件被换过、进度比实际时长大）会让 ffmpeg 直接顶在 EOF 上。
		// 从片尾之后续播更没有意义，直接从头开始。
		startTicks = clampStartTicks(startTicks, file.DurationTicks)
		plan.StartTicks = startTicks
	}

	resp := playStateResponse{
		Mode:            plan.Mode,
		Playable:        plan.Playable,
		Reasons:         plan.Reasons,
		Plan:            plan,
		ItemID:          item.ID,
		Title:           item.Title,
		Progress:        viewProgress(progress),
		State:           "error",
		DurationSeconds: ticksToSeconds(plan.DurationTicks),
	}

	if !plan.Playable {
		if plan.Mode == playback.ModeTranscode {
			resp.Error = "这个文件需要视频转码，但这台机器没有可用的编码器（或者目标编码客户端也不支持）；理由见 reasons"
		} else {
			resp.Error = "这个条目现在放不了；理由见 reasons"
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if !hasFile {
		// 极罕见：决策刚选中的文件在两次查询之间被删了。
		resp.Error = "文件已不在库里（可能刚被删除），刷新后重试"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 关键安全检查：文件必须真的在媒体库根目录下。
	// 路径是从库里读的，但库里的路径也可能被别处改过 —— serve 出去就是任意文件读。
	absPath, err := s.verifyMediaPath(r.Context(), item.ID, file.Path)
	if err != nil {
		s.log.Warn("拒绝播放：文件路径不在媒体库根目录下",
			"item", item.ID, "path", file.Path, "err", err)
		writeError(w, http.StatusForbidden, "文件路径不在媒体库范围內，已拒绝播放")
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		resp.Error = "文件读不到（网络盘可能掉线）：" + err.Error()
		return
	}

	ps := &playSession{
		UserID:          userID,
		ItemID:          item.ID,
		Item:            *item,
		FileID:          file.ID,
		FilePath:        absPath,
		FileMTime:       info.ModTime(),
		FileSize:        info.Size(),
		Plan:            plan,
		DurationSeconds: ticksToSeconds(plan.DurationTicks),
		CreatedAt:       time.Now(),
		lastSeen:        time.Now(),
	}
	if ps.DurationSeconds <= 0 {
		ps.DurationSeconds = ticksToSeconds(file.DurationTicks)
	}
	ps.StartSeconds = ticksToSeconds(startTicks)

	s.plays.add(ps)

	switch plan.Mode {
	case playback.ModeDirect:
		resp.PlaySessionID = ps.ID
		resp.State = "direct"
		resp.StartSeconds = ps.StartSeconds
		resp.DirectURL = "/api/v1/play/" + ps.ID + "/stream"
	default:
		url, state, errMsg, logTail := s.startHLS(r.Context(), ps, file)
		resp.PlaySessionID = ps.ID
		resp.State = state
		resp.Error = errMsg
		resp.Log = logTail
		resp.HLSURL = url
		resp.StartSeconds = ps.StartSeconds
		resp.Playable = state != "error"
	}

	if resp.State != "error" {
		// 字幕地址、字幕准备状态、转封装窗口末端都从这里一次性填好
		//（与 handlePlayState 共用，避免两边漏填）。
		s.setPlayURLs(&resp, ps)
	}
	writeJSON(w, http.StatusOK, resp)
}

// startHLS 启动（或复用）HLS 会话 —— 转封装与转码走的是同一条路，
// 区别只在 spec.Video 里是「复制」还是「转码参数」。
func (s *Server) startHLS(ctx context.Context, ps *playSession, file store.PlayableFile) (url, state, errMsg, logTail string) {
	if s.streams == nil {
		return "", "error", "转封装/转码服务未启用（ffmpeg 不可用？）", ""
	}
	video, audio, err := s.encodeArgs(ctx, ps)
	if err != nil {
		return "", "error", err.Error(), ""
	}
	spec := stream.Spec{
		Key:           streamKey(ps, video, audio),
		Path:          ps.FilePath,
		VideoIndex:    ps.Plan.Video.Index,
		VideoCodec:    ps.Plan.Video.Codec,
		AudioIndex:    ps.Plan.Audio.Index,
		Video:         video,
		Audio:         audio,
		SegmentFormat: ps.Plan.SegmentFormat,
		StartSeconds:  ps.StartSeconds,
	}
	sess, err := s.streams.Ready(ctx, spec, playStartTimeout)
	if err != nil {
		s.log.Warn("HLS 会话启动失败", "item", ps.ItemID, "file", file.ID, "mode", ps.Plan.Mode, "err", err)
		return "", "error", err.Error(), ""
	}
	ps.StreamKey = spec.Key
	return "/api/v1/play/" + ps.ID + "/index.m3u8", "ready", "", sess.Stat().Log
}

// encodeArgs 把决策结果翻译成 stream 层的「这两段怎么送」。
//
// 这里是「决策（要什么）」与「执行（怎么拼 ffmpeg 参数）」的交界：
// 转码时向 internal/encoder 要参数 —— 它才知道本机该用哪个后端、
// 哪种码率模式（探测出来的），stream 层只负责把它们拼进命令行。
func (s *Server) encodeArgs(ctx context.Context, ps *playSession) (stream.VideoEncode, stream.AudioEncode, error) {
	video := stream.CopyVideoEncode()
	audio := stream.CopyAudioEncode()

	if ps.Plan.Audio.Action != playback.ActionCopy {
		maxCh := ps.Plan.Audio.Channels
		if ps.Plan.Audio.Downmix {
			maxCh = 2
		}
		aa := encoder.AudioEncodeArgs(encoder.AudioArgsRequest{
			SourceCodec: ps.Plan.Audio.Codec,
			Channels:    ps.Plan.Audio.Channels,
			MaxChannels: maxCh,
		})
		audio = stream.AudioEncode{Args: aa.Args}
	}

	if ps.Plan.Video.Action != playback.ActionTranscode {
		return video, audio, nil
	}

	caps, err := s.encoders.Get(ctx)
	if err != nil {
		return video, audio, fmt.Errorf("读不到本机编码能力：%w", err)
	}
	backend := s.encoders.Preferred(caps)
	va := backend.VideoArgs(encoder.ArgsRequest{
		SourceCodec:     ps.Plan.Video.SourceCodec,
		Width:           ps.Plan.Video.SourceWidth,
		Height:          ps.Plan.Video.SourceHeight,
		BitDepth:        ps.Plan.Video.SourceBitDepth,
		HDR:             ps.Plan.Video.SourceHDR,
		Interlaced:      ps.Plan.Video.SourceInterlaced,
		TargetCodec:     ps.Plan.Video.TargetCodec,
		TargetWidth:     ps.Plan.Video.TargetWidth,
		TargetHeight:    ps.Plan.Video.TargetHeight,
		Quality:         encoder.QualityByName(ps.Plan.Video.Quality),
		KeyframeSeconds: s.cfg.Playback.HLSSegmentSeconds,
	})
	return stream.VideoEncode{
		InputArgs:  va.InputArgs,
		FilterArgs: va.FilterArgs,
		CodecArgs:  va.CodecArgs,
		Tag:        va.Tag,
	}, audio, nil
}

// streamKey 是转封装/转码会话的键：条目 + 文件 + 模式 + 起播点 + **编码参数指纹**。
//
// 编码参数必须进键：用户把画质从 1080p 切到 720p 时，条目、起播点、模式都没变，
// 但 ffmpeg 命令行完全不同 —— 键相同就会复用上一档的会话（切了档位画面不变）。
// 反过来，参数一样时键也一样，所以「同一部片子同一段共享一路 ffmpeg」仍然成立。
//
// 不带用户：两个人同时看同一部片子的同一段，共享一路 ffmpeg 就够了
// （这正是「会话复用」，也是 M4 并发控制的基础）。
func streamKey(ps *playSession, video stream.VideoEncode, audio stream.AudioEncode) string {
	parts := make([]string, 0, 16)
	parts = append(parts, video.InputArgs...)
	parts = append(parts, video.FilterArgs...)
	parts = append(parts, video.CodecArgs...)
	parts = append(parts, video.Tag...)
	parts = append(parts, fmt.Sprintf("vcopy=%v", video.Copy))
	parts = append(parts, audio.Args...)
	parts = append(parts, fmt.Sprintf("acopy=%v", audio.Copy))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return fmt.Sprintf("i%d-f%d-%s-%d-%x", ps.ItemID, ps.FileID, ps.Plan.Mode, int(ps.StartSeconds), sum[:6])
}

// handlePlayState 返回播放会话状态（播放器出错时用它拿到 ffmpeg 的 stderr 尾巴）。
func (s *Server) handlePlayState(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	resp := playStateResponse{
		PlaySessionID:   ps.ID,
		Mode:            ps.Plan.Mode,
		Playable:        true,
		Reasons:         ps.Plan.Reasons,
		Plan:            ps.Plan,
		ItemID:          ps.ItemID,
		Title:           ps.Item.Title,
		StartSeconds:    ps.StartSeconds,
		DurationSeconds: ps.DurationSeconds,
	}
	s.setPlayURLs(&resp, ps)
	resp.PlaybackSeconds = time.Since(ps.CreatedAt).Seconds()

	switch ps.Plan.Mode {
	case playback.ModeDirect:
		resp.State = "direct"
	default:
		sess := s.streams.Get(ps.StreamKey)
		if sess == nil {
			resp.State = "stopped"
			return
		}
		st := sess.Stat()
		resp.State = st.State
		resp.Error = st.Error
		resp.Log = st.Log
		resp.Streams = map[string]any{"segments": st.Segments, "idleSeconds": st.IdleSec}
	}
	writeJSON(w, http.StatusOK, resp)
}

// setPlayURLs 按模式填好播放地址与字幕状态。
func (s *Server) setPlayURLs(resp *playStateResponse, ps *playSession) {
	switch ps.Plan.Mode {
	case playback.ModeDirect:
		resp.DirectURL = "/api/v1/play/" + ps.ID + "/stream"
	default:
		resp.HLSURL = "/api/v1/play/" + ps.ID + "/index.m3u8"
	}
	if ps.Plan.Subtitle.Action == playback.ActionConvert {
		// ASS/SSA 给原文件（前端 libass 渲染，保留特效），其余给 WebVTT。
		suffix, format := "vtt", "vtt"
		if ps.Plan.Subtitle.DeliverAs == playback.DeliverLibass {
			suffix, format = "ass", "ass"
		}
		resp.SubtitleURL = fmt.Sprintf("/api/v1/play/%s/subtitles/%d.%s", ps.ID, ps.Plan.Subtitle.Index, suffix)
		resp.SubtitleFormat = format
		if s.subtitleReady(ps.FileID, ps.Plan.Subtitle.Index, suffix) {
			resp.SubtitleState = "ready"
		} else {
			resp.SubtitleState = "preparing"
		}
	}
	resp.WindowEndSeconds = s.windowEnd(ps)
}

// windowEnd 返回当前转封装窗口的结束位置（秒）。direct 模式或会话已回收时返回 0。
func (s *Server) windowEnd(ps *playSession) float64 {
	if ps.Plan.Mode == playback.ModeDirect || ps.StreamKey == "" {
		return 0
	}
	sess := s.streams.Get(ps.StreamKey)
	if sess == nil {
		return 0
	}
	end := sess.Spec.StartSeconds + float64(sess.Spec.WindowSeconds)
	if ps.DurationSeconds > 0 && end > ps.DurationSeconds {
		end = ps.DurationSeconds
	}
	return end
}

// subtitleReady 报告某条字幕的 WebVTT 是否已经在缓存里。
func (s *Server) subtitleReady(fileID int64, streamIndex int, suffix string) bool {
	p := filepath.Join(s.cfg.StreamsDirPath(), "subs", fmt.Sprintf("f%d-s%d.%s", fileID, streamIndex, suffix))
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

// handlePlaySeek 切换播放位置。
//
// direct 模式什么都不用做（浏览器自己发 Range 请求）；
// 转封装模式要按新位置起一段新的窗口 —— 旧的立刻回收，
// 否则一路快速拖动会在几秒内堆出十几个 ffmpeg。
func (s *Server) handlePlaySeek(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		PositionTicks int64 `json:"positionTicks"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.PositionTicks < 0 {
		body.PositionTicks = 0
	}
	// seek 到片尾之外也会把 ffmpeg 顶死，夹到最后一小段之前。
	ps.StartSeconds = clampSeekSeconds(ticksToSeconds(body.PositionTicks), ps.DurationSeconds)

	resp := playStateResponse{
		PlaySessionID:   ps.ID,
		Mode:            ps.Plan.Mode,
		Playable:        true,
		State:           "direct",
		Reasons:         ps.Plan.Reasons,
		Plan:            ps.Plan,
		ItemID:          ps.ItemID,
		Title:           ps.Item.Title,
		StartSeconds:    ps.StartSeconds,
		DurationSeconds: ps.DurationSeconds,
	}

	if ps.Plan.Mode == playback.ModeDirect {
		s.setPlayURLs(&resp, ps)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	oldKey := ps.StreamKey
	video, audio, err := s.encodeArgs(r.Context(), ps)
	if err != nil {
		writeJSON(w, http.StatusOK, playStateResponse{
			PlaySessionID: ps.ID, Mode: ps.Plan.Mode, State: "error",
			Error: err.Error(), Reasons: ps.Plan.Reasons, Plan: ps.Plan, ItemID: ps.ItemID,
		})
		return
	}
	spec := stream.Spec{
		Key:           streamKey(ps, video, audio),
		Path:          ps.FilePath,
		VideoIndex:    ps.Plan.Video.Index,
		VideoCodec:    ps.Plan.Video.Codec,
		AudioIndex:    ps.Plan.Audio.Index,
		Video:         video,
		Audio:         audio,
		SegmentFormat: ps.Plan.SegmentFormat,
		StartSeconds:  ps.StartSeconds,
	}
	sess, err := s.streams.Ready(r.Context(), spec, playStartTimeout)
	if err != nil {
		writeJSON(w, http.StatusOK, playStateResponse{
			PlaySessionID: ps.ID, Mode: ps.Plan.Mode, State: "error",
			Error: err.Error(), Reasons: ps.Plan.Reasons, Plan: ps.Plan,
		})
		return
	}
	ps.StreamKey = spec.Key
	// 新的起来之后再回收旧的：先停旧的话，万一起不来就彻底没得看了。
	if oldKey != "" && oldKey != spec.Key {
		s.streams.Stop(oldKey)
	}
	resp.State = "ready"
	resp.Log = sess.Stat().Log
	resp.PlaybackSeconds = time.Since(ps.CreatedAt).Seconds()
	s.setPlayURLs(&resp, ps)
	writeJSON(w, http.StatusOK, resp)
}

// handlePlayStop 结束播放：停掉转封装进程并释放会话。
func (s *Server) handlePlayStop(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	// 允许带最后一次进度，省一次往返（关闭页面时最有用）。
	var body struct {
		PositionTicks int64 `json:"positionTicks,omitempty"`
		DurationTicks int64 `json:"durationTicks,omitempty"`
	}
	// 关闭页面时的收尾请求：体是可选的，坏了也不该变成「取消失败」。
	if r.ContentLength != 0 {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
		if body.PositionTicks > 0 {
			if _, err := s.saveProgress(r.Context(), ps, body.PositionTicks, body.DurationTicks); err != nil {
				// 关闭页面时的最后一次进度：写失败只记日志，不能因此让 stop 失败。
				s.log.Warn("结束播放时保存进度失败", "item", ps.ItemID, "err", err)
			}
		}
	}
	if ps.StreamKey != "" {
		s.streams.Stop(ps.StreamKey)
	}
	s.plays.remove(ps.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePlayProgress 接收播放心跳。
func (s *Server) handlePlayProgress(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		PositionTicks int64 `json:"positionTicks"`
		DurationTicks int64 `json:"durationTicks,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// 进度上报是最准的「客户端看到哪了」：转封装/转码的节流用它来判断
	// 要不要暂停 ffmpeg（分片请求只能推出「下载到哪」，下载可以跑在播放前面）。
	if sess := s.streams.Get(ps.StreamKey); sess != nil {
		sess.MarkClientPosition(ticksToSeconds(body.PositionTicks))
	}
	p, err := s.saveProgress(r.Context(), ps, body.PositionTicks, body.DurationTicks)
	if err != nil {
		s.serverError(w, "保存播放进度失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": viewProgress(p), "played": p.Played})
}

// saveProgress 写进度，并在「基本看完」时标记已看。
func (s *Server) saveProgress(ctx context.Context, ps *playSession, positionTicks, durationTicks int64) (*store.PlaybackProgress, error) {
	duration := durationTicks
	if duration <= 0 {
		duration = int64(ps.DurationSeconds * float64(playback.TicksPerSecond))
	}

	started := !ps.started
	if err := s.store.SavePlaybackProgress(ctx, ps.UserID, ps.ItemID, positionTicks, duration, started); err != nil {
		return nil, err
	}
	if started {
		ps.started = true
	}

	// 看到片尾就自动标记已看（并清掉续播位置，否则首页会一直把这条拉回来）。
	if duration > 0 && float64(positionTicks) >= float64(duration)*playedRatio {
		if err := s.store.SetPlayed(ctx, ps.UserID, []int64{ps.ItemID}, true); err != nil {
			return nil, err
		}
	}
	p, err := s.store.GetPlaybackProgress(ctx, ps.UserID, ps.ItemID)
	if errors.Is(err, store.ErrNotFound) {
		return &store.PlaybackProgress{ItemID: ps.ItemID}, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ---------------------------------------------------------------- 媒体分发

// handlePlayStream 直出原文件（HTTP Range / ETag / Last-Modified）。
//
// 用 http.ServeContent：Range、If-Range、多区间、HEAD 全由标准库处理 ——
// 手写这些最容易在边界上出错（多区间分界、越界、offset 溢出），而这正是
// 播放器快速拖动时会踩的地方。
func (s *Server) handlePlayStream(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	f, err := os.Open(ps.FilePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "文件打不开："+err.Error())
		return
	}
	defer func() { _ = f.Close() }()

	h := w.Header()
	h.Set("Content-Type", contentTypeFor(ps.Plan.ContainerKind, ps.FilePath))
	h.Set("ETag", etagFor(ps))
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	http.ServeContent(w, r, filepath.Base(ps.FilePath), ps.FileMTime, f)
}

// handlePlayPlaylist 输出 HLS 播放列表。
func (s *Server) handlePlayPlaylist(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	if ps.Plan.Mode == playback.ModeDirect {
		writeError(w, http.StatusBadRequest, "这个播放会话是直出模式，没有播放列表")
		return
	}
	sess := s.streams.Get(ps.StreamKey)
	if sess == nil {
		writeError(w, http.StatusGone, "转封装会话已回收，请重新开始播放")
		return
	}
	data, err := sess.Playlist()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "播放列表还没生成好，稍后重试")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// handlePlaySegment 输出 HLS 分片（或 fMP4 的 init 段）。
//
// 路径故意做成「与 index.m3u8 同级」：m3u8 里是相对文件名，
// 客户端会拿播放列表 URL 作基准拼接。
//
// 为什么**不能**允许缓存：换一段窗口（seek）时分片文件名是一模一样的
//（都是从 seg_00000.m4s 开始），而内容完全不同。若允许浏览器缓存，
// seek 之后 hls.js 再要 seg_00000.m4s 就会拿回上一段的字节 ——
// 表现是「拖到 1:30 却从头开始放」，而且时间轴显示的是新位置（实测踩到）。
// 分片是“写一次、读一次”的临时文件，禁缓存没有任何代伷。
func (s *Server) handlePlaySegment(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	sess := s.streams.Get(ps.StreamKey)
	if sess == nil {
		writeError(w, http.StatusGone, "转封装会话已回收，请重新开始播放")
		return
	}
	name := r.PathValue("name")
	// 客户端拉分片 = 它消费到那个位置了：节流器据此决定要不要让 ffmpeg 歇一会儿。
	// 用「该分片的末尾」而不是开头：分片是整块下载的。
	if idx, ok := stream.SegmentIndex(name); ok {
		sess.MarkClientPosition(sess.Spec.StartSeconds + float64(idx+1)*float64(sess.SegmentSeconds()))
	}
	path, err := sess.SegmentPath(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "分片不存在")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "分片读不到")
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", segmentContentType(path))
	// 分片名在不同窗口里是复用的，绝不能缓存（见上面的说明）。
	w.Header().Set("Cache-Control", "no-store")
	// 传零值 modtime：不要 Last-Modified，也就不会再出现条件请求回 304
	// 而这种「304 = 用旧窗口的字节」正是我们要堵死的路径。
	http.ServeContent(w, r, filepath.Base(path), time.Time{}, f)
}

// handlePlaySubtitle 输出 WebVTT 字幕。
//
// 首次抽取可能需要读完整部片子（网络盘上要几十秒），因此这里**不等到底**：
// 最多等 subtitleWaitTimeout，还没好就回 202 让前端轮询；
// 后台任务照跑（进程内单飞 + 结果落盘缓存），下次播放直接命中。
func (s *Server) handlePlaySubtitle(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.playSessionForRequest(w, r)
	if !ok {
		return
	}
	// 扩展名决定交付形态：.vtt（浏览器原生轨道）/ .ass、.ssa（前端 libass 渲染）。
	name := r.PathValue("name")
	ext := filepath.Ext(name)
	suffix := strings.ToLower(strings.TrimPrefix(ext, "."))
	if suffix != "vtt" && suffix != "ass" && suffix != "ssa" {
		writeError(w, http.StatusBadRequest, "字幕格式不支持")
		return
	}
	idx, err := strconv.Atoi(strings.TrimSuffix(name, ext))
	if err != nil {
		writeError(w, http.StatusBadRequest, "字幕序号非法")
		return
	}
	path, err := s.subtitleFile(r.Context(), ps, idx, suffix)
	switch {
	case errors.Is(err, ErrSubtitlePending):
		w.Header().Set("Retry-After", "3")
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":  "preparing",
			"message": "字幕正在抽取（内嵌字幕要读一遍源文件），请稍后重试",
		})
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, "字幕提取失败："+err.Error())
		return
	}
	if suffix == "vtt" {
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	} else {
		// ASS/SSA：前端 libass 按文本读，字符集给清楚。
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

// subtitleFile 确保字幕的 VTT 版本已经抽好，返回文件路径。
//
// 三种结果：已就绪（返回路径）/ 还在抽（ErrSubtitlePending）/ 抽失败（返回错误）。
func (s *Server) subtitleFile(ctx context.Context, ps *playSession, streamIndex int, suffix string) (string, error) {
	dir := filepath.Join(s.cfg.StreamsDirPath(), "subs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, fmt.Sprintf("f%d-s%d.%s", ps.FileID, streamIndex, suffix))
	if st, err := os.Stat(out); err == nil && st.Size() > 0 {
		return out, nil
	}

	job := s.subs.start(out, func() error {
		return s.extractSubtitle(ps.FilePath, streamIndex, out, suffix)
	})
	select {
	case <-job.done:
		if job.err != nil {
			return "", job.err
		}
		return out, nil
	case <-time.After(subtitleWaitTimeout):
		return "", ErrSubtitlePending
	case <-ctx.Done():
		// 客户端不等了，但后台任务照跑（下次请求大概率直接命中）。
		return "", ErrSubtitlePending
	}
}

// extractSubtitle 用 ffmpeg 把一条内嵌字幕抽成 WebVTT。
//
// 用独立的 context（不受请求生命周期影响）：用户切走了、刷新了页面，
// 这次抽取仍然值得做完 —— 否则网络盘上那一分钟的读盘就白费了。
func (s *Server) extractSubtitle(path string, streamIndex int, out, suffix string) error {
	ctx, cancel := context.WithTimeout(context.Background(), subtitleExtractTimeout)
	defer cancel()

	ffmpeg := s.cfg.FFmpeg.Path
	if strings.TrimSpace(ffmpeg) == "" {
		ffmpeg = "ffmpeg"
	}
	tmp := out + ".tmp"
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", path,
		"-map", "0:" + strconv.Itoa(streamIndex),
	}
	// ASS/SSA 要**原样抽出**（-c:s copy）：定位、动画、样式全在原文件里，
	// 前端用 libass 解释它；转成 WebVTT 这些就没了。
	if suffix == "ass" || suffix == "ssa" {
		args = append(args, "-c:s", "copy", "-f", "ass")
	} else {
		args = append(args, "-f", "webvtt")
	}
	args = append(args, "-y", tmp)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	// 先写临时文件再改名：另一个请求可能正在读这个路径。
	return os.Rename(tmp, out)
}

// handleListPlaySessions 列出活跃播放会话（诊断 / 将来的监控页）。
func (s *Server) handleListPlaySessions(w http.ResponseWriter, r *http.Request) {
	authCtx := currentAuth(r)
	list := s.plays.list()
	out := make([]map[string]any, 0, len(list))
	for _, ps := range list {
		if !authCtx.User.IsAdmin && ps.UserID != authCtx.User.ID {
			continue
		}
		row := map[string]any{
			"playSessionId":   ps.ID,
			"itemId":          ps.ItemID,
			"title":           ps.Item.Title,
			"userId":          ps.UserID,
			"mode":            ps.Plan.Mode,
			"startSeconds":    ps.StartSeconds,
			"durationSeconds": ps.DurationSeconds,
			"ageSeconds":      int(time.Since(ps.CreatedAt).Seconds()),
			"idleSeconds":     int(time.Since(ps.lastSeen).Seconds()),
			"file":            filepath.Base(ps.FilePath),
		}
		if ps.StreamKey != "" {
			if sess := s.streams.Get(ps.StreamKey); sess != nil {
				row["stream"] = sess.Stat()
			}
		}
		out = append(out, row)
	}
	streams := []stream.SessionStat{}
	if s.streams != nil {
		streams = s.streams.Stats()
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "transcodeSessions": streams})
}

// handleStopTranscodeSession 强制终止某一路转封装/转码会话（管理员）。
//
// 播放会话那边已经有 stop（客户端主动告知「这段看完了」），但监控页还需要处理
// 「客户端已经不管了、进程还在烧 CPU」的漏网情况 —— 那就得直接按会话键揠。
func (s *Server) handleStopTranscodeSession(w http.ResponseWriter, r *http.Request) {
	if s.streams == nil {
		writeError(w, http.StatusServiceUnavailable, "转码服务未启用")
		return
	}
	key := r.PathValue("key")
	if !s.streams.Stop(key) {
		writeError(w, http.StatusNotFound, "转码会话不存在（可能已经被回收）")
		return
	}
	s.log.Info("终止转码会话", "key", key, "by", currentAuth(r).User.Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleContinueWatching 「继续观看」。
func (s *Server) handleContinueWatching(w http.ResponseWriter, r *http.Request) {
	authCtx := currentAuth(r)
	limit := atoiClamp(r.URL.Query().Get("limit"), 100)
	if limit == 0 {
		limit = 20
	}
	list, err := s.store.ListContinueWatching(r.Context(), authCtx.User.ID, limit)
	if err != nil {
		s.serverError(w, "读取继续观看列表失败", err)
		return
	}
	// 空列表要回 [] 而不是 null：前端不必为「空」单独写一支分支（jq 也不会炸）。
	if list == nil {
		list = []store.ContinueWatching{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// handleSetPlayed 标记已看 / 未看（支持批量）。
func (s *Server) handleSetPlayed(w http.ResponseWriter, r *http.Request) {
	authCtx := currentAuth(r)

	var body struct {
		ItemIDs []int64 `json:"itemIds"`
		Played  bool    `json:"played"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// 单条目也支持：/api/v1/items/{id}/played 用路径 id 覆盖请求体。
	if raw := r.PathValue("id"); raw != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "条目 id 非法")
			return
		}
		body.ItemIDs = []int64{id}
	}
	if len(body.ItemIDs) == 0 {
		writeError(w, http.StatusBadRequest, "缺少 itemIds")
		return
	}
	if len(body.ItemIDs) > 500 {
		writeError(w, http.StatusBadRequest, "一次最多标记 500 个条目")
		return
	}
	if err := s.store.SetPlayed(r.Context(), authCtx.User.ID, body.ItemIDs, body.Played); err != nil {
		s.serverError(w, "标记已看状态失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(body.ItemIDs), "played": body.Played})
}

// handleItemProgress 读取条目进度（详情页显示「看到哪了」）。
func (s *Server) handleItemProgress(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	authCtx := currentAuth(r)
	p, err := s.store.GetPlaybackProgress(r.Context(), authCtx.User.ID, item.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"progress": nil})
		return
	}
	if err != nil {
		s.serverError(w, "读取播放进度失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": p})
}

// handleItemPlaylist 返回播放器需要的「第几条文件 / 有哪些流可选」。
//
// 播放器要能让用户切音轨/字幕，所以这条接口把可选项列出来，
// 并与播放决策保持一致（同一个决策引擎，不另写一套判断）。
func (s *Server) handleItemPlaylist(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	files, err := s.store.ListPlayableFiles(r.Context(), item.ID)
	if err != nil {
		s.serverError(w, "读取条目文件失败", err)
		return
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		pf := playbackFile(f)
		out = append(out, map[string]any{
			"fileId":        f.ID,
			"container":     f.Container,
			"containerKind": pf.Kind(),
			"sizeBytes":     f.SizeBytes,
			"durationTicks": f.DurationTicks,
			"probeState":    f.ProbeState,
			"video":         pf.Video,
			"audio":         pf.Audio,
			"subtitles":     pf.Subtitle,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"itemId": item.ID, "files": out})
}

// ---------------------------------------------------------------- 工具

// playSessionForRequest 解析 /play/{sid} 并校验归属。
func (s *Server) playSessionForRequest(w http.ResponseWriter, r *http.Request) (*playSession, bool) {
	sid := strings.TrimSpace(r.PathValue("sid"))
	if sid == "" {
		writeError(w, http.StatusBadRequest, "缺少播放会话 id")
		return nil, false
	}
	ps := s.plays.get(sid)
	if ps == nil {
		writeError(w, http.StatusNotFound, "播放会话不存在或已过期，请重新开始播放")
		return nil, false
	}
	a := currentAuth(r)
	if a == nil || (ps.UserID != a.User.ID && !a.User.IsAdmin) {
		// 别人的播放会话等于别人的文件访问权，必须挡住。
		writeError(w, http.StatusForbidden, "这个播放会话不属于当前用户")
		return nil, false
	}
	return ps, true
}

// playbackFile 把库里的文件行转换成播放决策的输入。
//
// 三个流列表统一初始化成**空切片**而不是 nil：库里没有字幕时 jsonb 是 null，
// 反序列化后留在 nil 上，JSON 就会变成 `"subtitles": null` ——
// 前端一读 `.length` 就整页崩掉（实测踩到：条目页直接白屏）。
func playbackFile(f store.PlayableFile) playback.File {
	out := playback.File{
		ID:            f.ID,
		Path:          f.Path,
		SizeBytes:     f.SizeBytes,
		Container:     f.Container,
		DurationTicks: f.DurationTicks,
		Video:         []probe.VideoStream{},
		Audio:         []probe.AudioStream{},
		Subtitle:      []probe.SubtitleStream{},
	}
	if len(f.VideoStreams) > 0 {
		_ = json.Unmarshal(f.VideoStreams, &out.Video)
	}
	if len(f.AudioStreams) > 0 {
		_ = json.Unmarshal(f.AudioStreams, &out.Audio)
	}
	if len(f.SubtitleStreams) > 0 {
		_ = json.Unmarshal(f.SubtitleStreams, &out.Subtitle)
	}
	return out
}

// verifyMediaPath 校验路径确实落在所属媒体库的根目录下，返回绝对路径。
//
// 用 EvalSymlinks 之后再比：媒体目录里常有软链接指向外部磁盘，
// 只比字符串会在「/mnt/media/x → /srv/media/y」这种布局上误判。
func (s *Server) verifyMediaPath(ctx context.Context, itemID int64, path string) (string, error) {
	roots, err := s.store.LibraryRootsForItem(ctx, itemID)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", errors.New("媒体库没有配置根路径")
	}
	real := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		real = resolved
	}
	real = filepath.Clean(real)

	for _, root := range roots {
		r := root
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			r = resolved
		}
		r = filepath.Clean(r)
		if real == r || strings.HasPrefix(real, r+string(os.PathSeparator)) {
			return real, nil
		}
	}
	return "", fmt.Errorf("路径 %s 不在任何库根目录下", real)
}

func findPlayableFile(files []store.PlayableFile, id int64) (store.PlayableFile, bool) {
	for _, f := range files {
		if f.ID == id {
			return f, true
		}
	}
	return store.PlayableFile{}, false
}

// contentTypeFor 按容器族给出 Content-Type（浏览器靠它决定要不要播）。
func contentTypeFor(kind, path string) string {
	switch kind {
	case playback.ContainerMP4:
		return "video/mp4"
	case playback.ContainerWebM:
		return "video/webm"
	case playback.ContainerTS:
		return "video/mp2t"
	case playback.ContainerMKV:
		return "video/x-matroska"
	case playback.ContainerAVI:
		return "video/x-msvideo"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ts":
		return "video/mp2t"
	case ".mkv":
		return "video/x-matroska"
	}
	return "application/octet-stream"
}

// segmentContentType 按分片格式给出类型。
func segmentContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4s", ".mp4":
		return "video/iso.segment"
	case ".ts":
		return "video/mp2t"
	}
	return "application/octet-stream"
}

// etagFor 由「文件 id + 大小 + 修改时间」算 ETag。
//
// 不带用户与起播点：ETag 的语义是「这份字节内容有没有变」，
// 播放位置不同回的也是同一份 Range 数据。
func etagFor(ps *playSession) string {
	return fmt.Sprintf(`"%d-%x-%x"`, ps.FileID, ps.FileSize, ps.FileMTime.UnixNano())
}

func ticksToSeconds(ticks int64) float64 {
	if ticks <= 0 {
		return 0
	}
	return float64(ticks) / float64(playback.TicksPerSecond)
}

// clampStartTicks 把起播点夹进 [0, 时长)。
//
// 落在片尾之后（含正好等于时长）的续播位置一律回到 0：那种位置播不出东西，
// 用户看到的会是「点了播放没反应」（而 ffmpeg 其实已经因为读不到数据退出了）。
func clampStartTicks(start, durationTicks int64) int64 {
	if durationTicks <= 0 || start < durationTicks {
		return start
	}
	return 0
}

// clampSeekSeconds 把拖动位置夹进 [0, 时长 - 2s]（留一点余量，否则只剩一个包的窗口）。
func clampSeekSeconds(pos, durationSeconds float64) float64 {
	if pos < 0 {
		pos = 0
	}
	if durationSeconds <= 0 {
		return pos
	}
	maxPos := durationSeconds - 2
	if maxPos < 0 {
		maxPos = 0
	}
	if pos > maxPos {
		return maxPos
	}
	return pos
}

func intValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func intValueOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}
