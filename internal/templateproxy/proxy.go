// Package templateproxy 将主 API 的模板读取转发到已发布的 Worker 或模板服务器，
// 并做短时缓存与上游故障降级。
package templateproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxBody = 8 << 20

var ErrUnavailable = errors.New("template upstream unavailable")

type Response struct {
	Body   []byte
	Header http.Header
	Status int
}

type cacheEntry struct {
	resp    Response
	expires time.Time
}

type Proxy struct {
	base   *url.URL
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New 校验并固定上游地址；base 为空时所有读取返回 ErrUnavailable。
func New(base string, ttl time.Duration) (*Proxy, error) {
	p := &Proxy{
		client: &http.Client{Timeout: 10 * time.Second},
		ttl:    ttl,
		now:    time.Now,
		cache:  make(map[string]cacheEntry),
	}
	if strings.TrimSpace(base) == "" {
		return p, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("invalid template public base")
	}
	p.base = u
	return p, nil
}

// Enabled 报告是否配置了上游。
func (p *Proxy) Enabled() bool { return p.base != nil }

// Get 使用默认 TTL 读取上游路径。
func (p *Proxy) Get(ctx context.Context, key, path string, q url.Values) (Response, error) {
	return p.GetTTL(ctx, key, path, q, p.ttl)
}

// GetTTL 读取上游路径。key 由调用方提供（通常是路径+查询），命中缓存时直接返回。
func (p *Proxy) GetTTL(ctx context.Context, key, path string, q url.Values, ttl time.Duration) (Response, error) {
	if p.base == nil {
		return Response{}, ErrUnavailable
	}
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	p.mu.Lock()
	entry, ok := p.cache[key]
	if ok && p.now().Before(entry.expires) {
		p.mu.Unlock()
		return cloneResponse(entry.resp), nil
	}
	p.mu.Unlock()

	target := *p.base
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return p.staleOr(key, ErrUnavailable)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return p.staleOr(key, ErrUnavailable)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return p.staleOr(key, ErrUnavailable)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return p.staleOr(key, ErrUnavailable)
	}
	header := make(http.Header)
	for _, name := range []string{"Content-Type", "ETag", "Cache-Control", "X-Manifest-Signature", "X-Manifest-SHA256"} {
		if v := resp.Header.Get(name); v != "" {
			header.Set(name, v)
		}
	}
	header.Set("X-Template-Cache", "miss")
	out := Response{Body: body, Header: header, Status: http.StatusOK}
	p.mu.Lock()
	p.cache[key] = cacheEntry{resp: cloneResponse(out), expires: p.now().Add(ttl)}
	p.mu.Unlock()
	return out, nil
}

func (p *Proxy) staleOr(key string, err error) (Response, error) {
	p.mu.Lock()
	entry, ok := p.cache[key]
	p.mu.Unlock()
	if ok {
		resp := cloneResponse(entry.resp)
		resp.Header.Set("X-Template-Cache", "stale")
		return resp, nil
	}
	return Response{}, err
}

func cloneResponse(r Response) Response {
	out := Response{Body: append([]byte(nil), r.Body...), Status: r.Status}
	out.Header = r.Header.Clone()
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	return out
}
