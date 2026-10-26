package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
)

func TestScanAndFind(t *testing.T) {
	root := t.TempDir()

	dir := filepath.Join(root, "deepseek-v4.1")
	if err := config.SaveModel(dir, "deepseek-v4.1", "gguf", "w.gguf"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "w.gguf"), []byte("GGUF...."), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, warns := Scan(root)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(entries) != 1 || entries[0].Model.Name != "deepseek-v4.1" {
		t.Fatalf("unexpected scan result: %+v", entries)
	}
	if entries[0].Size != 8 {
		t.Fatalf("size = %d, want 8", entries[0].Size)
	}

	m, err := Find(root, "DeepSeek v4.1")
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}
	if m.Name != "deepseek-v4.1" {
		t.Fatalf("Find did not normalize: %s", m.Name)
	}

	if _, err := Find(root, "not-exist"); err == nil {
		t.Fatal("missing model should fail")
	}
}
