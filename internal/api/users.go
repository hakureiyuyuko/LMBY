package api

// 用户管理（管理员）。设计见 docs/notes/users-permissions.md。
//
// 三条硬规则（都有测试）：
//
//  1. 只有管理员能碰这些接口；
//  2. **不能改自己的管理员位 / 禁用位**（防手一滑把自己锁在外面）；
//  3. **不能把最后一个活跃管理员弄没**（禁用 / 降级 / 删除都算）——
//     这条在 store 里兜底（ErrLastAdmin），这里只把它翻成人话。
//
// 另外：改口令、禁用、收紧库白名单之后**立刻吊销那个用户的会话** ——
// 否则旧 token 还能照旧看片子，权限改了等于没改。

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/auth"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// userView 是下发给界面的用户信息（**绝不带口令哈希**）。
type userView struct {
	ID                   int64   `json:"id"`
	Username             string  `json:"username"`
	DisplayName          string  `json:"displayName"`
	IsAdmin              bool    `json:"isAdmin"`
	IsDisabled           bool    `json:"isDisabled"`
	MaxConcurrentStreams int32   `json:"maxConcurrentStreams"`
	RestrictedLibraries  bool    `json:"restrictedLibraries"`
	LibraryIDs           []int64 `json:"libraryIds"`
	AllowTranscode       bool    `json:"allowTranscode"`
	AllowLiveTV          bool    `json:"allowLiveTV"`
	LastLoginAt          string  `json:"lastLoginAt,omitempty"`
	CreatedAt            string  `json:"createdAt"`
	ActiveSessions       int     `json:"activeSessions"`
}

func (s *Server) userViews(r *http.Request, users []store.User) ([]userView, error) {
	out := make([]userView, 0, len(users))
	for i := range users {
		u := &users[i]
		ids, err := s.store.ListUserLibraryIDs(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			ids = []int64{}
		}
		sessions, err := s.store.CountUserSessions(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		v := userView{
			ID:                   u.ID,
			Username:             u.Username,
			DisplayName:          u.DisplayName,
			IsAdmin:              u.IsAdmin,
			IsDisabled:           u.IsDisabled,
			MaxConcurrentStreams: u.MaxConcurrentStreams,
			RestrictedLibraries:  u.RestrictedLibraries,
			LibraryIDs:           ids,
			AllowTranscode:       u.AllowTranscode,
			AllowLiveTV:          u.AllowLiveTV,
			CreatedAt:            u.CreatedAt.Format(timeLayout),
			ActiveSessions:       sessions,
		}
		if u.LastLoginAt != nil {
			v.LastLoginAt = u.LastLoginAt.Format(timeLayout)
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, "读取用户列表失败", err)
		return
	}
	views, err := s.userViews(r, users)
	if err != nil {
		s.serverError(w, "读取用户权限失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": views})
}

type createUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	IsAdmin     bool   `json:"isAdmin"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || strings.ContainsAny(username, " \t\n") {
		writeError(w, http.StatusBadRequest, "用户名不能为空、不能带空格")
		return
	}
	if err := validatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}
	hash, err := auth.HashPassword(req.Password, auth.DefaultParams)
	if err != nil {
		s.serverError(w, "生成口令哈希失败", err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), username, displayName, hash, req.IsAdmin)
	if err != nil {
		// store 层用 pg 的唯一约束报冲突，这里按错误文本判一下（避免为它多开一个错误类型）
		if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			writeError(w, http.StatusConflict, "用户名已被占用")
			return
		}
		s.serverError(w, "创建用户失败", err)
		return
	}
	s.audit(r.Context(), r, "user.create", "user:"+u.Username, "ok",
		map[string]any{"username": u.Username, "isAdmin": u.IsAdmin})
	views, err := s.userViews(r, []store.User{*u})
	if err != nil {
		s.serverError(w, "读取用户权限失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": views[0]})
}

type updateUserRequest struct {
	DisplayName          *string `json:"displayName"`
	IsAdmin              *bool   `json:"isAdmin"`
	IsDisabled           *bool   `json:"isDisabled"`
	MaxConcurrentStreams *int32  `json:"maxConcurrentStreams"`
	RestrictedLibraries  *bool   `json:"restrictedLibraries"`
	AllowTranscode       *bool   `json:"allowTranscode"`
	AllowLiveTV          *bool   `json:"allowLiveTV"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	actor := currentAuth(r).User
	// 规则 2：不能改自己的管理员位 / 禁用位（其余字段改自己没关系）
	if actor.ID == id {
		if req.IsAdmin != nil && *req.IsAdmin != actor.IsAdmin {
			writeError(w, http.StatusBadRequest, "不能修改自己的管理员权限（请让另一个管理员来改）")
			return
		}
		if req.IsDisabled != nil && *req.IsDisabled {
			writeError(w, http.StatusBadRequest, "不能禁用自己的账号")
			return
		}
	}
	if req.MaxConcurrentStreams != nil && (*req.MaxConcurrentStreams < 0 || *req.MaxConcurrentStreams > 64) {
		writeError(w, http.StatusBadRequest, "并发流上限要在 0~64 之间（0 = 用全局默认）")
		return
	}
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if name == "" {
			writeError(w, http.StatusBadRequest, "显示名不能为空")
			return
		}
		req.DisplayName = &name
	}

	u, err := s.store.UpdateUser(r.Context(), id, store.UserPatch{
		DisplayName:          req.DisplayName,
		IsAdmin:              req.IsAdmin,
		IsDisabled:           req.IsDisabled,
		MaxConcurrentStreams: toIntPtr(req.MaxConcurrentStreams),
		RestrictedLibraries:  req.RestrictedLibraries,
		AllowTranscode:       req.AllowTranscode,
		AllowLiveTV:          req.AllowLiveTV,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrLastAdmin):
			writeError(w, http.StatusBadRequest, "这是最后一个管理员，不能禁用或降级（先指定另一个管理员）")
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "用户不存在")
		default:
			s.serverError(w, "修改用户失败", err)
		}
		return
	}
	// 被禁用 → 立刻把这个人踢下线（否则旧 token 还能继续用）
	if u.IsDisabled {
		if err := s.store.DeleteUserSessions(r.Context(), u.ID); err != nil {
			s.serverError(w, "吊销会话失败", err)
			return
		}
	}
	s.audit(r.Context(), r, "user.update", "user:"+u.Username, "ok",
		map[string]any{"isAdmin": u.IsAdmin, "disabled": u.IsDisabled})
	views, err := s.userViews(r, []store.User{*u})
	if err != nil {
		s.serverError(w, "读取用户权限失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": views[0]})
}

type userLibrariesRequest struct {
	LibraryIDs []int64 `json:"libraryIds"`
}

// handleSetUserLibraries 整份替换可见库（白名单语义，与媒体库路径一致）。
func (s *Server) handleSetUserLibraries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req userLibrariesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// 校验每个库真的存在（不然界面上会出现「幽灵库」授权）
	for _, libID := range req.LibraryIDs {
		if _, err := s.store.FindLibraryByID(r.Context(), libID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusBadRequest, "要授权的媒体库不存在")
				return
			}
			s.serverError(w, "校验媒体库失败", err)
			return
		}
	}
	if err := s.store.ReplaceUserLibraries(r.Context(), id, req.LibraryIDs); err != nil {
		s.serverError(w, "保存库授权失败", err)
		return
	}
	// 收紧可见范围 → 立刻吊销会话：否则他手里的旧 token 还带着旧的可见集合
	if err := s.store.DeleteUserSessions(r.Context(), id); err != nil {
		s.serverError(w, "吊销会话失败", err)
		return
	}
	u, err := s.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "用户不存在")
		return
	}
	s.audit(r.Context(), r, "user.libraries", "user:"+u.Username, "ok",
		map[string]any{"count": len(req.LibraryIDs)})
	views, err := s.userViews(r, []store.User{*u})
	if err != nil {
		s.serverError(w, "读取用户权限失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": views[0]})
}

type setPasswordRequest struct {
	Password string `json:"password"`
}

// handleSetUserPassword 管理员重置某个用户的口令（重置即吊销其全部会话）。
func (s *Server) handleSetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req setPasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password, auth.DefaultParams)
	if err != nil {
		s.serverError(w, "生成口令哈希失败", err)
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), id, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "用户不存在")
			return
		}
		s.serverError(w, "重置口令失败", err)
		return
	}
	// 口令**绝不进审计**：只记「谁给谁重置了口令」这个事实（重置已吊销其全部会话）。
	s.audit(r.Context(), r, "user.password_reset", fmt.Sprintf("user:%d", id), "ok", nil)
	if err := s.store.DeleteUserSessions(r.Context(), id); err != nil {
		s.serverError(w, "吊销会话失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteUser 删用户（连带收藏 / 播放列表 / 观看进度）。
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if actor := currentAuth(r).User; actor.ID == id {
		writeError(w, http.StatusBadRequest, "不能删自己的账号")
		return
	}
	err := s.store.DeleteUser(r.Context(), id)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, http.StatusBadRequest, "这是最后一个管理员，删了就没人管得了服务器")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "用户不存在")
		return
	default:
		s.serverError(w, "删除用户失败", err)
		return
	}
	s.audit(r.Context(), r, "user.delete", fmt.Sprintf("user:%d", id), "ok", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func toIntPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}

// timeLayout 是下发时间用的格式（与其它接口一致：RFC3339）。
const timeLayout = "2006-01-02T15:04:05Z07:00"
