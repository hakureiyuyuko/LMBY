package encoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultCacheTTL 是能力缓存的有效期。
//
// 为什么要缓存：探测要真跑十几次 ffmpeg（1~3 秒），而「这台机器能不能硬解 HEVC」
// 在一天之内不会变。但也不能永久缓存 —— 用户装好驱动、换机器、升级 ffmpeg 之后
// 应该自动生效，所以给一个不算长的有效期，并且提供手动刷新（界面/接口）。
const DefaultCacheTTL = 24 * time.Hour

// StoreOptions 建能力表仓库的参数。
type StoreOptions struct {
	FFmpeg string
	// CachePath 为磁盘缓存位置（空 = 不落盘，测试用）。
	CachePath string
	// WorkDir 探测时放小样文件的目录（空 = 系统临时目录）。
	WorkDir string
	// DeviceOverride 显式指定的硬件设备节点（空 = 自动从 /dev/dri 里挑）。
	DeviceOverride string
	// Prefer 强制指定后端；为空按默认顺序（硬件优先、最后兜底软编）。
	Prefer Kind
	Log    *slog.Logger
}

// Store 持有当前机器的能力表，并负责「首次懒探测 + 落盘缓存 + 手动刷新」。
//
// 并发下只允许一次探测（single-flight）：界面刷新按钮连点、或者多个请求
// 同时开播时，不应该真的跑十几次 ffmpeg 把机器打满。
type Store struct {
	ffmpeg  string
	path    string
	ttl     time.Duration
	log     *slog.Logger
	workDir string
	device  string
	prefer  Kind

	mu    sync.Mutex
	caps  *Capabilities
	busy  chan struct{} // 探测进行中时非 nil，其它调用者等它关闭
	force bool          // 手动刷新时置上：下一次 Get 跳过内存与磁盘缓存，真探一遍
}

// NewStore 建一个能力表仓库。
func NewStore(opts StoreOptions) *Store {
	return &Store{
		ffmpeg:  opts.FFmpeg,
		path:    opts.CachePath,
		ttl:     DefaultCacheTTL,
		log:     opts.Log,
		workDir: opts.WorkDir,
		device:  opts.DeviceOverride,
		prefer:  opts.Prefer,
	}
}

// Preferred 给出运行时该用的后端：先看配置指定，再按默认顺序。
//
// 配置里指定的后端在本机不可用时**回退到自动选择并告警**，而不是直接报错：
// 配置文件往往是在另一台机器上写好后拷过来的，因为一个后端不可用就打不开
// 播放，比“先跑起来但画质不如预期”糟糕得多。
func (s *Store) Preferred(caps *Capabilities) Backend {
	if s.prefer != "" {
		if s.prefer == KindSoftware {
			return caps.Software
		}
		for _, b := range caps.Backends {
			if b.Kind == s.prefer && b.Usable() {
				return b
			}
		}
		if s.log != nil {
			s.log.Warn("配置指定的转码后端在本机不可用，回退自动选择", "want", s.prefer, "fallback", caps.Best().Kind)
		}
	}
	return caps.Best()
}

// Get 返回能力表：内存 → 磁盘缓存 → 现场探测。
func (s *Store) Get(ctx context.Context) (*Capabilities, error) {
	s.mu.Lock()
	force := s.force
	s.force = false
	if !force && s.caps != nil && time.Since(s.caps.ProbedAt) < s.ttl {
		c := s.caps
		s.mu.Unlock()
		return c, nil
	}
	if s.busy != nil {
		wait := s.busy
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
		c := s.caps
		s.mu.Unlock()
		if c != nil {
			return c, nil
		}
		return nil, errors.New("能力探测失败")
	}
	// 内存里没有（或过期了、或手动要求刷新）：先看磁盘缓存够不够新
	if !force && s.caps == nil {
		if cached := s.loadFromDisk(); cached != nil && time.Since(cached.ProbedAt) < s.ttl {
			s.caps = cached
			c := s.caps
			s.mu.Unlock()
			return c, nil
		}
	}
	ch := make(chan struct{})
	s.busy = ch
	s.mu.Unlock()

	caps, err := s.probe(ctx)

	s.mu.Lock()
	s.busy = nil
	if err == nil {
		s.caps = caps
	}
	prev := s.caps
	s.mu.Unlock()
	close(ch)

	if err != nil {
		// 探测失败但手里有旧结果：先用旧的，别让播放直接挂掉
		if prev != nil {
			if s.log != nil {
				s.log.Warn("能力探测失败，沿用上一次结果", "err", err)
			}
			return prev, nil
		}
		return nil, err
	}
	return caps, nil
}

// Refresh 强制重新探测（界面上的「重新探测」按钮 / 装完驱动之后）。
//
// 必须跳过磁盘缓存：装驱动、换机器这种事的频率远低于 24 小时的有效期，
// 用户点了刷新却拿到旧结论（而且界面显示的时间还没变）会直接怀疑功能坏了。
func (s *Store) Refresh(ctx context.Context) (*Capabilities, error) {
	s.mu.Lock()
	s.caps = nil
	s.force = true
	s.mu.Unlock()
	return s.Get(ctx)
}

// GetCached 只读内存里的结果（没有就是 nil），不触发探测 —— 给热路径用。
func (s *Store) GetCached() *Capabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps
}

func (s *Store) probe(ctx context.Context) (*Capabilities, error) {
	start := time.Now()
	caps, err := Probe(ctx, s.ffmpeg, ProbeOptions{WorkDir: s.workDir, DeviceOverride: s.device})
	if err != nil {
		return nil, err
	}
	if s.log != nil {
		best := caps.Best()
		s.log.Info("编码能力探测完成",
			"耗时ms", caps.ElapsedMS,
			"首选后端", best.Name,
			"软件编码", caps.Software.Encode,
			"硬件后端", summarize(caps.Backends),
			"警告", len(caps.Warnings))
	}
	s.saveToDisk(caps)
	_ = start
	return caps, nil
}

func summarize(bs []Backend) map[string]string {
	out := map[string]string{}
	for _, b := range bs {
		switch {
		case b.Usable():
			out[string(b.Kind)] = fmt.Sprintf("可用(编%v 解%v 质量%v)", b.Encode, b.Decode, b.Quality)
		case len(b.Encode) > 0 || len(b.Notes) > 0:
			out[string(b.Kind)] = "不可用"
		}
	}
	return out
}

// Save/Load 用 gob? 不 —— 用 JSON：能力表是要给人看、给界面显示的，
// 落盘格式跟接口返回保持一致，排查问题时 cat 一下就够了。

func (s *Store) saveToDisk(caps *Capabilities) {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	buf, err := json.MarshalIndent(caps, "", "  ")
	if err != nil {
		return
	}
	// 先写临时文件再 rename：避免界面正好读到写了一半的 JSON
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

func (s *Store) loadFromDisk() *Capabilities {
	if s.path == "" {
		return nil
	}
	buf, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var caps Capabilities
	if err := json.Unmarshal(buf, &caps); err != nil {
		return nil
	}
	if caps.FFmpeg != s.ffmpeg {
		return nil // 换过 ffmpeg 路径，旧结果不作数
	}
	return &caps
}
