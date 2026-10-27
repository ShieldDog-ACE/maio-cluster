// Package inference runs llama.cpp locally or across RPC workers.
package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

// server is one llama-server child process.
type server struct {
	cmd     *exec.Cmd
	port    int
	logPath string
	done    chan struct{}
	mu      sync.Mutex
	stopped bool
}

func (s *server) baseURL() string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port))
}

func (s *server) stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	killGroup(s.cmd)
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	}
}

func startServer(bin string, model string, extra []string, logDir, tag string) (*server, error) {
	if bin == "" {
		return nil, errs.New(errs.Unavailable, "llama-server was not found").
			WithHint("install the maio llama package, or set llama.server_bin / agent.llama_bin").
			WithHint("the binary must be built with -DGGML_RPC=ON for distributed jobs")
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot create log directory")
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("llama-%s-%d.log", tag, time.Now().UnixNano()))
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot open llama log")
	}
	args := []string{
		"--model", model,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-webui",
	}
	args = append(args, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Env = llamaEnv(bin)
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, errs.Wrap(errs.Unavailable, err, "cannot start llama-server").
			WithHint("check the binary path and shared libraries")
	}
	s := &server{cmd: cmd, port: port, logPath: logPath, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		lf.Close()
		close(s.done)
		if err != nil {
			// The caller reports the log tail when startup fails.
			return
		}
	}()
	return s, nil
}

func (s *server) waitReady(ctx context.Context, timeout time.Duration) error {
	base := s.baseURL()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return errs.New(errs.Unavailable, "llama-server exited during startup").
				WithHint("log: %s\n%s", s.logPath, tail(s.logPath, 2000))
		case <-time.After(300 * time.Millisecond):
		}
		resp, err := client.Get(base + "/health")
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == http.StatusOK {
				return nil
			}
			if code != http.StatusServiceUnavailable && code != http.StatusTooEarly {
				return errs.New(errs.Unavailable, "llama-server health returned HTTP %d", code).
					WithHint("log: %s\n%s", s.logPath, tail(s.logPath, 2000))
			}
		}
	}
	return errs.New(errs.Timeout, "llama-server did not become ready within %s", timeout).
		WithHint("log: %s\n%s", s.logPath, tail(s.logPath, 2000))
}

type completionChunk struct {
	Content string `json:"content"`
	Stop    bool   `json:"stop"`
	Tokens  int    `json:"tokens_predicted"`
}

func streamCompletion(ctx context.Context, base, prompt string, maxTokens int, emit func(text string, tokens int) error) (int, error) {
	body := map[string]any{
		"prompt":       prompt,
		"stream":       true,
		"n_predict":    maxTokens,
		"temperature":  0.7,
		"top_p":        0.95,
		"stop":         []string{"</s>"},
		"cache_prompt": true,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/completion", bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Minute,
	}}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errs.Wrap(errs.Network, err, "completion request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return 0, errs.New(errs.Unavailable, "llama-server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	tokens := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		data := line
		if strings.HasPrefix(data, "data: ") {
			data = strings.TrimPrefix(data, "data: ")
		}
		var chunk completionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Non-JSON lines are llama.cpp progress output; ignore them.
			continue
		}
		if chunk.Content != "" {
			tokens++
			if err := emit(chunk.Content, tokens); err != nil {
				return tokens, err
			}
		}
		if chunk.Stop {
			break
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return tokens, errs.Wrap(errs.Network, err, "completion stream ended")
	}
	return tokens, ctx.Err()
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, errs.Wrap(errs.Network, err, "cannot allocate a local port")
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func tail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, _ := f.Stat()
	if st == nil {
		return ""
	}
	off := st.Size() - int64(n)
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	_, _ = f.ReadAt(buf, off)
	return strings.TrimSpace(string(buf))
}

func llamaEnv(bin string) []string {
	env := os.Environ()
	dir := filepath.Dir(bin)
	lib := filepath.Join(dir, "..", "lib")
	var key string
	switch runtime.GOOS {
	case "darwin":
		key = "DYLD_LIBRARY_PATH"
	case "windows":
		key = "PATH"
	default:
		key = "LD_LIBRARY_PATH"
	}
	sep := string(os.PathListSeparator)
	cur := os.Getenv(key)
	parts := []string{dir, lib}
	if cur != "" {
		parts = append(parts, cur)
	}
	env = append(env, key+"="+strings.Join(parts, sep))
	return env
}

// findLlama locates llama-server.
func findLlama(configured string) string {
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
	}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(d, "llama-server"),
			filepath.Join(d, "..", "lib", "maio-llama", "bin", "llama-server"),
			filepath.Join(d, "..", "maio-llama", "bin", "llama-server"),
			"/usr/lib/maio/bin/llama-server",
		)
	}
	candidates = append(candidates, "llama-server", "/opt/llama.cpp/build/bin/llama-server")
	for _, c := range candidates {
		if strings.ContainsRune(c, os.PathSeparator) {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}
