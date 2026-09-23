package api

// 管理 API 密钥：给 bot / 脚本 / 自动化用的长期凭据（用户管理接口）。
//
// 与登录会话的关系：会话是「人」在用 —— cookie 跟着浏览器、会过期、改口令就被吊销；
// 机器需要的是「放在 Header 里的长期凭据」。所以这里有一种**独立**的认证方式：
//
//	Authorization: Bearer <密钥>     或     X-API-Key: <密钥>
//
// **最小权限是刻意的**：这把钥匙只在「用户管理」那几个路径上有效。
// 泄露时最坏情况是「乱建/乱删账号」，而不是「改设置、删媒体库、读别人库里的内容」。
// 想要更大的权限，就该给它一个真正的管理员账号，而不是把钥匙越配越大。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/settings"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// botCtxKeyType 是「这次请求由管理密钥认证」的 context 键。
type botCtxKeyType struct{}

var botCtxKey botCtxKeyType

// botActorName 是审计里管理密钥的「操作者」名（这样一眼能看出是程序干的）。
const botActorName = "api-token"

// botKeyPrefix 是密钥的明文前缀（便于人肉辨认「这是 LMBY 的密钥」）。
const botKeyPrefix = "lmby_"

// withBotAuth 认出管理密钥并限制在白名单路径上。
//
// 挂在 requireAuth 之前：带密钥的请求**不需要**会话；没带密钥的请求原样往下走
// （继续由 requireAuth 要求登录）。
func (s *Server) withBotAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := botKeyFromRequest(r)
		if key == "" || s.settings == nil {
			next.ServeHTTP(w, r)
			return
		}
		ok, err := s.settings.VerifyBotKey(r.Context(), key)
		if err != nil {
			s.log.Error("校验管理密钥失败", "err", err)
			writeError(w, http.StatusInternalServerError, "校验管理密钥失败")
			return
		}
		if !ok {
			s.log.Warn("管理密钥不正确", "ip", clientIP(r), "path", r.URL.Path)
			writeError(w, http.StatusUnauthorized, "管理密钥不正确")
			return
		}
		if !botAllowedPath(r) {
			writeError(w, http.StatusForbidden, "这个管理密钥只能用于用户管理接口")
			return
		}
		// 注入一个「系统操作者」：让 requireAdmin、审计与各 handler 都能照常工作。
		// ID=0 与真实用户不冲突，「不能改自己」这类判断对它自然不成立。
		ctx := context.WithValue(r.Context(), botCtxKey, true)
		ctx = context.WithValue(ctx, authCtxKey, &authContext{
			User: &store.User{ID: 0, Username: botActorName, DisplayName: "API 密钥", IsAdmin: true},
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// botKeyFromRequest 从 Authorization: Bearer 或 X-API-Key 取密钥。
func botKeyFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-API-Key")); v != "" {
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// botAllowedPath 是管理密钥能开的门：**只有用户管理**。
//
//	GET    /api/v1/users
//	POST   /api/v1/users
//	GET    /api/v1/users/{id}
//	PATCH  /api/v1/users/{id}
//	DELETE /api/v1/users/{id}
//	PUT    /api/v1/users/{id}/libraries
//	POST   /api/v1/users/{id}/password
func botAllowedPath(r *http.Request) bool {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if p == "/api/v1/users" {
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	rest, ok := strings.CutPrefix(p, "/api/v1/users/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		return r.Method == http.MethodGet || r.Method == http.MethodPatch || r.Method == http.MethodDelete
	case 2:
		return (parts[1] == "libraries" && r.Method == http.MethodPut) ||
			(parts[1] == "password" && r.Method == http.MethodPost)
	}
	return false
}

// ---- 密钥的生成 / 查看 / 撤销（管理员，会话认证） ----

// handleGetBotKey 返回当前密钥的元信息（**明文只在生成时出现一次**）。
func (s *Server) handleGetBotKey(w http.ResponseWriter, r *http.Request) {
	info, err := s.settings.BotKey(r.Context())
	if err != nil {
		if errors.Is(err, settings.ErrNoBotKey) {
			writeJSON(w, http.StatusOK, map[string]any{"configured": false})
			return
		}
		s.serverError(w, "读取管理密钥失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true,
		"prefix":     info.Prefix,
		"createdAt":  info.CreatedAt,
	})
}

// handleCreateBotKey 生成（或轮换）一把管理密钥：**明文只在这里返回一次**。
func (s *Server) handleCreateBotKey(w http.ResponseWriter, r *http.Request) {
	key, err := newBotKey()
	if err != nil {
		s.serverError(w, "生成管理密钥失败", err)
		return
	}
	if err := s.settings.SetBotKey(r.Context(), key); err != nil {
		s.serverError(w, "保存管理密钥失败", err)
		return
	}
	// 审计只记「生成了/轮换了一把」，**不记密钥本身**。
	s.audit(r.Context(), r, "settings.bot_key_created", "bot_api_key",
		map[string]any{"prefix": key[:len(botKeyPrefix)+8]})
	writeJSON(w, http.StatusOK, map[string]any{
		"key":    key,
		"prefix": key[:len(botKeyPrefix)+8],
		"note":   "这串密钥只显示这一次；请立刻保存到 bot 的配置里",
	})
}

// handleDeleteBotKey 撤销管理密钥（立刻失效）。
func (s *Server) handleDeleteBotKey(w http.ResponseWriter, r *http.Request) {
	existed, err := s.settings.ClearBotKey(r.Context())
	if err != nil {
		s.serverError(w, "撤销管理密钥失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": existed})
	if existed {
		s.audit(r.Context(), r, "settings.bot_key_revoked", "bot_api_key", nil)
	}
}

// newBotKey 生成一把密钥：前缀 + 32 字节随机（base64url）。
func newBotKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("读取随机数失败: %w", err)
	}
	return botKeyPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
