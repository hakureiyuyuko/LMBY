// Command lmby 是 LMBY 媒体服务器的可执行入口。
//
// 用法：
//
//	lmby serve    [--config 路径] [--migrate=false]   启动服务（默认命令）
//	lmby migrate  [--config 路径]                     仅应用数据库迁移
//	lmby version                                      打印版本
//	lmby help                                         打印帮助
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/api"
	"github.com/hakureiyuyuko/lmby/internal/auth"
	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/ffmpeg"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "serve":
		return cmdServe(args)
	case "migrate":
		return cmdMigrate(args)
	case "version", "--version", "-v":
		fmt.Println("lmby " + version.String())
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("未知命令 %q", cmd)
	}
}

func usage() {
	fmt.Print(`LMBY — Light 的 Emby

用法:
  lmby serve   [--config 路径] [--migrate=false]   启动服务（默认命令）
  lmby migrate [--config 路径]                     仅应用数据库迁移
  lmby version                                     打印版本
  lmby help                                        打印本帮助

配置优先级：默认值 < 配置文件 < 环境变量（LMBY_*）
默认配置文件查找顺序：/etc/lmby/config.toml、./config.toml
`)
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Database.DSN, cfg.Database.MaxConns)
	if err != nil {
		return err
	}
	defer st.Close()

	applied, err := store.Migrate(ctx, st)
	if err != nil {
		return err
	}
	for _, m := range applied {
		log.Info("已应用迁移", "version", m.Version, "name", m.Name)
	}

	v, err := store.SchemaVersion(ctx, st)
	if err != nil {
		return fmt.Errorf("读取数据库结构版本失败: %w", err)
	}
	log.Info("数据库结构已就绪", "schemaVersion", v)
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	runMigrate := fs.Bool("migrate", true, "启动时自动应用数据库迁移")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	log.Info("启动 LMBY",
		"version", version.String(),
		"listen", cfg.Listen,
		"dataDir", cfg.DataDir)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录 %s 失败: %w", cfg.DataDir, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Database.DSN, cfg.Database.MaxConns)
	if err != nil {
		return err
	}
	defer st.Close()

	if cfg.Database.AutoMigrate && *runMigrate {
		applied, err := store.Migrate(ctx, st)
		if err != nil {
			return err
		}
		if len(applied) > 0 {
			log.Info("数据库迁移完成", "applied", len(applied))
		}
	}

	schemaVersion, err := store.SchemaVersion(ctx, st)
	if err != nil {
		return fmt.Errorf("读取数据库结构版本失败: %w", err)
	}
	log.Info("数据库就绪", "schemaVersion", schemaVersion)

	ff := ffmpeg.Detect(ctx, cfg.FFmpeg.Path)
	if ff.Available {
		log.Info("ffmpeg 就绪",
			"path", ff.Path,
			"version", ff.Version,
			"hwaccels", strings.Join(ff.HWAccels, ","))
	} else {
		log.Warn("ffmpeg 不可用：流信息探测与转码将无法工作",
			"path", cfg.FFmpeg.Path, "err", ff.Error)
	}

	if err := bootstrapAdmin(ctx, st, log); err != nil {
		return err
	}

	srv := api.New(cfg, st, log, ff)
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 刻意不设 WriteTimeout：媒体流与 HLS 是长连接，任何固定写超时都会误杀它们。
	}

	go janitor(ctx, st, srv, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("HTTP 服务已开始监听", "addr", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	case <-ctx.Done():
		log.Info("收到退出信号，正在优雅关闭…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("优雅关闭失败: %w", err)
		}
		log.Info("已退出")
		return nil
	}
}

// bootstrapAdmin 在系统还没有任何账号时创建初始管理员。
//
// 口令来自 LMBY_ADMIN_PASSWORD（便于自动化部署）；未设置时留给 Web 初始化页面。
func bootstrapAdmin(ctx context.Context, st *store.Store, log *slog.Logger) error {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	password := os.Getenv("LMBY_ADMIN_PASSWORD")
	if password == "" {
		log.Warn("系统尚无账号：请打开 Web 界面完成初始化，或设置 LMBY_ADMIN_PASSWORD 自动创建")
		return nil
	}

	username := strings.TrimSpace(os.Getenv("LMBY_ADMIN_USERNAME"))
	if username == "" {
		username = "admin"
	}

	hash, err := auth.HashPassword(password, auth.DefaultParams)
	if err != nil {
		return fmt.Errorf("生成管理员口令哈希失败: %w", err)
	}
	u, err := st.CreateFirstAdmin(ctx, username, username, hash)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("创建初始管理员失败: %w", err)
	}
	log.Info("已根据 LMBY_ADMIN_PASSWORD 创建初始管理员", "username", u.Username)
	return nil
}

// janitor 周期性做后台清理：过期会话、限流计数。
func janitor(ctx context.Context, st *store.Store, srv *api.Server, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if n, err := st.CleanupSessions(cleanupCtx); err != nil {
				log.Warn("清理过期会话失败", "err", err)
			} else if n > 0 {
				log.Info("已清理过期会话", "count", n)
			}
			if n := srv.CleanupRateLimits(); n > 0 {
				log.Debug("已清理登录限流记录", "count", n)
			}
			cancel()
		}
	}
}
