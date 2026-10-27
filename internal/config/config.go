// Package config handles the global maio.conf, the per-user fallback config and
// the per-model maio.toml.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/naming"
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

	// RuntimeDir is the effective directory used for run/ and log/. It is the
	// configured StateDir when writable, otherwise a per-user state directory.
	RuntimeDir string
	// Source is the file this config was loaded from ("" when defaults).
	Source string
}

// DefaultGlobal returns the built-in defaults. Start-on-boot is on by default.
func DefaultGlobal() *Global {
	return &Global{
		Boot:       1,
		ModelsRoot: defaultModelsRoot,
		StateDir:   defaultStateDir,
		Host:       defaultHost,
		Port:       defaultPort,
		Mirror:     defaultMirror,
		RuntimeDir: defaultStateDir,
	}
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

// IndexPath returns the model index path (same directory as the global config).
func IndexPath() string {
	return filepath.Join(filepath.Dir(ConfPath()), "index.toml")
}

// UserIndexPath returns the per-user index path.
func UserIndexPath() string {
	return filepath.Join(filepath.Dir(UserConfPath()), "index.toml")
}

// LoadGlobal reads the global config; a missing file means defaults.
func LoadGlobal() (*Global, error) {
	g := DefaultGlobal()
	p := ConfPath()
	g.Source = p
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
	known := []string{"boot", "models_root", "state_dir", "host", "port", "mirror", "llama_bin", "llama_lib", "ctx_size"}
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
	if err := g.Validate(); err != nil {
		return nil, err
	}
	g.RuntimeDir = resolveRuntimeDir(g.StateDir)
	return g, nil
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
	g.ModelsRoot = filepath.Clean(g.ModelsRoot)
	g.StateDir = filepath.Clean(g.StateDir)
	return nil
}

// Render produces the maio.conf text.
func (g *Global) Render() string {
	var b strings.Builder
	b.WriteString("# maio global config (one per machine or user; not maio.toml)\n")
	fmt.Fprintf(&b, "boot        = %d\n", g.Boot)
	fmt.Fprintf(&b, "models_root = %q\n", g.ModelsRoot)
	fmt.Fprintf(&b, "state_dir   = %q\n", g.StateDir)
	fmt.Fprintf(&b, "host        = %q\n", g.Host)
	fmt.Fprintf(&b, "port        = %d\n", g.Port)
	fmt.Fprintf(&b, "mirror      = %q\n", g.Mirror)
	fmt.Fprintf(&b, "llama_bin   = %q\n", g.LlamaBin)
	fmt.Fprintf(&b, "llama_lib   = %q\n", g.LlamaLib)
	fmt.Fprintf(&b, "ctx_size    = %d\n", g.CtxSize)
	return b.String()
}

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
			WithHint("run as root (sudo maio ...), or use a per-user config with --user")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(g.Render()), 0o644); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write config %s", path).
			WithHint("check directory permissions, or run with sudo")
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
