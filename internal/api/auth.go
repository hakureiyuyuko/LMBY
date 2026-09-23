package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/auth"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

const (
	minPasswordLength = 8
	maxPasswordLength = 256
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,31}$`)

// sessionUserResponse 是「当前登录用户」的响应体。
type sessionUserResponse struct {
	User        *store.User        `json:"user"`
	Preferences *store.Preferences `json:"preferences"`
}

// ---------------------------------------------------------------- 初始化

type setupRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// handleSetup 创建初始管理员并直接登录。系统已有账号时返回 409。
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	username := strings.TrimSpace(req.Username)
	if err := validateUsername(username); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

	// CreateFirstAdmin 内部保证「系统无用户」这一条件，天然防并发重复初始化。
	u, err := s.store.CreateFirstAdmin(r.Context(), username, displayName, hash)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "系统已完成初始化，无法重复创建管理员")
			return
		}
		s.serverError(w, "创建管理员失败", err)
		return
	}

	s.log.Info("已创建初始管理员", "username", u.Username)
	if !s.startSession(w, r, u) {
		return
	}
	s.writeSessionUser(w, r, http.StatusCreated, u)
}

// ---------------------------------------------------------------- 登录/登出

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin 校验口令并建立会话。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "请输入用户名和口令")
		return
	}

	limitKey := strings.ToLower(username) + "|" + clientIP(r)
	if ok, retryAfter := s.limiter.Allow(limitKey); !ok {
		seconds := int(retryAfter.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("登录尝试过于频繁，请 %d 秒后再试", seconds))
		return
	}

	u, err := s.store.GetUserByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 关键：用户不存在时也做一次等价的口令哈希运算，抹平响应时间差异。
			auth.DummyVerify(req.Password)
			s.limiter.Fail(limitKey)
			s.auditNamed(r.Context(), r, username, "auth.login", "", "failed",
				map[string]any{"reason": "用户名不存在"})
			writeError(w, http.StatusUnauthorized, "用户名或口令不正确")
			return
		}
		s.serverError(w, "查询账号失败", err)
		return
	}

	if u.IsDisabled {
		s.limiter.Fail(limitKey)
		s.auditNamed(r.Context(), r, u.Username, "auth.login", "user:"+u.Username, "failed",
			map[string]any{"reason": "账号已禁用"})
		writeError(w, http.StatusUnauthorized, "该账号已被禁用")
		return
	}

	ok, needsRehash, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil {
		s.serverError(w, "校验口令失败", err)
		return
	}
	if !ok {
		s.limiter.Fail(limitKey)
		s.log.Warn("登录失败", "username", u.Username, "ip", clientIP(r))
		s.auditNamed(r.Context(), r, u.Username, "auth.login", "user:"+u.Username, "failed",
			map[string]any{"reason": "口令不正确"})
		writeError(w, http.StatusUnauthorized, "用户名或口令不正确")
		return
	}
	s.limiter.Reset(limitKey)
	// 登录成功也进审计：失败那条记的是「谁尝试了」，这条记的是「谁进来了」。
	{
		uid := u.ID
		s.writeAudit(r.Context(), store.AuditEntry{
			ActorID: &uid, ActorName: u.Username, Action: "auth.login",
			Target: "user:" + u.Username, Result: "ok", IP: clientIP(r),
		})
	}

	// 代价参数升级后静默重算，用户无感。
	if needsRehash {
		if h, err := auth.HashPassword(req.Password, auth.DefaultParams); err == nil {
			if err := s.store.UpdateUserPassword(r.Context(), u.ID, h); err != nil {
				s.log.Warn("重算口令哈希失败", "err", err)
			}
		}
	}

	if err := s.store.TouchUserLogin(r.Context(), u.ID, clientIP(r)); err != nil {
		s.log.Warn("更新登录记录失败", "err", err)
	}
	s.log.Info("登录成功", "username", u.Username, "ip", clientIP(r))

	if !s.startSession(w, r, u) {
		return
	}
	s.writeSessionUser(w, r, http.StatusOK, u)
}

// handleLogout 撤销当前会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	if err := s.store.RevokeSession(r.Context(), a.Session.ID); err != nil {
		s.serverError(w, "登出失败", err)
		return
	}
	clearSessionCookie(w, s.cfg.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- 个人中心

// handleMe 返回当前用户与偏好。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	s.writeSessionUser(w, r, http.StatusOK, a.User)
}

// 说明：/me 的用户对象里带上 allowLiveTV / allowTranscode / restrictedLibraries
// （见 writeSessionUser → sessionUser 视图），前端据此隐藏「直播」导航、
// 在播放器里给出「不允许转码」的提示 —— 界面上不该出现点了就报 403 的入口。

type updateProfileRequest struct {
	DisplayName *string `json:"displayName"`
}

// handleUpdateProfile 修改显示名。
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	var req updateProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if len([]rune(name)) > 64 {
			writeError(w, http.StatusBadRequest, "显示名过长（最多 64 个字符）")
			return
		}
		if err := s.store.UpdateUserDisplayName(r.Context(), a.User.ID, name); err != nil {
			s.serverError(w, "更新显示名失败", err)
			return
		}
		a.User.DisplayName = name
	}
	s.writeSessionUser(w, r, http.StatusOK, a.User)
}

// handleUpdatePreferences 覆盖写入偏好（主题、语言等）。
func (s *Server) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	var req store.Preferences
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Theme != "" && !store.ValidTheme(req.Theme) {
		writeError(w, http.StatusBadRequest, "主题只能是 light / dark / system")
		return
	}
	if err := s.store.UpsertPreferences(r.Context(), a.User.ID, req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeSessionUser(w, r, http.StatusOK, a.User)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleChangePassword 修改自己的口令。
//
// 安全要点：
//   - 必须验证旧口令；
//   - 改完撤销其它设备上的会话，只保留当前这一个。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "请填写当前口令与新口令")
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CurrentPassword == req.NewPassword {
		writeError(w, http.StatusBadRequest, "新口令不能与当前口令相同")
		return
	}

	ok, _, err := auth.VerifyPassword(req.CurrentPassword, a.User.PasswordHash)
	if err != nil {
		s.serverError(w, "校验当前口令失败", err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "当前口令不正确")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword, auth.DefaultParams)
	if err != nil {
		s.serverError(w, "生成口令哈希失败", err)
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), a.User.ID, hash); err != nil {
		s.serverError(w, "更新口令失败", err)
		return
	}

	revoked, err := s.store.RevokeOtherSessions(r.Context(), a.User.ID, a.Session.ID)
	if err != nil {
		s.log.Warn("撤销其他会话失败", "err", err)
	}
	s.log.Info("口令已修改", "username", a.User.Username, "revokedSessions", revoked)
	s.audit(r.Context(), r, "auth.password_change", "user:"+a.User.Username,
		map[string]any{"revokedSessions": revoked})

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"revokedSessions": revoked,
	})
}

// handleListSessions 列出自己的有效会话（「我的设备」）。
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	sessions, err := s.store.ListSessions(r.Context(), a.User.ID)
	if err != nil {
		s.serverError(w, "查询会话列表失败", err)
		return
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == a.Session.ID
	}
	if sessions == nil {
		sessions = []store.Session{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handleRevokeSession 撤销自己的某一条会话。
func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	a := currentAuth(r)
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "缺少会话 ID")
		return
	}

	// 撤销的是当前会话时，等同于登出：顺手清掉 Cookie。
	isCurrent := id == a.Session.ID

	ok, err := s.store.RevokeSessionForUser(r.Context(), a.User.ID, id)
	if err != nil {
		s.serverError(w, "撤销会话失败", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "会话不存在或已失效")
		return
	}
	if isCurrent {
		clearSessionCookie(w, s.cfg.SecureCookies)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- 工具

// startSession 生成会话并写入 Cookie；失败时已经写过响应。
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) bool {
	plain, hash, err := auth.NewSessionToken()
	if err != nil {
		s.serverError(w, "生成会话令牌失败", err)
		return false
	}
	ttl := time.Duration(s.cfg.SessionTTLHours) * time.Hour
	if err := s.store.CreateSession(r.Context(), hash, u.ID, ttl, r.UserAgent(), clientIP(r)); err != nil {
		s.serverError(w, "创建会话失败", err)
		return false
	}
	setSessionCookie(w, plain, time.Now().Add(ttl), s.cfg.SecureCookies)
	return true
}

// writeSessionUser 输出用户 + 偏好。
func (s *Server) writeSessionUser(w http.ResponseWriter, r *http.Request, status int, u *store.User) {
	prefs, err := s.store.GetPreferences(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, "读取用户偏好失败", err)
		return
	}
	writeJSON(w, status, sessionUserResponse{User: u, Preferences: prefs})
}

func validateUsername(v string) error {
	if !usernamePattern.MatchString(v) {
		return errors.New("用户名需为 3-32 位，以字母或数字开头，只能包含字母、数字、点、下划线、连字符")
	}
	return nil
}

func validatePassword(v string) error {
	if len([]rune(v)) < minPasswordLength {
		return fmt.Errorf("口令至少需要 %d 个字符", minPasswordLength)
	}
	if len(v) > maxPasswordLength {
		return fmt.Errorf("口令过长（最多 %d 字节）", maxPasswordLength)
	}
	return nil
}
