//go:build !noadminui

package templateserver

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:webui/dist
var webuiFS embed.FS

// handleWebUI 挂载内嵌的模板管理台（React + Ant Design 构建产物）。
// 静态资源命中文件；其余路径回退 index.html（SPA）。
// 页面本身不含凭据，所有操作走需要 Bearer Token 的 /api/v1 管理接口。
func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, rel, err := readWebUIAsset(r.URL.Path)
	if err != nil {
		http.Error(w, "admin UI not built", http.StatusInternalServerError)
		return
	}
	if strings.HasPrefix(rel, "assets/") {
		// 带内容哈希的静态资源可长期缓存；入口页保持即时更新。
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	ct := mime.TypeByExtension(path.Ext(rel))
	if ct != "" && strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "charset") {
		ct += "; charset=utf-8"
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// handleIndex 提供服务入口说明，避免根路径返回 404。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "接口不存在", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service":  "azurpilot-templates",
		"admin_ui": "/admin/",
		"manifest": "/api/v3/get/templates/manifest",
	})
}

func readWebUIAsset(urlPath string) ([]byte, string, error) {
	sub, err := fs.Sub(webuiFS, "webui/dist")
	if err != nil {
		return nil, "", err
	}
	rel := strings.TrimPrefix(urlPath, "/admin")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "index.html"
	}
	data, err := fs.ReadFile(sub, rel)
	if err != nil {
		// SPA 回退：未知路径返回入口页。
		data, err = fs.ReadFile(sub, "index.html")
		if err != nil {
			return nil, "", err
		}
		rel = "index.html"
	}
	return data, rel, nil
}
