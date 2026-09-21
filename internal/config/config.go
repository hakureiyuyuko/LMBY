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
	FFmpeg   FFmpegConfig   `toml:"ffmpeg"`
	Tasks    TasksConfig    `toml:"tasks"`
	TMDB     TMDBConfig     `toml:"tmdb"`
	Images   ImagesConfig   `toml:"images"`
	Playback PlaybackConfig `toml:"playback"`
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
}

// StreamsDirPath 返回解析过默认值的分片目录。
func (c *Config) StreamsDirPath() string {
	if strings.TrimSpace(c.Playback.StreamsDir) != "" {
		return c.Playback.StreamsDir
	}
	return filepath.Join(c.DataDir, "streams")
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
			HLSSegmentSeconds: 4,
			HLSWindowSeconds:  300,
			MaxSessions:       4,
			IdleSeconds:       45,
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
	return nil
}
