// Package api 实现 LMBY 的 HTTP 接口与前端静态资源服务。
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/config"
	"github.com/hakureiyuyuko/lmby/internal/ffmpeg"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// Server 持有处理请求所需的全部依赖。
type Server struct {
	cfg     *config.Config
	store   *store.Store
	log     *slog.Logger
	ffmpeg  ffmpeg.Info
	started time.Time
	limiter *loginLimiter
}

// New 构造 Server。
func New(cfg *config.Config, st *store.Store, log *slog.Logger, ff ffmpeg.Info) *Server {
	return &Server{
		cfg:     cfg,
		store:   st,
		log:     log,
		ffmpeg:  ff,
		started: time.Now(),
		limiter: newLoginLimiter(8, 15*time.Minute),
	}
}

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
