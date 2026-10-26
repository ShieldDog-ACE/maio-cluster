// Package config handles the global maio.conf and the per-model maio.toml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/toml"
)

const (
	defaultModelsRoot = "/var/lib/maio/models"
	defaultStateDir   = "/var/lib/maio"
	defaultHost       = "127.0.0.1"
	defaultPort       = 11434
	defaultMirror     = "https://www.modelscope.cn"
)

// Global is the global config (one per machine, /etc/maio/maio.conf).
type Global struct {
	Boot       int    // start on boot: 1=yes, 0=no
	ModelsRoot string // model root (default download root)
	StateDir   string // runtime state and log root
	Host       string
	Port       int
	Mirror     string // download mirror
	LlamaBin   string // path to llama-server; empty = auto-detect
	LlamaLib   string // extra library dirs, colon separated; empty = auto-detect
	CtxSize    int    // context length; 0 = model default
}

// DefaultGlobal returns the built-in defaults.
func DefaultGlobal() *Global {
	return &Global{
		Boot:       0,
		ModelsRoot: defaultModelsRoot,
		StateDir:   defaultStateDir,
		Host:       defaultHost,
		Port:       defaultPort,
		Mirror:     defaultMirror,
	}
}

// ConfPath returns the global config path (MAIO_CONF overrides it, for tests).
func ConfPath() string {
	if p := os.Getenv("MAIO_CONF"); p != "" {
		return p
	}
	return "/etc/maio/maio.conf"
}

// IndexPath returns the model index path (same directory as the global config).
func IndexPath() string {
	return filepath.Join(filepath.Dir(ConfPath()), "index.toml")
}

// LoadGlobal reads the global config; missing file means defaults.
func LoadGlobal() (*Global, error) {
	g := DefaultGlobal()
	data, err := os.ReadFile(ConfPath())
	if err != nil {
		if os.IsNotExist(err) {
			return g, nil
		}
		return nil, fmt.Errorf("failed to read global config: %w", err)
	}
	doc, err := toml.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse global config %s: %w", ConfPath(), err)
	}
	if v, ok := doc.Int("boot"); ok {
		if v != 0 && v != 1 {
			return nil, fmt.Errorf("maio.conf: boot must be 0 or 1")
		}
		g.Boot = int(v)
	}
	if v, ok := doc.String("models_root"); ok {
		g.ModelsRoot = v
	}
	if v, ok := doc.String("state_dir"); ok {
		g.StateDir = v
	}
	if v, ok := doc.String("host"); ok {
		g.Host = v
	}
	if v, ok := doc.Int("port"); ok {
		g.Port = int(v)
	}
	if v, ok := doc.String("mirror"); ok {
		g.Mirror = v
	}
	if v, ok := doc.String("llama_bin"); ok {
		g.LlamaBin = v
	}
	if v, ok := doc.String("llama_lib"); ok {
		g.LlamaLib = v
	}
	if v, ok := doc.Int("ctx_size"); ok {
		g.CtxSize = int(v)
	}
	if err := g.validate(); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Global) validate() error {
	if g.ModelsRoot == "" || !filepath.IsAbs(g.ModelsRoot) {
		return fmt.Errorf("maio.conf: models_root must be an absolute path")
	}
	if g.StateDir == "" || !filepath.IsAbs(g.StateDir) {
		return fmt.Errorf("maio.conf: state_dir must be an absolute path")
	}
	if g.Port < 1 || g.Port > 65535 {
		return fmt.Errorf("maio.conf: port out of range")
	}
	if g.Host == "" {
		return fmt.Errorf("maio.conf: host must not be empty")
	}
	if g.CtxSize < 0 {
		return fmt.Errorf("maio.conf: ctx_size must not be negative")
	}
	return nil
}

// Render produces the maio.conf text.
func (g *Global) Render() string {
	var b strings.Builder
	b.WriteString("# maio global config (not maio.toml; one per machine)\n")
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

// Save atomically writes the global config.
func (g *Global) Save() error {
	p := ConfPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(g.Render()), 0o644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("failed to replace config: %w", err)
	}
	return nil
}

// RunDir returns the runtime state directory.
func (g *Global) RunDir() string { return filepath.Join(g.StateDir, "run") }

// LogDir returns the log directory.
func (g *Global) LogDir() string { return filepath.Join(g.StateDir, "log") }

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

// LoadModel reads <dir>/maio.toml.
func LoadModel(dir string) (*Model, error) {
	p := ModelConfigPath(dir)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	doc, err := toml.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", p, err)
	}
	name, _ := doc.String("name")
	format, _ := doc.String("format")
	path, _ := doc.String("path")
	if name == "" || format == "" || path == "" {
		return nil, fmt.Errorf("%s is missing required fields (name/format/path)", p)
	}
	return &Model{Name: name, Format: format, Path: path, Dir: dir}, nil
}

// FilePath returns the absolute model file path.
func (m *Model) FilePath() string {
	if filepath.IsAbs(m.Path) {
		return m.Path
	}
	return filepath.Join(m.Dir, m.Path)
}

// SaveModel writes <dir>/maio.toml (three fields, fixed layout).
func SaveModel(dir, name, format, relPath string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("# maio.toml: one per model, exactly three fields\nname   = %q\nformat = %q\npath   = %q\n",
		name, format, relPath)
	if err := os.WriteFile(ModelConfigPath(dir), []byte(body), 0o644); err != nil {
		return err
	}
	return nil
}
