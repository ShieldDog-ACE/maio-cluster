package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testManager(t *testing.T, archive []byte) (*Manager, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owner/repo/tar.gz/refs/heads/main" {
			w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Manager{
		Dir:          filepath.Join(t.TempDir(), "plugins"),
		HTTP:         srv.Client(),
		CodeloadBase: srv.URL,
	}, srv
}

func TestInstallListRemove(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"repo-main/plugin.json": `{"name":"hello","version":"1.2.3","author":"me","description":"d"}`,
		"repo-main/main.sh":     "#!/bin/sh\necho hi\n",
	})
	mgr, _ := testManager(t, archive)
	inst, err := mgr.InstallRepo(context.Background(), "hello", "https://github.com/owner/repo", "main", "me", "d", false)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Version != "1.2.3" || inst.ArchiveSHA == "" {
		t.Fatalf("bad metadata: %+v", inst)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "main.sh")); err != nil {
		t.Fatal(err)
	}
	list, problems := mgr.List()
	if len(problems) != 0 || len(list) != 1 {
		t.Fatalf("list = %v, problems = %v", list, problems)
	}
	if _, err := mgr.InstallRepo(context.Background(), "hello", "https://github.com/owner/repo", "main", "", "", false); err == nil {
		t.Fatal("expected conflict without force")
	}
	if _, err := mgr.InstallRepo(context.Background(), "hello", "https://github.com/owner/repo", "main", "", "", true); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Remove("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Get("hello"); err == nil {
		t.Fatal("expected not found")
	}
}

func TestRejectTraversal(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"repo-main/plugin.json":  `{"name":"evil","version":"1"}`,
		"repo-main/../../escape": "x",
	})
	mgr, _ := testManager(t, archive)
	if _, err := mgr.InstallRepo(context.Background(), "evil", "https://github.com/o/r", "main", "", "", false); err == nil {
		t.Fatal("expected unsafe path error")
	}
}
