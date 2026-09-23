package api

import (
	"net/http"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/settings"
)

// 本文件是设置页的接口（管理员专属）。
//
// 目前只开放一项：TMDB 凭据。以前修改它必须编辑 config.toml 再重启，
// 现在可以在界面上填、存进数据库、**立刻生效**。
//
// 一条硬规则：**密钥永远不回显**。GET 只回 hasReadToken / hasApiKey 这样的布尔，
// 前端把它渲染成「已设置（要替换就输入新的）」。

// handleGetSettings 返回设置页要的全部内容（不含任何密钥）。
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		s.serverError(w, "设置服务未初始化", nil)
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	info, err := s.store.SystemInfoOf(ctx)
	if err != nil {
		s.serverError(w, "读取系统信息失败", err)
		return
	}

	cur := s.settings.TMDB()
	fallback := s.settings.Fallback()
	writeJSON(w, http.StatusOK, map[string]any{
		"tmdb": map[string]any{
			"configured":   cur.Configured(),
			"hasReadToken": cur.ReadToken != "",
			"hasApiKey":    cur.APIKey != "",
			"language":     cur.Language,
			// 值来自数据库还是配置文件 —— 界面上要显示出来（答案影响行为）
			"fromDb":    cur.FromDB,
			"encrypted": s.settings.Encrypted(),
			"fallback": map[string]any{
				"hasReadToken": fallback.ReadToken != "",
				"hasApiKey":    fallback.APIKey != "",
				"language":     fallback.Language,
			},
		},
		"system": info,
	})
}

// tmdbSettingsRequest 是保存 TMDB 设置的请求体。
//
// 三个字段都是**指针**：nil = 不改（界面上没动这一项），指向空串 = 清掉。
// 密钥类字段必须能区分这两种意图，因为界面不可能回显原来的密钥。
type tmdbSettingsRequest struct {
	ReadToken *string `json:"readToken"`
	APIKey    *string `json:"apiKey"`
	Language  *string `json:"language"`
}

// handleUpdateTMDBSettings 保存 TMDB 凭据（写库 + 立刻生效，不必重启）。
func (s *Server) handleUpdateTMDBSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		s.serverError(w, "设置服务未初始化", nil)
		return
	}
	var req tmdbSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ReadToken == nil && req.APIKey == nil && req.Language == nil {
		writeError(w, http.StatusBadRequest, "没有需要更新的字段")
		return
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	next, langChanged, err := s.settings.Apply(ctx, settings.Patch{
		ReadToken: req.ReadToken,
		APIKey:    req.APIKey,
		Language:  req.Language,
	})
	if err != nil {
		s.serverError(w, "保存 TMDB 设置失败", err)
		return
	}

	// 换了语言就把旧语言的缓存清掉：否则要等 TTL 过期才看得到新语言的元数据，
	// 用户会以为「改了没用」。
	cleared := int64(0)
	if langChanged {
		if n, err := s.store.ClearProviderCache(ctx, "tmdb"); err != nil {
			s.log.Warn("清理旧语言的元数据缓存失败", "err", err)
		} else {
			cleared = n
		}
	}

	// 审计只记「改了哪些开关、语言是什么」——**凭据本身（ReadToken/APIKey）不落审计**。
	s.audit(r.Context(), r, "settings.update", "tmdb", map[string]any{
		"configured": next.Configured(), "language": next.Language,
		"hasReadToken": next.ReadToken != "", "hasApiKey": next.APIKey != "",
		"clearedCache": cleared,
	})
	s.log.Info("已保存 TMDB 设置",
		"language", next.Language, "configured", next.Configured(),
		"langChanged", langChanged, "clearedCache", cleared, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"tmdb": map[string]any{
			"configured":   next.Configured(),
			"hasReadToken": next.ReadToken != "",
			"hasApiKey":    next.APIKey != "",
			"language":     next.Language,
			"fromDb":       next.FromDB,
			"encrypted":    s.settings.Encrypted(),
		},
		"clearedCache": cleared,
	})
}

// handleResetTMDBSettings 删掉数据库里的 TMDB 设置，回落到配置文件的值。
func (s *Server) handleResetTMDBSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		s.serverError(w, "设置服务未初始化", nil)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	cur, err := s.settings.Reset(ctx)
	if err != nil {
		s.serverError(w, "恢复为配置文件的值失败", err)
		return
	}
	s.log.Info("已恢复 TMDB 设置为配置文件的值",
		"language", cur.Language, "configured", cur.Configured(), "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"tmdb": map[string]any{
			"configured":   cur.Configured(),
			"hasReadToken": cur.ReadToken != "",
			"hasApiKey":    cur.APIKey != "",
			"language":     cur.Language,
			"fromDb":       cur.FromDB,
		},
	})
}
