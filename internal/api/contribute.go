package api

// 贡献到 KeqDB：管理员手动上传单个条目，以及查本实例的贡献状态。
//
// （「开关打开后新入库自动贡献」是扫描/刮削侧的钩子，不在这里。）

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/keqdb"
)

// keqdbWriter 按当前设置构造写客户端（token 解密后只在内存里用，不落日志）。
func (s *Server) keqdbWriter(ctx context.Context) (*keqdb.Client, error) {
	cfg, err := s.settings.KeqDB(ctx)
	if err != nil {
		return nil, err
	}
	return keqdb.New(s.settings.KeqDBBaseURL(), cfg.Token, s.log), nil
}

// handleContributeItem 把一个条目贡献给 KeqDB。
//
// 剧集/季会自动归到它所属的剧，一次把全部季集带上（对接文档 §5.4 的建议）。
func (s *Server) handleContributeItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "条目 id 不合法"})
		return
	}

	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()

	pkg, err := keqdb.NewBuilder(s.store).Build(ctx, id)
	if err != nil {
		s.serverError(w, "组装贡献数据失败", err)
		return
	}

	// dryRun：只把组装结果回给管理员自查（不提交；配 token 前也能用）。
	if r.URL.Query().Get("dryRun") != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dryRun": true, "payload": pkg})
		return
	}

	client, err := s.keqdbWriter(ctx)
	if err != nil {
		s.serverError(w, "读取 KeqDB 配置失败", err)
		return
	}
	if !client.Configured() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "还没填 KeqDB 实例 token（设置 → 元数据共享改进计划）",
		})
		return
	}

	res, err := client.Contribute(ctx, pkg)
	if err != nil {
		if errors.Is(err, keqdb.ErrUnauthorized) {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "KeqDB 拒绝了实例 token（失效或实例被禁用）",
			})
			return
		}
		s.serverError(w, "提交贡献失败", err)
		return
	}

	s.audit(ctx, r, "keqdb.contribute", "item:"+strconv.FormatInt(id, 10), map[string]any{
		"state": res.State, "deduped": res.Deduped, "contributionId": res.ID,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"contribution": map[string]any{
			"id":      res.ID,
			"state":   res.State,
			"deduped": res.Deduped,
			"message": res.Message,
		},
	})
}

// handleSharingStatus 查本实例在 KeqDB 的贡献状态（已上传/已采纳/被驳回）。
func (s *Server) handleSharingStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	client, err := s.keqdbWriter(ctx)
	if err != nil {
		s.serverError(w, "读取 KeqDB 配置失败", err)
		return
	}
	if !client.Configured() {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}

	me, err := client.Me(ctx)
	if err != nil {
		if errors.Is(err, keqdb.ErrUnauthorized) {
			writeJSON(w, http.StatusOK, map[string]any{
				"configured": true, "tokenValid": false,
				"error": "实例 token 失效或实例被禁用",
			})
			return
		}
		s.serverError(w, "查询贡献状态失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":  true,
		"tokenValid":  true,
		"instance":    me.Instance,
		"sitePending": me.SitePending,
	})
}
