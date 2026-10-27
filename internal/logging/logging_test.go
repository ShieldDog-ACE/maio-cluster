package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

func TestLogRecordsAndRedacts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log", "maio.log")
	if _, err := InitPath(p); err != nil {
		t.Fatal(err)
	}
	defer Close()
	Info("install.done", "model", "tiny", "url", "https://example.com/x.gguf?token=secret")
	Error("install.failed", errs.New(errs.Download, "HTTP 404").WithHint("check the URL"), "model", "tiny")
	Close()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "secret") {
		t.Fatalf("query token leaked: %s", text)
	}
	if !strings.Contains(text, `"action":"install.done"`) || !strings.Contains(text, `"code":"MAIO-E006"`) {
		t.Fatalf("unexpected log content: %s", text)
	}
	if !strings.Contains(text, `"hint":"check the URL"`) {
		t.Fatalf("hint missing: %s", text)
	}
}

func TestInitFallsBack(t *testing.T) {
	// A read-only state dir must not fail Init.
	l := Init("/proc/maio-not-writable")
	if l == nil {
		t.Fatal("Init returned nil")
	}
	Close()
}

func TestLogRotatesInProcess(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "maio.log")
	l, err := InitPath(p)
	if err != nil {
		t.Fatal(err)
	}
	defer Close()
	l.mu.Lock()
	l.size = maxLogBytes + 1
	l.mu.Unlock()
	Info("rotate.test", "value", 1)
	Close()
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() >= maxLogBytes {
		t.Fatalf("new log is not fresh: %d bytes", st.Size())
	}
}
