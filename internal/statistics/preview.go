package statistics

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Preview is an authenticated, uncached stream from B. C never stores originals.
func (s *Service) preview(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	index, e := strconv.Atoi(r.PathValue("index"))
	if e != nil {
		fail(w, 400, "VALIDATION_ERROR", "无效图片索引")
		return
	}
	frame := 0
	if r.URL.Query().Get("frame") != "" {
		frame, e = strconv.Atoi(r.URL.Query().Get("frame"))
	}
	if !hashRE.MatchString(sha) || e != nil || index < 0 || index >= 1000 || frame < 0 || frame >= 10 {
		fail(w, 400, "VALIDATION_ERROR", "无效图片索引")
		return
	}
	if s.WorkerURL == "" || s.WorkerToken == "" {
		fail(w, 503, "WORKER_UNAVAILABLE", "请配置 RECOGNIZER_URL 和 WORKER_TOKEN 以读取原始帧")
		return
	}
	var source json.RawMessage
	e = s.Pool.QueryRow(r.Context(), `SELECT payload->'source' FROM stat_results WHERE sha256=$1`, sha).Scan(&source)
	if errors.Is(e, pgx.ErrNoRows) {
		fail(w, 404, "NOT_FOUND", "样本不存在")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	body, _ := json.Marshal(map[string]any{"source": source, "file_index": index, "frame_index": frame})
	req, e := s.workerRequest(r.Context(), "/internal/v1/preview", bytes.NewReader(body))
	if e != nil {
		internal(w, e)
		return
	}
	response, e := workerClient(50 * time.Second).Do(req)
	if e != nil {
		internal(w, e)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		fail(w, 502, "WORKER_UNAVAILABLE", "B 原图读取失败，请检查节点连接和 123 盘下载权限")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.CopyBuffer(w, io.LimitReader(response.Body, 10<<20), make([]byte, 64<<10))
}
