// Package engine manages the llama.cpp child process: start, health check, stop.
// The child serves the native llama-server API on a random 127.0.0.1 port and maio proxies it internally.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShieldDog-ACE/maio--cluster/internal/config"
)

var healthClient = &http.Client{Timeout: 2 * time.Second}

// Instance is the runtime state of a loaded model (state_dir/run/<name>.json).
type Instance struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Model   string `json:"model"`
	Started string `json:"started"`
}

// Manager manages engine child processes.
type Manager struct {
	Conf *config.Global
}

// New creates a manager.
func New(conf *config.Global) *Manager { return &Manager{Conf: conf} }

func (m *Manager) instancePath(name string) string {
	return filepath.Join(m.Conf.RunDir(), name+".json")
}

// List returns all runtime states.
func (m *Manager) List() ([]Instance, error) {
	ents, err := os.ReadDir(m.Conf.RunDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Instance
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.Conf.RunDir(), e.Name()))
		if err != nil {
			continue
		}
		var inst Instance
		if err := json.Unmarshal(data, &inst); err != nil {
			continue
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get reads one runtime state; returns (nil, nil) when absent.
func (m *Manager) Get(name string) (*Instance, error) {
	data, err := os.ReadFile(m.instancePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var inst Instance
	if err := json.Unmarshal(data, &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

// Ensure makes sure a model is loaded and ready, reusing an existing process.
func (m *Manager) Ensure(ctx context.Context, md *config.Model) (*Instance, error) {
	if inst, err := m.Get(md.Name); err == nil && inst != nil {
		if m.Health(inst.Port) == nil {
			return inst, nil
		}
		_ = m.Stop(md.Name)
	}
	bin, err := m.BinPath()
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate a port: %w", err)
	}
	if err := os.MkdirAll(m.Conf.RunDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.Conf.LogDir(), 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(m.Conf.LogDir(), md.Name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	args := []string{
		"-m", md.FilePath(),
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-webui",
		"-ngl", "99",
	}
	if m.Conf.CtxSize > 0 {
		args = append(args, "-c", strconv.Itoa(m.Conf.CtxSize))
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = m.childEnv(bin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("failed to start engine (%s): %w", bin, err)
	}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()

	inst := &Instance{
		Name:    md.Name,
		Port:    port,
		PID:     cmd.Process.Pid,
		Model:   md.FilePath(),
		Started: time.Now().Format(time.RFC3339),
	}
	if err := m.waitReady(ctx, port, 180*time.Second); err != nil {
		signalGroup(cmd.Process.Pid, syscall.SIGKILL)
		return nil, fmt.Errorf("engine not ready: %w (log %s)", err, logPath)
	}
	if err := os.WriteFile(m.instancePath(md.Name), mustJSON(inst), 0o644); err != nil {
		return nil, err
	}
	return inst, nil
}

// Stop stops a model's engine and cleans up its runtime state.
func (m *Manager) Stop(name string) error {
	inst, err := m.Get(name)
	if err != nil {
		return err
	}
	if inst == nil {
		return nil
	}
	signalGroup(inst.PID, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && processAlive(inst.PID) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(inst.PID) {
		signalGroup(inst.PID, syscall.SIGKILL)
	}
	return os.Remove(m.instancePath(name))
}

// StopAll stops everything.
func (m *Manager) StopAll() error {
	insts, err := m.List()
	if err != nil {
		return err
	}
	for _, in := range insts {
		if err := m.Stop(in.Name); err != nil {
			return err
		}
	}
	return nil
}

// Health probes engine health.
func (m *Manager) Health(port int) error {
	resp, err := healthClient.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) waitReady(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.Health(port) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for health check (%s)", timeout)
}

// BinPath locates llama-server: config > env > common paths > PATH.
func (m *Manager) BinPath() (string, error) {
	var cands []string
	if m.Conf.LlamaBin != "" {
		cands = append(cands, m.Conf.LlamaBin)
	}
	if p := os.Getenv("MAIO_LLAMA_BIN"); p != "" {
		cands = append(cands, p)
	}
	cands = append(cands, "/opt/maio/bin/llama-server", "/usr/local/bin/llama-server")
	if p, err := exec.LookPath("llama-server"); err == nil {
		cands = append(cands, p)
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs, nil
			}
			return c, nil
		}
	}
	return "", fmt.Errorf("llama-server not found: set llama_bin in maio.conf (e.g. /opt/maio/bin/llama-server)")
}

func (m *Manager) childEnv(bin string) []string {
	libs := []string{filepath.Dir(bin)}
	if m.Conf.LlamaLib != "" {
		for _, p := range strings.Split(m.Conf.LlamaLib, ":") {
			if p != "" {
				libs = append(libs, p)
			}
		}
	}
	cands := []string{
		"/usr/local/cuda/lib64",
		"/usr/local/lib/ollama/cuda_v12",
		filepath.Join(filepath.Dir(bin), "..", "cuda"),
	}
	if matches, err := filepath.Glob("/usr/local/cuda*/lib64"); err == nil {
		cands = append(cands, matches...)
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			libs = append(libs, c)
		}
	}
	return setEnv(os.Environ(), "LD_LIBRARY_PATH", strings.Join(dedupe(libs), ":"))
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return b
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+value)
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
