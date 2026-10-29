//go:build noadminui

package templateserver

import "net/http"

// handleWebUI 在 noadminui 构建下返回提示页；管理 API 不受影响。
func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>模板管理台</title>" +
		"<p>此构建未包含管理界面。请先构建 template-web 后重新编译模板服务器。</p>"))
}
