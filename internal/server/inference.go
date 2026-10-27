package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/queue"
)

type generateRequest struct {
	Model     string          `json:"model"`
	Prompt    string          `json:"prompt"`
	Suffix    string          `json:"suffix"`
	Stream    *bool           `json:"stream"`
	Raw       bool            `json:"raw"`
	Format    json.RawMessage `json:"format"`
	KeepAlive json.RawMessage `json:"keep_alive"`
	Options   map[string]any  `json:"options"`
}

type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type chatRequest struct {
	Model     string          `json:"model"`
	Messages  []chatMessage   `json:"messages"`
	Stream    *bool           `json:"stream"`
	Format    json.RawMessage `json:"format"`
	KeepAlive json.RawMessage `json:"keep_alive"`
	Options   map[string]any  `json:"options"`
}

type timings struct {
	PromptN     int     `json:"prompt_n"`
	PromptMS    float64 `json:"prompt_ms"`
	PredictedN  int     `json:"predicted_n"`
	PredictedMS float64 `json:"predicted_ms"`
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var req generateRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model").
			WithHint(`include a model name, e.g. {"model":"qwen3.5-9b"}`))
		return
	}
	ka, kaErr := parseKeepAlive(req.KeepAlive)
	if kaErr != nil {
		writeErr(w, r, http.StatusBadRequest, kaErr)
		return
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()
	r = r.WithContext(ctx)
	release, ok := s.acquireSlot(w, r, ctx)
	if !ok {
		return
	}
	defer release()
	md, inst, err := s.ensure(ctx, req.Model)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.acquireModel(md.Name)
	defer s.applyKeepAlive(md.Name, ka)
	defer s.releaseModel(md.Name)

	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	payload := completionPayload(&req, stream)
	if stream {
		s.streamCompletion(w, r, inst.Port, md.Name, payload)
		return
	}
	content, usage, err := s.completionOnce(ctx, inst.Port, payload)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	resp := ollamaDone(md.Name, usage)
	resp["response"] = content
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var req chatRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		writeErr(w, r, statusFor(err), err)
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
	ka, kaErr := parseKeepAlive(req.KeepAlive)
	if kaErr != nil {
		writeErr(w, r, http.StatusBadRequest, kaErr)
		return
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()
	r = r.WithContext(ctx)
	release, ok := s.acquireSlot(w, r, ctx)
	if !ok {
		return
	}
	defer release()
	md, inst, err := s.ensure(ctx, req.Model)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.acquireModel(md.Name)
	defer s.applyKeepAlive(md.Name, ka)
	defer s.releaseModel(md.Name)

	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	payload := chatPayload(&req, stream)
	if stream {
		s.streamChat(w, r, inst.Port, md.Name, payload)
		return
	}
	content, usage, err := s.chatOnce(ctx, inst.Port, payload)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	resp := ollamaDone(md.Name, usage)
	resp["message"] = map[string]string{"role": "assistant", "content": content}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var req struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if err := s.decodeJSON(w, r, &req); err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	if req.Model == "" || len(req.Input) == 0 {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model or input"))
		return
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()
	release, ok := s.acquireSlot(w, r, ctx)
	if !ok {
		return
	}
	defer release()
	md, inst, err := s.ensure(ctx, req.Model)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.acquireModel(md.Name)
	defer s.releaseModel(md.Name)
	vecs, err := s.embedVectors(ctx, inst.Port, md.Name, req.Input)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": md.Name, "embeddings": vecs})
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var req struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := s.decodeJSON(w, r, &req); err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	if req.Model == "" || req.Prompt == "" {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model or prompt"))
		return
	}
	input, _ := json.Marshal(req.Prompt)
	ctx, cancel := s.requestContext(r)
	defer cancel()
	release, ok := s.acquireSlot(w, r, ctx)
	if !ok {
		return
	}
	defer release()
	md, inst, err := s.ensure(ctx, req.Model)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.acquireModel(md.Name)
	defer s.releaseModel(md.Name)
	vecs, err := s.embedVectors(ctx, inst.Port, md.Name, input)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	if len(vecs) == 0 {
		writeErr(w, r, http.StatusBadGateway, errs.New(errs.Engine, "engine returned no embedding"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"embedding": vecs[0]})
}

// handleV1Proxy passes OpenAI-compatible chat, completion and embedding
// requests straight through to llama-server after admission and model loading.
// Streaming and all OpenAI parameters are preserved byte-for-byte.
func (s *Server) handleV1Proxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	body, err := s.readBody(w, r)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	name := modelFromBody(body)
	if strings.TrimSpace(name) == "" {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model").
			WithHint(`include a model name in the JSON body`))
		return
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()
	release, ok := s.acquireSlot(w, r, ctx)
	if !ok {
		return
	}
	defer release()
	md, inst, err := s.ensure(ctx, name)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.acquireModel(md.Name)
	defer s.releaseModel(md.Name)

	upstream := fmt.Sprintf("http://127.0.0.1:%d%s", inst.Port, r.URL.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, errs.Wrap(errs.Internal, err, "cannot build the engine request"))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		writeErr(w, r, http.StatusBadGateway, errs.Wrap(errs.Engine, err, "cannot reach the model engine").
			WithHint("check the engine log in %s", s.Conf.LogDir()))
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
		return
	}
	if n, err := copyStream(w, resp.Body); err != nil && !errors.Is(err, context.Canceled) {
		logging.Warn("http.proxy_stream_failed", "model", md.Name, "error", err.Error())
	} else {
		s.Metrics.AddStreamedBytes(uint64(n))
	}
}

func (s *Server) requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	if s.Conf.RequestTimeout > 0 {
		return context.WithTimeout(r.Context(), time.Duration(s.Conf.RequestTimeout)*time.Second)
	}
	return r.Context(), func() {}
}

// acquireSlot takes an admission slot. It writes the error response itself and
// returns ok=false when the request cannot run.
func (s *Server) acquireSlot(w http.ResponseWriter, r *http.Request, ctx context.Context) (func(), bool) {
	waited, err := s.Queue.Acquire(ctx)
	s.Metrics.SetQueue(s.Queue.Depth(), s.Queue.MaxQueue())
	switch {
	case err == nil:
		if waited {
			logging.Info("http.queued", "path", r.URL.Path, "depth", s.Queue.Depth())
		}
		return func() {
			s.Queue.Release()
			s.Metrics.SetQueue(s.Queue.Depth(), s.Queue.MaxQueue())
		}, true
	case errors.Is(err, queue.ErrFull):
		s.Metrics.IncQueueRejected()
		w.Header().Set("Retry-After", "5")
		writeErr(w, r, http.StatusTooManyRequests, errs.New(errs.Busy, "the inference queue is full").
			WithHint("retry shortly, or raise max_queue/max_concurrency in maio.conf"))
	case errors.Is(err, queue.ErrTimeout):
		s.Metrics.IncQueueTimeout()
		w.Header().Set("Retry-After", "10")
		writeErr(w, r, http.StatusServiceUnavailable, errs.New(errs.Busy, "timed out waiting for an inference slot").
			WithHint("retry later, or raise queue_timeout/max_concurrency in maio.conf"))
	case errors.Is(err, queue.ErrCanceled):
		logging.Info("http.queue_cancelled", "path", r.URL.Path)
	}
	return nil, false
}

func completionPayload(req *generateRequest, stream bool) map[string]any {
	p := map[string]any{"prompt": req.Prompt, "stream": stream}
	if req.Suffix != "" {
		p["suffix"] = req.Suffix
	}
	applyOllamaOptions(p, req.Options, false)
	if isJSONFormat(req.Format) {
		p["json_schema"] = map[string]any{"type": "object"}
	}
	return p
}

func chatPayload(req *chatRequest, stream bool) map[string]any {
	p := map[string]any{
		"model":    req.Model,
		"messages": openAIMessages(req.Messages),
		"stream":   stream,
	}
	applyOllamaOptions(p, req.Options, true)
	if isJSONFormat(req.Format) {
		p["response_format"] = map[string]any{"type": "json_object"}
	}
	return p
}

// applyOllamaOptions maps Ollama option names onto the llama.cpp payload.
// Unknown options are ignored rather than forwarded blindly.
func applyOllamaOptions(dst map[string]any, opts map[string]any, chat bool) {
	if opts == nil {
		return
	}
	mapping := map[string]string{
		"num_predict":       "n_predict",
		"temperature":       "temperature",
		"top_p":             "top_p",
		"top_k":             "top_k",
		"seed":              "seed",
		"stop":              "stop",
		"repeat_penalty":    "repeat_penalty",
		"presence_penalty":  "presence_penalty",
		"frequency_penalty": "frequency_penalty",
		"min_p":             "min_p",
		"typical_p":         "typical_p",
		"tfs_z":             "tfs_z",
		"repeat_last_n":     "repeat_last_n",
		"mirostat":          "mirostat",
		"mirostat_tau":      "mirostat_tau",
		"mirostat_eta":      "mirostat_eta",
	}
	for k, v := range opts {
		target, ok := mapping[k]
		if !ok || v == nil {
			continue
		}
		if chat {
			switch target {
			case "n_predict":
				target = "max_tokens"
			case "repeat_penalty", "top_k", "min_p", "typical_p", "tfs_z", "repeat_last_n", "mirostat", "mirostat_tau", "mirostat_eta":
				// llama-server accepts these extensions on its OpenAI route.
			}
		}
		dst[target] = v
	}
}

func isJSONFormat(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	s := strings.TrimSpace(string(raw))
	return s == `"json"` || strings.Contains(s, `"type"`)
}

func openAIMessages(msgs []chatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		msg := map[string]any{"role": m.Role}
		if len(m.Images) == 0 {
			msg["content"] = m.Content
			out = append(out, msg)
			continue
		}
		parts := make([]map[string]any, 0, len(m.Images)+1)
		if m.Content != "" {
			parts = append(parts, map[string]any{"type": "text", "text": m.Content})
		}
		for _, img := range m.Images {
			url := img
			if !strings.HasPrefix(url, "data:") {
				url = "data:image/png;base64," + url
			}
			parts = append(parts, map[string]any{
				"type": "image_url", "image_url": map[string]any{"url": url},
			})
		}
		msg["content"] = parts
		out = append(out, msg)
	}
	return out
}

func (s *Server) completionOnce(ctx context.Context, port int, payload map[string]any) (string, *timings, error) {
	body, err := s.upstreamPost(ctx, port, "/completion", payload)
	if err != nil {
		return "", nil, err
	}
	defer body.Close()
	var resp struct {
		Content string   `json:"content"`
		Timings *timings `json:"timings"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return "", nil, errs.Wrap(errs.Engine, err, "failed to parse the engine response")
	}
	return resp.Content, resp.Timings, nil
}

func (s *Server) chatOnce(ctx context.Context, port int, payload map[string]any) (string, *timings, error) {
	body, err := s.upstreamPost(ctx, port, "/v1/chat/completions", payload)
	if err != nil {
		return "", nil, err
	}
	defer body.Close()
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Timings *timings `json:"timings"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return "", nil, errs.Wrap(errs.Engine, err, "failed to parse the engine response")
	}
	if len(resp.Choices) == 0 {
		return "", nil, errs.New(errs.Engine, "engine returned no content")
	}
	return resp.Choices[0].Message.Content, resp.Timings, nil
}

func (s *Server) streamCompletion(w http.ResponseWriter, r *http.Request, port int, model string, payload map[string]any) {
	ctx := r.Context()
	body, err := s.upstreamPost(ctx, port, "/completion", payload)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	defer body.Close()
	streamHeaders(w)
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	sc := newScanner(body)
	sentDone := false
	var last timings
	var streamed int
	for sc.Scan() {
		data := sseData(sc.Text())
		if data == "" {
			continue
		}
		var chunk struct {
			Content string   `json:"content"`
			Stop    bool     `json:"stop"`
			Timings *timings `json:"timings"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Timings != nil {
			last = *chunk.Timings
		}
		if chunk.Content != "" {
			streamed += len(chunk.Content)
			_ = enc.Encode(map[string]any{"model": model, "created_at": now(),
				"response": chunk.Content, "done": false})
			flush(flusher)
		}
		if chunk.Stop {
			sentDone = true
			done := ollamaDone(model, &last)
			done["response"] = ""
			_ = enc.Encode(done)
			flush(flusher)
			break
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		logging.Error("http.stream_failed", errs.Wrap(errs.Engine, err, "completion stream failed"), "model", model)
	}
	if !sentDone {
		done := ollamaDone(model, &last)
		done["response"] = ""
		_ = enc.Encode(done)
		flush(flusher)
	}
	s.Metrics.AddTokens(uint64(last.PromptN), uint64(last.PredictedN))
	s.Metrics.AddStreamedBytes(uint64(streamed))
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, port int, model string, payload map[string]any) {
	ctx := r.Context()
	body, err := s.upstreamPost(ctx, port, "/v1/chat/completions", payload)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	defer body.Close()
	streamHeaders(w)
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	sc := newScanner(body)
	var last timings
	var streamed int
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
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Timings *timings `json:"timings"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Timings != nil {
			last = *chunk.Timings
		}
		if chunk.Usage != nil {
			if last.PromptN == 0 {
				last.PromptN = chunk.Usage.PromptTokens
			}
			if last.PredictedN == 0 {
				last.PredictedN = chunk.Usage.CompletionTokens
			}
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content == "" {
				continue
			}
			streamed += len(c.Delta.Content)
			_ = enc.Encode(map[string]any{
				"model": model, "created_at": now(),
				"message": map[string]string{"role": "assistant", "content": c.Delta.Content},
				"done":    false,
			})
			flush(flusher)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		logging.Error("http.stream_failed", errs.Wrap(errs.Engine, err, "chat stream failed"), "model", model)
	}
	resp := ollamaDone(model, &last)
	resp["message"] = map[string]string{"role": "assistant", "content": ""}
	_ = enc.Encode(resp)
	flush(flusher)
	s.Metrics.AddTokens(uint64(last.PromptN), uint64(last.PredictedN))
	s.Metrics.AddStreamedBytes(uint64(streamed))
}

func (s *Server) embedVectors(ctx context.Context, port int, model string, input json.RawMessage) ([][]float64, error) {
	payload := map[string]any{"model": model, "input": input}
	body, err := s.upstreamPost(ctx, port, "/v1/embeddings", payload)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var resp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, errs.Wrap(errs.Engine, err, "failed to parse the embedding response")
	}
	out := make([][]float64, 0, len(resp.Data))
	for _, d := range resp.Data {
		out = append(out, d.Embedding)
	}
	return out, nil
}

func (s *Server) upstreamPost(ctx context.Context, port int, path string, payload any) (io.ReadCloser, error) {
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
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		return nil, errs.Newf(errs.Engine, "engine returned HTTP %d: %s", resp.StatusCode,
			strings.TrimSpace(string(data)))
	}
	return resp.Body, nil
}

func ollamaDone(model string, t *timings) map[string]any {
	resp := map[string]any{
		"model": model, "created_at": now(),
		"done": true, "done_reason": "stop",
	}
	if t != nil {
		resp["prompt_eval_count"] = t.PromptN
		resp["eval_count"] = t.PredictedN
		resp["prompt_eval_duration"] = int64(t.PromptMS * 1e6)
		resp["eval_duration"] = int64(t.PredictedMS * 1e6)
		resp["total_duration"] = int64((t.PromptMS + t.PredictedMS) * 1e6)
	}
	return resp
}

func parseKeepAlive(raw json.RawMessage) (*time.Duration, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		var d time.Duration
		if n < 0 {
			d = -1
		} else {
			d = time.Duration(n * float64(time.Second))
		}
		return &d, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errs.New(errs.Usage, "keep_alive must be a number of seconds or a duration string")
	}
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "forever" || s == "-1" {
		d := time.Duration(-1)
		return &d, nil
	}
	if s == "0" || s == "0s" {
		d := time.Duration(0)
		return &d, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, errs.Newf(errs.Usage, "invalid keep_alive %q", s).
			WithHint(`use seconds (300), a duration ("5m"), 0 to unload, or -1 to pin`)
	}
	return &d, nil
}

func streamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func flush(f http.Flusher) {
	if f != nil {
		f.Flush()
	}
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

var copyBufPool = sync.Pool{New: func() any {
	b := make([]byte, 32*1024)
	return &b
}}

func copyStream(w http.ResponseWriter, src io.Reader) (int64, error) {
	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)
	buf := *bufp
	flusher, _ := w.(http.Flusher)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			total += int64(wn)
			if flusher != nil {
				flusher.Flush()
			}
			if werr != nil {
				return total, werr
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
