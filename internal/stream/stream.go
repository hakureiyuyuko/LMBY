// Package stream 负责「转封装」：把原文件里的视频流原样复制、换一个浏览器能播的
// 容器（HLS 分片），必要时只把音频转成 AAC。
//
// 与 Jellyfin 的 DirectStream 对应：**视频不重新编码**，所以几乎不占 CPU
// （实测 1080p 一路约 3~5% 单核）。真正需要重编码的部分是 M4。
//
// 关键约束：`-c copy` 比实时快几十倍（一次拖到片尾会把整部电影拷进磁盘），
// 所以每次只预生成一小段（窗口，见 Options.WindowSeconds），窗口用完再由
// 播放端按当前位置续下一段。这也是 M4「节流与回收」的雏形。
package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrTooMany 表示并发转封装会话已达上限。
var ErrTooMany = errors.New("同时播放的路数已达上限，请稍后再试")

// ErrStartTimeout 表示等第一个分片超时（源文件读不动或参数不对）。
var ErrStartTimeout = errors.New("等待转封装起步超时")

// Options 是转封装会话的运行参数（由 config.Playback 派生）。
type Options struct {
	// FFmpeg 是 ffmpeg 可执行文件路径。
	FFmpeg string
	// Root 是分片目录的父目录（每次播放一个子目录）。
	Root string
	// SegmentSeconds 是单个分片时长（秒）。
	SegmentSeconds int
	// WindowSeconds 是一次预生成多远的媒体内容（秒）。
	WindowSeconds int
	// MaxSessions 是并发会话上限。
	MaxSessions int
	// IdleSeconds 是无客户端访问后回收会话的秒数。
	IdleSeconds int
}

// Normalize 修正非法取值（配置已经在启动时校验过一次，这里是防御性的第二道）。
func (o Options) Normalize() Options {
	if strings.TrimSpace(o.FFmpeg) == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.SegmentSeconds < 1 || o.SegmentSeconds > 30 {
		o.SegmentSeconds = 4
	}
	if o.WindowSeconds < 30 || o.WindowSeconds > 7200 {
		o.WindowSeconds = 300
	}
	if o.MaxSessions < 1 || o.MaxSessions > 64 {
		o.MaxSessions = 4
	}
	if o.IdleSeconds < 10 || o.IdleSeconds > 3600 {
		o.IdleSeconds = 45
	}
	return o
}

// Spec 描述一次转封装要生成什么。
type Spec struct {
	// Key 是会话标识（同一个 Key、同一个起播点可以复用同一个 ffmpeg）。
	Key string
	// Path 是源文件绝对路径。
	Path string

	// VideoIndex 是视频流的**绝对**序号（ffprobe 的 index），-1 表示不要视频。
	VideoIndex int
	// VideoCodec 用于决定要不要打包 hvc1 标签（hevc 在 fMP4 里必须打）。
	VideoCodec string

	// AudioIndex 是音频流的绝对序号，-1 表示没有音频。
	AudioIndex int

	// Video 与 Audio 是「这两段怎么送」，由上层（api/encoder）填好：
	// Video.Copy / Audio.Copy 为真时是原样复制，否则用它们给的参数转码。
	// 零值等价于“视频复制”，所以老的调用点（直出/转封装）不用改。
	Video VideoEncode
	Audio AudioEncode

	// SegmentFormat 取 fmp4 / ts。
	SegmentFormat string

	// StartSeconds 是起播位置（秒）。
	StartSeconds float64
	// WindowSeconds 是这一段生成多长（0 表示用 Options 的默认值）。
	WindowSeconds int
}

// Session 是一个转封装会话：一个 ffmpeg 进程 + 一个分片目录。
type Session struct {
	Key  string
	Spec Spec

	dir     string
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	started time.Time
	last    atomic.Int64
	ready   atomic.Bool

	exitOnce sync.Once
	exited   chan struct{}
	exitMu   sync.Mutex
	exitErr  error

	logs *ringLog
}

// Manager 管理全部转封装会话。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	opts     Options
	log      *slog.Logger
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewManager 创建管理器并清理上次运行留下的分片目录。
func NewManager(opts Options, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	opts = opts.Normalize()
	m := &Manager{
		sessions: map[string]*Session{},
		opts:     opts,
		log:      log,
		stopCh:   make(chan struct{}),
	}
	if opts.Root != "" {
		// 进程重启后旧分片没有任何意义（会话表是内存里的），直接清掉，
		// 否则一次异常退出会永久占着磁盘。
		_ = os.RemoveAll(opts.Root)
		_ = os.MkdirAll(opts.Root, 0o755)
	}
	return m
}

// Start 启动后台回收协程。
func (m *Manager) Start() { go m.reapLoop() }

// StopAll 停止全部会话（进程退出时调用，避免留下孤儿 ffmpeg）。
func (m *Manager) StopAll() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.mu.Lock()
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range list {
		s.close()
	}
}

func (m *Manager) reapLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			m.reap()
		}
	}
}

// reap 回收「出错退出」与「太久没人看」的会话。
//
// 注意**不能**按「进程已退出」回收：窗口生成完（源文件比窗口短，或用户只看完了这一小段）
// ffmpeg 会正常退出，但分片还在服务中 —— 那时把会话收掉，播放器会在几秒后突然断流。
// 正常退出的会话一律等空闲超时。
func (m *Manager) reap() {
	var victims []*Session
	now := time.Now()
	m.mu.Lock()
	for k, s := range m.sessions {
		failed := s.exitedNow() && (s.err() != nil || s.segmentCount() == 0)
		idle := now.Sub(s.LastActive()) > time.Duration(m.opts.IdleSeconds)*time.Second
		if failed || idle {
			delete(m.sessions, k)
			victims = append(victims, s)
		}
	}
	m.mu.Unlock()
	for _, s := range victims {
		m.log.Info("回收转封装会话", "key", s.Key, "idle_s", int(now.Sub(s.LastActive()).Seconds()))
		s.close()
	}
}

// Stats 返回当前会话概况（供管理接口与日志）。
func (m *Manager) Stats() []SessionStat {
	m.mu.Lock()
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()

	out := make([]SessionStat, 0, len(list))
	for _, s := range list {
		out = append(out, s.Stat())
	}
	return out
}

// Stop 停止某个会话（播放端主动告知「这段看完了」）。
func (m *Manager) Stop(key string) bool {
	m.mu.Lock()
	s, ok := m.sessions[key]
	if ok {
		delete(m.sessions, key)
	}
	m.mu.Unlock()
	if ok {
		s.close()
	}
	return ok
}

// Get 返回已存在的会话。
func (m *Manager) Get(key string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[key]
}

// Ready 取得（必要时启动）会话，并等到第一个分片可用。
//
// 等第一个分片而不是立刻返回：播放器第一次拉 m3u8 时若文件还不存在，
// hls.js 会直接报致命错误，用户看到的是「播放失败」而不是「转圈两秒」。
func (m *Manager) Ready(ctx context.Context, spec Spec, timeout time.Duration) (*Session, error) {
	s, err := m.acquire(spec)
	if err != nil {
		return nil, err
	}
	if s.Ready() {
		return s, nil
	}
	if err := s.waitReady(ctx, timeout); err != nil {
		m.drop(spec.Key, s)
		s.close()
		return nil, fmt.Errorf("%w：%w%s", ErrStartTimeout, err, s.logSuffix())
	}
	return s, nil
}

// acquire 找到可复用的会话，或新建一个。
func (m *Manager) acquire(spec Spec) (*Session, error) {
	m.mu.Lock()
	// 已退出的会话只要有分片就还能用（窗口生成完了而已），照旧复用。
	if s, ok := m.sessions[spec.Key]; ok && (!s.exitedNow() || s.hasSegments()) {
		s.touch()
		m.mu.Unlock()
		return s, nil
	}

	var victim *Session
	// 同键的旧会话若已经退出（窗口生成完 / 出错），不能直接覆盖写 map：
	// 那样它的分片目录再也没人会清（reap 只遍历 map 里还在的会话）。
	if old, ok := m.sessions[spec.Key]; ok {
		delete(m.sessions, spec.Key)
		victim = old
	}
	if len(m.sessions) >= m.opts.MaxSessions {
		// 淘汰最久没人看的；全都在活跃观看就拒绝，而不是把别人的流掐掉。
		var oldest *Session
		for _, s := range m.sessions {
			if oldest == nil || s.LastActive().Before(oldest.LastActive()) {
				oldest = s
			}
		}
		if oldest == nil || time.Since(oldest.LastActive()) < 15*time.Second {
			m.mu.Unlock()
			return nil, ErrTooMany
		}
		delete(m.sessions, oldest.Key)
		victim = oldest
	}

	s := newSession(m.opts, spec)
	m.sessions[spec.Key] = s
	m.mu.Unlock()

	if victim != nil {
		m.log.Info("替换旧的转封装会话", "key", victim.Key)
		victim.close()
	}

	if err := s.start(m.opts); err != nil {
		m.drop(spec.Key, s)
		return nil, err
	}
	m.log.Info("启动转封装", "key", spec.Key, "file", spec.Path,
		"start_s", spec.StartSeconds, "format", spec.SegmentFormat)
	return s, nil
}

func (m *Manager) drop(key string, s *Session) {
	m.mu.Lock()
	if cur, ok := m.sessions[key]; ok && cur == s {
		delete(m.sessions, key)
	}
	m.mu.Unlock()
}

// ---------------------------------------------------------------- 会话

// SessionStat 是对外暴露的会话状态。
type SessionStat struct {
	Key       string  `json:"key"`
	State     string  `json:"state"`
	StartSec  float64 `json:"startSeconds"`
	UptimeSec int     `json:"uptimeSec"`
	IdleSec   int     `json:"idleSec"`
	Segments  int     `json:"segments"`
	Error     string  `json:"error,omitempty"`
	Log       string  `json:"log,omitempty"`
}

func newSession(opts Options, spec Spec) *Session {
	window := spec.WindowSeconds
	if window <= 0 {
		window = opts.WindowSeconds
	}
	spec.WindowSeconds = window
	return &Session{
		Key:    spec.Key,
		Spec:   spec,
		dir:    filepath.Join(opts.Root, safeDirName(spec.Key)),
		exited: make(chan struct{}),
		logs:   newRingLog(120),
	}
}

// safeDirName 把会话键变成安全的目录名（键里有 id 与冒号）。
func safeDirName(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String() + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (s *Session) touch() { s.last.Store(time.Now().UnixNano()) }

// LastActive 返回最后一次被访问的时间。
func (s *Session) LastActive() time.Time {
	if v := s.last.Load(); v != 0 {
		return time.Unix(0, v)
	}
	return s.started
}

// Dir 返回分片目录。
func (s *Session) Dir() string { return s.dir }

// Ready 表示是否已经产出可播放的分片。
func (s *Session) Ready() bool { return s.ready.Load() || s.hasSegments() }

// started 时间（Stat 用）。
func (s *Session) start(opts Options) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("创建分片目录失败: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	args := hlsArgs(opts, s.Spec, s.dir)
	cmd := exec.CommandContext(ctx, opts.FFmpeg, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = s.logs
	s.cmd = cmd
	s.started = time.Now()
	s.touch()

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("启动 ffmpeg 失败（%s）: %w", opts.FFmpeg, err)
	}
	go func() {
		err := cmd.Wait()
		s.exitMu.Lock()
		s.exitErr = err
		s.exitMu.Unlock()
		s.exitOnce.Do(func() { close(s.exited) })
	}()
	return nil
}

func (s *Session) exitedNow() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

func (s *Session) err() error {
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitErr
}

func (s *Session) logSuffix() string {
	if l := s.logs.Tail(6); l != "" {
		return "\n" + l
	}
	return ""
}

func (s *Session) playlistPath() string { return filepath.Join(s.dir, "index.m3u8") }

// segmentCount 统计播放列表里已有的分片数。
func (s *Session) segmentCount() int {
	b, err := os.ReadFile(s.playlistPath())
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}

// hasSegments 报告播放列表里是否已经有分片。
func (s *Session) hasSegments() bool { return s.segmentCount() > 0 }

// waitReady 等待出现第一个分片。
//
// 一个容易踩的竞态：短文件（几十秒）的 ffmpeg 会在几百毫秒内跑完并退出，
// 而 select 可能先收到 exited 再去看分片 —— 于是“进程退出”被当成启动失败，
// 但分片其实已经写好了（实测踩到：10 秒的测试片必定被判失败）。
// 所以收到 exited 时必须回查一次分片。
func (s *Session) waitReady(ctx context.Context, timeout time.Duration) error {
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if s.hasSegments() {
			s.ready.Store(true)
			s.touch()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.exited:
			if s.hasSegments() {
				s.ready.Store(true)
				return nil
			}
			if err := s.err(); err != nil {
				return fmt.Errorf("ffmpeg 已退出：%w", err)
			}
			return errors.New("ffmpeg 已退出，但没有产出任何分片（源文件可能没有视频流或太短）")
		case <-deadline.C:
			return fmt.Errorf("等待第一个分片超过 %s", timeout)
		case <-tick.C:
		}
	}
}

// Playlist 读取播放列表。
//
// ffmpeg 是按「写临时文件再改名」的方式更新 m3u8 的，但读到的仍可能是
// 没写完的中间状态，所以重试几次并优先返回「以换行结尾」的那一份。
func (s *Session) Playlist() ([]byte, error) {
	var last []byte
	for i := 0; i < 4; i++ {
		b, err := os.ReadFile(s.playlistPath())
		if err == nil {
			last = b
			if bytes.HasSuffix(b, []byte("\n")) {
				s.touch()
				return b, nil
			}
		} else if last != nil {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if last == nil {
		return nil, os.ErrNotExist
	}
	s.touch()
	return last, nil
}

var segNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]*$`)

// SegmentPath 校验并返回分片文件路径（拒绝一切路径穿越）。
func (s *Session) SegmentPath(name string) (string, error) {
	if !segNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("非法分片名")
	}
	p := filepath.Join(s.dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	s.touch()
	return p, nil
}

// close 停止进程并清掉分片目录。
func (s *Session) close() {
	if s.cancel != nil {
		s.cancel()
	}
	select {
	case <-s.exited:
	case <-time.After(3 * time.Second):
		// 常规情况下 SIGTERM 就够了；窗口结束的 ffmpeg 会正常退出。
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		select {
		case <-s.exited:
		case <-time.After(2 * time.Second):
		}
	}
	_ = os.RemoveAll(s.dir)
}

// Stat 返回会话状态。
func (s *Session) Stat() SessionStat {
	st := SessionStat{
		Key:       s.Key,
		StartSec:  s.Spec.StartSeconds,
		UptimeSec: int(time.Since(s.started).Seconds()),
		IdleSec:   int(time.Since(s.LastActive()).Seconds()),
		Segments:  s.segmentCount(),
	}
	switch {
	case s.ready.Load() && s.exitedNow() && s.err() == nil:
		// 窗口已经生成完（ffmpeg 正常退出），但分片还在这里供播放器读。
		st.State = "finished"
	case s.ready.Load():
		st.State = "ready"
	case s.exitedNow():
		st.State = "error"
		st.Error = errString(s.err())
	default:
		st.State = "starting"
	}
	if st.State == "error" {
		st.Log = s.logs.Tail(8)
	}
	return st
}

func errString(err error) string {
	if err == nil {
		return "被中断"
	}
	return err.Error()
}
