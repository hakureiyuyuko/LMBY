// Package config 负责加载与校验 LMBY 的配置。
//
// 优先级：默认值 < 配置文件 < 环境变量（LMBY_*）。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config 是 LMBY 的全部可配置项。
type Config struct {
	// Listen 是 HTTP 监听地址，例如 ":8099" 或 "127.0.0.1:8099"。
	Listen string `toml:"listen"`

	// DataDir 存放运行期数据（缓存、头像、临时分片等）。数据库内容不在这里。
	DataDir string `toml:"data_dir"`

	// LogLevel 取 debug / info / warn / error。
	LogLevel string `toml:"log_level"`

	// BaseURL 是可选的对外访问地址，用于生成分享链接等。
	BaseURL string `toml:"base_url"`

	// SecureCookies 为 true 时给会话 Cookie 加 Secure 标记。
	// 只有走 HTTPS（通常是反向代理）时才应打开，否则浏览器不会回传 Cookie。
	SecureCookies bool `toml:"secure_cookies"`

	// SessionTTLHours 是会话有效期（小时），默认 720（30 天）。
	SessionTTLHours int `toml:"session_ttl_hours"`

	Database DatabaseConfig `toml:"database"`
	// Scan 是扫描器的小旋钮（M6 补：之前写在代码常量里，换不出手）。
	Scan     ScanConfig     `toml:"scan"`
	FFmpeg   FFmpegConfig   `toml:"ffmpeg"`
	Tasks    TasksConfig    `toml:"tasks"`
	TMDB     TMDBConfig     `toml:"tmdb"`
	Images   ImagesConfig   `toml:"images"`
	Playback PlaybackConfig `toml:"playback"`
	LiveTV   LiveTVConfig   `toml:"livetv"`
}

// PlaybackConfig 是播放（转封装 / 会话）相关配置。
//
// 这些参数都直接影响磁盘与 CPU，所以做成可配置：家里的小主机与
// 独立服务器能承受的并发完全不是一回事。
type PlaybackConfig struct {
	// StreamsDir 是 HLS 分片目录，默认 <data_dir>/streams。
	StreamsDir string `toml:"streams_dir"`
	// HLSSegmentSeconds 是分片时长（秒），默认 4。
	// 太短会让播放列表请求变密，太长会让起播变慢。
	HLSSegmentSeconds int `toml:"hls_segment_seconds"`
	// HLSWindowSeconds 是一次预生成多远的媒体内容（秒），默认 300。
	//
	// 为什么要有窗口：`-c copy` 的转封装比实时快几十倍，不限量的话
	// 播一部 2 小时的电影会在几秒内把整部片拷进数据目录（几十 GB）。
	// 窗口用完就按当前播放位置再起一段（见 internal/stream）。
	HLSWindowSeconds int `toml:"hls_window_seconds"`
	// MaxSessions 是同时存在的转封装会话上限，默认 4。
	MaxSessions int `toml:"max_sessions"`
	// IdleSeconds 是无客户端访问后回收会话的秒数，默认 45。
	IdleSeconds int `toml:"idle_seconds"`

	// VAAPIDevice 显式指定硬件设备节点（例如 "/dev/dri/renderD128"）。
	//
	// 留空 = 自动探测：从 /dev/dri 里挑第一个 renderD*。
	// 什么时候要手写：一台机器有多张卡要挑其中一张；或者容器里设备映射到
	// 非默认路径。故意不写死默认值 —— 写死了别的机器就对不上。
	VAAPIDevice string `toml:"vaapi_device"`

	// Encoder 强制指定转码后端：auto（默认）/ vaapi / qsv / nvenc /
	// videotoolbox / amf / software。
	//
	// 留空或 auto = 按「硬件优先、最后兜底软编」的顺序自动挑。
	// 只在**真的需要**时指定（比如用户更在意画质、宁愿用 CPU 软编）。
	// 指定的后端在本机不可用时会回退自动选择并在日志里告警，不会导致播放失败。
	Encoder string `toml:"encoder"`

	// ThrottleSeconds 是转码/转封装时的「预生成提前量」（秒），默认 60；
	// 填 0 = 关闭节流。
	//
	// 已生成位置比客户端消费到的位置超前这么多时，服务端会把 ffmpeg 暂停
	// （SIGSTOP）让它歇着，等客户端追上来再恢复（SIGCONT）。转码比实时快得多
	//（本机 1080p 实测 20x+），不节流就是「用户刚点开、CPU 已把 300 秒窗口编完」。
	ThrottleSeconds int `toml:"throttle_seconds"`

	// TranscodeMaxHeight 是转码输出的高度上限（像素），默认 1080；
	// 竖填 0 = 不限（目标分辨率只用客户端上报的上限）。
	//
	// 为什么要压：转码是「边编边播」，输出分辨率直接决定能不能实时。
	// 实测（i5-10500T + VAAPI）：4K HDR 做色调映射只有 0.1x —— 一个 4 秒分片
	// 要 40 秒才出来，起播直接超时；压到 1080p 后回到可播范围。
	// 直出与转封装不走这个上限（它们不重编码，4K 原样送出更快）。
	//
	// 注意：这是**实例测量值推出来的默认值**，不是项目常量 —— 机器够强
	// （或想保留 4K 输出）就把它调大或改成 0。
	TranscodeMaxHeight int `toml:"transcode_max_height"`
}

// StreamsDirPath 返回解析过默认值的分片目录。
func (c *Config) StreamsDirPath() string {
	if strings.TrimSpace(c.Playback.StreamsDir) != "" {
		return c.Playback.StreamsDir
	}
	return filepath.Join(c.DataDir, "streams")
}

// LiveTVConfig 是直播电视（M5）的运行参数。
type LiveTVConfig struct {
	// AutoRefresh 是否按各直播源自己的间隔（refresh_interval_minutes）定时刷新，默认 true。
	//
	// 关掉只影响「自动」那一路：界面上手动刷新、CLI 的 refresh 都不受影响。
	// 单个源不想被自动刷新，把它的间隔填 0 即可。
	AutoRefresh bool `toml:"auto_refresh"`

	// RefreshTickSeconds 是调度器多久检查一次「有没有源到点了」，默认 60。
	//
	// 它只是检查频率，不是刷新间隔 —— 间隔由每个源自己的 refresh_interval_minutes 决定。
	// 所以填得比最短的刷新间隔小就行（默认 1 分钟足够）。
	RefreshTickSeconds int `toml:"refresh_tick_seconds"`

	// ProbeTimeoutSeconds 是单个频道的探测超时（秒），默认 8。
	//
	// 这个是**实例相关的**：一次全量探测耗时基本等于
	// 「死源数量 × 这个值 ÷ probe_concurrency」。实测可用的直播源 1~2 秒就返回流信息，
	// 所以这个值只影响不可达的那些要等多久。
	ProbeTimeoutSeconds int `toml:"probe_timeout_seconds"`

	// ProbeConcurrency 是并发探测数，默认 4。
	//
	// 别开太大：探测是真连源站，并发高了可能被源站限流，而排队的那些频道
	// 反而会因为「等太久」被算成失败。
	ProbeConcurrency int `toml:"probe_concurrency"`
}

// ImagesConfig 是图片管线配置。
//
// 与「元数据只写 PG」不同，图片是二进制：库里只存路径与尺寸，
// 文件本体分两处 —— 媒体目录里的本地图（权威，只读）+ LMBY 数据目录下的
// 回源图与缩放缓存。
type ImagesConfig struct {
	// CacheDir 是回源图与缩放结果的落盘目录，默认 <data_dir>/images。
	CacheDir string `toml:"cache_dir"`
	// MaxCacheMB 是缩放缓存上限（MB），默认 512。回源原图算数据，不参与清理。
	MaxCacheMB int `toml:"max_cache_mb"`
}

// ImagesCacheDir 返回解析过默认值的图片目录。
func (c *Config) ImagesCacheDir() string {
	if strings.TrimSpace(c.Images.CacheDir) != "" {
		return c.Images.CacheDir
	}
	return filepath.Join(c.DataDir, "images")
}

// OverlayDir 返回「只读媒体库」的叠加层目录（数据目录下）。
//
// 它存的是只读库的刮削产物（每库一块：元数据快照 + 图片）——
// 算数据而不是缓存，所以不跟图片缓存放在一起（那个会按上限清理），
// 也不放在媒体目录里（只读挂载上根本写不进去）。
func (c *Config) OverlayDir() string {
	return filepath.Join(c.DataDir, "overlay")
}

// TMDBConfig 是 TMDB 刮削源配置。
//
// 两个凭据任选其一即可：ReadToken（v4，Bearer）优先；
// 都没有时 provider 会被判为未配置，刮削功能自动跳1过。
type TMDBConfig struct {
	ReadToken string `toml:"read_token"`
	APIKey    string `toml:"api_key"`
	// Language 是优先语言，FallbackLanguage 在优先语言缺数据时补。
	Language         string `toml:"language"`
	FallbackLanguage string `toml:"fallback_language"`
}

// TasksConfig 是后台任务队列的配置。
type TasksConfig struct {
	// Workers 是并发的任务处理数。探测/刮削都是 IO 密集型，几个就够。
	Workers int `toml:"workers"`
}

// DatabaseConfig 是 PostgreSQL 连接配置。
type DatabaseConfig struct {
	DSN string `toml:"dsn"`
	// MaxConns 是连接池上限。
	MaxConns int32 `toml:"max_conns"`
	// AutoMigrate 为 true 时，服务启动会自动应用迁移。
	AutoMigrate bool `toml:"auto_migrate"`
}

// FFmpegConfig 指向外部 ffmpeg 可执行文件。
type FFmpegConfig struct {
	Path      string `toml:"path"`
	ProbePath string `toml:"probe_path"`
}

// Default 返回带默认值的配置。
// ScanConfig 是扫描器的小旋钮。
type ScanConfig struct {
	// MinFileSize 是「小于该字节数的文件不当视频」的下限（0 = 用内置默认）。
	//
	// 它只该用来挡真垃圾（缩略图、被改成 .mp4 的文本、下载残片），
	// **不是画质/时长过滤器** —— 实测有 28 秒的 1080p h264 短片只有 646 KB，
	// 阈值开大了会把它们当成垃圾直接跳过（用户2026-09-22 真踩到：一个目录 63 个文件被跳）。
	MinFileSize int64 `toml:"min_file_size"`
}

func Default() *Config {
	return &Config{
		Listen:          ":8099",
		DataDir:         "./data",
		LogLevel:        "info",
		SessionTTLHours: 720,
		Database: DatabaseConfig{
			DSN:         "postgres://lmby@localhost:5432/lmby?sslmode=disable",
			MaxConns:    10,
			AutoMigrate: true,
		},
		FFmpeg: FFmpegConfig{
			Path:      "ffmpeg",
			ProbePath: "ffprobe",
		},
		Tasks: TasksConfig{
			// 探测与刮削都是 IO 密集型；4 个 worker 既能压满带宽又不会把
			// 小机器（或网盘）打爆。
			Workers: 4,
		},
		TMDB: TMDBConfig{
			Language:         "zh-CN",
			FallbackLanguage: "en-US",
		},
		Images: ImagesConfig{
			MaxCacheMB: 512,
		},
		Playback: PlaybackConfig{
			HLSSegmentSeconds:  4,
			HLSWindowSeconds:   300,
			MaxSessions:        4,
			IdleSeconds:        45,
			TranscodeMaxHeight: 1080,
			ThrottleSeconds:    60,
		},
		LiveTV: LiveTVConfig{
			AutoRefresh:         true,
			RefreshTickSeconds:  60,
			ProbeTimeoutSeconds: 8,
			ProbeConcurrency:    4,
		},
	}
}

// DefaultSearchPaths 是未显式指定 --config 时的查找顺序。
func DefaultSearchPaths() []string {
	paths := []string{"/etc/lmby/config.toml", "config.toml"}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, xdg+"/lmby/config.toml")
	}
	return paths
}

// Load 读取配置文件（可为空路径）并叠加环境变量。
func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		for _, p := range DefaultSearchPaths() {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}

	if path != "" {
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("stat config %s: %w", path, err)
			}
		} else if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv 用 LMBY_* 环境变量覆盖配置。空值不覆盖。
func applyEnv(cfg *Config) error {
	setStr(&cfg.Listen, "LMBY_LISTEN")
	setStr(&cfg.DataDir, "LMBY_DATA_DIR")
	setStr(&cfg.LogLevel, "LMBY_LOG_LEVEL")
	setStr(&cfg.BaseURL, "LMBY_BASE_URL")
	setStr(&cfg.Database.DSN, "LMBY_DATABASE_DSN")
	setStr(&cfg.FFmpeg.Path, "LMBY_FFMPEG_PATH")
	setStr(&cfg.FFmpeg.ProbePath, "LMBY_FFPROBE_PATH")
	setInt(&cfg.Tasks.Workers, "LMBY_TASKS_WORKERS")
	setStr(&cfg.TMDB.ReadToken, "LMBY_TMDB_READ_TOKEN")
	setStr(&cfg.TMDB.APIKey, "LMBY_TMDB_API_KEY")
	setStr(&cfg.TMDB.Language, "LMBY_TMDB_LANGUAGE")
	setStr(&cfg.FFmpeg.Path, "LMBY_FFMPEG_PATH")
	setStr(&cfg.FFmpeg.ProbePath, "LMBY_FFPROBE_PATH")
	setStr(&cfg.Playback.StreamsDir, "LMBY_PLAYBACK_STREAMS_DIR")
	setInt(&cfg.Playback.HLSSegmentSeconds, "LMBY_PLAYBACK_HLS_SEGMENT_SECONDS")
	setInt(&cfg.Playback.HLSWindowSeconds, "LMBY_PLAYBACK_HLS_WINDOW_SECONDS")
	setInt(&cfg.Playback.MaxSessions, "LMBY_PLAYBACK_MAX_SESSIONS")
	setInt(&cfg.Playback.IdleSeconds, "LMBY_PLAYBACK_IDLE_SECONDS")
	setInt(&cfg.LiveTV.RefreshTickSeconds, "LMBY_LIVETV_REFRESH_TICK_SECONDS")
	setInt(&cfg.LiveTV.ProbeTimeoutSeconds, "LMBY_LIVETV_PROBE_TIMEOUT_SECONDS")
	setInt(&cfg.LiveTV.ProbeConcurrency, "LMBY_LIVETV_PROBE_CONCURRENCY")

	if v, ok := os.LookupEnv("LMBY_LIVETV_AUTO_REFRESH"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("LMBY_LIVETV_AUTO_REFRESH: %w", err)
		}
		cfg.LiveTV.AutoRefresh = b
	}

	if v, ok := os.LookupEnv("LMBY_SECURE_COOKIES"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("LMBY_SECURE_COOKIES: %w", err)
		}
		cfg.SecureCookies = b
	}
	if v, ok := os.LookupEnv("LMBY_SESSION_TTL_HOURS"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("LMBY_SESSION_TTL_HOURS: %w", err)
		}
		cfg.SessionTTLHours = n
	}
	if v, ok := os.LookupEnv("LMBY_DATABASE_MAX_CONNS"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("LMBY_DATABASE_MAX_CONNS: %w", err)
		}
		cfg.Database.MaxConns = int32(n)
	}
	if v, ok := os.LookupEnv("LMBY_AUTO_MIGRATE"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("LMBY_AUTO_MIGRATE: %w", err)
		}
		cfg.Database.AutoMigrate = b
	}
	return nil
}

func setStr(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		*dst = v
	}
}

// setInt 用环境变量覆盖整数配置项。非法值忽略（保持默认），
// 以免一个手误让服务起不来。
func setInt(dst *int, key string) {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*dst = n
		}
	}
}

// Validate 检查配置的合法性。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("listen 不能为空")
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return errors.New("database.dsn 不能为空（可用 LMBY_DATABASE_DSN 环境变量设置）")
	}
	if c.Database.MaxConns < 1 {
		return errors.New("database.max_conns 必须 >= 1")
	}
	if c.SessionTTLHours < 1 {
		return errors.New("session_ttl_hours 必须 >= 1")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data_dir 不能为空")
	}
	// 播放参数只做「修正」不做「报错」：这些值调歪了顶多画质/磁盘不理想，
	// 没有理由让整个服务起不来。
	if c.Playback.HLSSegmentSeconds < 1 || c.Playback.HLSSegmentSeconds > 30 {
		c.Playback.HLSSegmentSeconds = 4
	}
	if c.Playback.HLSWindowSeconds < 30 || c.Playback.HLSWindowSeconds > 7200 {
		c.Playback.HLSWindowSeconds = 300
	}
	if c.Playback.MaxSessions < 1 || c.Playback.MaxSessions > 64 {
		c.Playback.MaxSessions = 4
	}
	if c.Playback.IdleSeconds < 10 || c.Playback.IdleSeconds > 3600 {
		c.Playback.IdleSeconds = 45
	}
	// 0 = 不限（合法的显式选择），负数与超过 8K 的值当写错处理。
	if c.Playback.TranscodeMaxHeight < 0 || c.Playback.TranscodeMaxHeight > 4320 {
		c.Playback.TranscodeMaxHeight = 1080
	}
	// 0 = 关闭节流（合法），负数与超过 1 小时的值当写错处理。
	if c.Playback.ThrottleSeconds < 0 || c.Playback.ThrottleSeconds > 3600 {
		c.Playback.ThrottleSeconds = 60
	}
	// 直播的调度/探测参数同样只「修正」不「报错」。
	if c.LiveTV.RefreshTickSeconds < 5 || c.LiveTV.RefreshTickSeconds > 3600 {
		c.LiveTV.RefreshTickSeconds = 60
	}
	if c.LiveTV.ProbeTimeoutSeconds < 2 || c.LiveTV.ProbeTimeoutSeconds > 120 {
		c.LiveTV.ProbeTimeoutSeconds = 8
	}
	if c.LiveTV.ProbeConcurrency < 1 || c.LiveTV.ProbeConcurrency > 32 {
		c.LiveTV.ProbeConcurrency = 4
	}
	return nil
}
