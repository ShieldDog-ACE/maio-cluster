package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAIO_CONF", filepath.Join(dir, "etc", "maio.conf"))

	g := DefaultGlobal()
	g.Boot = 1
	g.ModelsRoot = filepath.Join(dir, "models")
	g.StateDir = filepath.Join(dir, "state")
	if err := g.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	got, err := LoadGlobal()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got.Boot != 1 {
		t.Fatalf("boot = %d, want 1", got.Boot)
	}
	if got.ModelsRoot != g.ModelsRoot {
		t.Fatalf("models_root = %q, want %q", got.ModelsRoot, g.ModelsRoot)
	}
	if got.Port != 11434 {
		t.Fatalf("default port = %d, want 11434", got.Port)
	}
}

func TestBootOnlyZeroOrOne(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "maio.conf")
	t.Setenv("MAIO_CONF", p)
	if err := os.WriteFile(p, []byte("boot = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGlobal(); err == nil {
		t.Fatal("boot = 2 should fail")
	}
}

func TestModelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, "deepseek-v4.1")
	if err := SaveModel(modelDir, "deepseek-v4.1", "gguf", "w.gguf"); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	m, err := LoadModel(modelDir)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if m.Name != "deepseek-v4.1" || m.Format != "gguf" || m.Path != "w.gguf" {
		t.Fatalf("unexpected fields: %+v", m)
	}
	if want := filepath.Join(modelDir, "w.gguf"); m.FilePath() != want {
		t.Fatalf("FilePath = %q, want %q", m.FilePath(), want)
	}
}

func TestDefaultBootIsOn(t *testing.T) {
	if got := DefaultGlobal().Boot; got != 1 {
		t.Fatalf("default boot = %d, want 1", got)
	}
}

func TestUnknownKeyIsReported(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "maio.conf")
	t.Setenv("MAIO_CONF", p)
	if err := os.WriteFile(p, []byte("boot = 1\nboott = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadGlobal()
	if err == nil {
		t.Fatal("unknown key should fail")
	}
	if !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWrongTypeIsReported(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "maio.conf")
	t.Setenv("MAIO_CONF", p)
	if err := os.WriteFile(p, []byte("boot = \"yes\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadGlobal()
	if err == nil || !strings.Contains(err.Error(), "boot must be an integer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestModelNameMustMatchDirectory(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, "wrong-dir")
	if err := SaveModel(modelDir, "other-name", "gguf", "w.gguf"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModel(modelDir); err == nil {
		t.Fatal("mismatched name should fail")
	}
}

func TestModelRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, "m")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "name = \"m\"\nformat = \"gguf\"\npath = \"x.gguf\"\nextra = 1\n"
	if err := os.WriteFile(ModelConfigPath(modelDir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModel(modelDir); err == nil {
		t.Fatal("unknown model key should fail")
	}
}

func TestV3KeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "maio.conf")
	t.Setenv("MAIO_CONF", p)
	body := `boot = 1
models_root = "` + dir + `/models"
state_dir = "` + dir + `/state"
host = "0.0.0.0"
port = 12000
auth = "on"
api_key = "0123456789abcdef0123456789abcdef"
rate_limit = 120
rate_burst = 20
max_concurrency = 4
max_queue = 8
queue_timeout = 30
request_timeout = 600
max_body_mb = 8
idle_unload = 60
access_log = 1
parallel = 3
threads = 8
kv_type = "q8_0"
flash_attn = "auto"
mmap = 1
memory_limit_mb = 160
max_procs = 6
max_open_files = 65536
restore = 1
run_as = "maio"
tls_cert = "/etc/maio/tls.crt"
tls_key = "/etc/maio/tls.key"
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.Auth != "on" || g.MaxConcurrency != 4 || g.MaxQueue != 8 || g.Parallel != 3 ||
		g.KVType != "q8_0" || g.MemoryLimitMB != 160 || g.RunAs != "maio" || !g.TLSEnabled() {
		t.Fatalf("unexpected V3 config: %+v", g)
	}
	if g.AuthKeyFile() != filepath.Join(dir, "state", "auth", "api.key") {
		t.Fatalf("AuthKeyFile = %q", g.AuthKeyFile())
	}
}

func TestV3EnumValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "maio.conf")
	t.Setenv("MAIO_CONF", p)
	for _, body := range []string{
		`auth = "maybe"`,
		`kv_type = "f32"`,
		`flash_attn = "sometimes"`,
		`parallel = 0`,
		`max_concurrency = 0`,
		`tls_cert = "/tmp/cert"`,
		`run_as = "Not Valid"`,
	} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadGlobal(); err == nil {
			t.Fatalf("expected an error for %q", body)
		}
	}
}

func TestUserScopeMissingConfigIsWritable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAIO_CONF", filepath.Join(dir, "missing.conf"))
	g, err := LoadGlobalMode("user")
	if err != nil {
		t.Fatal(err)
	}
	if g.Scope != "user" {
		t.Fatalf("scope = %q", g.Scope)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if g.ModelsRoot != filepath.Join(home, "AI") {
		t.Fatalf("models root = %q", g.ModelsRoot)
	}
	if g.StateDir != filepath.Join(home, ".local", "state", "maio") {
		t.Fatalf("state dir = %q", g.StateDir)
	}
}

func TestScopeExplicit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maio.conf")
	if err := os.WriteFile(p, []byte("port = 23456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIO_CONF", p)
	user, err := LoadGlobalMode("user")
	if err != nil {
		t.Fatal(err)
	}
	if user.Scope != "user" || user.Port != 23456 {
		t.Fatalf("user scope = %q port %d", user.Scope, user.Port)
	}
	system, err := LoadGlobalMode("system")
	if err != nil {
		t.Fatal(err)
	}
	if system.Scope != "system" || system.Port != 23456 {
		t.Fatalf("system scope = %q port %d", system.Scope, system.Port)
	}
}
