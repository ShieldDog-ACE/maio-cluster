package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPaths(dir string) Paths {
	return Paths{
		ConfigPath: filepath.Join(dir, "cluster.toml"),
		DataDir:    filepath.Join(dir, "data"),
		ModelsDir:  filepath.Join(dir, "models"),
		StateDir:   filepath.Join(dir, "data"),
		LogDir:     filepath.Join(dir, "logs"),
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	paths := testPaths(dir)
	c := Default(paths)
	c.Cluster.Token = strings.Repeat("a", 43)
	c.Cluster.Seeds = []string{"192.168.1.2:47800"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(paths.ConfigPath, paths)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cluster.Port != 47800 || got.Agent.ModelsDir != paths.ModelsDir {
		t.Fatalf("unexpected config: %+v", got)
	}
	if len(got.Cluster.Seeds) != 1 || got.Cluster.Seeds[0] != "192.168.1.2:47800" {
		t.Fatalf("seeds: %v", got.Cluster.Seeds)
	}
}

func TestUnknownKeyPointsAtLine(t *testing.T) {
	dir := t.TempDir()
	paths := testPaths(dir)
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[cluster]\nname = \"x\"\nport = 47800\nbogus = 1\n"
	if err := os.WriteFile(paths.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(paths.ConfigPath, paths)
	if err == nil {
		t.Fatal("unknown key accepted")
	}
	if !strings.Contains(err.Error(), ":4:") || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("missing line/detail: %v", err)
	}
}

func TestTypeError(t *testing.T) {
	dir := t.TempDir()
	paths := testPaths(dir)
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[cluster]\nport = \"47800\"\n"
	if err := os.WriteFile(paths.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(paths.ConfigPath, paths); err == nil {
		t.Fatal("type error accepted")
	}
}
