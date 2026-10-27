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
