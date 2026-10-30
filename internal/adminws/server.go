// Package adminws provides authenticated commands and change-only state subscriptions.
package adminws

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/time/rate"
)

type Fault struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
}

func (e Fault) Error() string { return e.Message }

type Command func(context.Context, string, json.RawMessage) (any, error)
type Snapshot func(context.Context) (any, error)
type Server struct {
	Token    string
	Origins  []string
	Command  Command
	Snapshot Snapshot
	mu       sync.Mutex
	clients  map[*session]struct{}
	notify   chan struct{}
	cancel   context.CancelFunc
	closed   bool
	wg       sync.WaitGroup
}
type session struct {
	conn       *websocket.Conn
	mu         sync.Mutex
	subscribed atomic.Bool
	lastHash   [32]byte
}
type request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func New(token string, origins []string, command Command, snapshot Snapshot) *Server {
	return &Server{Token: token, Origins: origins, Command: command, Snapshot: snapshot, clients: make(map[*session]struct{}), notify: make(chan struct{}, 1)}
}
func (s *Server) Run(parent context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil || s.closed {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.broadcast(ctx)
			case <-s.notify:
				s.broadcast(ctx)
			}
		}
	}()
}
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	clients := make([]*session, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	for _, client := range clients {
		_ = client.conn.CloseNow()
	}
	s.wg.Wait()
}
func (s *Server) Notify() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
func (s *Server) broadcast(parent context.Context) {
	s.mu.Lock()
	clients := make([]*session, 0, len(s.clients))
	for client := range s.clients {
		if client.subscribed.Load() {
			clients = append(clients, client)
		}
	}
	s.mu.Unlock()
	if len(clients) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	data, err := s.Snapshot(ctx)
	if err != nil {
		return
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return
	}
	hash := sha256.Sum256(encoded)
	for _, client := range clients {
		client.mu.Lock()
		if client.lastHash != hash {
			writeCtx, stop := context.WithTimeout(parent, 5*time.Second)
			err = wsjson.Write(writeCtx, client.conn, map[string]any{"type": "event", "topic": "state", "data": data})
			stop()
			if err == nil {
				client.lastHash = hash
			} else {
				_ = client.conn.CloseNow()
			}
		}
		client.mu.Unlock()
	}
}
func (client *session) send(ctx context.Context, value any) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, client.conn, value)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Token == "" {
		http.Error(w, "Management disabled", http.StatusServiceUnavailable)
		return
	}
	allowed := false
	for _, origin := range s.Origins {
		if r.Header.Get("Origin") == origin {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "Origin not allowed", http.StatusForbidden)
		return
	}
	// Exact scheme+host origins were checked above. Never accept credentials in a URL.
	if r.URL.RawQuery != "" {
		http.Error(w, "Query parameters are not accepted", http.StatusBadRequest)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	authCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	err = wsjson.Read(authCtx, conn, &auth)
	stop()
	client := &session{conn: conn}
	if err != nil || auth.Type != "auth" || subtle.ConstantTimeCompare([]byte(auth.Token), []byte(s.Token)) != 1 {
		_ = client.send(ctx, map[string]any{"type": "error", "error": Fault{Code: "UNAUTHORIZED", Message: "管理凭据无效", Status: 401}})
		_ = conn.Close(websocket.StatusPolicyViolation, "Unauthorized")
		return
	}
	auth.Token = ""
	if client.send(ctx, map[string]any{"type": "authenticated"}) != nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.clients[client] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, client)
		s.mu.Unlock()
		s.wg.Done()
	}()
	limiter := rate.NewLimiter(10, 50)
	semaphore := make(chan struct{}, 4)
	var running sync.WaitGroup
	defer func() { cancel(); running.Wait() }()
	running.Add(1)
	go func() {
		defer running.Done()
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, finish := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pingCtx)
				finish()
				if err != nil {
					_ = conn.CloseNow()
					return
				}
			}
		}
	}()
	for {
		kind, raw, readErr := conn.Read(ctx)
		if readErr != nil {
			return
		}
		var req request
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if kind != websocket.MessageText || decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.ID == "" || len(req.ID) > 64 || len(req.Method) > 80 {
			_ = client.send(ctx, map[string]any{"type": "error", "error": Fault{Code: "INVALID_REQUEST", Message: "无效的管理命令", Status: 400}})
			continue
		}
		if !limiter.Allow() {
			_ = client.send(ctx, map[string]any{"id": req.ID, "type": "error", "error": Fault{Code: "RATE_LIMITED", Message: "管理请求过于频繁", Status: 429}})
			continue
		}
		select {
		case semaphore <- struct{}{}:
		default:
			_ = client.send(ctx, map[string]any{"id": req.ID, "type": "error", "error": Fault{Code: "BUSY", Message: "正在处理的管理命令过多", Status: 429}})
			continue
		}
		running.Add(1)
		go func(req request) {
			defer running.Done()
			defer func() { <-semaphore }()
			callCtx, finish := context.WithTimeout(ctx, 100*time.Second)
			defer finish()
			var data any
			var callErr error
			switch req.Method {
			case "state.get", "state.subscribe":
				data, callErr = s.Snapshot(callCtx)
			case "ping":
				data = map[string]bool{"pong": true}
			default:
				data, callErr = s.Command(callCtx, req.Method, req.Params)
			}
			if callErr != nil {
				fault := Fault{Code: "INTERNAL", Message: "管理操作失败", Status: 500}
				var known Fault
				if errors.As(callErr, &known) {
					fault = known
				}
				_ = client.send(callCtx, map[string]any{"id": req.ID, "type": "error", "error": fault})
				return
			}
			result := map[string]any{"id": req.ID, "type": "result", "data": data}
			if req.Method == "state.subscribe" {
				// The initial response precedes every change event on this socket.
				// Otherwise a newer push could be overwritten by an older response.
				client.mu.Lock()
				writeCtx, stop := context.WithTimeout(callCtx, 10*time.Second)
				err := wsjson.Write(writeCtx, client.conn, result)
				stop()
				if err == nil {
					encoded, _ := json.Marshal(data)
					client.lastHash = sha256.Sum256(encoded)
					client.subscribed.Store(true)
				}
				client.mu.Unlock()
			} else {
				_ = client.send(callCtx, result)
			}
			s.Notify()
		}(req)
	}
}
