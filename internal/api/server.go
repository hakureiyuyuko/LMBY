// Package api 实现 LMBY 的 HTTP 接口与前端静态资源服务。
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/encoder"
	"github.com/hakureiyuyuko/lmby/internal/ffmpeg"
	"github.com/hakureiyuyuko/lmby/internal/images"
	"github.com/hakureiyuyuko/lmby/internal/livetv"
	"github.com/hakureiyuyuko/lmby/internal/livetvsync"
	"github.com/hakureiyuyuko/lmby/internal/overlay"
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
	// live 是直播电视的运行层（订阅源刷新、频道探测）；解析与落库分别在
	// internal/livetv 与 internal/store。
	live *livetvsync.Service
	// liveSigner 给直播外链 token 签名（M5；实现是 secrets.Cipher）。
	// 为空表示没装密钥（单测/无数据目录），此时外链接口回 503。
	liveSigner livetv.Signer
	// livePlays 是直播播放会话表（sid → 频道）。
	livePlays *livePlayRegistry
	// tvProbe 是「频道探测」这一次后台运行的状态。
	//
	// 只有 running 是内存态（防重入）；探了多少、通不通全部从库里的
	// probe/probe_ok 现算（见 store.TVChannelProbeStats），
	// 所以刷新页面、重启进程都不会把进度归零。
	tvProbe tvProbeState

	// baseCtx 是服务的生命周期 context（后台任务用，见 SetBaseContext）。
	baseCtx context.Context

	// overlay 是只读媒体库的写入层（库级：元数据快照 + 刮削到的图片）。
	// 可为 nil（未接入时相关统计不显示）。
	overlay *overlay.Service
}

// newScanManager 构造扫描管理器并注入 config 里的小旋钮。
func newScanManager(cfg *config.Config, st *store.Store, log *slog.Logger) *scan.Manager {
	m := scan.NewManager(st, log)
	// 0 时留给 scanner 的内置默认（见 scanner.defaultMinFileSize）
	m.SetMinFileSize(cfg.Scan.MinFileSize)
	return m
}

// 不接时相关界面只显示只读开关与说明，不显示叠加层占用。
func (s *Server) SetOverlay(o *overlay.Service) { s.overlay = o }

// New 构造 Server。
func New(cfg *config.Config, st *store.Store, log *slog.Logger, ff ffmpeg.Info, img *images.Service,
	scraper *scrape.Handler, settingsSvc *settings.Service, meta provider.Client,
	streams *stream.Manager, encoders *encoder.Store, signer livetv.Signer) *Server {
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
		scans:    newScanManager(cfg, st, log),
		streams:  streams,
		plays:    newPlayRegistry(),
		subs:     newSubtitleJobs(),
		encoders: encoders,
		live: livetvsync.New(st, livetvsync.Options{
			ProbePath:        cfg.FFmpeg.ProbePath,
			ProbeTimeout:     time.Duration(cfg.LiveTV.ProbeTimeoutSeconds) * time.Second,
			ProbeConcurrency: cfg.LiveTV.ProbeConcurrency,
			Logger:           log,
		}),
		liveSigner: signer,
		livePlays:  newLivePlayRegistry(),
	}
}

// SetBaseContext 把服务的生命周期 context 交给 Server。
//
// 后台任务（直播频道探测）不能挂在某一次 HTTP 请求上 —— 请求一返回 ctx 就取消了，
// 探测会刚起步就被掐掉。没设置时后台任务用 context.Background()。
func (s *Server) SetBaseContext(ctx context.Context) { s.baseCtx = ctx }

// base 返回后台任务用的 context。
func (s *Server) base() context.Context {
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
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
	// 只读库的刮削产物（overlay）：清空 + 看板统计
	mux.Handle("DELETE /api/v1/libraries/{id}/overlay", s.requireAuth(s.handleClearLibraryOverlay))
	mux.Handle("GET /api/v1/overlay", s.requireAuth(s.handleOverlayStats))
	mux.Handle("POST /api/v1/libraries/{id}/scan", s.requireAuth(s.handleStartScan))
	mux.Handle("DELETE /api/v1/libraries/{id}/scan", s.requireAuth(s.handleCancelScan))
	mux.Handle("GET /api/v1/libraries/{id}/scan", s.requireAuth(s.handleScanStatus))
	mux.Handle("GET /api/v1/libraries/{id}/items", s.requireAuth(s.handleListItems))

	// ---- 设置（管理员）----
	mux.Handle("GET /api/v1/settings", s.requireAdmin(s.handleGetSettings))
	mux.Handle("PUT /api/v1/settings/tmdb", s.requireAdmin(s.handleUpdateTMDBSettings))
	mux.Handle("DELETE /api/v1/settings/tmdb", s.requireAdmin(s.handleResetTMDBSettings))
	mux.Handle("POST /api/v1/provider/test", s.requireAdmin(s.handleTestProvider))

	// ---- 收藏（按账号；「收藏数」是全站视角）----
	mux.Handle("GET /api/v1/favorites", s.requireAuth(s.handleListFavorites))
	mux.Handle("GET /api/v1/items/{id}/favorite", s.requireAuth(s.handleGetFavorite))
	mux.Handle("POST /api/v1/items/{id}/favorite", s.requireAuth(s.handleSetFavorite))

	// ---- 播放列表 / 合集 ----
	mux.Handle("GET /api/v1/playlists", s.requireAuth(s.handleListPlaylists))
	mux.Handle("POST /api/v1/playlists", s.requireAuth(s.handleCreatePlaylist))
	mux.Handle("GET /api/v1/playlists/{id}", s.requireAuth(s.handleGetPlaylist))
	mux.Handle("PATCH /api/v1/playlists/{id}", s.requireAuth(s.handleUpdatePlaylist))
	mux.Handle("DELETE /api/v1/playlists/{id}", s.requireAuth(s.handleDeletePlaylist))
	mux.Handle("GET /api/v1/playlists/{id}/items", s.requireAuth(s.handleListPlaylistItems))
	mux.Handle("POST /api/v1/playlists/{id}/items", s.requireAuth(s.handleAddPlaylistItems))
	mux.Handle("PUT /api/v1/playlists/{id}/items", s.requireAuth(s.handleReorderPlaylist))
	mux.Handle("DELETE /api/v1/playlists/{id}/items/{itemId}", s.requireAuth(s.handleRemovePlaylistItem))
	mux.Handle("GET /api/v1/playlists/{id}/neighbors", s.requireAuth(s.handlePlaylistNeighbors))

	// ---- 首页（轮播 + 继续观看 + 推荐行，M6）----
	mux.Handle("GET /api/v1/home", s.requireAuth(s.handleHome))

	// ---- 搜索（标题 / 原始标题 / 人名，中文二元组）----
	// 结果与分面分开：分面只在「词或筛选变了」时需要重算，翻页不必跟着算（见 internal/api/search.go）
	mux.Handle("GET /api/v1/search", s.requireAuth(s.handleSearch))
	mux.Handle("GET /api/v1/search/facets", s.requireAuth(s.handleSearchFacets))
	mux.Handle("GET /api/v1/search/people", s.requireAuth(s.handleSearchPeople))
	mux.Handle("GET /api/v1/search/suggest", s.requireAuth(s.handleSearchSuggest))

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

	// ---- 详情页（M6）：演职员与相关推荐 ----
	mux.Handle("GET /api/v1/items/{id}/people", s.requireAuth(s.handleItemPeople))
	mux.Handle("GET /api/v1/items/{id}/related", s.requireAuth(s.handleItemRelated))

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

	// ---- 直播电视（M5）----
	// 频道列表与播放是普通登录用户就能用；源的增删改（会把频道表整体改动的操作）限管理员。
	mux.Handle("GET /api/v1/livetv/channels", s.requireAuth(s.handleListTVChannels))
	mux.Handle("GET /api/v1/livetv/channels/{id}", s.requireAuth(s.handleGetTVChannel))
	mux.Handle("PATCH /api/v1/livetv/channels/{id}", s.requireAuth(s.handleUpdateTVChannel))
	mux.Handle("POST /api/v1/livetv/channels/{id}/favorite", s.requireAuth(s.handleToggleTVFavorite))
	// 频道探测（失效源标记）：真连一次源站，把结果写回 probe / probe_ok / probe_at。
	// 会真的去连几百个源站，所以只给管理员；查进度普通用户也能看。
	mux.Handle("GET /api/v1/livetv/channels/probe", s.requireAuth(s.handleTVProbeStatus))
	mux.Handle("POST /api/v1/livetv/channels/probe", s.requireAdmin(s.handleStartTVProbe))
	mux.Handle("GET /api/v1/livetv/export.m3u", s.requireAuth(s.handleExportTVM3U))
	mux.Handle("GET /api/v1/livetv/sources", s.requireAuth(s.handleListTVSources))
	mux.Handle("POST /api/v1/livetv/sources", s.requireAdmin(s.handleCreateTVSource))
	mux.Handle("PATCH /api/v1/livetv/sources/{id}", s.requireAdmin(s.handleUpdateTVSource))
	mux.Handle("DELETE /api/v1/livetv/sources/{id}", s.requireAdmin(s.handleDeleteTVSource))
	mux.Handle("POST /api/v1/livetv/sources/{id}/refresh", s.requireAdmin(s.handleRefreshTVSource))
	// 直播播放（M5）：同一频道所有观众共用一路 ffmpeg。
	mux.Handle("POST /api/v1/livetv/channels/{id}/play", s.requireAuth(s.handleStartLivePlay))
	mux.Handle("POST /api/v1/livetv/channels/{id}/share", s.requireAuth(s.handleShareLiveChannel))
	mux.Handle("GET /api/v1/livetv/sessions", s.requireAuth(s.handleListLiveSessions))
	// 分片路由必须与播放列表同级：m3u8 里写的是相对文件名，客户端会拿
	// 播放列表的 URL 当基准去拼（与点播那边同一个坑，见 handlePlayStream 的注释）。
	mux.Handle("GET /api/v1/live/{sid}/index.m3u8", s.requireAuth(s.handleLivePlaylist))
	mux.Handle("GET /api/v1/live/{sid}/{name}", s.requireAuth(s.handleLivePlaySegment))
	mux.Handle("POST /api/v1/live/{sid}/stop", s.requireAuth(s.handleStopLivePlay))
	// 外链出口（无需登录，靠签名 token）：给 VLC / 手机播放器用。
	mux.HandleFunc("GET /s/{token}/playlist.m3u", s.handleSharedLivePlaylist)
	mux.HandleFunc("GET /s/{token}/{name}", s.handleSharedLiveSegment)

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
