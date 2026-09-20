package api

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/auth"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

type ctxKey int

const authCtxKey ctxKey = iota

// authContext 是挂在 request context 上的登录态。
type authContext struct {
	Session *store.Session
	User    *store.User
}

// currentAuth 取出登录态。只应在 requireAuth 保护的 handler 里调用。
func currentAuth(r *http.Request) *authContext {
	a, _ := r.Context().Value(authCtxKey).(*authContext)
	return a
}

// requireAuth 校验会话 Cookie，把登录态注入 context。
func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := readSessionCookie(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "未登录")
			return
		}

		id := auth.HashSessionToken(token)
		sess, user, err := s.store.GetSessionUser(r.Context(), id)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("读取会话失败", "err", err)
			}
			clearSessionCookie(w, s.cfg.SecureCookies)
			writeError(w, http.StatusUnauthorized, "会话已失效，请重新登录")
			return
		}

		// 每 5 分钟才真正写一次库，避免每个请求都写 sessions 表。
		if err := s.store.TouchSessionIfStale(r.Context(), id, 5*time.Minute); err != nil {
			s.log.Warn("刷新会话活跃时间失败", "err", err)
		}

		ctx := context.WithValue(r.Context(), authCtxKey, &authContext{Session: sess, User: user})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder 记录响应状态码与字节数，供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests 输出访问日志（含耗时与状态码）。
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		level := s.log.Info
		if status >= 500 {
			level = s.log.Error
		}
		level("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"ip", clientIP(r),
		)
	})
}

// recoverPanic 兜住 panic，避免单个请求拖垮整个进程。
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("请求处理 panic",
					"err", rec,
					"path", r.URL.Path,
					"stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders 设置一组通用安全响应头。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
