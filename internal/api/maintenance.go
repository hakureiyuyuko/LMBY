package api

// 维护：缓存占用与清理（设置 → 缓存与清理）。
//
// 这一页只处理**缓存性质**的东西：
//   - 图片缓存（`<数据目录>/images/`）：能重新下载/重新生成，按上限 LRU 淘汰，也可以一键清空；
//   - 叠加层里的**孤儿**（库或条目已经不在库里）：删条目/删库只动数据库，
//     叠加层是另一个模块的数据 —— 不清就是永远的垃圾；
//   - 转码分片与探测工作目录：进程重启时由各自的 Manager 清上一次运行留下的
//     （见 stream.NewManager），这里只显示占用。
//
// 三条边界（写在这里，免得以后有人顺手扩大它）：
//  1. **绝不碰媒体文件**；
//  2. 叠加层里**还在库里的条目**一个字节都不动（那是刮削产物的备份，不是缓存）；
//  3. 清空图片缓存会带来一次「重新下载」的流量 —— 所以它是显式动作，不做自动调用。

import (
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// cacheUsage 是某个目录的占用。
type cacheUsage struct {
	Dir   string `json:"dir"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	// Note 用来解释「这个目录是什么、清了会怎样」（界面直接显示）。
	Note string `json:"note"`
}

// handleMaintenance 返回各缓存目录的占用 + 叠加层孤儿的统计（不删）。
func (s *Server) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	out := map[string]any{}
	for _, item := range []struct {
		key  string
		dir  string
		note string
	}{
		{"images", filepath.Join(s.cfg.DataDir, "images"), "图片缓存：按上限自动淘汰，也可以一键清空（下次访问重新生成/下载）"},
		{"streams", filepath.Join(s.cfg.DataDir, "streams"), "转码分片：服务重启时会清掉上一次运行留下的目录"},
		{"probe", filepath.Join(s.cfg.DataDir, "probe"), "探测工作目录：探测完成后应自行清空，这里只显示占用"},
		{"overlay", filepath.Join(s.cfg.DataDir, "overlay"), "叠加层：只读库的刮削产物备份（不清除非确定是孤儿）"},
	} {
		files, bytes := dirUsageOf(item.dir)
		out[item.key] = cacheUsage{Dir: item.dir, Files: files, Bytes: bytes, Note: item.note}
	}

	// 叠加层孤儿：只统计。清不清由管理员点按钮决定 —— 删数据这种事不做自动。
	if s.overlay != nil {
		files, bytes, err := s.overlay.OrphanStats(ctx)
		if err != nil {
			s.serverError(w, "统计叠加层孤儿失败", err)
			return
		}
		out["overlayOrphans"] = cacheUsage{Files: files, Bytes: bytes, Note: "库或条目已经不在库里的叠加层数据"}
	} else {
		out["overlayOrphans"] = cacheUsage{Note: "叠加层未启用"}
	}

	writeJSON(w, http.StatusOK, out)
}

type maintenanceCleanRequest struct {
	// Images 清空图片缓存（会重新下载）
	Images bool `json:"images"`
	// OverlayOrphans 删掉叠加层孤儿
	OverlayOrphans bool `json:"overlayOrphans"`
}

// handleMaintenanceClean 执行清理。返回每一项清掉了多少 —— 界面要如实显示结果。
func (s *Server) handleMaintenanceClean(w http.ResponseWriter, r *http.Request) {
	var req maintenanceCleanRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second) // 清大目录可能慢
	defer cancel()

	res := map[string]any{}
	if req.Images {
		dir := filepath.Join(s.cfg.DataDir, "images")
		files, bytes := dirUsageOf(dir)
		// 删内容但保留目录本身：服务运行中随时会往里写，目录不存在会让写入端多一个分支。
		if err := os.RemoveAll(dir); err != nil {
			s.serverError(w, "清空图片缓存失败", err)
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			s.serverError(w, "重建图片缓存目录失败", err)
			return
		}
		res["images"] = map[string]any{"files": files, "bytes": bytes}
		s.log.Info("已清空图片缓存", "files", files, "bytes", bytes)
		s.audit(ctx, r, "maintenance.images_cleared", "images",
			map[string]any{"files": files, "bytes": bytes})
	}
	if req.OverlayOrphans {
		if s.overlay == nil {
			writeError(w, http.StatusServiceUnavailable, "叠加层未启用")
			return
		}
		files, bytes, err := s.overlay.PurgeOrphans(ctx)
		if err != nil {
			s.serverError(w, "清理叠加层孤儿失败", err)
			return
		}
		res["overlayOrphans"] = map[string]any{"files": files, "bytes": bytes}
		s.log.Info("已清理叠加层孤儿", "files", files, "bytes", bytes)
		s.audit(ctx, r, "maintenance.overlay_orphans_purged", "overlay",
			map[string]any{"files": files, "bytes": bytes})
	}

	writeJSON(w, http.StatusOK, map[string]any{"cleaned": res})
}

// dirUsageOf 统计目录里的文件数与总字节数。
func dirUsageOf(dir string) (int, int64) {
	var files int
	var bytes int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}
