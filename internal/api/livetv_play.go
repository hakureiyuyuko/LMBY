package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/livetv"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/stream"
)

// 本文件是直播电视（M5）的**播放**部分：
//
//	浏览器：POST /api/v1/livetv/channels/{id}/play → sid
//	        GET  /api/v1/live/{sid}/index.m3u8 与 /api/v1/live/{sid}/{name}
//	外部播放器：GET /s/{token}/playlist.m3u 与 /s/{token}/{name}（签名 token，无需登录）
//
// 与点播的关键差别：**同一频道只有一路 ffmpeg**。所有观众共享同一个
// stream.Manager 会话（key = live:ch<id>），所以「两个人看同一频道」不会拉两路源；
// 谁都不看了由 stream 包的空闲回收统一收口（见 Options.IdleSeconds，默认 45 秒）。

// liveStartTimeout 是等第一片分片的上限。
//
// 单播源实测可用频道起播 1~2 秒；给到 8 秒是留有余量。
// 为什么不是 15 秒：真实列表里**一定有死源**（重庆联通这 149 台里就不止一个），
// 那些地址在 RTSP 打开阶段直接挂住，等得越久用户越难判断是不是自己网络的问题。
// 超过 8 秒还没分片就判定失败，让上层把「拉流失败」如实回给用户。
//
// （ffmpeg 自己的 `-timeout` 是另一回事：它管已经建连后的读超时。）
const liveStartTimeout = 8 * time.Second

// liveSegmentSeconds / liveListSize 是直播的分片时长与滚动窗口大小。
//
// 1 秒分片是**实测逼出来的**（CETV1 单播源，容器内直测）：
//
//	hls_time=2 → 第一个分片要等 1859 ms（用户点开就在等这 1.9 秒）
//	hls_time=1 → 第一个分片只等 520 ms
//
// 因为分片时长就是「起播延迟」的下限，DoD 要求浏览器起播 < 2 秒，
// 2 秒分片在源站 GOP 较长时必顶到上限。代价是请求数翻倍（本地网络无所谓）
// 与客户端缓冲变短，所以窗口放到 10 片（约 10 秒）来补。
const (
	liveSegmentSeconds = 1
	liveListSize       = 10
)

// livePlayTTL 是「播放会话多久没动静就丢掉」。
//
// hls.js 每 2~6 秒拉一次播放列表；超过这个时间没来，说明标签页关了或断网了。
const livePlayTTL = 3 * time.Minute

// ---------------------------------------------------------------- 播放会话注册表

// livePlaySession 是一次直播播放（一个浏览器标签页 / 一个外部播放器）。
type livePlaySession struct {
	sid       string
	channelID int64
	streamKey string
	userID    int64
	createdAt time.Time
	lastSeen  time.Time
}

// livePlayRegistry 保存「sid → 频道」的映射。
//
// 为什么不落库：它只是「这次播放属于哪个频道」，进程重启后播放器反正要重新起播。
// 落库反而会留下永远清不完的僵尸会话（这正是 Jellyfin 那套 PlaySession 的常见运维痛点）。
type livePlayRegistry struct {
	mu sync.Mutex
	m  map[string]*livePlaySession
}

func newLivePlayRegistry() *livePlayRegistry {
	return &livePlayRegistry{m: map[string]*livePlaySession{}}
}

func (r *livePlayRegistry) add(p *livePlaySession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gcLocked(time.Now())
	r.m[p.sid] = p
}

// get 取出会话并刷新活跃时间（顺带清理太久没动静的）。
func (r *livePlayRegistry) get(sid string, now time.Time) *livePlaySession {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gcLocked(now)
	p, ok := r.m[sid]
	if !ok {
		return nil
	}
	p.lastSeen = now
	return p
}

func (r *livePlayRegistry) remove(sid string) *livePlaySession {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[sid]
	if !ok {
		return nil
	}
	delete(r.m, sid)
	return p
}

// viewers 返回某频道当前的播放会话数。
func (r *livePlayRegistry) viewers(channelID int64, now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gcLocked(now)
	n := 0
	for _, p := range r.m {
		if p.channelID == channelID {
			n++
		}
	}
	return n
}

func (r *livePlayRegistry) gcLocked(now time.Time) {
	for sid, p := range r.m {
		if now.Sub(p.lastSeen) > livePlayTTL {
			delete(r.m, sid)
		}
	}
}

func newLivePlayID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 随机数取不到就没有安全的会话 id 可发（正常环境不会走到这里）
		return ""
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- 会话配方

// liveStreamKey 是每个频道固定的 stream 会话 key：同一频道共用一路 ffmpeg。
func liveStreamKey(channelID int64) string { return fmt.Sprintf("live:ch%d", channelID) }

// liveInputArgs 按源地址的协议给出输入参数。
//
// rtsp 的 `-rtsp_transport tcp` 与 `-timeout`（微秒）不是拍脑袋：单播源实测
// UDP 会偶发丢包导致花屏，TCP 稳得多；不给 timeout 时源站抽风会让 ffmpeg 挂死。
func liveInputArgs(ch store.TVChannel) []string {
	var args []string
	switch livetv.Kind(ch.URL) {
	case "rtsp":
		args = append(args, "-rtsp_transport", "tcp", "-timeout", "15000000")
	case "rtmp":
		args = append(args, "-rtmp_live", "live")
	}
	if h := strings.TrimSpace(ch.Headers); h != "" {
		args = append(args, "-headers", h)
	}
	return args
}

// liveSpec 组装直播会话的规格：视频原样复制、音频转 AAC（IPTV 源多为 MP2，
// 浏览器放不了），滚动窗口。
func (s *Server) liveSpec(ch store.TVChannel) stream.Spec {
	return stream.Spec{
		Key:            liveStreamKey(ch.ID),
		Path:           ch.URL,
		Live:           true,
		SegmentSeconds: liveSegmentSeconds,
		LiveListSize:   liveListSize,
		Video: stream.VideoEncode{
			Copy: true,
			// 直播源参数与转码的硬件加速参数都在 -i 之前，走同一个字段
			InputArgs: liveInputArgs(ch),
		},
		Audio: stream.AudioEncode{Args: []string{"-c:a", "aac", "-b:a", "192k", "-ac", "2"}},
	}
}

// ensureLiveSession 取（必要时拉起）某频道的直播会话。
func (s *Server) ensureLiveSession(ctx context.Context, ch store.TVChannel) (*stream.Session, error) {
	return s.streams.Ready(ctx, s.liveSpec(ch), liveStartTimeout)
}

// ---------------------------------------------------------------- 起播 / 停止

// livePlayResponse 是起播接口的响应。
type livePlayResponse struct {
	SID         string `json:"sid"`
	ChannelID   int64  `json:"channelId"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	PlaylistURL string `json:"playlistUrl"`
	StartupMs   int64  `json:"startupMs"`
	SegmentSec  int    `json:"segmentSeconds"`
	ListSize    int    `json:"listSize"`
	Viewers     int    `json:"viewers"`
}

// handleStartLivePlay 起播一个频道（所有观众共用一路 ffmpeg）。
func (s *Server) handleStartLivePlay(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, liveStartTimeout+10*time.Second)
	defer cancel()

	ch, err := s.store.GetTVChannel(ctx, a.User.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "频道不存在")
		return
	}
	if err != nil {
		s.serverError(w, "读取频道失败", err)
		return
	}
	if ch.Disabled {
		writeError(w, http.StatusBadRequest, "频道已停用，先在直播页把它启用")
		return
	}
	if strings.TrimSpace(ch.URL) == "" {
		writeError(w, http.StatusBadRequest, "这条频道没有播放地址")
		return
	}

	started := time.Now()
	sess, err := s.ensureLiveSession(ctx, *ch)
	startupMs := time.Since(started).Milliseconds()
	if err != nil {
		s.log.Warn("直播起播失败", "channel", ch.ID, "name", ch.Name, "err", err)
		writeError(w, http.StatusBadGateway, "拉流失败："+err.Error())
		return
	}

	sid := newLivePlayID()
	if sid == "" {
		s.serverError(w, "生成播放会话失败", errors.New("随机数不可用"))
		return
	}
	s.livePlays.add(&livePlaySession{
		sid:       sid,
		channelID: ch.ID,
		streamKey: liveStreamKey(ch.ID),
		userID:    a.User.ID,
		createdAt: time.Now(),
		lastSeen:  time.Now(),
	})
	s.log.Info("直播起播", "channel", ch.ID, "name", ch.Name, "sid", sid,
		"startup_ms", startupMs, "viewers", s.livePlays.viewers(ch.ID, time.Now()),
		"username", usernameOf(r))

	writeJSON(w, http.StatusOK, livePlayResponse{
		SID:         sid,
		ChannelID:   ch.ID,
		Name:        ch.Name,
		Kind:        livetv.KindLabel(ch.URL),
		PlaylistURL: "/api/v1/live/" + sid + "/index.m3u8",
		StartupMs:   startupMs,
		SegmentSec:  sess.SegmentSeconds(),
		ListSize:    liveListSize,
		Viewers:     s.livePlays.viewers(ch.ID, time.Now()),
	})
}

// handleStopLivePlay 主动停止一次播放（浏览器切台/关页面时会调）。
func (s *Server) handleStopLivePlay(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	ps := s.livePlays.remove(sid)
	if ps == nil {
		writeError(w, http.StatusNotFound, "播放会话不存在")
		return
	}
	// 只有这个频道再没有别的观众时才真掐掉 ffmpeg：
	// 同一频道的多路观众共用一路流，不能因为一个标签页关了就把别人踢下线。
	viewers := s.livePlays.viewers(ps.channelID, time.Now())
	if viewers == 0 {
		s.streams.Stop(ps.streamKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "viewers": viewers})
}

// ---------------------------------------------------------------- 播放列表与分片

// liveSessionForRequest 按 sid 取直播会话；stream 会话被回收了就就地重拉。
//
// 为什么要「就地重拉」：hls.js 在窗口滚完、或标签页切回来时会重新拉播放列表，
// 那时 ffmpeg 可能已经因为空闲被回收了。直接回 410 只会让用户看到「播放失败」，
// 而这里重新拉一路，用户那侧只是卡一下。
func (s *Server) liveSessionForRequest(w http.ResponseWriter, r *http.Request) (*livePlaySession, *store.TVChannel, *stream.Session, bool) {
	sid := r.PathValue("sid")
	ps := s.livePlays.get(sid, time.Now())
	if ps == nil {
		writeError(w, http.StatusNotFound, "播放会话不存在或已过期，请重新起播")
		return nil, nil, nil, false
	}
	ctx, cancel := contextWithTimeout(r, liveStartTimeout+5*time.Second)
	defer cancel()

	ch, err := s.store.GetTVChannel(ctx, ps.userID, ps.channelID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "频道已被删除")
		return nil, nil, nil, false
	}
	if err != nil {
		s.serverError(w, "读取频道失败", err)
		return nil, nil, nil, false
	}

	sess := s.streams.Get(ps.streamKey)
	if sess == nil || (!sess.Ready() && sess.Exited()) {
		sess, err = s.ensureLiveSession(ctx, *ch)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "拉流失败："+err.Error())
			return nil, nil, nil, false
		}
	}
	return ps, ch, sess, true
}

// handleLivePlaylist 输出直播的滚动播放列表。
func (s *Server) handleLivePlaylist(w http.ResponseWriter, r *http.Request) {
	_, _, sess, ok := s.liveSessionForRequest(w, r)
	if !ok {
		return
	}
	b, err := sess.Playlist()
	if err != nil || len(b) == 0 {
		// 分片还没出来：让客户端过一会儿再来（hls.js 会自己重试）
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusServiceUnavailable, "直播分片还没准备好")
		return
	}
	// 直播播放列表**绝不能缓存**：它每次都在滚动，缓存住等于客户端永远停在过去。
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// handleLivePlaySegment 输出直播分片。
func (s *Server) handleLivePlaySegment(w http.ResponseWriter, r *http.Request) {
	_, _, sess, ok := s.liveSessionForRequest(w, r)
	if !ok {
		return
	}
	s.serveLiveSegment(w, r, sess)
}

// serveLiveSegment 校验分片名并把它发出去。
func (s *Server) serveLiveSegment(w http.ResponseWriter, r *http.Request, sess *stream.Session) {
	path, err := sess.SegmentPath(r.PathValue("name"))
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
	// 分片名在滚动窗口里会复用（seg_00007.ts 过一会儿是另一段内容），绝不能缓存。
	w.Header().Set("Cache-Control", "no-store")
	// 零 modtime：不发 Last-Modified，避免条件请求拿到 304 之后客户端拿旧字节。
	http.ServeContent(w, r, filepath.Base(path), time.Time{}, f)
}

// ---------------------------------------------------------------- 会话监控

// handleListLiveSessions 列出正在跑的直播会话（监控与验收用）。
func (s *Server) handleListLiveSessions(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	out := make([]map[string]any, 0, 4)
	for _, st := range s.streams.Stats() {
		if !strings.HasPrefix(st.Key, "live:") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(st.Key, "live:ch"), 10, 64)
		if err != nil {
			continue
		}
		name := ""
		if ch, err := s.store.GetTVChannel(ctx, 0, id); err == nil {
			name = ch.Name
		}
		out = append(out, liveSessionView(st, name, id, s.livePlays.viewers(id, now)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "count": len(out)})
}

func liveSessionView(st stream.SessionStat, name string, channelID int64, viewers int) map[string]any {
	return map[string]any{
		"key":       st.Key,
		"channelId": channelID,
		"name":      name,
		"state":     st.State,
		"viewers":   viewers,
		"uptimeSec": st.UptimeSec,
		"idleSec":   st.IdleSec,
		"segments":  st.Segments,
		"fps":       st.FPS,
		"speed":     st.Speed,
		"bitrate":   st.Bitrate,
		"error":     st.Error,
	}
}

// ---------------------------------------------------------------- 外链（签名 token）

// hasSigner 报告能不能给外链 token 签名（没装密钥时不能）。
func (s *Server) hasSigner() bool { return s.liveSigner != nil }

// handleShareLiveChannel 生成一个外部播放器可用的链接（无需登录）。
func (s *Server) handleShareLiveChannel(w http.ResponseWriter, r *http.Request) {
	if !s.hasSigner() {
		writeError(w, http.StatusServiceUnavailable, "服务未装载签名密钥，无法生成外链")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Hours *int `json:"hours"`
	}
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	hours := 24
	if req.Hours != nil {
		hours = *req.Hours
	}
	if hours < 1 || hours > 168 {
		writeError(w, http.StatusBadRequest, "有效期只能是 1~168 小时")
		return
	}
	a := currentAuth(r)
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	ch, err := s.store.GetTVChannel(ctx, a.User.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "频道不存在")
		return
	}
	if err != nil {
		s.serverError(w, "读取频道失败", err)
		return
	}

	exp := time.Now().Add(time.Duration(hours) * time.Hour)
	token := livetv.SignPlayToken(s.liveSigner, ch.ID, exp)
	path := "/s/" + token + "/playlist.m3u"

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	s.log.Info("生成直播外链", "channel", ch.ID, "name", ch.Name,
		"hours", hours, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       path,
		"url":        scheme + "://" + r.Host + path,
		"expiresAt":  exp.Format(time.RFC3339),
		"channelId":  ch.ID,
		"name":       ch.Name,
		"segmentSec": liveSegmentSeconds})
}

// sharedChannel 校验外链 token 并保证直播会话在跑。
func (s *Server) sharedChannel(w http.ResponseWriter, r *http.Request) (*store.TVChannel, *stream.Session, bool) {
	if !s.hasSigner() {
		writeError(w, http.StatusServiceUnavailable, "服务未装载签名密钥")
		return nil, nil, false
	}
	token := r.PathValue("token")
	channelID, err := livetv.ParsePlayToken(s.liveSigner, token, time.Now())
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, livetv.ErrTokenExpired) {
			status = http.StatusGone
		}
		writeError(w, status, err.Error())
		return nil, nil, false
	}
	ctx, cancel := contextWithTimeout(r, liveStartTimeout+5*time.Second)
	defer cancel()

	ch, err := s.store.GetTVChannel(ctx, 0, channelID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "频道不存在")
		return nil, nil, false
	}
	if err != nil {
		s.serverError(w, "读取频道失败", err)
		return nil, nil, false
	}
	if ch.Disabled {
		writeError(w, http.StatusForbidden, "频道已停用")
		return nil, nil, false
	}
	sess, err := s.ensureLiveSession(ctx, *ch)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "拉流失败："+err.Error())
		return nil, nil, false
	}
	return ch, sess, true
}

// handleSharedLivePlaylist 外链播放列表（外部播放器直接拉这个地址）。
func (s *Server) handleSharedLivePlaylist(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.sharedChannel(w, r)
	if !ok {
		return
	}
	b, err := sess.Playlist()
	if err != nil || len(b) == 0 {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusServiceUnavailable, "直播分片还没准备好")
		return
	}
	// 播放列表里的分片是相对文件名（seg_00001.ts），外部播放器会以
	// `/s/<token>/` 为基准去拼 —— 所以分片路由必须在同一层（/s/{token}/{name}）。
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// handleSharedLiveSegment 外链分片。
func (s *Server) handleSharedLiveSegment(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.sharedChannel(w, r)
	if !ok {
		return
	}
	s.serveLiveSegment(w, r, sess)
}
