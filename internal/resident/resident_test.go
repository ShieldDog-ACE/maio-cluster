package resident

import (
	"os"
	"testing"
)

func TestAddRemoveAndLoad(t *testing.T) {
	dir := t.TempDir()
	names, err := Add(dir, "tiny")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "tiny" {
		t.Fatalf("names = %v", names)
	}
	if _, err := Add(dir, "Qwen 3.5"); err != nil {
		t.Fatal(err)
	}
	names, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "qwen-3.5" || names[1] != "tiny" {
		t.Fatalf("loaded names = %v", names)
	}
	if _, err := Remove(dir, "tiny"); err != nil {
		t.Fatal(err)
	}
	names, _ = Load(dir)
	if len(names) != 1 || names[0] != "qwen-3.5" {
		t.Fatalf("after remove = %v", names)
	}
}

func TestMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	names, err := Load(dir)
	if err != nil || len(names) != 0 {
		t.Fatalf("missing file: %v %v", names, err)
	}
	if err := os.WriteFile(Path(dir), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("corrupt resident state should be reported")
	}
}

func TestInvalidNamesIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte(`{"models":["good","../escape",""]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "good" {
		t.Fatalf("names = %v", names)
	}
}
