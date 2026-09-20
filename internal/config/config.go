// Package config 负责加载与校验 LMBY 的配置。
//
// 优先级：默认值 < 配置文件 < 环境变量（LMBY_*）。
package config

import (
	"errors"
	"fmt"
	"os"
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
	return nil
}
