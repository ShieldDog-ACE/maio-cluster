// Package server exposes a minimal Ollama-compatible HTTP API and proxies to llama-server.
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
	"time"

	"github.com/ShieldDog-ACE/maio--cluster/internal/config"
	"github.com/ShieldDog-ACE/maio--cluster/internal/engine"
	"github.com/ShieldDog-ACE/maio--cluster/internal/registry"
)

// Server is the maio HTTP service.
type Server struct {
	Conf    *config.Global
	Eng     *engine.Manager
	Version string
	client  *http.Client
}

// New creates the service.
func New(conf *config.Global, eng *engine.Manager, version string) *Server {
	return &Server{Conf: conf, Eng: eng, Version: version, client: &http.Client{}}
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

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
}

type tagModel struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
	Details    struct {
		Format string `json:"format"`
		Family string `json:"family"`
	} `json:"details"`
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	entries, _ := registry.Scan(s.Conf.ModelsRoot)
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
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not JSON: "+err.Error())
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "missing model")
		return
	}
	md, inst, err := s.ensure(r.Context(), req.Model)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	if stream {
		s.streamCompletion(w, r.Context(), inst.Port, md.Name, req.Prompt)
		return
	}
	out, err := s.completionOnce(r.Context(), inst.Port, req.Prompt)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
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
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not JSON: "+err.Error())
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "missing model or messages")
		return
	}
	md, inst, err := s.ensure(r.Context(), req.Model)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	if stream {
		s.streamChat(w, r.Context(), inst.Port, md.Name, msgs)
		return
	}
	out, err := s.chatOnce(r.Context(), inst.Port, md.Name, msgs)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
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
	inst, err := s.Eng.Ensure(ctx, md)
	if err != nil {
		return nil, nil, err
	}
	return md, inst, nil
}

func (s *Server) streamCompletion(w http.ResponseWriter, ctx context.Context, port int, model, prompt string) {
	body, err := s.llamaPost(ctx, port, "/completion", map[string]any{"prompt": prompt, "stream": true})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
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
		return "", fmt.Errorf("failed to parse engine response: %w", err)
	}
	return resp.Content, nil
}

func (s *Server) streamChat(w http.ResponseWriter, ctx context.Context, port int, model string, msgs []map[string]string) {
	body, err := s.llamaPost(ctx, port, "/v1/chat/completions", map[string]any{
		"model": model, "messages": msgs, "stream": true,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
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
		return "", fmt.Errorf("failed to parse engine response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("engine returned no content")
	}
	return resp.Choices[0].Message.Content, nil
}

func (s *Server) llamaPost(ctx context.Context, port int, path string, payload any) (io.ReadCloser, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach engine: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("engine returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return resp.Body, nil
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

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
