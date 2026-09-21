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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/api"
	"github.com/hakureiyuyuko/lmby/internal/auth"
	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/encoder"
	"github.com/hakureiyuyuko/lmby/internal/ffmpeg"
	"github.com/hakureiyuyuko/lmby/internal/images"
	"github.com/hakureiyuyuko/lmby/internal/probe"
	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/provider/tmdb"
	"github.com/hakureiyuyuko/lmby/internal/scrape"
	"github.com/hakureiyuyuko/lmby/internal/secrets"
	"github.com/hakureiyuyuko/lmby/internal/settings"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/stream"
	"github.com/hakureiyuyuko/lmby/internal/version"
	"github.com/hakureiyuyuko/lmby/internal/worker"
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
	case "user":
		return cmdUser(args)
	case "provider":
		return cmdProvider(args)
	case "match":
		return cmdMatch(args)
	case "scrape":
		return cmdScrape(args)
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
  lmby user add <用户名> [--admin]                 创建账号
  lmby user passwd <用户名>                        重置口令
  lmby user ls                                    列出账号
  lmby match   [--kind tv|movie] --title <本地标题> [--year 2009]   用真实
               TMDB 候选验证匹配打分器（--deep 会再取详情，把集数/时长
               也拉进来参与打分）
  lmby scrape  enqueue|run|status|reset                             元数据刮削
               （run = 入队并就地跑完，便于验收；详见 lmby scrape）
  lmby version                                     打印版本
  lmby help                                        打印本帮助

口令来源（按优先级）：--password 参数 < 环境变量 LMBY_PASSWORD < 标准输入。
避免把口令写在命令行上（ps 与 shell 历史里都会留下痕迹）。

配置优先级：默认值 < 配置文件 < 环境变量（LMBY_*）
默认配置文件查找顺序：/etc/lmby/config.toml、./config.toml
`)
}

// cmdUser 提供最小可用的账号管理能力。
//
// 为什么需要它：初始化向导只能建第一个账号，而后台部署、
// 自动化测试、忘记口令的救援都需要一个命令行入口。
func cmdUser(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("缺少子命令（add / passwd / ls）")
	}
	sub := args[0]
	flagArgs, posArgs := splitFlagsAndPositionals(args[1:])
	fs := flag.NewFlagSet("user "+sub, flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	password := fs.String("password", "", "口令（建议改用环境变量 LMBY_PASSWORD 或标准输入）")
	admin := fs.Bool("admin", false, "是否管理员")
	displayName := fs.String("display-name", "", "显示名")
	if err := fs.Parse(flagArgs); err != nil {
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

	switch sub {
	case "ls":
		users, err := st.ListUsers(ctx)
		if err != nil {
			return err
		}
		if len(users) == 0 {
			fmt.Println("（没有任何账号）")
			return nil
		}
		fmt.Printf("%-6s %-20s %-6s %-8s %s\n", "ID", "用户名", "管理员", "已禁用", "显示名")
		for _, u := range users {
			fmt.Printf("%-6d %-20s %-6v %-8v %s\n", u.ID, u.Username, u.IsAdmin, u.IsDisabled, u.DisplayName)
		}
		return nil

	case "add":
		if len(posArgs) < 1 {
			return errors.New("用法: lmby user add [--admin] <用户名>")
		}
		username := strings.TrimSpace(posArgs[0])
		pw, err := readPassword(*password)
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw, auth.DefaultParams)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(*displayName)
		if name == "" {
			name = username
		}
		u, err := st.CreateUser(ctx, username, name, hash, *admin)
		if errors.Is(err, store.ErrAlreadyExists) {
			return fmt.Errorf("用户名 %s 已存在", username)
		}
		if err != nil {
			return err
		}
		log.Info("已创建账号", "id", u.ID, "username", u.Username, "admin", u.IsAdmin)
		if !u.IsAdmin {
			fmt.Println("提示：这是普通账号；需要管理员请加 --admin")
		}
		return nil

	case "passwd":
		if len(posArgs) < 1 {
			return errors.New("用法: lmby user passwd <用户名>")
		}
		username := strings.TrimSpace(posArgs[0])
		pw, err := readPassword(*password)
		if err != nil {
			return err
		}
		u, err := st.GetUserByUsername(ctx, username)
		if err != nil {
			return fmt.Errorf("找不到账号 %s", username)
		}
		hash, err := auth.HashPassword(pw, auth.DefaultParams)
		if err != nil {
			return err
		}
		if err := st.UpdateUserPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		// 口令变了，旧会话必须失效
		if n, err := st.RevokeOtherSessions(ctx, u.ID, ""); err == nil && n > 0 {
			log.Info("已撤销该账号的全部会话", "count", n)
		}
		log.Info("已重置口令", "username", u.Username)
		return nil

	default:
		usage()
		return fmt.Errorf("未知子命令 %q", sub)
	}
}

// splitFlagsAndPositionals 把参数拆成「旗标」与「位置参数」两部分。
//
// Go 的 flag 包遇到第一个非旗标参数就会停止解析，
// 于是 `user add devtest --admin` 里的 --admin 会被当成位置参数丢掉。
// 命令行工具不该对顺序这么敏感，所以这里手动拆一次。
func splitFlagsAndPositionals(args []string) (flags, positional []string) {
	valueFlags := map[string]bool{
		"--config": true, "-config": true,
		"--password": true, "-password": true,
		"--display-name": true, "-display-name": true,
		// provider 子命令的开关：不登记的话 `--kind tv` 里的 `tv`
		// 会被当成位置参数，把「tv」拼进搜索关键词里（已踩过）。
		"--kind": true, "-kind": true,
		"--year": true, "-year": true,
		// match 子命令的开关
		"--title": true, "-title": true,
		"--orig-title": true, "-orig-title": true,
		"--aliases": true, "-aliases": true,
		"--season": true, "-season": true,
		"--episodes": true, "-episodes": true,
		"--runtime": true, "-runtime": true,
		"--top": true, "-top": true,
		// scrape 子命令的开关
		"--library": true, "-library": true,
		"--limit": true, "-limit": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name, hasValue := a, false
		if eq := strings.Index(a, "="); eq >= 0 {
			name, hasValue = a[:eq], true
		}
		if !hasValue && valueFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional
}

// readPassword 按 参数 > 环境变量 > 标准输入 的顺序取口令。
func readPassword(flagValue string) (string, error) {
	if pw := strings.TrimSpace(flagValue); pw != "" {
		return pw, nil
	}
	if pw := strings.TrimSpace(os.Getenv("LMBY_PASSWORD")); pw != "" {
		return pw, nil
	}
	fmt.Fprint(os.Stderr, "请输入口令: ")
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("读取口令失败: %w", err)
	}
	pw := strings.TrimSpace(line)
	if pw == "" {
		return "", errors.New("口令不能为空")
	}
	return pw, nil
}

func newLogger(level string) *slog.Logger {
	return newLoggerTo(os.Stdout, level)
}

// encoderPreference 把配置里的字符串转成后端偏好；auto / 空 = 自动挑。
//
// 这里刻意**不**做白名单校验：认不出的值会在选择时因为「本机没有这个后端」
// 而回退到自动选择，并记一条告警 —— 比启动就报错友好。
func encoderPreference(v string) encoder.Kind {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "auto":
		return ""
	default:
		return encoder.Kind(strings.ToLower(strings.TrimSpace(v)))
	}
}

// newCLILogger 把日志写到 stderr。
//
// CLI 工具的 stdout 要留给数据（方便 jq/脚本处理），否则日志会和 JSON 结果
// 混在一起 —— 这个坑已经踩过一次。
func newCLILogger(level string) *slog.Logger {
	return newLoggerTo(os.Stderr, level)
}

func newLoggerTo(w io.Writer, level string) *slog.Logger {
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
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv}))
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	acceptChecksums := fs.Bool("accept-checksum-changes", false,
		"接受「已应用过的迁移文件被修改」并重新登记校验和（仅在刚发布就写错时使用）")
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

	res, err := store.Migrate(ctx, st, store.MigrateOptions{AcceptChecksumChanges: *acceptChecksums})
	if err != nil {
		return err
	}
	for _, m := range res.Reconciled {
		log.Warn("已重新登记被修改的历史迁移的校验和", "version", m.Version, "name", m.Name)
	}
	for _, m := range res.Applied {
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
		res, err := store.Migrate(ctx, st, store.MigrateOptions{})
		if err != nil {
			return err
		}
		if len(res.Applied) > 0 {
			log.Info("数据库迁移完成", "applied", len(res.Applied))
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

	// 后台任务队列：探测、后续的刮削都跑在这里。
	// 与 HTTP 服务共享同一个 ctx，收到退出信号时一起优雅停止。
	//
	// 启动前先把上次进程留下的 running 任务立刻回收：
	// 那些任务不可能再有 worker 认领（worker 随进程一起死了），
	// 而维护协程的回收阀值（30 分钟）会让它们白等很久。
	if n, err := st.RecoverStaleTasks(ctx, 0); err != nil {
		log.Warn("回收上次中断的任务失败", "err", err)
	} else if n > 0 {
		log.Info("已回收上次中断的任务", "count", n)
	}

	pool := worker.New(st, log, cfg.Tasks.Workers)
	pool.Register(probe.NewHandler(st, cfg.FFmpeg.ProbePath, log))

	// 运行期可改的全局设置（目前是 TMDB 凭据）。
	// 密钥文件放在数据目录里（0600）；生成不了也不拦启动，只是退化成明文存库。
	cipher := loadSecretsCipher(cfg, log)
	settingsSvc := settings.New(st, cipher, settings.TMDB{
		ReadToken: cfg.TMDB.ReadToken,
		APIKey:    cfg.TMDB.APIKey,
		Language:  cfg.TMDB.Language,
	}, log)
	if _, err := settingsSvc.Load(ctx); err != nil {
		log.Warn("从数据库装载 TMDB 设置失败，本次先用配置文件的值", "err", err)
	}

	// 元数据源（TMDB）在刮削、图片回源、人工匹配与设置页四处都要用，所以只构造一次。
	//
	// 注意：**即使当下没有凭据也要构造** —— 设置页可以在运行期把 Key 填进来，
	// 那时候这个客户端必须已经被各服务拿在手里（见 settingsSvc.SetOnChange）。
	cached, tmdbClient := newTMDBProvider(st, settingsSvc.TMDB().ReadToken,
		settingsSvc.TMDB().APIKey, settingsSvc.TMDB().Language)
	settingsSvc.SetOnChange(func(t settings.TMDB) {
		tmdbClient.SetCredentials(t.ReadToken, t.APIKey, t.Language)
		log.Info("TMDB 凭据已更新（无需重启）", "language", t.Language,
			"auth", authMode(config.TMDBConfig{ReadToken: t.ReadToken, APIKey: t.APIKey}))
	})

	scraper := scrape.NewHandler(st, cached, log)
	pool.Register(scraper)
	if !settingsSvc.Configured() {
		log.Warn("尚未配置 TMDB 凭据：元数据刮削、图片回源与人工匹配暂不可用（可在设置页里填，或写进 config.toml）")
	} else {
		log.Info("已启用 TMDB 刮削源", "language", settingsSvc.TMDB().Language,
			"auth", authMode(cfg.TMDB), "fromDb", settingsSvc.TMDB().FromDB)
	}
	go pool.Run(ctx)

	// 图片管线：本地图只读，回源图与缩放缓存落在数据目录
	imgSvc, err := images.NewService(st, cached, cfg.ImagesCacheDir(), cfg.Images.MaxCacheMB, log)
	if err != nil {
		return err
	}
	log.Info("图片管线就绪", "cacheDir", cfg.ImagesCacheDir(), "maxCacheMB", cfg.Images.MaxCacheMB)

	// 转封装（播放）会话：每次播放一个子目录，空闲自动回收。
	//
	// 放在 api.New 之前创建并 Start：进程退出时 StopAll 会杀掉所有 ffmpeg ——
	// 否则一次 Ctrl-C 就会留下一堆还在写分片的孤儿进程。
	streams := stream.NewManager(stream.Options{
		FFmpeg:         cfg.FFmpeg.Path,
		Root:           cfg.StreamsDirPath(),
		SegmentSeconds: cfg.Playback.HLSSegmentSeconds,
		WindowSeconds:  cfg.Playback.HLSWindowSeconds,
		MaxSessions:    cfg.Playback.MaxSessions,
		IdleSeconds:    cfg.Playback.IdleSeconds,
	}, log)
	streams.Start()
	defer streams.StopAll()
	log.Info("播放（转封装）就绪",
		"streamsDir", cfg.StreamsDirPath(),
		"segmentSeconds", cfg.Playback.HLSSegmentSeconds,
		"windowSeconds", cfg.Playback.HLSWindowSeconds,
		"maxSessions", cfg.Playback.MaxSessions)

	// 编码能力表：启动时后台真跑探测一次（约 1~3 秒，不阻塞启动）。
	// 这样第一次点播放时不用现等，界面一打开也能看到「这台机器能用什么」。
	encStore := encoder.NewStore(encoder.StoreOptions{
		FFmpeg:         cfg.FFmpeg.Path,
		CachePath:      filepath.Join(cfg.DataDir, "capabilities.json"),
		WorkDir:        filepath.Join(cfg.DataDir, "probe"),
		DeviceOverride: cfg.Playback.VAAPIDevice,
		Prefer:         encoderPreference(cfg.Playback.Encoder),
		Log:            log,
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := encStore.Get(ctx); err != nil {
			log.Warn("启动时编码能力探测失败（首次转码时会再试一次）", "err", err)
		}
	}()

	// 传**未包缓存的**客户端给 API：设置页的「测试连接」必须真打一次网络，
	// 否则缓存命中时它会回「通着」—— 而用户正是想验证凭据能不能用（实测踩到）。
	srv := api.New(cfg, st, log, ff, imgSvc, scraper, settingsSvc, tmdbClient, streams, encStore)

	// 上次进程被中断时可能留下「正在扫描」的幽灵记录，启动时收尾。
	if n, err := st.MarkStaleRunsFailed(ctx); err != nil {
		log.Warn("清理中断的扫描记录失败", "err", err)
	} else if n > 0 {
		log.Info("已清理中断的扫描记录", "count", n)
	}

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

// newTMDBProvider 用给定凭据构造「带缓存的 TMDB provider」。
//
// 总是返回非 nil：没凭据时客户端照样存在，只是发请求会 401 ——
// 这样设置页在运行期填入 Key 之后（见 tmdb.Client.SetCredentials）不必重建任何东西。
func newTMDBProvider(st *store.Store, readToken, apiKey, language string) (*provider.Cached, *tmdb.Client) {
	client := tmdb.New(tmdb.Config{
		ReadToken: readToken,
		APIKey:    apiKey,
		Language:  language,
	})
	return provider.NewCached(client, st, provider.DefaultTTLs()), client
}

// buildTMDBProvider 是命令行（`lmby provider ...`）用的：从配置里取凭据。
func buildTMDBProvider(cfg *config.Config, st *store.Store, log *slog.Logger) (*provider.Cached, *tmdb.Client) {
	cached, raw := newTMDBProvider(st, cfg.TMDB.ReadToken, cfg.TMDB.APIKey, cfg.TMDB.Language)
	if raw.Configured() {
		log.Info("已启用 TMDB 刮削源", "language", cfg.TMDB.Language, "auth", authMode(cfg.TMDB))
	}
	return cached, raw
}

// loadSecretsCipher 装载「存库凭据」的加密密钥（数据目录里的 0600 密钥文件）。
//
// 失败只警告并返回 nil（退化成明文存储）：加密是加固，不该让服务起不来 ——
// 设置页会把这个状态显式告诉用户。
func loadSecretsCipher(cfg *config.Config, log *slog.Logger) *secrets.Cipher {
	path := filepath.Join(cfg.DataDir, "secret.key")
	c, err := secrets.LoadOrCreate(path)
	if err != nil {
		log.Warn("加密密钥不可用：设置页里填的 TMDB 凭据将以明文存库", "path", path, "err", err)
		return nil
	}
	return c
}

// authMode 把凭据形态描述成人话（日志与 CLI 输出用）。
func authMode(c config.TMDBConfig) string {
	if c.ReadToken != "" {
		return "v4 read access token"
	}
	if c.APIKey != "" {
		return "v3 api key"
	}
	return "未配置"
}

// cmdProvider 是对着真实 provider 排查问题用的命令行工具。
//
// 用法：
//
//	lmby provider status
//	lmby provider search --kind tv --year 2009 钢之炼金术师
//	lmby provider movie 550
//	lmby provider tv 31911
//	lmby provider season 31911 1
//	lmby provider image-url /abc.jpg w500
func cmdProvider(args []string) error {
	fs := flag.NewFlagSet("provider", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径")
	kind := fs.String("kind", "tv", "search 的条目类型：movie | tv")
	year := fs.Int("year", 0, "search 的年份过滤")
	if len(args) == 0 {
		fmt.Print(`用法：
  lmby provider status
  lmby provider search [--kind movie|tv] [--year 2009] <标题>
  lmby provider movie <id>
  lmby provider tv <id>
  lmby provider season <tvId> <season>
  lmby provider image-url <path> [size]
`)
		return nil
	}

	flags, positional := splitFlagsAndPositionals(args)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(positional) == 0 {
		return errors.New("缺少子命令")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := newCLILogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Database.DSN, cfg.Database.MaxConns)
	if err != nil {
		return err
	}
	defer st.Close()

	cached, raw := buildTMDBProvider(cfg, st, log)
	if !raw.Configured() {
		return errors.New("未配置 TMDB 凭据（config.toml 的 [tmdb] 段，或 LMBY_TMDB_READ_TOKEN）")
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	sub, rest := positional[0], positional[1:]
	switch sub {
	case "status":
		stats, err := st.ProviderCacheStatsOf(ctx)
		if err != nil {
			return err
		}
		hits, misses := cached.Stats()
		return enc.Encode(map[string]any{
			"provider": cached.Name(),
			"auth":     authMode(cfg.TMDB),
			"language": cfg.TMDB.Language,
			"cache":    stats,
			"session":  map[string]any{"hits": hits, "misses": misses},
		})

	case "search":
		if len(rest) == 0 {
			return errors.New("缺少搜索关键词")
		}
		query := strings.Join(rest, " ")
		opts := provider.SearchOptions{Year: *year, Lang: cfg.TMDB.Language}
		var results []provider.SearchResult
		if *kind == provider.KindMovie {
			results, err = cached.SearchMovie(ctx, query, opts)
		} else {
			results, err = cached.SearchSeries(ctx, query, opts)
		}
		if err != nil {
			return err
		}
		if len(results) > 10 {
			results = results[:10]
		}
		return enc.Encode(results)

	case "movie", "tv":
		if len(rest) == 0 {
			return fmt.Errorf("%s 需要 id", sub)
		}
		id, err := strconv.Atoi(rest[0])
		if err != nil {
			return fmt.Errorf("id 必须是数字: %w", err)
		}
		if sub == "movie" {
			m, err := cached.Movie(ctx, id, cfg.TMDB.Language)
			if err != nil {
				return err
			}
			return enc.Encode(m)
		}
		s, err := cached.Series(ctx, id, cfg.TMDB.Language)
		if err != nil {
			return err
		}
		return enc.Encode(s)

	case "season":
		if len(rest) < 2 {
			return errors.New("season 需要 <tvId> <season>")
		}
		id, err := strconv.Atoi(rest[0])
		if err != nil {
			return err
		}
		season, err := strconv.Atoi(rest[1])
		if err != nil {
			return err
		}
		s, err := cached.Season(ctx, id, season, cfg.TMDB.Language)
		if err != nil {
			return err
		}
		return enc.Encode(s)

	case "image-url":
		if len(rest) < 1 {
			return errors.New("image-url 需要 <path> [size]")
		}
		size := "w500"
		if len(rest) > 1 {
			size = rest[1]
		}
		fmt.Println(raw.ImageURL(rest[0], size))
		return nil

	default:
		return fmt.Errorf("未知的 provider 子命令 %q", sub)
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
			if n := srv.CleanupPlaySessions(); n > 0 {
				log.Debug("已清理过期播放会话", "count", n)
			}
			cancel()
		}
	}
}
