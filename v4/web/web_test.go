package webui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallRemove(t *testing.T) {
	data := t.TempDir()
	if Installed(data) {
		t.Fatal("fresh dir should not have the console")
	}
	dir, err := Install(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if !Installed(data) {
		t.Fatal("console not installed")
	}
	for _, name := range []string{"index.html", "app.js", "style.css", ".maio-web.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := Install(data, false); err == nil {
		t.Fatal("second install without force should conflict")
	}
	if _, err := Install(data, true); err != nil {
		t.Fatal(err)
	}
	if err := Remove(data); err != nil {
		t.Fatal(err)
	}
	if Installed(data) {
		t.Fatal("console still installed")
	}
}
