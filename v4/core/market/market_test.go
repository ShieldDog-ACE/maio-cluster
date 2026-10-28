package market

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalMarket(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "plugins.json", `{"schema":1,"plugins":[
		{"name":"hello","author":"a","repo":"https://github.com/a/hello","description":"d"}]}`)
	write(t, dir, "models.json", `{"schema":1,"models":[
		{"name":"qwen","author":"q","url":"https://example.com/q.gguf","description":"d"}]}`)

	c := New(dir)
	p, err := c.FindPlugin(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if p.Branch != "main" {
		t.Fatalf("default branch = %q", p.Branch)
	}
	m, err := c.FindModel(t.Context(), "QWEN")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "qwen" {
		t.Fatalf("model = %q", m.Name)
	}
}

func TestMarketValidation(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "plugins.json", `{"schema":1,"plugins":[
		{"name":"bad name","author":"a","repo":"https://github.com/a/hello","description":"d"}]}`)
	write(t, dir, "models.json", `{"schema":1,"models":[]}`)
	if _, err := New(dir).Plugins(t.Context()); err == nil {
		t.Fatal("expected invalid name error")
	}

	write(t, dir, "plugins.json", `{"schema":1,"plugins":[
		{"name":"x","author":"a","repo":"https://gitlab.com/a/x","description":"d"}]}`)
	if _, err := New(dir).Plugins(t.Context()); err == nil {
		t.Fatal("expected invalid repo error")
	}
}
