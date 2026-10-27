package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/engine"
)

func testConfig(t *testing.T) *config.Global {
	t.Helper()
	dir := t.TempDir()
	conf := config.DefaultGlobal()
	conf.RuntimeDir = dir
	conf.StateDir = dir
	conf.ModelsRoot = filepath.Join(dir, "models")
	conf.Host = "127.0.0.1"
	conf.AccessLog = 0
	conf.RateLimit = 0
	conf.Auth = "off"
	conf.AllowInsecure = 1
	if err := os.MkdirAll(conf.ModelsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mdDir := filepath.Join(conf.ModelsRoot, "tiny")
	if err := os.MkdirAll(mdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveModel(mdDir, "tiny", "gguf", "model.gguf"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdDir, "model.gguf"), []byte("GGUF"), 0o644); err != nil {
		t.Fatal(err)
	}
	return conf
}

func newTestServer(t *testing.T, conf *config.Global) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New(conf, engine.New(conf), "3.0.0")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestHealthAndAuth(t *testing.T) {
	conf := testConfig(t)
	conf.Auth = "on"
	conf.APIKey = "0123456789abcdef0123456789abcdef"
	_, ts := newTestServer(t, conf)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/api/tags")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated tags = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/tags", nil)
	req.Header.Set("Authorization", "Bearer "+conf.APIKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("authenticated tags = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	models, _ := body["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %#v", body["models"])
	}

	// Metrics require the same key.
	resp, _ = http.Get(ts.URL + "/metrics")
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated metrics = %d", resp.StatusCode)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("GET", ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+conf.APIKey)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("authenticated metrics = %d", resp.StatusCode)
	}
	data := make([]byte, 256)
	n, _ := resp.Body.Read(data)
	resp.Body.Close()
	if !strings.Contains(string(data[:n]), "maio_build_info") {
		t.Fatalf("metrics body = %q", data[:n])
	}
}

func TestRateLimitAndOptions(t *testing.T) {
	conf := testConfig(t)
	conf.RateLimit = 1
	conf.RateBurst = 1
	_, ts := newTestServer(t, conf)

	for i, want := range []int{200, 429} {
		resp, err := http.Get(ts.URL + "/api/tags")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("request %d = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	req, _ := http.NewRequest("OPTIONS", ts.URL+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS = %d", resp.StatusCode)
	}
}

func TestShowStatsAndBodyLimit(t *testing.T) {
	conf := testConfig(t)
	conf.MaxBodyMB = 1
	_, ts := newTestServer(t, conf)

	resp, err := http.Post(ts.URL+"/api/show", "application/json", strings.NewReader(`{"name":"tiny"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("show = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("stats = %d", resp.StatusCode)
	}
	var stats map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	resp.Body.Close()
	if _, ok := stats["metrics"]; !ok {
		t.Fatalf("stats body = %#v", stats)
	}

	big := `{"name":"` + strings.Repeat("x", 2<<20) + `"}`
	resp, err = http.Post(ts.URL+"/api/show", "application/json", bytes.NewBufferString(big))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", resp.StatusCode)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	conf := testConfig(t)
	_, ts := newTestServer(t, conf)
	req, _ := http.NewRequest("POST", ts.URL+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST tags = %d, want 405", resp.StatusCode)
	}
}

func TestUnloadRequests(t *testing.T) {
	conf := testConfig(t)
	_, ts := newTestServer(t, conf)
	for _, body := range []string{`{"all":true}`, `{"model":"tiny"}`} {
		resp, err := http.Post(ts.URL+"/api/unload", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("unload %s = %d", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp, err := http.Post(ts.URL+"/api/unload", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty unload = %d, want 400", resp.StatusCode)
	}
}
