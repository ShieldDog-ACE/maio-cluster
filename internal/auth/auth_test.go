package auth

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "localhost": true, "::1": true, "[::1]": true, "": true,
		"0.0.0.0": false, "192.168.1.5": false, "example.com": false,
	}
	for host, want := range cases {
		if got := IsLoopbackHost(host); got != want {
			t.Fatalf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestAutoLoopbackDoesNotRequireKey(t *testing.T) {
	t.Setenv("MAIO_API_KEY", "")
	a, err := Load(Options{Mode: ModeAuto, Host: "127.0.0.1", KeyFile: filepath.Join(t.TempDir(), "api.key")})
	if err != nil {
		t.Fatal(err)
	}
	if a.Required() {
		t.Fatal("loopback listener should not require a key in auto mode")
	}
	if !a.Check(httptest.NewRequest("GET", "/", nil)) {
		t.Fatal("no-key mode should allow requests")
	}
}

func TestAutoRemoteGeneratesKey(t *testing.T) {
	t.Setenv("MAIO_API_KEY", "")
	keyFile := filepath.Join(t.TempDir(), "auth", "api.key")
	a, err := Load(Options{Mode: ModeAuto, Host: "0.0.0.0", KeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Required() {
		t.Fatal("non-loopback listener must require a key")
	}
	st, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o, want 600", st.Mode().Perm())
	}
	key := a.Key()
	if !strings.HasPrefix(key, "maio_") || len(key) < 32 {
		t.Fatalf("unexpected generated key %q", key)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	if !a.Check(req) {
		t.Fatal("valid bearer token rejected")
	}
	req.Header.Set("Authorization", "Bearer wrong")
	if a.Check(req) {
		t.Fatal("invalid bearer token accepted")
	}
	req.Header.Del("Authorization")
	req.Header.Set("X-API-Key", key)
	if !a.Check(req) {
		t.Fatal("X-API-Key rejected")
	}
}

func TestOffRequiresAllowInsecureOnRemote(t *testing.T) {
	t.Setenv("MAIO_API_KEY", "")
	_, err := Load(Options{Mode: ModeOff, Host: "0.0.0.0"})
	if err == nil {
		t.Fatal("auth=off on a remote listener should fail")
	}
	a, err := Load(Options{Mode: ModeOff, Host: "0.0.0.0", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.Required() {
		t.Fatal("auth=off with allow_insecure should not require a key")
	}
}

func TestShortKeyRejected(t *testing.T) {
	t.Setenv("MAIO_API_KEY", "")
	_, err := Load(Options{Mode: ModeOn, Key: "short", Host: "127.0.0.1"})
	if err == nil {
		t.Fatal("short key should be rejected")
	}
}

func TestSaveAndStoredKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.key")
	key, err := GenerateAndSave(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := StoredKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != key {
		t.Fatalf("stored key = %q, want %q", got, key)
	}
	if Mask(key) == key || !strings.Contains(Mask(key), "*") {
		t.Fatalf("mask did not hide the key: %q", Mask(key))
	}
}
