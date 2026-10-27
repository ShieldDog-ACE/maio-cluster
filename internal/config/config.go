// Package config handles the global maio.conf, the per-user fallback config and
// the per-model maio.toml.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/auth"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/naming"
	"github.com/ShieldDog-ACE/maio-cluster/internal/owner"
	"github.com/ShieldDog-ACE/maio-cluster/internal/toml"
)

const (
	defaultModelsRoot = "/var/lib/maio/models"
	defaultStateDir   = "/var/lib/maio"
	defaultHost       = "127.0.0.1"
	defaultPort       = 11434
	defaultMirror     = "https://www.modelscope.cn"
	systemConfPath    = "/etc/maio/maio.conf"
)

// Global is the global config (one per machine, /etc/maio/maio.conf, or a
// per-user fallback for rootless installs).
type Global struct {
	Boot       int    // start on boot: 1=yes, 0=no (default 1)
	ModelsRoot string // model root (default download root)
	StateDir   string // configured runtime state and log root
	Host       string
	Port       int
	Mirror     string // download mirror
	LlamaBin   string // path to llama-server; empty = auto-detect
	LlamaLib   string // extra library dirs, colon separated; empty = auto-detect
	CtxSize    int    // context length; 0 = model default

	// V3: HTTP service, security and admission control.
	Auth           string // api key mode: auto, on, off
	APIKey         string // explicit key; the key file is preferred
	AllowInsecure  int    // 1 = allow auth=off on a non-loopback listener
	RateLimit      int    // requests per minute per client; 0 = disabled
	RateBurst      int    // token bucket burst
	MaxConcurrency int    // active inference requests
	MaxQueue       int    // requests allowed to wait; beyond this = 429
	QueueTimeout   int    // seconds to wait for a slot; 0 = until cancelled
	RequestTimeout int    // whole-request timeout in seconds; 0 = none
	MaxBodyMB      int    // request body limit in MiB
	IdleUnload     int    // seconds before an unpinned model is unloaded; 0 = never
	AccessLog      int    // 1 = one JSONL audit line per HTTP request
	TrustProxy     int    // 1 = trust X-Forwarded-For for the client IP
	CORS           int    // 1 = send Access-Control-Allow-Origin: *

	// V3: llama.cpp engine tuning.
	Parallel     int    // server slots (-np); also bounds KV memory
	Threads      int    // CPU threads; 0 = llama.cpp default
	KVType       string // KV cache type: f16, q8_0, q4_0
	FlashAttn    string // auto, on, off
	Mmap         int    // 1 = mmap model weights (default), 0 = read fully
	LogVerbosity int    // llama-server -lv: 0 generic ... 5 debug

	// V3: process limits and daemon behavior.
	MemoryLimitMB int    // Go soft memory limit for the control plane
	MaxProcs      int    // GOMAXPROCS; 0 = all cores
	MaxOpenFiles  int    // RLIMIT_NOFILE target
	Restore       int    // 1 = reload resident models on service start
	RunAs         string // systemd User= for the generated unit; empty = root

	// V3: optional TLS termination.
	TLSCert string
	TLSKey  string
	TLSCA   string

	// RuntimeDir is the effective directory used for run/ and log/. It is the
	// configured StateDir when writable, otherwise a per-user state directory.
	RuntimeDir string
	// Source is the file this config was loaded from ("" when defaults).
	Source string
	// Scope is "user" or "system"; it selects the config/state/service set.
	Scope string
}

// DefaultGlobal returns the built-in defaults. Start-on-boot is on by default.
func DefaultGlobal() *Global {
	return &Global{
		Boot:           1,
		ModelsRoot:     defaultModelsRoot,
		StateDir:       defaultStateDir,
		Host:           defaultHost,
		Port:           defaultPort,
		Mirror:         defaultMirror,
		RuntimeDir:     defaultStateDir,
		Auth:           "auto",
		RateLimit:      600,
		RateBurst:      120,
		MaxConcurrency: 8,
		MaxQueue:       64,
		QueueTimeout:   300,
		MaxBodyMB:      16,
		IdleUnload:     300,
		AccessLog:      1,
		CORS:           0,
		Parallel:       2,
		KVType:         "f16",
		FlashAttn:      "auto",
		Mmap:           1,
		LogVerbosity:   3,
		MemoryLimitMB:  180,
		MaxOpenFiles:   1048576,
		Restore:        1,
	}
}

// userDefaults gives a user-scope config writable, self-contained paths.
func userDefaults() (modelsRoot, stateDir string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "maio-models"), filepath.Join(os.TempDir(), "maio-state")
	}
	return filepath.Join(home, "AI"), filepath.Join(home, ".local", "state", "maio")
}

// UserConfPath returns the per-user config path.
func UserConfPath() string {
	if p := os.Getenv("XDG_CONFIG_HOME"); p != "" {
		return filepath.Join(p, "maio", "maio.conf")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "maio.conf")
	}
	return filepath.Join(home, ".config", "maio", "maio.conf")
}

// ConfPath returns the global config path.
//
// Lookup order:
//  1. MAIO_CONF
//  2. root: /etc/maio/maio.conf, then the root user's fallback
//  3. non-root: the per-user config (if it exists), then /etc/maio/maio.conf
func ConfPath() string {
	if p := os.Getenv("MAIO_CONF"); p != "" {
		return p
	}
	system := systemConfPath
	user := UserConfPath()
	if os.Geteuid() == 0 {
		if fileExists(system) {
			return system
		}
		if fileExists(user) {
			return user
		}
		return system
	}
	if fileExists(user) {
		return user
	}
	if fileExists(system) {
		return system
	}
	return system
}

// ConfPathFor returns the config path for an explicit scope. MAIO_CONF always
// wins so tests and unusual layouts can override both scopes.
func ConfPathFor(scope string) string {
	if p := os.Getenv("MAIO_CONF"); p != "" {
		return p
	}
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "system":
		return systemConfPath
	case "user":
		return UserConfPath()
	default:
		return ConfPath()
	}
}

// IndexPath returns the model index path (same directory as the global config).
func IndexPath() string {
	return filepath.Join(filepath.Dir(ConfPath()), "index.toml")
}

// UserIndexPath returns the per-user index path.
func UserIndexPath() string {
	return filepath.Join(filepath.Dir(UserConfPath()), "index.toml")
}

// LoadGlobal reads the config using the legacy root/user auto-detection.
func LoadGlobal() (*Global, error) { return LoadGlobalMode("auto") }

// LoadGlobalMode reads the user or system config explicitly. A missing file
// means built-in defaults for that scope.
func LoadGlobalMode(scope string) (*Global, error) {
	g := DefaultGlobal()
	p := ConfPathFor(scope)
	g.Source = p
	requested := strings.ToLower(strings.TrimSpace(scope))
	switch requested {
	case "system":
		g.Scope = "system"
	case "user":
		g.Scope = "user"
		g.ModelsRoot, g.StateDir = userDefaults()
		g.RunAs = ""
	default:
		// Legacy auto: system config means system scope, everything else user.
		if p == systemConfPath {
			g.Scope = "system"
		} else {
			g.Scope = "user"
		}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			g.RuntimeDir = resolveRuntimeDir(g.StateDir)
			return g, nil
		}
		if os.IsPermission(err) {
			return nil, errs.Wrap(errs.Permission, err, "cannot read global config %s", p).
				WithHint("check file permissions, or set MAIO_CONF to a readable config")
		}
		return nil, errs.Wrap(errs.IO, err, "cannot read global config %s", p)
	}
	doc, err := toml.Parse(data)
	if err != nil {
		return nil, errs.Wrap(errs.Config, err, "invalid global config %s", p).
			WithHint("fix the reported line, or delete %s to recreate defaults", p)
	}
	known := []string{
		"boot", "models_root", "state_dir", "host", "port", "mirror", "llama_bin", "llama_lib", "ctx_size",
		"auth", "api_key", "allow_insecure", "rate_limit", "rate_burst", "max_concurrency", "max_queue",
		"queue_timeout", "request_timeout", "max_body_mb", "idle_unload", "access_log", "trust_proxy", "cors",
		"parallel", "threads", "kv_type", "flash_attn", "mmap", "llama_log_verbosity", "memory_limit_mb", "max_procs",
		"max_open_files", "restore", "run_as", "tls_cert", "tls_key", "tls_ca",
	}
	knownSet := make(map[string]bool, len(known))
	for _, k := range known {
		knownSet[k] = true
	}
	for _, k := range doc.Keys() {
		if !knownSet[k] {
			return nil, errs.Newf(errs.Config, "%s: line %d: unknown key %q", p, doc.Line(k), k).
				WithHint("valid keys: %s", strings.Join(known, ", "))
		}
	}

	if doc.Has("boot") {
		v, ok := doc.Int("boot")
		if !ok {
			return nil, typeErr(p, doc, "boot", "an integer 0 or 1")
		}
		if v != 0 && v != 1 {
			return nil, errs.Newf(errs.Config, "%s: line %d: boot must be 0 or 1", p, doc.Line("boot")).
				WithHint("use boot = 1 (start on boot) or boot = 0 (manual start)")
		}
		g.Boot = int(v)
	}
	if doc.Has("models_root") {
		v, ok := doc.String("models_root")
		if !ok {
			return nil, typeErr(p, doc, "models_root", `an absolute path string such as "/var/lib/maio/models"`)
		}
		g.ModelsRoot = v
	}
	if doc.Has("state_dir") {
		v, ok := doc.String("state_dir")
		if !ok {
			return nil, typeErr(p, doc, "state_dir", `an absolute path string such as "/var/lib/maio"`)
		}
		g.StateDir = v
	}
	if doc.Has("host") {
		v, ok := doc.String("host")
		if !ok {
			return nil, typeErr(p, doc, "host", `an address string such as "127.0.0.1" or "0.0.0.0"`)
		}
		g.Host = v
	}
	if doc.Has("port") {
		v, ok := doc.Int("port")
		if !ok {
			return nil, typeErr(p, doc, "port", "an integer between 1 and 65535")
		}
		g.Port = int(v)
	}
	if doc.Has("mirror") {
		v, ok := doc.String("mirror")
		if !ok {
			return nil, typeErr(p, doc, "mirror", "a URL string, or \"\" to disable the mirror")
		}
		g.Mirror = v
	}
	if doc.Has("llama_bin") {
		v, ok := doc.String("llama_bin")
		if !ok {
			return nil, typeErr(p, doc, "llama_bin", "an absolute path string, or \"\" for auto-detection")
		}
		g.LlamaBin = v
	}
	if doc.Has("llama_lib") {
		v, ok := doc.String("llama_lib")
		if !ok {
			return nil, typeErr(p, doc, "llama_lib", `colon-separated absolute directories, or ""`)
		}
		g.LlamaLib = v
	}
	if doc.Has("ctx_size") {
		v, ok := doc.Int("ctx_size")
		if !ok {
			return nil, typeErr(p, doc, "ctx_size", "a non-negative integer (0 = model default)")
		}
		g.CtxSize = int(v)
	}
	if err := g.loadV3(doc, p); err != nil {
		return nil, err
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	g.RuntimeDir = resolveRuntimeDir(g.StateDir)
	return g, nil
}

// loadV3 reads the server, security and engine keys added in V3. Every key is
// optional so a V2 config keeps loading with the new defaults.
func (g *Global) loadV3(doc *toml.Document, p string) error {
	if err := optString(doc, p, "auth", &g.Auth, `"auto", "on" or "off"`); err != nil {
		return err
	}
	if err := optString(doc, p, "api_key", &g.APIKey, "a key string (prefer the key file managed by maio token)"); err != nil {
		return err
	}
	if err := optBool(doc, p, "allow_insecure", &g.AllowInsecure); err != nil {
		return err
	}
	if err := optInt(doc, p, "rate_limit", &g.RateLimit, "requests per minute (0 disables limiting)"); err != nil {
		return err
	}
	if err := optInt(doc, p, "rate_burst", &g.RateBurst, "a positive burst size"); err != nil {
		return err
	}
	if err := optInt(doc, p, "max_concurrency", &g.MaxConcurrency, "the number of active inference requests"); err != nil {
		return err
	}
	if err := optInt(doc, p, "max_queue", &g.MaxQueue, "the number of waiting requests"); err != nil {
		return err
	}
	if err := optInt(doc, p, "queue_timeout", &g.QueueTimeout, "seconds to wait for a slot (0 = forever)"); err != nil {
		return err
	}
	if err := optInt(doc, p, "request_timeout", &g.RequestTimeout, "seconds per request (0 = no limit)"); err != nil {
		return err
	}
	if err := optInt(doc, p, "max_body_mb", &g.MaxBodyMB, "the request body limit in MiB"); err != nil {
		return err
	}
	if err := optInt(doc, p, "idle_unload", &g.IdleUnload, "seconds before an unpinned model unloads (0 = never)"); err != nil {
		return err
	}
	if err := optBool(doc, p, "access_log", &g.AccessLog); err != nil {
		return err
	}
	if err := optBool(doc, p, "trust_proxy", &g.TrustProxy); err != nil {
		return err
	}
	if err := optBool(doc, p, "cors", &g.CORS); err != nil {
		return err
	}
	if err := optInt(doc, p, "parallel", &g.Parallel, "the number of llama-server slots"); err != nil {
		return err
	}
	if err := optInt(doc, p, "threads", &g.Threads, "the CPU thread count (0 = engine default)"); err != nil {
		return err
	}
	if err := optString(doc, p, "kv_type", &g.KVType, `"f16", "q8_0" or "q4_0"`); err != nil {
		return err
	}
	if err := optString(doc, p, "flash_attn", &g.FlashAttn, `"auto", "on" or "off"`); err != nil {
		return err
	}
	if err := optBool(doc, p, "mmap", &g.Mmap); err != nil {
		return err
	}
	if err := optInt(doc, p, "llama_log_verbosity", &g.LogVerbosity, "0-5 (2 = warnings and errors only)"); err != nil {
		return err
	}
	if err := optInt(doc, p, "memory_limit_mb", &g.MemoryLimitMB, "the Go soft memory limit in MiB (0 = unlimited)"); err != nil {
		return err
	}
	if err := optInt(doc, p, "max_procs", &g.MaxProcs, "GOMAXPROCS (0 = all cores)"); err != nil {
		return err
	}
	if err := optInt(doc, p, "max_open_files", &g.MaxOpenFiles, "the RLIMIT_NOFILE target"); err != nil {
		return err
	}
	if err := optBool(doc, p, "restore", &g.Restore); err != nil {
		return err
	}
	if err := optString(doc, p, "run_as", &g.RunAs, `a service user name, or "" for root`); err != nil {
		return err
	}
	if err := optString(doc, p, "tls_cert", &g.TLSCert, `an absolute certificate path, or ""`); err != nil {
		return err
	}
	if err := optString(doc, p, "tls_key", &g.TLSKey, `an absolute private key path, or ""`); err != nil {
		return err
	}
	if err := optString(doc, p, "tls_ca", &g.TLSCA, `an absolute CA bundle path, or ""`); err != nil {
		return err
	}
	return nil
}

func optString(doc *toml.Document, p, key string, dst *string, want string) error {
	if !doc.Has(key) {
		return nil
	}
	v, ok := doc.String(key)
	if !ok {
		return typeErr(p, doc, key, "a string ("+want+")")
	}
	*dst = v
	return nil
}

func optInt(doc *toml.Document, p, key string, dst *int, want string) error {
	if !doc.Has(key) {
		return nil
	}
	v, ok := doc.Int(key)
	if !ok {
		return typeErr(p, doc, key, "an integer ("+want+")")
	}
	*dst = int(v)
	return nil
}

func optBool(doc *toml.Document, p, key string, dst *int) error {
	if !doc.Has(key) {
		return nil
	}
	v, ok := doc.Int(key)
	if !ok {
		return typeErr(p, doc, key, "an integer 0 or 1")
	}
	if v != 0 && v != 1 {
		return errs.Newf(errs.Config, "%s: line %d: %s must be 0 or 1", p, doc.Line(key), key).
			WithHint("use 0 for off or 1 for on")
	}
	*dst = int(v)
	return nil
}

// Validate checks value ranges and path shapes.
func (g *Global) Validate() error {
	if g.Boot != 0 && g.Boot != 1 {
		return errs.Newf(errs.Config, "boot must be 0 or 1")
	}
	if g.ModelsRoot == "" || !filepath.IsAbs(g.ModelsRoot) {
		return errs.Newf(errs.Config, "models_root must be an absolute path (got %q)", g.ModelsRoot).
			WithHint(`use an absolute path such as "/var/lib/maio/models"`)
	}
	if g.StateDir == "" || !filepath.IsAbs(g.StateDir) {
		return errs.Newf(errs.Config, "state_dir must be an absolute path (got %q)", g.StateDir).
			WithHint(`use an absolute path such as "/var/lib/maio"`)
	}
	if g.Port < 1 || g.Port > 65535 {
		return errs.Newf(errs.Config, "port must be between 1 and 65535 (got %d)", g.Port)
	}
	if strings.TrimSpace(g.Host) == "" || strings.ContainsAny(g.Host, " \t/") {
		return errs.Newf(errs.Config, "host must be a plain address such as 127.0.0.1 or 0.0.0.0 (got %q)", g.Host)
	}
	if g.CtxSize < 0 {
		return errs.Newf(errs.Config, "ctx_size must not be negative (got %d)", g.CtxSize)
	}
	if g.CtxSize > 1048576 {
		return errs.Newf(errs.Config, "ctx_size is unreasonably large (got %d; maximum 1048576)", g.CtxSize)
	}
	if g.Mirror != "" {
		u, err := url.Parse(g.Mirror)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errs.Newf(errs.Config, "mirror must be an http(s) URL (got %q)", g.Mirror).
				WithHint(`use a mirror such as "https://www.modelscope.cn", or ""`)
		}
	}
	if g.LlamaBin != "" && !filepath.IsAbs(g.LlamaBin) {
		return errs.Newf(errs.Config, "llama_bin must be an absolute path (got %q)", g.LlamaBin)
	}
	if g.LlamaLib != "" {
		for _, p := range strings.Split(g.LlamaLib, ":") {
			if p == "" {
				continue
			}
			if !filepath.IsAbs(p) {
				return errs.Newf(errs.Config, "llama_lib entries must be absolute paths (got %q)", p)
			}
		}
	}
	if err := g.validateV3(); err != nil {
		return err
	}
	g.ModelsRoot = filepath.Clean(g.ModelsRoot)
	g.StateDir = filepath.Clean(g.StateDir)
	return nil
}

var runAsPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

func (g *Global) validateV3() error {
	switch strings.ToLower(strings.TrimSpace(g.Auth)) {
	case "", "auto":
		g.Auth = "auto"
	case "on":
		g.Auth = "on"
	case "off":
		g.Auth = "off"
	default:
		return errs.Newf(errs.Config, "auth must be \"auto\", \"on\" or \"off\" (got %q)", g.Auth).
			WithHint("use auth = \"auto\" unless you have a specific reason")
	}
	if g.Auth == "off" && g.APIKey != "" {
		return errs.New(errs.Config, "auth = \"off\" conflicts with an api_key").
			WithHint("remove api_key, or set auth = \"auto\"")
	}
	if g.APIKey != "" {
		if err := auth.ValidateKey(g.APIKey); err != nil {
			return err
		}
	}
	if g.AllowInsecure != 0 && g.AllowInsecure != 1 {
		return errs.New(errs.Config, "allow_insecure must be 0 or 1")
	}
	if g.RateLimit < 0 || g.RateLimit > 10_000_000 {
		return errs.Newf(errs.Config, "rate_limit must be between 0 and 10000000 (got %d)", g.RateLimit)
	}
	if g.RateBurst < 1 || g.RateBurst > 1_000_000 {
		return errs.Newf(errs.Config, "rate_burst must be between 1 and 1000000 (got %d)", g.RateBurst)
	}
	if g.MaxConcurrency < 1 || g.MaxConcurrency > 4096 {
		return errs.Newf(errs.Config, "max_concurrency must be between 1 and 4096 (got %d)", g.MaxConcurrency)
	}
	if g.MaxQueue < 0 || g.MaxQueue > 1_000_000 {
		return errs.Newf(errs.Config, "max_queue must be between 0 and 1000000 (got %d)", g.MaxQueue)
	}
	if g.QueueTimeout < 0 || g.QueueTimeout > 86400 {
		return errs.Newf(errs.Config, "queue_timeout must be between 0 and 86400 seconds (got %d)", g.QueueTimeout)
	}
	if g.RequestTimeout < 0 || g.RequestTimeout > 86400 {
		return errs.Newf(errs.Config, "request_timeout must be between 0 and 86400 seconds (got %d)", g.RequestTimeout)
	}
	if g.MaxBodyMB < 1 || g.MaxBodyMB > 1024 {
		return errs.Newf(errs.Config, "max_body_mb must be between 1 and 1024 (got %d)", g.MaxBodyMB)
	}
	if g.IdleUnload < 0 || g.IdleUnload > 604800 {
		return errs.Newf(errs.Config, "idle_unload must be between 0 and 604800 seconds (got %d)", g.IdleUnload)
	}
	if g.AccessLog != 0 && g.AccessLog != 1 {
		return errs.New(errs.Config, "access_log must be 0 or 1")
	}
	if g.TrustProxy != 0 && g.TrustProxy != 1 {
		return errs.New(errs.Config, "trust_proxy must be 0 or 1")
	}
	if g.CORS != 0 && g.CORS != 1 {
		return errs.New(errs.Config, "cors must be 0 or 1")
	}
	if g.Parallel < 1 || g.Parallel > 128 {
		return errs.Newf(errs.Config, "parallel must be between 1 and 128 (got %d)", g.Parallel)
	}
	if g.Threads < 0 || g.Threads > 4096 {
		return errs.Newf(errs.Config, "threads must be between 0 and 4096 (got %d)", g.Threads)
	}
	switch strings.ToLower(strings.TrimSpace(g.KVType)) {
	case "", "f16":
		g.KVType = "f16"
	case "q8_0", "q4_0":
		g.KVType = strings.ToLower(strings.TrimSpace(g.KVType))
	default:
		return errs.Newf(errs.Config, "kv_type must be f16, q8_0 or q4_0 (got %q)", g.KVType).
			WithHint("q8_0 roughly halves KV cache memory; f16 is the default")
	}
	switch strings.ToLower(strings.TrimSpace(g.FlashAttn)) {
	case "", "auto":
		g.FlashAttn = "auto"
	case "on", "off":
		g.FlashAttn = strings.ToLower(strings.TrimSpace(g.FlashAttn))
	default:
		return errs.Newf(errs.Config, "flash_attn must be auto, on or off (got %q)", g.FlashAttn)
	}
	if g.Mmap != 0 && g.Mmap != 1 {
		return errs.New(errs.Config, "mmap must be 0 or 1")
	}
	if g.LogVerbosity < 0 || g.LogVerbosity > 5 {
		return errs.Newf(errs.Config, "llama_log_verbosity must be between 0 and 5 (got %d)", g.LogVerbosity).
			WithHint("use 3 for normal logs, or 2 to keep only warnings and errors on a busy host")
	}
	if g.MemoryLimitMB != 0 && (g.MemoryLimitMB < 64 || g.MemoryLimitMB > 65536) {
		return errs.Newf(errs.Config, "memory_limit_mb must be 0 or between 64 and 65536 (got %d)", g.MemoryLimitMB)
	}
	if g.MaxProcs < 0 || g.MaxProcs > 4096 {
		return errs.Newf(errs.Config, "max_procs must be between 0 and 4096 (got %d)", g.MaxProcs)
	}
	if g.MaxOpenFiles < 256 || g.MaxOpenFiles > 1048576 {
		return errs.Newf(errs.Config, "max_open_files must be between 256 and 1048576 (got %d)", g.MaxOpenFiles)
	}
	if g.Restore != 0 && g.Restore != 1 {
		return errs.New(errs.Config, "restore must be 0 or 1")
	}
	if g.RunAs != "" && !runAsPattern.MatchString(g.RunAs) {
		return errs.Newf(errs.Config, "run_as %q is not a valid user name", g.RunAs).
			WithHint("use a lower-case system user such as \"maio\", or \"\" for root")
	}
	if (g.TLSCert == "") != (g.TLSKey == "") {
		return errs.New(errs.Config, "tls_cert and tls_key must be set together").
			WithHint("set both paths, or leave both empty for plain HTTP")
	}
	for _, p := range []string{g.TLSCert, g.TLSKey, g.TLSCA} {
		if p != "" && !filepath.IsAbs(p) {
			return errs.Newf(errs.Config, "TLS paths must be absolute (got %q)", p)
		}
	}
	return nil
}

// Render produces the maio.conf text.
func (g *Global) Render() string {
	var b strings.Builder
	b.WriteString("# maio global config (one per machine or user; not maio.toml)\n")
	if g.Scope == "system" {
		b.WriteString("# Start-on-boot is on by default. Use \"sudo maio --system boot 0\" to disable it.\n\n")
	} else {
		b.WriteString("# Start-on-boot is on by default. Use \"maio boot 0\" to disable it.\n\n")
	}
	b.WriteString("# --- model store ---\n")
	fmt.Fprintf(&b, "boot        = %d\n", g.Boot)
	fmt.Fprintf(&b, "models_root = %q\n", g.ModelsRoot)
	fmt.Fprintf(&b, "state_dir   = %q\n", g.StateDir)
	fmt.Fprintf(&b, "mirror      = %q\n", g.Mirror)
	b.WriteString("\n# --- HTTP service ---\n")
	fmt.Fprintf(&b, "host           = %q\n", g.Host)
	fmt.Fprintf(&b, "port           = %d\n", g.Port)
	fmt.Fprintf(&b, "auth           = %q\n", g.Auth)
	fmt.Fprintf(&b, "api_key        = %q\n", g.APIKey)
	fmt.Fprintf(&b, "allow_insecure = %d\n", g.AllowInsecure)
	fmt.Fprintf(&b, "trust_proxy    = %d\n", g.TrustProxy)
	fmt.Fprintf(&b, "cors           = %d\n", g.CORS)
	fmt.Fprintf(&b, "tls_cert       = %q\n", g.TLSCert)
	fmt.Fprintf(&b, "tls_key        = %q\n", g.TLSKey)
	fmt.Fprintf(&b, "tls_ca         = %q\n", g.TLSCA)
	b.WriteString("\n# --- admission control and timeouts ---\n")
	fmt.Fprintf(&b, "rate_limit      = %d\n", g.RateLimit)
	fmt.Fprintf(&b, "rate_burst      = %d\n", g.RateBurst)
	fmt.Fprintf(&b, "max_concurrency = %d\n", g.MaxConcurrency)
	fmt.Fprintf(&b, "max_queue       = %d\n", g.MaxQueue)
	fmt.Fprintf(&b, "queue_timeout   = %d\n", g.QueueTimeout)
	fmt.Fprintf(&b, "request_timeout = %d\n", g.RequestTimeout)
	fmt.Fprintf(&b, "max_body_mb     = %d\n", g.MaxBodyMB)
	fmt.Fprintf(&b, "idle_unload     = %d\n", g.IdleUnload)
	fmt.Fprintf(&b, "access_log      = %d\n", g.AccessLog)
	b.WriteString("\n# --- llama.cpp engine ---\n")
	fmt.Fprintf(&b, "llama_bin  = %q\n", g.LlamaBin)
	fmt.Fprintf(&b, "llama_lib  = %q\n", g.LlamaLib)
	fmt.Fprintf(&b, "ctx_size   = %d\n", g.CtxSize)
	fmt.Fprintf(&b, "parallel   = %d\n", g.Parallel)
	fmt.Fprintf(&b, "threads    = %d\n", g.Threads)
	fmt.Fprintf(&b, "kv_type    = %q\n", g.KVType)
	fmt.Fprintf(&b, "flash_attn = %q\n", g.FlashAttn)
	fmt.Fprintf(&b, "mmap       = %d\n", g.Mmap)
	fmt.Fprintf(&b, "llama_log_verbosity = %d\n", g.LogVerbosity)
	b.WriteString("\n# --- daemon and process limits ---\n")
	fmt.Fprintf(&b, "memory_limit_mb = %d\n", g.MemoryLimitMB)
	fmt.Fprintf(&b, "max_procs       = %d\n", g.MaxProcs)
	fmt.Fprintf(&b, "max_open_files  = %d\n", g.MaxOpenFiles)
	fmt.Fprintf(&b, "restore         = %d\n", g.Restore)
	fmt.Fprintf(&b, "run_as          = %q\n", g.RunAs)
	return b.String()
}

// AuthKeyFile returns the path of the generated API key file.
func (g *Global) AuthKeyFile() string {
	return filepath.Join(g.runtimeDir(), "auth", "api.key")
}

// LockFile returns the service singleton lock path.
func (g *Global) LockFile() string {
	return filepath.Join(g.RunDir(), "maio.lock")
}

// TLSEnabled reports whether the service should terminate TLS itself.
func (g *Global) TLSEnabled() bool { return g.TLSCert != "" && g.TLSKey != "" }

// Save atomically writes the global config to the path it was loaded from.
func (g *Global) Save() error {
	p := g.Source
	if p == "" {
		p = ConfPath()
	}
	return g.SaveTo(p)
}

// SaveTo atomically writes the global config to an explicit path.
func (g *Global) SaveTo(path string) error {
	if path == "" {
		return errs.New(errs.Config, "config path is empty")
	}
	if err := g.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create config directory %s", dir).
			WithHint("use the default user scope (maio ...), or system scope: sudo maio --system ...")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(g.Render()), 0o640); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write config %s", path).
			WithHint("check directory permissions, or run with sudo")
	}
	if g.Scope == "system" {
		owner.ChownMode(tmp, g.RunAs, 0o640)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errs.Wrap(errs.IO, err, "cannot replace config %s", path)
	}
	return nil
}

// RunDir returns the runtime state directory (state_dir/run).
func (g *Global) RunDir() string { return filepath.Join(g.runtimeDir(), "run") }

// LogDir returns the log directory (state_dir/log).
func (g *Global) LogDir() string { return filepath.Join(g.runtimeDir(), "log") }

// AuditLogPath returns the CLI audit log path.
func (g *Global) AuditLogPath() string { return filepath.Join(g.LogDir(), "maio.log") }

// EngineLogPath returns the engine log path for one model.
func (g *Global) EngineLogPath(name string) string {
	return filepath.Join(g.LogDir(), name+".log")
}

func (g *Global) runtimeDir() string {
	if g.RuntimeDir != "" {
		return g.RuntimeDir
	}
	return resolveRuntimeDir(g.StateDir)
}

// resolveRuntimeDir returns StateDir when writable, otherwise the per-user
// state directory for non-root processes. Root never silently changes scope.
func resolveRuntimeDir(stateDir string) string {
	if isWritableDir(stateDir) {
		return stateDir
	}
	if os.Geteuid() != 0 {
		user := userStateDir()
		if isWritableDir(user) {
			return user
		}
	}
	return stateDir
}

func userStateDir() string {
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "maio")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "maio-state")
	}
	return filepath.Join(home, ".local", "state", "maio")
}

func isWritableDir(dir string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".maio-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func typeErr(path string, doc *toml.Document, key, want string) error {
	return errs.Newf(errs.Config, "%s: line %d: %s must be %s (got %s)",
		path, doc.Line(key), key, want, doc.TypeName(key)).
		WithHint("edit the reported line and try again")
}

// Model holds the three fields of maio.toml.
type Model struct {
	Name   string
	Format string
	Path   string
	Dir    string
}

// ModelDir returns the model directory (<root>/<name>).
func ModelDir(root, name string) string { return filepath.Join(root, name) }

// ModelConfigPath returns the model config path.
func ModelConfigPath(dir string) string { return filepath.Join(dir, "maio.toml") }

// LoadModel reads <dir>/maio.toml and validates the three-field contract.
func LoadModel(dir string) (*Model, error) {
	p := ModelConfigPath(dir)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.Wrap(errs.NotFound, err, "model config %s does not exist", p)
		}
		if os.IsPermission(err) {
			return nil, errs.Wrap(errs.Permission, err, "cannot read model config %s", p).
				WithHint("run with sudo, or fix the file permissions")
		}
		return nil, errs.Wrap(errs.IO, err, "cannot read model config %s", p)
	}
	doc, err := toml.Parse(data)
	if err != nil {
		return nil, errs.Wrap(errs.Config, err, "invalid model config %s", p).
			WithHint("maio.toml must contain exactly name, format and path")
	}
	known := []string{"name", "format", "path"}
	knownSet := make(map[string]bool, len(known))
	for _, k := range known {
		knownSet[k] = true
	}
	for _, k := range doc.Keys() {
		if !knownSet[k] {
			return nil, errs.Newf(errs.Config, "%s: line %d: unknown key %q", p, doc.Line(k), k).
				WithHint("maio.toml has exactly three keys: name, format, path")
		}
	}
	name, ok := doc.String("name")
	if !ok {
		return nil, errs.Newf(errs.Config, "%s: line %d: name must be a string", p, doc.Line("name")).
			WithHint("example: name = \"deepseek-v4.1\"")
	}
	format, ok := doc.String("format")
	if !ok {
		return nil, errs.Newf(errs.Config, "%s: line %d: format must be a string", p, doc.Line("format")).
			WithHint(`example: format = "gguf"`)
	}
	path, ok := doc.String("path")
	if !ok {
		return nil, errs.Newf(errs.Config, "%s: line %d: path must be a string", p, doc.Line("path")).
			WithHint(`example: path = "model-Q4_K_M.gguf"`)
	}
	if name == "" {
		return nil, errs.Newf(errs.Config, "%s: line %d: name must not be empty", p, doc.Line("name"))
	}
	if err := ValidateName(name); err != nil {
		return nil, errs.Wrap(errs.Config, err, "%s: invalid model name %q", p, name)
	}
	if base := filepath.Base(filepath.Clean(dir)); base != name {
		return nil, errs.Newf(errs.Config, "%s: name %q does not match its directory %q", p, name, base).
			WithHint("rename the directory or fix name in maio.toml so both match")
	}
	if strings.ToLower(format) != "gguf" {
		return nil, errs.Newf(errs.Config, "%s: unsupported format %q", p, format).
			WithHint(`V2 supports format = "gguf" only`)
	}
	if path == "" {
		return nil, errs.Newf(errs.Config, "%s: line %d: path must not be empty", p, doc.Line("path"))
	}
	if !filepath.IsAbs(path) {
		clean := filepath.Clean(path)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, errs.Newf(errs.Config, "%s: path %q escapes the model directory", p, path).
				WithHint("use a relative path inside the model directory, or an absolute path")
		}
		path = clean
	}
	return &Model{Name: name, Format: "gguf", Path: path, Dir: dir}, nil
}

// ValidateName checks an already normalized model name.
func ValidateName(name string) error { return naming.Validate(name) }

// FilePath returns the absolute model file path.
func (m *Model) FilePath() string {
	if filepath.IsAbs(m.Path) {
		return m.Path
	}
	return filepath.Join(m.Dir, m.Path)
}

// SaveModel writes <dir>/maio.toml (three fields, fixed layout).
func SaveModel(dir, name, format, relPath string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create model directory %s", dir)
	}
	body := fmt.Sprintf("# maio.toml: one model per directory, exactly three fields\nname   = %q\nformat = %q\npath   = %q\n",
		name, format, relPath)
	if err := os.WriteFile(ModelConfigPath(dir), []byte(body), 0o644); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write %s", ModelConfigPath(dir))
	}
	return nil
}
