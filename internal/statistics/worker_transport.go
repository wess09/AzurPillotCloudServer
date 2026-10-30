package statistics

import (
	"context"
	"io"
	"net/http"
	"time"

	"ap-cloud-server/internal/config"
)

func (s *Service) WithInsecureWorker(allowed bool) *Service {
	s.AllowInsecureWorker = allowed
	return s
}

func (s *Service) workerRequest(ctx context.Context, path string, body io.Reader) (*http.Request, error) {
	if e := config.ValidateRecognizerURL(s.WorkerURL, s.AllowInsecureWorker); e != nil {
		return nil, e
	}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, s.WorkerURL+path, body)
	if e != nil {
		return nil, e
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.WorkerToken)
	return request, nil
}

func workerClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
