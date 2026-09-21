// Package api 实现 LMBY 的 HTTP 接口与前端静态资源服务。
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/encoder"
	"github.com/hakureiyuyuko/lmby/internal/ffmpeg"
	"github.com/hakureiyuyuko/lmby/internal/images"
	"github.com/hakureiyuyuko/lmby/internal/provider"
	"github.com/hakureiyuyuko/lmby/internal/scan"
	"github.com/hakureiyuyuko/lmby/internal/scrape"
	"github.com/hakureiyuyuko/lmby/internal/settings"
	"github.com/hakureiyuyuko/lmby/internal/store"
	"github.com/hakureiyuyuko/lmby/internal/stream"
)

// Server 持有处理请求所需的全部依赖。
type Server struct {
	cfg    *config.Config
	store  *store.Store
	log    *slog.Logger
	ffmpeg ffmpeg.Info
	images *images.Service
	// scraper 用于人工匹配（应用候选/重新搜索/标记无需匹配）。
	// 没配元数据源时为 nil，对应接口返回 409。
	scraper *scrape.Handler
	// settings 是运行期可改的全局设置（目前是 TMDB 凭据）。
	settings *settings.Service
	// meta 是元数据源本体（设置页的「测试连接」直接用它打一次真请求）。
	meta    provider.Client
	started time.Time
	limiter *loginLimiter
	scans   *scan.Manager
	// streams 管理转封装（HLS）会话；plays 管理播放会话。
	streams *stream.Manager
	plays   *playRegistry
	// subs 管理「内嵌字幕抽成 WebVTT」的后台任务（同一文件+同一轨只抽一次）。
	subs *subtitleJobs
	// encoders 是本机编码能力表（真跑探测过，带磁盘缓存）。
	encoders *encoder.Store
}

// New 构造 Server。
func New(cfg *config.Config, st *store.Store, log *slog.Logger, ff ffmpeg.Info, img *images.Service,
	scraper *scrape.Handler, settingsSvc *settings.Service, meta provider.Client,
	streams *stream.Manager, encoders *encoder.Store) *Server {
	if streams == nil {
		// 没有 ffmpeg 时也要有个非 nil 的管理器（各处的调用会给出明确的失败原因），
		// 而不是让每个 handler 都要判一次 nil。
		streams = stream.NewManager(stream.Options{}, log)
	}
	if encoders == nil {
		// 同理：未接能力表时给个空仓库，接口会现场探测，不会 nil panic。
		encoders = encoder.NewStore(encoder.StoreOptions{FFmpeg: cfg.FFmpeg.Path, Log: log})
	}
	return &Server{
		cfg:      cfg,
		store:    st,
		log:      log,
		ffmpeg:   ff,
		images:   img,
		scraper:  scraper,
		settings: settingsSvc,
		meta:     meta,
		started:  time.Now(),
		limiter:  newLoginLimiter(8, 15*time.Minute),
		scans:    scan.NewManager(st, log),
		streams:  streams,
		plays:    newPlayRegistry(),
		subs:     newSubtitleJobs(),
		encoders: encoders,
	}
}

// Streams 暴露转封装管理器（供 main 在退出时停干净，不留孤儿 ffmpeg）。
func (s *Server) Streams() *stream.Manager { return s.streams }

// Scans 暴露扫描管理器（供 main 在启动时做残留清理等）。
func (s *Server) Scans() *scan.Manager { return s.scans }

// Handler 返回完整的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- 无需登录 ----
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/meta", s.handleMeta)
	mux.HandleFunc("POST /api/v1/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)

	// ---- 需要登录 ----
	mux.Handle("POST /api/v1/auth/logout", s.requireAuth(s.handleLogout))
	mux.Handle("GET /api/v1/auth/me", s.requireAuth(s.handleMe))
	mux.Handle("PATCH /api/v1/auth/me", s.requireAuth(s.handleUpdateProfile))
	mux.Handle("PATCH /api/v1/auth/me/preferences", s.requireAuth(s.handleUpdatePreferences))
	mux.Handle("POST /api/v1/auth/password", s.requireAuth(s.handleChangePassword))
	mux.Handle("GET /api/v1/auth/sessions", s.requireAuth(s.handleListSessions))
	mux.Handle("DELETE /api/v1/auth/sessions/{id}", s.requireAuth(s.handleRevokeSession))

	// ---- 媒体库与扫描 ----
	mux.Handle("GET /api/v1/libraries", s.requireAuth(s.handleListLibraries))
	mux.Handle("POST /api/v1/libraries", s.requireAuth(s.handleCreateLibrary))
	mux.Handle("GET /api/v1/libraries/{id}", s.requireAuth(s.handleGetLibrary))
	mux.Handle("PATCH /api/v1/libraries/{id}", s.requireAuth(s.handleUpdateLibrary))
	mux.Handle("DELETE /api/v1/libraries/{id}", s.requireAuth(s.handleDeleteLibrary))
	mux.Handle("POST /api/v1/libraries/{id}/scan", s.requireAuth(s.handleStartScan))
	mux.Handle("DELETE /api/v1/libraries/{id}/scan", s.requireAuth(s.handleCancelScan))
	mux.Handle("GET /api/v1/libraries/{id}/scan", s.requireAuth(s.handleScanStatus))
	mux.Handle("GET /api/v1/libraries/{id}/items", s.requireAuth(s.handleListItems))

	// ---- 设置（管理员）----
	mux.Handle("GET /api/v1/settings", s.requireAdmin(s.handleGetSettings))
	mux.Handle("PUT /api/v1/settings/tmdb", s.requireAdmin(s.handleUpdateTMDBSettings))
	mux.Handle("DELETE /api/v1/settings/tmdb", s.requireAdmin(s.handleResetTMDBSettings))
	mux.Handle("POST /api/v1/provider/test", s.requireAdmin(s.handleTestProvider))

	// ---- 搜索（标题 / 原始标题，中文二元组）----
	mux.Handle("GET /api/v1/search", s.requireAuth(s.handleSearch))

	// ---- 浏览（海报墙 / 子项）与批量操作 ----
	mux.Handle("GET /api/v1/libraries/{id}/browse", s.requireAuth(s.handleBrowseLibrary))
	mux.Handle("GET /api/v1/items/{id}/children", s.requireAuth(s.handleItemChildren))
	mux.Handle("POST /api/v1/items/batch", s.requireAuth(s.handleBatchItems))

	// ---- 条目详情与人工编辑（字段锁定）----
	mux.Handle("GET /api/v1/items/{id}", s.requireAuth(s.handleGetItem))
	mux.Handle("PATCH /api/v1/items/{id}", s.requireAuth(s.handleUpdateItem))
	mux.Handle("POST /api/v1/items/{id}/scrape", s.requireAuth(s.handleEnqueueItemScrape))

	// ---- 图片 ----
	mux.Handle("GET /api/v1/items/{id}/images", s.requireAuth(s.handleListImages))
	mux.Handle("GET /api/v1/items/{id}/images/{kind}", s.requireAuth(s.handleItemImage))

	// ---- 人工匹配 ----
	mux.Handle("GET /api/v1/items/{id}/match", s.requireAuth(s.handleGetItemMatch))
	mux.Handle("POST /api/v1/items/{id}/match", s.requireAuth(s.handleApplyItemMatch))
	mux.Handle("POST /api/v1/items/{id}/match/search", s.requireAuth(s.handleSearchItemMatch))
	mux.Handle("POST /api/v1/items/{id}/match/skip", s.requireAuth(s.handleSkipItemMatch))

	// ---- 后台任务队列（探测 / 刮削）----
	mux.Handle("GET /api/v1/tasks", s.requireAuth(s.handleTaskStats))
	mux.Handle("POST /api/v1/libraries/{id}/probe", s.requireAuth(s.handleEnqueueProbes))
	mux.Handle("POST /api/v1/libraries/{id}/probe/reset", s.requireAuth(s.handleResetFailedProbes))

	// ---- 刮削（元数据）----
	mux.Handle("POST /api/v1/libraries/{id}/scrape", s.requireAuth(s.handleEnqueueScrapes))
	mux.Handle("GET /api/v1/libraries/{id}/scrape", s.requireAuth(s.handleScrapeStatus))
	mux.Handle("POST /api/v1/libraries/{id}/scrape/reset", s.requireAuth(s.handleResetFailedScrapes))

	// ---- 实时事件（SSE）----
	mux.Handle("GET /api/v1/events", s.requireAuth(s.handleEvents))

	// ---- 播放（M3）----
	mux.Handle("POST /api/v1/items/{id}/play", s.requireAuth(s.handleStartPlayback))
	mux.Handle("GET /api/v1/items/{id}/playlist", s.requireAuth(s.handleItemPlaylist))
	mux.Handle("GET /api/v1/items/{id}/progress", s.requireAuth(s.handleItemProgress))
	mux.Handle("POST /api/v1/items/{id}/played", s.requireAuth(s.handleSetPlayed))
	mux.Handle("POST /api/v1/items/played", s.requireAuth(s.handleSetPlayed))
	mux.Handle("GET /api/v1/continue", s.requireAuth(s.handleContinueWatching))
	mux.Handle("GET /api/v1/playback/sessions", s.requireAuth(s.handleListPlaySessions))
	// 前端的 libass 兑底字体（libass/WASM 只能用它自己文件系统里的字体，
	// 看不到客户端的系统字体）。
	mux.Handle("GET /api/v1/fonts/{name}", s.requireAuth(s.handleFont))
	// 监控页的一键终止：掐掉某一路转封装/转码进程（管理员）。
	mux.Handle("POST /api/v1/playback/streams/{key}/stop", s.requireAdmin(s.handleStopTranscodeSession))

	// 编码能力（M4）：看这台机器到底能用哪个转码后端。
	mux.Handle("GET /api/v1/transcode/capabilities", s.requireAuth(s.handleTranscodeCapabilities))
	mux.Handle("POST /api/v1/transcode/capabilities/refresh", s.requireAdmin(s.handleRefreshTranscodeCapabilities))

	// 播放会话下的媒体分发：直出原文件 / HLS 播放列表与分片 / 字幕。
	//
	// 分片的路由必须是「与播放列表同级」（/play/{sid}/{name}）：m3u8 里写的是
	// 相对文件名（init.mp4 / seg_00000.m4s），任何标准 HLS 客户端（hls.js、
	// Safari、ffprobe）都会拿播放列表的 URL 作基准去拼 —— 放在 /seg/ 子路径下
	// 就会全部 404（实测被 ffprobe 当场抓包）。
	mux.Handle("GET /api/v1/play/{sid}/stream", s.requireAuth(s.handlePlayStream))
	mux.Handle("GET /api/v1/play/{sid}/index.m3u8", s.requireAuth(s.handlePlayPlaylist))
	mux.Handle("GET /api/v1/play/{sid}/{name}", s.requireAuth(s.handlePlaySegment))
	mux.Handle("GET /api/v1/play/{sid}/subtitles/{name}", s.requireAuth(s.handlePlaySubtitle))
	// 内封字体（mkv 附件）：列表 + 按序号取字节。给前端 libass 用 ——
	// 不给的话，字幕里 fn 引用的特效字体会退化成兜底字体。
	mux.Handle("GET /api/v1/play/{sid}/fonts", s.requireAuth(s.handlePlayFonts))
	mux.Handle("GET /api/v1/play/{sid}/fonts/{n}", s.requireAuth(s.handlePlayFont))
	mux.Handle("GET /api/v1/play/{sid}", s.requireAuth(s.handlePlayState))
	mux.Handle("POST /api/v1/play/{sid}/seek", s.requireAuth(s.handlePlaySeek))
	mux.Handle("POST /api/v1/play/{sid}/progress", s.requireAuth(s.handlePlayProgress))
	mux.Handle("POST /api/v1/play/{sid}/stop", s.requireAuth(s.handlePlayStop))

	// ---- 前端静态资源（必须最后注册，作为兜底）----
	mux.Handle("/", s.staticHandler())

	return s.recoverPanic(s.logRequests(s.securityHeaders(mux)))
}

// serverError 记录内部错误并返回 500，避免把细节泄露给客户端。
func (s *Server) serverError(w http.ResponseWriter, message string, err error) {
	if err != nil {
		s.log.Error(message, "err", err)
	}
	writeError(w, http.StatusInternalServerError, message)
}

// CleanupRateLimits 清理登录限流器中已过期的记录，返回清理条数。
// 由后台 janitor 定时调用，避免被大量不同来源的失败登录撑大内存。
func (s *Server) CleanupRateLimits() int {
	return s.limiter.Cleanup()
}
