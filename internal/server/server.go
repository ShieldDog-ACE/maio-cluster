// Package server exposes a minimal Ollama-compatible HTTP API and proxies to
// llama-server. Errors are returned as JSON with a stable maio code and hint.
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/engine"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/registry"
)

// IdleUnload is how long a model may sit unused before its VRAM is released.
const IdleUnload = 5 * time.Minute

// Server is the maio HTTP service.
type Server struct {
	Conf    *config.Global
	Eng     *engine.Manager
	Version string
	client  *http.Client

	mu      sync.Mutex
	lastUse map[string]time.Time
	active  map[string]int
	loaded  map[string]bool
}

// New creates the service.
func New(conf *config.Global, eng *engine.Manager, version string) *Server {
	return &Server{
		Conf:    conf,
		Eng:     eng,
		Version: version,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 120 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		},
		lastUse: make(map[string]time.Time),
		active:  make(map[string]int),
		loaded:  make(map[string]bool),
	}
}

// Handler returns the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/tags", s.handleTags)
	mux.HandleFunc("/api/generate", s.handleGenerate)
	mux.HandleFunc("/api/chat", s.handleChat)
	return mux
}

// StartIdleUnloader starts a background reaper that stops models unused for
// idle. The returned function stops the reaper.
func (s *Server) StartIdleUnloader(ctx context.Context, idle time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reap(idle)
			}
		}
	}()
	return cancel
}

func (s *Server) reap(idle time.Duration) {
	insts, err := s.Eng.List()
	if err != nil {
		logging.Warn("http.idle_scan_failed", "error", err.Error())
		return
	}
	now := time.Now()
	for _, in := range insts {
		s.mu.Lock()
		active := s.active[in.Name]
		last := s.lastUse[in.Name]
		loaded := s.loaded[in.Name]
		s.mu.Unlock()
		if active > 0 || !loaded {
			continue
		}
		if last.IsZero() {
			if t, err := time.Parse(time.RFC3339, in.Started); err == nil {
				last = t
			} else {
				last = now
			}
		}
		if now.Sub(last) >= idle {
			logging.Info("engine.idle_unload", "model", in.Name, "idle_s", int(now.Sub(last).Seconds()))
			if err := s.Eng.Stop(in.Name); err != nil {
				logging.Warn("engine.idle_unload_failed", "model", in.Name, "error", err.Error())
			}
			s.mu.Lock()
			delete(s.lastUse, in.Name)
			delete(s.loaded, in.Name)
			s.mu.Unlock()
		}
	}
}

func (s *Server) acquire(name string) {
	s.mu.Lock()
	s.active[name]++
	s.lastUse[name] = time.Now()
	s.mu.Unlock()
}

func (s *Server) release(name string) {
	s.mu.Lock()
	if s.active[name] > 0 {
		s.active[name]--
	}
	s.lastUse[name] = time.Now()
	s.mu.Unlock()
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
}

type tagModel struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
	Details    struct {
		Format        string `json:"format"`
		Family        string `json:"family"`
		Quantization  string `json:"quantization_level,omitempty"`
		ContextLength uint32 `json:"context_length,omitempty"`
	} `json:"details"`
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	entries, warns := registry.Scan(s.Conf.ModelsRoot)
	for _, warn := range warns {
		if errs.IsCode(warn, errs.Permission) || errs.IsCode(warn, errs.IO) {
			writeErr(w, r, statusFor(warn), warn)
			return
		}
		logging.Warn("registry.warning", "error", warn.Error())
	}
	models := make([]tagModel, 0, len(entries))
	for _, e := range entries {
		var tm tagModel
		tm.Name = e.Model.Name
		tm.Model = e.Model.Name
		tm.Size = e.Size
		tm.Details.Format = e.Model.Format
		tm.Details.Family = "gguf"
		if st, err := os.Stat(e.Model.FilePath()); err == nil {
			tm.ModifiedAt = st.ModTime().UTC().Format(time.RFC3339Nano)
		}
		models = append(models, tm)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream *bool  `json:"stream"`
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var req generateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, http.StatusBadRequest, errs.Newf(errs.Usage, "invalid JSON body: %v", err))
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model").
			WithHint("include a model name in the JSON body, e.g. {\"model\":\"qwen3.5-9b\"}"))
		return
	}
	md, inst, err := s.ensure(r.Context(), req.Model)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.acquire(md.Name)
	defer s.release(md.Name)
	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	if stream {
		s.streamCompletion(w, r, inst.Port, md.Name, req.Prompt)
		return
	}
	out, err := s.completionOnce(r.Context(), inst.Port, req.Prompt)
	if err != nil {
		writeErr(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model": md.Name, "created_at": now(), "response": out, "done": true, "done_reason": "stop",
	})
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   *bool         `json:"stream"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var req chatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, http.StatusBadRequest, errs.Newf(errs.Usage, "invalid JSON body: %v", err))
		return
	}
	if strings.TrimSpace(req.Model) == "" || len(req.Messages) == 0 {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model or messages").
			WithHint("include model and at least one message in the JSON body"))
		return
	}
	for i, m := range req.Messages {
		if strings.TrimSpace(m.Role) == "" {
			writeErr(w, r, http.StatusBadRequest, errs.Newf(errs.Usage, "message %d has an empty role", i+1))
			return
		}
	}
	md, inst, err := s.ensure(r.Context(), req.Model)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.acquire(md.Name)
	defer s.release(md.Name)
	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	if stream {
		s.streamChat(w, r, inst.Port, md.Name, msgs)
		return
	}
	out, err := s.chatOnce(r.Context(), inst.Port, md.Name, msgs)
	if err != nil {
		writeErr(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model": md.Name, "created_at": now(),
		"message": map[string]string{"role": "assistant", "content": out},
		"done":    true, "done_reason": "stop",
	})
}

func (s *Server) ensure(ctx context.Context, name string) (*config.Model, *engine.Instance, error) {
	md, err := registry.Find(s.Conf.ModelsRoot, name)
	if err != nil {
		return nil, nil, err
	}
	inst, err := s.Eng.Ensure(ctx, md, engine.Options{Ctx: s.Conf.CtxSize, ContextFlag: "ctx_size"})
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	s.loaded[md.Name] = true
	s.lastUse[md.Name] = time.Now()
	s.mu.Unlock()
	return md, inst, nil
}

func (s *Server) streamCompletion(w http.ResponseWriter, r *http.Request, port int, model, prompt string) {
	ctx := r.Context()
	body, err := s.llamaPost(ctx, port, "/completion", map[string]any{"prompt": prompt, "stream": true})
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	sc := newScanner(body)
	sentDone := false
	for sc.Scan() {
		data := sseData(sc.Text())
		if data == "" {
			continue
		}
		var chunk struct {
			Content string `json:"content"`
			Stop    bool   `json:"stop"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Content != "" {
			_ = enc.Encode(map[string]any{"model": model, "created_at": now(), "response": chunk.Content, "done": false})
			if flusher != nil {
				flusher.Flush()
			}
		}
		if chunk.Stop {
			sentDone = true
			_ = enc.Encode(map[string]any{"model": model, "created_at": now(), "response": "", "done": true, "done_reason": "stop"})
			if flusher != nil {
				flusher.Flush()
			}
			break
		}
	}
	if err := sc.Err(); err != nil {
		logging.Error("http.stream_failed", errs.Wrap(errs.Engine, err, "completion stream failed"), "model", model)
	}
	if !sentDone {
		_ = enc.Encode(map[string]any{"model": model, "created_at": now(), "response": "", "done": true, "done_reason": "stop"})
	}
}

func (s *Server) completionOnce(ctx context.Context, port int, prompt string) (string, error) {
	body, err := s.llamaPost(ctx, port, "/completion", map[string]any{"prompt": prompt, "stream": false})
	if err != nil {
		return "", err
	}
	defer body.Close()
	var resp struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return "", errs.Wrap(errs.Engine, err, "failed to parse the engine response")
	}
	return resp.Content, nil
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, port int, model string, msgs []map[string]string) {
	ctx := r.Context()
	body, err := s.llamaPost(ctx, port, "/v1/chat/completions", map[string]any{
		"model": model, "messages": msgs, "stream": true,
	})
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	sc := newScanner(body)
	for sc.Scan() {
		data := sseData(sc.Text())
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content == "" {
				continue
			}
			_ = enc.Encode(map[string]any{
				"model": model, "created_at": now(),
				"message": map[string]string{"role": "assistant", "content": c.Delta.Content},
				"done":    false,
			})
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	if err := sc.Err(); err != nil {
		logging.Error("http.stream_failed", errs.Wrap(errs.Engine, err, "chat stream failed"), "model", model)
	}
	_ = enc.Encode(map[string]any{
		"model": model, "created_at": now(),
		"message": map[string]string{"role": "assistant", "content": ""},
		"done":    true, "done_reason": "stop",
	})
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) chatOnce(ctx context.Context, port int, model string, msgs []map[string]string) (string, error) {
	body, err := s.llamaPost(ctx, port, "/v1/chat/completions", map[string]any{
		"model": model, "messages": msgs, "stream": false,
	})
	if err != nil {
		return "", err
	}
	defer body.Close()
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return "", errs.Wrap(errs.Engine, err, "failed to parse the engine response")
	}
	if len(resp.Choices) == 0 {
		return "", errs.New(errs.Engine, "engine returned no content")
	}
	return resp.Choices[0].Message.Content, nil
}

func (s *Server) llamaPost(ctx context.Context, port int, path string, payload any) (io.ReadCloser, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err, "cannot build the engine request")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err, "cannot build the engine request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.Wrap(errs.Interrupted, ctx.Err(), "request cancelled")
		}
		return nil, errs.Wrap(errs.Engine, err, "cannot reach the model engine on 127.0.0.1:%d", port).
			WithHint("check the engine log in %s", s.Conf.LogDir())
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, errs.Newf(errs.Engine, "engine returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return resp.Body, nil
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	return dec.Decode(v)
}

func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return sc
}

func sseData(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:"))
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, err error) {
	e := errs.As(err)
	body := map[string]any{"error": err.Error()}
	if e != nil {
		body["code"] = string(e.Code)
		body["message"] = e.Msg
		if h := errs.HintOf(err); h != "" {
			body["hint"] = h
		}
	}
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	logging.Error("http.error", err, "status", status, "path", path)
	writeJSON(w, status, body)
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeErr(w, r, http.StatusMethodNotAllowed, errs.Newf(errs.Usage, "method not allowed; use %s", allow))
}

func statusFor(err error) int {
	e := errs.As(err)
	if e == nil {
		return http.StatusInternalServerError
	}
	switch e.Code {
	case errs.NotFound:
		return http.StatusNotFound
	case errs.Usage, errs.Config:
		return http.StatusBadRequest
	case errs.Permission:
		return http.StatusForbidden
	case errs.Hardware:
		return http.StatusServiceUnavailable
	case errs.Engine, errs.Verify, errs.Download:
		return http.StatusBadGateway
	case errs.Interrupted:
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}
