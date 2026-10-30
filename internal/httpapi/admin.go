package httpapi

import (
	"context"

	"ap-cloud-server/internal/publish"
)

type announcementRequest struct {
	Title   string  `json:"title"`
	Content *string `json:"content"`
	URL     *string `json:"url"`
}

type updatePolicyRequest struct {
	ForceUpdate *bool `json:"force_update"`
}

// publishNotice 触发公告 Worker 的发布。
func (s *Server) publishNotice(ctx context.Context) publish.Result {
	a, err := s.store.LatestAnnouncement(ctx)
	if err != nil {
		return publish.Result{Error: "读取公告失败: " + err.Error()}
	}
	content, err := publish.RenderNotice(a, s.now())
	if err != nil {
		return publish.Result{Error: err.Error()}
	}
	noticePath, _ := publish.Paths(s.publishDir)
	return s.publisher.Submit(ctx, publish.Job{
		Worker:    publish.NoticeWorker,
		Path:      "/api/v2/get/notice/data.json",
		LocalPath: noticePath,
		Content:   content,
		PublicURL: s.noticePublicURL,
	}, s.publishTimeout)
}

// publishUpdate 触发更新策略 Worker 的发布。
func (s *Server) publishUpdate(ctx context.Context) publish.Result {
	force, err := s.store.GetUpdatePolicy(ctx)
	if err != nil {
		return publish.Result{Error: "读取更新策略失败: " + err.Error()}
	}
	content, err := publish.RenderUpdate(force, s.now())
	if err != nil {
		return publish.Result{Error: err.Error()}
	}
	_, updatePath := publish.Paths(s.publishDir)
	return s.publisher.Submit(ctx, publish.Job{
		Worker:    publish.UpdateWorker,
		Path:      "/api/v2/get/update/data.json",
		LocalPath: updatePath,
		Content:   content,
		PublicURL: s.updatePublicURL,
	}, s.publishTimeout)
}

func publishResultJSON(res publish.Result) map[string]any {
	return map[string]any{
		"success": res.Success,
		"skipped": res.Skipped,
		"error":   res.Error,
	}
}
