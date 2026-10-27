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
	"sync"
	"syscall"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/owner"
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
	Parallel  int    `json:"parallel,omitempty"`
	KVType    string `json:"kv_type,omitempty"`
	VRAMBytes uint64 `json:"vram_bytes,omitempty"`
}

// Options controls one engine start.
type Options struct {
	Ctx          int    // context length; 0 = config/default
	Force        bool   // skip the VRAM budget check
	ContextFlag  string // how to name the context switch in budget hints
	Parallel     int    // server slots; 0 = config/default
	KVType       string // f16, q8_0, q4_0; "" = config/default
	Threads      int    // CPU threads; 0 = config/default
	FlashAttn    string // auto, on, off; "" = config/default
	NoMmap       bool   // force --no-mmap for this start
	LogVerbosity int    // llama.cpp -lv; 0 = config/default
}

// Manager manages engine child processes.
type Manager struct {
	Conf *config.Global

	// loadMu serializes starts and stops. VRAM budgeting is only meaningful if
	// two concurrent requests cannot both pass the check and then both start.
	loadMu sync.Mutex
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
		if e.Name() == "resident.json" {
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
		if inst.Name == "" || inst.Port <= 0 || inst.PID <= 0 {
			logging.Warn("engine.instance_invalid", "path", p, "error", "missing name, port or pid")
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

// engineConfig is the resolved engine tuning for one start.
type engineConfig struct {
	ctx      int
	parallel int
	threads  int
	kvType   string
	flash    string
	noMmap   bool
	logLevel int
}

func (m *Manager) resolveOptions(opt Options) engineConfig {
	c := engineConfig{
		ctx:      opt.Ctx,
		parallel: opt.Parallel,
		threads:  opt.Threads,
		kvType:   opt.KVType,
		flash:    opt.FlashAttn,
		noMmap:   opt.NoMmap,
		logLevel: opt.LogVerbosity,
	}
	if c.ctx <= 0 {
		c.ctx = m.Conf.CtxSize
	}
	if c.parallel <= 0 {
		c.parallel = m.Conf.Parallel
	}
	if c.parallel < 1 {
		c.parallel = 1
	}
	if c.threads <= 0 {
		c.threads = m.Conf.Threads
	}
	if c.kvType == "" {
		c.kvType = m.Conf.KVType
	}
	if c.kvType == "" {
		c.kvType = "f16"
	}
	if c.flash == "" {
		c.flash = m.Conf.FlashAttn
	}
	if c.flash == "" {
		c.flash = "auto"
	}
	if !c.noMmap && m.Conf.Mmap == 0 {
		c.noMmap = true
	}
	if c.logLevel <= 0 {
		c.logLevel = m.Conf.LogVerbosity
	}
	if c.logLevel < 0 {
		c.logLevel = 0
	}
	return c
}

// instanceMatches reports whether an existing process already has the tuning
// the caller asked for. A field that was not persisted (old V2 instance state)
// is treated as a match when the default applies, and as stale otherwise.
func instanceMatches(inst *Instance, c engineConfig) bool {
	if inst == nil {
		return false
	}
	if c.ctx > 0 && inst.Context > 0 && int(inst.Context) != c.ctx {
		return false
	}
	instParallel := inst.Parallel
	if instParallel < 1 {
		instParallel = 1
	}
	if instParallel != c.parallel {
		return false
	}
	if inst.KVType != "" && inst.KVType != c.kvType {
		return false
	}
	if inst.KVType == "" && c.kvType != "f16" {
		return false
	}
	return true
}

// Ensure makes sure a model is loaded and ready, reusing an existing process.
func (m *Manager) Ensure(ctx context.Context, md *config.Model, opt Options) (*Instance, error) {
	cfg := m.resolveOptions(opt)
	if inst, err := m.Get(md.Name); err == nil && inst != nil {
		if m.Health(inst.Port) == nil && processAlive(inst.PID) && instanceMatches(inst, cfg) {
			return inst, nil
		}
	}
	if cfg.kvType != "f16" && cfg.flash == "off" {
		return nil, errs.New(errs.Config, "a quantized KV cache requires flash attention").
			WithHint(`set flash_attn = "auto" or "on", or use kv_type = "f16"`)
	}

	// Starts are serialized so two concurrent requests cannot both pass the
	// VRAM budget and then both start an engine.
	m.loadMu.Lock()
	defer m.loadMu.Unlock()

	if inst, err := m.Get(md.Name); err == nil && inst != nil {
		if m.Health(inst.Port) == nil && processAlive(inst.PID) && instanceMatches(inst, cfg) {
			return inst, nil
		}
		if err := m.stopLocked(md.Name); err != nil {
			logging.Warn("engine.replace_failed", "model", md.Name, "error", err.Error())
		}
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
	ctxLen := cfg.ctx
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

	// Pick the largest number of slots that fits. A request for more slots than
	// the GPU can hold degrades to a smaller number instead of failing.
	chosen := cfg.parallel
	var budget *vram.Budget
	for p := cfg.parallel; p >= 1; p-- {
		b := vram.EstimateWith(info, st.Size()+extraWeights, vram.Options{
			Context: ctxLen, Parallel: p, KVType: cfg.kvType,
		}, hw)
		budget = b
		chosen = p
		if !hw.HasGPU() || b.Fits() || opt.Force {
			break
		}
	}
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
	if chosen < cfg.parallel {
		logging.Warn("engine.parallel_reduced", "model", md.Name, "requested", cfg.parallel,
			"chosen", chosen, "reason", "vram")
		fmt.Fprintf(os.Stderr, "maio: warning: %s fits only %d parallel slot(s), not %d; reduce ctx_size or use kv_type = \"q8_0\" to allow more\n",
			md.Name, chosen, cfg.parallel)
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
	logFile, err := m.openEngineLog(logPath)
	if err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot open engine log %s", logPath).
			WithHint("check state_dir permissions, or run with sudo")
	}

	ctxToPass := ctxLen
	if ctxToPass <= 0 {
		ctxToPass = int(info.ContextLength)
	}
	args := []string{
		"-m", filePath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-webui",
		"--parallel", strconv.Itoa(chosen),
		"--cache-type-k", cfg.kvType,
		"--cache-type-v", cfg.kvType,
		"--flash-attn", cfg.flash,
		"-lv", strconv.Itoa(cfg.logLevel),
	}
	if hw.HasGPU() {
		args = append(args, "-ngl", "99")
	} else {
		args = append(args, "-ngl", "0")
	}
	if ctxToPass > 0 {
		args = append(args, "-c", strconv.Itoa(ctxToPass))
	}
	if cfg.threads > 0 {
		args = append(args, "-t", strconv.Itoa(cfg.threads))
	}
	if cfg.noMmap {
		args = append(args, "--no-mmap")
	}
	if mmproj != "" {
		args = append(args, "--mmproj", mmproj)
	} else if info.Vision {
		fmt.Fprintf(os.Stderr, "maio: warning: %s looks like a vision model but no mmproj*.gguf was found; vision input is unavailable\n", md.Name)
	}
	logging.Info("engine.start", "model", md.Name, "bin", bin, "port", port,
		"ctx", ctxToPass, "parallel", chosen, "kv_type", cfg.kvType, "flash_attn", cfg.flash,
		"mmproj", mmproj, "vram_needed", budget.Total, "vram_free", budget.Free)
	fmt.Fprintf(os.Stderr, "maio: loading %s (port %d, ctx %d, slots %d, kv %s)\n", md.Name, port, ctxToPass, chosen, cfg.kvType)

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
		Parallel:  chosen,
		KVType:    cfg.kvType,
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
	logging.Info("engine.ready", "model", md.Name, "port", port, "pid", inst.PID,
		"parallel", chosen, "vram_bytes", budget.Total)
	return inst, nil
}

// Stop stops a model's engine and cleans up its runtime state.
func (m *Manager) Stop(name string) error {
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	return m.stopLocked(name)
}

func (m *Manager) stopLocked(name string) error {
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
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	insts, err := m.List()
	if err != nil {
		return err
	}
	var firstErr error
	for _, in := range insts {
		if err := m.stopLocked(in.Name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CleanupStale removes runtime state whose process is gone or no longer looks
// like llama-server. Live engines are kept and can be adopted by Ensure.
func (m *Manager) CleanupStale() (int, error) {
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	insts, err := m.List()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, in := range insts {
		alive := processAlive(in.PID)
		ours := alive && strings.Contains(procCmdline(in.PID), "llama-server")
		if ours {
			if in.ProcStart != "" {
				if now := procStart(in.PID); now != "" && now != in.ProcStart {
					ours = false
				}
			}
		}
		if ours {
			continue
		}
		if err := os.Remove(m.instancePath(in.Name)); err != nil && !os.IsNotExist(err) {
			logging.Warn("engine.cleanup_failed", "model", in.Name, "error", err.Error())
			continue
		}
		removed++
		logging.Warn("engine.cleanup_stale", "model", in.Name, "pid", in.PID, "alive", alive)
	}
	return removed, nil
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
	cands = append(cands,
		"/opt/maio/llama/bin/llama-server",
		"/opt/maio/bin/llama-server",
		"/usr/local/bin/llama-server",
		"/usr/bin/llama-server",
	)
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
	libs := []string{filepath.Dir(bin), filepath.Join(filepath.Dir(bin), "..", "lib"), "/opt/maio/llama/lib"}
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

// maxEngineLogBytes keeps a single engine log bounded on a 24/7 host. The
// previous log is kept as <name>.log.1 for post-mortems.
const maxEngineLogBytes = 8 << 20

func (m *Manager) openEngineLog(path string) (*os.File, error) {
	if st, err := os.Stat(path); err == nil && st.Size() > maxEngineLogBytes {
		_ = os.Rename(path, path+".1")
	}
	runAs := m.Conf.RunAs
	if strings.HasPrefix(path, "/home/") {
		runAs = ""
	}
	owner.ChownMode(path, runAs, 0o640)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	owner.ChownMode(path, runAs, 0o640)
	return f, nil
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
