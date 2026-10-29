package templateproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxIngestBody = 1 << 20

// Ingest 把客户端未知模板上传转发给模板服务器，使用独立的上传凭据。
type Ingest struct {
	endpoint *url.URL
	token    string
	client   *http.Client
}

// NewIngest 在 endpoint 为空时返回 nil，表示上传能力未启用。
func NewIngest(endpoint, token string) (*Ingest, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("invalid template ingest URL")
	}
	return &Ingest{
		endpoint: u,
		token:    strings.TrimSpace(token),
		client:   &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (i *Ingest) Enabled() bool { return i != nil && i.endpoint != nil }

// Submit 转发一次上传；上游返回的 4xx/5xx 原样交给调用方。
func (i *Ingest) Submit(ctx context.Context, body []byte) (Response, error) {
	if !i.Enabled() {
		return Response{}, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Response{}, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if i.token != "" {
		req.Header.Set("Authorization", "Bearer "+i.token)
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return Response{}, ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIngestBody+1))
	if err != nil || len(data) > maxIngestBody {
		return Response{}, ErrUnavailable
	}
	header := make(http.Header)
	for _, name := range []string{"Content-Type", "Retry-After"} {
		if v := resp.Header.Get(name); v != "" {
			header.Set(name, v)
		}
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json; charset=utf-8")
	}
	return Response{Body: data, Header: header, Status: resp.StatusCode}, nil
}
