package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func payload() []byte {
	return append([]byte("GGUF"), bytes.Repeat([]byte("x"), 4096)...)
}

func newTestServer() *httptest.Server {
	data := payload()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "m.gguf", time.Time{}, bytes.NewReader(data))
	}))
}

func TestFetchFromScratch(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "m.gguf")
	if err := Fetch(context.Background(), Options{URL: srv.URL, DestPath: dest}); err != nil {
		t.Fatalf("download failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload()) {
		t.Fatalf("content mismatch: %d vs %d bytes", len(got), len(payload()))
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal("temporary file was not cleaned up")
	}
	// existing file: skip without error
	if err := Fetch(context.Background(), Options{URL: srv.URL, DestPath: dest}); err != nil {
		t.Fatalf("re-download failed: %v", err)
	}
}

func TestFetchResume(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(dest+".part", payload()[:1000], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(context.Background(), Options{URL: srv.URL, DestPath: dest}); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload()) {
		t.Fatal("content mismatch after resume")
	}
}

func TestFetchSHA(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	dir := t.TempDir()
	sum := sha256.Sum256(payload())
	want := hex.EncodeToString(sum[:])

	ok := filepath.Join(dir, "ok.gguf")
	if err := Fetch(context.Background(), Options{URL: srv.URL, DestPath: ok, ExpectSHA256: "sha256:" + want}); err != nil {
		t.Fatalf("correct SHA should pass: %v", err)
	}

	bad := filepath.Join(dir, "bad.gguf")
	if err := Fetch(context.Background(), Options{URL: srv.URL, DestPath: bad, ExpectSHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("wrong SHA should fail")
	}
}

func TestVerifyRejectsNonGGUF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.gguf")
	if err := os.WriteFile(path, []byte("NOPE1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path, ""); err == nil {
		t.Fatal("non-GGUF file should fail")
	}
}
