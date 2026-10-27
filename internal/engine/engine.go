// Package engine manages llama.cpp child processes: start, health check, stop.
// The child serves the native llama-server API on a random 127.0.0.1 port and
// maio proxies it internally.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/vram"
)

var healthClient = &http.Client{Timeout: 2 * time.Second}

const (
	readyTimeout    = 180 * time.Second
	stopGracePeriod = 5 * time.Second
	logTailLines    = 20
)

// Instance is the runtime state of a loaded model (state_dir/run/<name>.json).
type Instance struct {
	Name      string `json:"name"`
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	Model     string `json:"model"`
	Started   string `json:"started"`
	ProcStart string `json:"proc_start,omitempty"`
	Context   uint32 `json:"context,omitempty"`
	VRAMBytes uint64 `json:"vram_bytes,omitempty"`
}

// Options controls one engine start.
type Options struct {
	Ctx         int    // context length; 0 = config/default
	Force       bool   // skip the VRAM budget check
	ContextFlag string // how to name the context switch in budget hints
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
		return nil, errs.Wrap(errs.Permission, err, "cannot read runtime directory %s", m.Conf.RunDir()).
			WithHint("check state_dir permissions, or run with sudo")
	}
	var out []Instance
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(m.Conf.RunDir(), e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			logging.Warn("engine.instance_unreadable", "path", p, "error", err.Error())
			continue
		}
		var inst Instance
		if err := json.Unmarshal(data, &inst); err != nil {
			logging.Warn("engine.instance_invalid", "path", p, "error", err.Error())
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
		return nil, errs.Wrap(errs.Permission, err, "cannot read runtime state for %s", name)
	}
	var inst Instance
	if err := json.Unmarshal(data, &inst); err != nil {
		return nil, errs.Wrap(errs.IO, err, "corrupt runtime state for %s", name).
			WithHint("delete %s and retry", m.instancePath(name))
	}
	return &inst, nil
}

// Ensure makes sure a model is loaded and ready, reusing an existing process.
func (m *Manager) Ensure(ctx context.Context, md *config.Model, opt Options) (*Instance, error) {
	if inst, err := m.Get(md.Name); err == nil && inst != nil {
		if m.Health(inst.Port) == nil && processAlive(inst.PID) {
			return inst, nil
		}
		_ = m.Stop(md.Name)
	}
	filePath := md.FilePath()
	st, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.Newf(errs.NotFound, "model file is missing: %s", filePath).
				WithHint("run maio show %s, or install the model again", md.Name)
		}
		return nil, errs.Wrap(errs.Permission, err, "cannot read model file %s", filePath).
			WithHint("check file permissions, or run with sudo")
	}
	if !st.Mode().IsRegular() {
		return nil, errs.Newf(errs.Verify, "model path is not a regular file: %s", filePath)
	}
	info, err := gguf.Read(filePath)
	if err != nil {
		return nil, err
	}
	ctxLen := opt.Ctx
	if ctxLen <= 0 {
		ctxLen = m.Conf.CtxSize
	}
	if ctxLen <= 0 {
		ctxLen = int(info.ContextLength)
	}
	mmproj := findMMProj(md.Dir, filePath)
	extraWeights := int64(0)
	if mmproj != "" {
		if ms, statErr := os.Stat(mmproj); statErr == nil {
			extraWeights = ms.Size()
		}
	}
	hw := hardware.Detect(m.Conf.ModelsRoot)
	budget := vram.Estimate(info, st.Size()+extraWeights, ctxLen, hw)
	if hw.HasGPU() && !budget.Fits() && !opt.Force {
		logging.Error("engine.vram_refused", nil,
			"model", md.Name, "needed", budget.Total, "free", budget.Free, "ctx", budget.Context)
		flag := opt.ContextFlag
		if flag == "" {
			flag = "--ctx"
		}
		return nil, errs.Newf(errs.Hardware, "not enough GPU memory for %s: %s", md.Name, budget.Summary()).
			WithHint("%s", budget.Suggestion(flag))
	}
	if hw.HasGPU() && !budget.KVKnown {
		logging.Warn("engine.vram_estimate_partial", "model", md.Name)
		fmt.Fprintf(os.Stderr, "maio: warning: GGUF metadata is incomplete; the VRAM estimate for %s is approximate\n", md.Name)
	}
	if opt.Force && hw.HasGPU() && !budget.Fits() {
		logging.Warn("engine.vram_forced", "model", md.Name, "needed", budget.Total, "free", budget.Free)
		fmt.Fprintf(os.Stderr, "maio: warning: skipping the VRAM check (--force); the engine may fail with an out-of-memory error\n")
	}

	bin, err := m.BinPath()
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, errs.Wrap(errs.Engine, err, "cannot allocate a local port for the engine").
			WithHint("check the local network stack, then retry")
	}
	if err := os.MkdirAll(m.Conf.RunDir(), 0o755); err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot create runtime directory %s", m.Conf.RunDir()).
			WithHint("set state_dir to a writable directory, or run with sudo")
	}
	if err := os.MkdirAll(m.Conf.LogDir(), 0o755); err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot create log directory %s", m.Conf.LogDir()).
			WithHint("set state_dir to a writable directory, or run with sudo")
	}
	logPath := m.Conf.EngineLogPath(md.Name)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot open engine log %s", logPath).
			WithHint("check state_dir permissions, or run with sudo")
	}

	args := []string{
		"-m", filePath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-webui",
	}
	if hw.HasGPU() {
		args = append(args, "-ngl", "99")
	} else {
		args = append(args, "-ngl", "0")
	}
	ctxToPass := ctxLen
	if ctxToPass <= 0 {
		ctxToPass = int(info.ContextLength)
	}
	if ctxToPass > 0 {
		args = append(args, "-c", strconv.Itoa(ctxToPass))
	}
	if mmproj != "" {
		args = append(args, "--mmproj", mmproj)
	} else if info.Vision {
		fmt.Fprintf(os.Stderr, "maio: warning: %s looks like a vision model but no mmproj*.gguf was found; vision input is unavailable\n", md.Name)
	}
	logging.Info("engine.start", "model", md.Name, "bin", bin, "port", port,
		"ctx", ctxToPass, "mmproj", mmproj, "vram_needed", budget.Total, "vram_free", budget.Free)
	fmt.Fprintf(os.Stderr, "maio: loading %s (port %d, ctx %d)\n", md.Name, port, ctxToPass)

	cmd := exec.Command(bin, args...)
	cmd.Env = m.childEnv(bin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, errs.Wrap(errs.Engine, err, "cannot start llama-server (%s)", bin).
			WithHint("check llama_bin in maio.conf, file permissions and shared libraries")
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		_ = logFile.Close()
	}()
	inst := &Instance{
		Name:      md.Name,
		Port:      port,
		PID:       cmd.Process.Pid,
		Model:     filePath,
		Started:   time.Now().Format(time.RFC3339),
		ProcStart: procStart(cmd.Process.Pid),
		Context:   uint32(ctxToPass),
		VRAMBytes: budget.Total,
	}
	if err := m.waitReady(ctx, port, done); err != nil {
		signalGroup(cmd.Process.Pid, syscall.SIGKILL)
		tail := tailFile(logPath, logTailLines)
		logging.Error("engine.start_failed", err, "model", md.Name, "log", logPath)
		if tail != "" {
			return nil, errs.Wrap(errs.Engine, err, "engine failed to become ready: %v", err).
				WithHint("last log lines from %s:\n%s", logPath, tail)
		}
		return nil, errs.Wrap(errs.Engine, err, "engine failed to become ready for %s", md.Name).
			WithHint("inspect %s for details", logPath)
	}
	if err := writeInstance(m.instancePath(md.Name), inst); err != nil {
		signalGroup(cmd.Process.Pid, syscall.SIGKILL)
		return nil, err
	}
	logging.Info("engine.ready", "model", md.Name, "port", port, "pid", inst.PID, "vram_bytes", budget.Total)
	return inst, nil
}

// Stop stops a model's engine and cleans up its runtime state.
func (m *Manager) Stop(name string) error {
	inst, err := m.Get(name)
	if err != nil {
		return err
	}
	if inst == nil {
		logging.Info("engine.stop", "model", name, "state", "not-loaded")
		return nil
	}
	if !processAlive(inst.PID) {
		_ = os.Remove(m.instancePath(name))
		logging.Info("engine.stop", "model", name, "state", "stale", "pid", inst.PID)
		return nil
	}
	if inst.ProcStart != "" {
		if now := procStart(inst.PID); now != "" && now != inst.ProcStart {
			return errs.Newf(errs.Engine, "refusing to stop %s: PID %d was reused by another process", name, inst.PID).
				WithHint("delete %s after checking ps, then retry", m.instancePath(name))
		}
	}
	if !strings.Contains(procCmdline(inst.PID), "llama-server") {
		return errs.Newf(errs.Engine, "refusing to stop %s: PID %d is not llama-server", name, inst.PID).
			WithHint("delete %s after checking ps, then retry", m.instancePath(name))
	}
	logging.Info("engine.stop", "model", name, "pid", inst.PID, "port", inst.Port)
	signalGroup(inst.PID, syscall.SIGTERM)
	deadline := time.Now().Add(stopGracePeriod)
	for time.Now().Before(deadline) && processAlive(inst.PID) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(inst.PID) {
		signalGroup(inst.PID, syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
	}
	if err := os.Remove(m.instancePath(name)); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.IO, err, "engine stopped but its runtime state could not be removed").
			WithHint("delete %s manually", m.instancePath(name))
	}
	return nil
}

// StopAll stops everything.
func (m *Manager) StopAll() error {
	insts, err := m.List()
	if err != nil {
		return err
	}
	var firstErr error
	for _, in := range insts {
		if err := m.Stop(in.Name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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

func (m *Manager) waitReady(ctx context.Context, port int, done <-chan error) error {
	deadline := time.Now().Add(readyTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if m.Health(port) == nil {
			return nil
		}
		select {
		case err := <-done:
			if err == nil {
				return fmt.Errorf("engine exited before it became ready")
			}
			return fmt.Errorf("engine exited before it became ready: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for the health check after %s", readyTimeout)
		}
	}
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
	cands = append(cands, "/opt/maio/bin/llama-server", "/usr/local/bin/llama-server", "/usr/bin/llama-server")
	if p, err := exec.LookPath("llama-server"); err == nil {
		cands = append(cands, p)
	}
	for _, c := range cands {
		st, err := os.Stat(c)
		if err != nil || st.IsDir() {
			continue
		}
		if st.Mode()&0o111 == 0 {
			return "", errs.Newf(errs.Engine, "llama-server is not executable: %s", c).
				WithHint("run chmod +x %s, or fix llama_bin in maio.conf", c)
		}
		abs, err := filepath.Abs(c)
		if err == nil {
			return abs, nil
		}
		return c, nil
	}
	return "", errs.New(errs.Engine, "llama-server was not found").
		WithHint("set llama_bin in %s (for example /opt/maio/bin/llama-server), or install llama.cpp", m.Conf.Source)
}

// Version runs llama-server --version and returns its first non-empty line.
func Version(bin string) (string, error) { return VersionEnv(bin, nil) }

// VersionEnv is Version with an explicit child environment (needed when
// llama-server has shared libraries outside the default loader path).
func VersionEnv(bin string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "unknown", nil
}

// Version reports the detected llama-server version with maio's library path.
func (m *Manager) Version() (string, error) {
	bin, err := m.BinPath()
	if err != nil {
		return "", err
	}
	return VersionEnv(bin, m.childEnv(bin))
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

func findMMProj(dir, modelFile string) string {
	dir = filepath.Dir(modelFile)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var cands []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
			continue
		}
		if !strings.Contains(strings.ToLower(e.Name()), "mmproj") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if p != modelFile {
			cands = append(cands, p)
		}
	}
	sort.Strings(cands)
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func writeInstance(path string, inst *Instance) error {
	data, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return errs.Wrap(errs.IO, err, "cannot serialize runtime state")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write runtime state %s", path).
			WithHint("check state_dir permissions")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errs.Wrap(errs.IO, err, "cannot save runtime state %s", path)
	}
	return nil
}

func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func procStart(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return ""
	}
	fields := strings.Fields(s[i+2:])
	// fields[0] is state (field 3), so starttime (field 22) is fields[19].
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}

func procCmdline(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(data), "\x00", " ")
}

func tailFile(path string, lines int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	const maxBytes = 16 << 10
	start := st.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	text := strings.TrimRight(string(data), "\n")
	parts := strings.Split(text, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
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
