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
//   - 其它路径 → 交给前端路由（返回 index.html）
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
