package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/hakureiyuyuko/lmby/web"
)

// staticHandler 提供前端静态资源，并为前端路由做 SPA 回退。
//
// 规则：
//   - /api/ 开头但没匹配到任何接口 → 返回 JSON 404（而不是把 index.html 吐回去）
//   - 命中真实文件 → 直接给；/assets/ 下的带哈希文件名可长期缓存
//   - **找不到的静态资源（带扩展名 / /assets/ 下）→ 真 404**，绝不回退 index.html
//   - 其它路径 → 交给前端路由（返回 index.html）
//
// 中间那条是踩出来的：如果缺失的 JS 也回退成 index.html，浏览器会把 HTML 当 JS 执行
// 然后静默报语法错 —— 页面全白，而网络面板里每个请求都是 200。
// 实测后果：前端 index.html 引用了一个没发布出去的 bundle，看起来像「界面里少了几项」，
// 但没有任何错误提示，极难定位。
func (s *Server) staticHandler() http.Handler {
	dist, err := fs.Sub(web.FS, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusInternalServerError, "前端资源未打包进二进制")
		})
	}

	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "接口不存在")
			return
		}

		clean := path.Clean(r.URL.Path)
		rel := strings.TrimPrefix(clean, "/")

		if rel != "" {
			if f, err := dist.Open(rel); err == nil {
				_ = f.Close()
				w.Header().Set("Cache-Control", cacheControlFor(clean))
				fileServer.ServeHTTP(w, r)
				return
			}
			if looksLikeAsset(clean) {
				writeError(w, http.StatusNotFound, "静态资源不存在（前端版本与后端不匹配？）")
				return
			}
		}

		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			writeError(w, http.StatusNotFound, "前端未构建（缺少 web/dist/index.html）")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}

// looksLikeAsset 判断一个未命中的路径是不是「静态资源」而不是前端路由。
//
// 判据：在 /assets/ 下，或最后一段带扩展名（如 /favicon.ico、/logo.png）。
// 前端路由（/library/12、/items/34）都不带扩展名，两者不会混。
func looksLikeAsset(p string) bool {
	if strings.HasPrefix(p, "/assets/") {
		return true
	}
	base := path.Base(p)
	ext := path.Ext(base)
	return ext != "" && ext != ".html"
}

// cacheControlFor 按路径决定缓存策略。
//
// Vite 产物在 /assets/ 下带内容哈希，可以永久缓存；其余一律不缓存，
// 避免用户拿到旧的 index.html 却引用已被删除的旧资源。
func cacheControlFor(p string) string {
	if strings.HasPrefix(p, "/assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}
