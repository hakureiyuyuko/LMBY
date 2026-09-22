package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/images"
	"github.com/hakureiyuyuko/lmby/internal/store"
)

// itemForRequest 解析 /items/{id} 并取出条目；出错时已经把响应写好。
func (s *Server) itemForRequest(w http.ResponseWriter, r *http.Request) (*store.Item, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "条目 id 非法")
		return nil, false
	}
	item, err := s.store.GetItem(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "条目不存在")
		return nil, false
	}
	if err != nil {
		s.serverError(w, "读取条目失败", err)
		return nil, false
	}
	// 权限：条目所在的库看不见 → **404**（不区分「不存在」与「不给你看」，
	// 免得用状态码把别人的库探出来）。这里是所有条目级接口（详情/编辑/取图/播放）的
	// 共同入口，所以权限只在这里判一次。
	v, verr := s.viewerFor(r.Context(), r)
	if verr != nil {
		s.serverError(w, "读取用户权限失败", verr)
		return nil, false
	}
	if !v.CanSeeLibrary(item.LibraryID) {
		writeError(w, http.StatusNotFound, "条目不存在")
		return nil, false
	}
	return item, true
}

// handleListImages 列出某个条目现有的图片。
//
// 这一步**不回源**：只是让界面知道本地有什么（详情页先画本地海报，
// 再决定要不要别的图）。返回的 URL 就是取图地址。
func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	if s.images == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []images.Info{}})
		return
	}
	list, err := s.images.List(r.Context(), item)
	if err != nil {
		s.serverError(w, "读取图片失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// handleItemImage 输出某类图片，按需缩放。
//
// 取值顺序：媒体目录里的本地图 > 用户手选 > provider 下载缓存；都没有就回源一次。
// 本地图能直接用就直接送原文件（零拷贝），需要缩放/转格式才落缓存。
//
// 参数：w / h（最大尺寸，等比缩放进这个框，不放大）、format=jpeg|png、q（jpeg 质量）。
func (s *Server) handleItemImage(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	if s.images == nil {
		writeError(w, http.StatusNotFound, "图片服务未启用")
		return
	}

	kind := strings.TrimSpace(r.PathValue("kind"))
	if kind == "" {
		writeError(w, http.StatusBadRequest, "缺少图片类型")
		return
	}

	q := r.URL.Query()
	req := images.Request{
		Width:   atoiClamp(q.Get("w"), 4096),
		Height:  atoiClamp(q.Get("h"), 4096),
		Format:  strings.TrimSpace(q.Get("format")),
		Quality: atoiClamp(q.Get("q"), 100),
	}

	rendered, err := s.images.Open(r.Context(), item, kind, req)
	if errors.Is(err, images.ErrNoImage) {
		writeError(w, http.StatusNotFound, "这个条目没有这类图片")
		return
	}
	if err != nil {
		s.serverError(w, "输出图片失败", err)
		return
	}

	f, err := os.Open(rendered.Path)
	if err != nil {
		s.serverError(w, "读取图片失败", err)
		return
	}
	defer func() { _ = f.Close() }()

	head := w.Header()
	head.Set("Content-Type", rendered.ContentType)
	head.Set("ETag", `"`+rendered.ETag+`"`)
	// 图片地址稳定但内容可能变（用户换了本地图、重新刮削过），
	// 所以给一个不长的强缓存 + ETag，让浏览器复访时走 304 省流量。
	head.Set("Cache-Control", "private, max-age=3600")
	head.Set("X-Image-Width", strconv.Itoa(rendered.Width))
	head.Set("X-Image-Height", strconv.Itoa(rendered.Height))

	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, rendered.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// ServeContent 会帮忙处理 Range 与条件请求
	http.ServeContent(w, r, "", time.Time{}, f)
}

// atoiClamp 解析非负整数并截到 max；空值、非法值或负数返回 0（= 不限制）。
func atoiClamp(s string, max int) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	if n > max {
		return max
	}
	return n
}
