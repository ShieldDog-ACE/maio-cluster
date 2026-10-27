// Package auth implements maio's bearer-token API authentication. The key can
// come from MAIO_API_KEY, the config file, or a 0600 key file under the state
// directory. In "auto" mode a non-loopback listener always gets a key: one is
// generated on first start so the service never exposes an unauthenticated
// inference API to the LAN by accident.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

// Modes accepted by config.
const (
	ModeAuto = "auto"
	ModeOn   = "on"
	ModeOff  = "off"
)

// Options describes how to find or create the key.
type Options struct {
	Mode          string // auto, on, off
	Key           string // explicit key from config
	KeyFile       string // 0600 key file
	Host          string // listen address
	AllowInsecure bool   // permit auth=off on a non-loopback listener
}

// Info is safe to log and print: it never contains the key itself.
type Info struct {
	Required  bool
	Source    string
	Generated bool
	KeyFile   string
	Loopback  bool
}

// Authenticator checks Authorization: Bearer or X-API-Key.
type Authenticator struct {
	required bool
	key      []byte
	info     Info
}

// Key returns the raw key (used by the CLI to print it on explicit request).
func (a *Authenticator) Key() string {
	if a == nil {
		return ""
	}
	return string(a.key)
}

// Info returns non-secret metadata.
func (a *Authenticator) Info() Info {
	if a == nil {
		return Info{}
	}
	return a.info
}

// Required reports whether a key is enforced.
func (a *Authenticator) Required() bool { return a != nil && a.required }

// Load resolves authentication settings and creates the key file when the
// effective mode needs a key and none exists.
func Load(opts Options) (*Authenticator, error) {
	mode := normalizeMode(opts.Mode)
	loopback := IsLoopbackHost(opts.Host)
	key, source, err := Lookup(opts)
	if err != nil {
		return nil, err
	}

	switch mode {
	case ModeOff:
		if !loopback && !opts.AllowInsecure {
			return nil, errs.New(errs.Config, "refusing to disable API authentication on a non-loopback listener").
				WithHint(`set auth = "auto" (recommended), or set allow_insecure = 1 if this machine is isolated`)
		}
		return &Authenticator{info: Info{Required: false, Source: "disabled", Loopback: loopback}}, nil
	case ModeOn:
		if key == "" {
			key, err = GenerateAndSave(opts.KeyFile)
			if err != nil {
				return nil, err
			}
			source = "generated"
		}
	default: // auto
		if key == "" {
			if loopback {
				return &Authenticator{info: Info{Required: false, Source: "loopback", Loopback: loopback}}, nil
			}
			key, err = GenerateAndSave(opts.KeyFile)
			if err != nil {
				return nil, err
			}
			source = "generated"
		}
	}

	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	a := &Authenticator{
		required: true,
		key:      []byte(key),
		info: Info{
			Required:  true,
			Source:    source,
			Generated: source == "generated",
			KeyFile:   opts.KeyFile,
			Loopback:  loopback,
		},
	}
	return a, nil
}

// Lookup reads an existing key without generating one. Its second result names
// the source: env, config, file, or "".
func Lookup(opts Options) (key, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("MAIO_API_KEY")); v != "" {
		return v, "env", nil
	}
	if v := strings.TrimSpace(opts.Key); v != "" {
		return v, "config", nil
	}
	if opts.KeyFile != "" {
		data, rerr := os.ReadFile(opts.KeyFile)
		if rerr == nil {
			v := strings.TrimSpace(string(data))
			if v != "" {
				return v, "file", nil
			}
		} else if !os.IsNotExist(rerr) {
			return "", "", errs.Wrap(errs.Permission, rerr, "cannot read the API key file %s", opts.KeyFile).
				WithHint("check permissions, or run: sudo maio token rotate")
		}
	}
	return "", "", nil
}

// ValidateKey enforces a minimum key length to avoid trivially guessable keys.
func ValidateKey(key string) error {
	if len(strings.TrimSpace(key)) < 16 {
		return errs.New(errs.Config, "the API key must be at least 16 characters long").
			WithHint("generate one with: maio token rotate")
	}
	return nil
}

// GenerateKey returns a new 256-bit URL-safe key.
func GenerateKey() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot generate a random API key")
	}
	return "maio_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// GenerateAndSave creates a key and atomically writes it to path with 0600.
func GenerateAndSave(path string) (string, error) {
	key, err := GenerateKey()
	if err != nil {
		return "", err
	}
	if err := SaveKey(path, key); err != nil {
		return "", err
	}
	return key, nil
}

// SaveKey atomically writes a key file with restrictive permissions.
func SaveKey(path, key string) error {
	if path == "" {
		return errs.New(errs.Config, "no API key file path is configured").
			WithHint("set state_dir to a writable directory so maio can store the key")
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create the API key directory %s", dir).
			WithHint("check state_dir permissions, or run with sudo")
	}
	_ = os.Chmod(dir, 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(key)+"\n"), 0o600); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write the API key file %s", tmp)
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errs.Wrap(errs.IO, err, "cannot replace the API key file %s", path)
	}
	return nil
}

// StoredKey reads a key file. A missing file is not an error.
func StoredKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Check validates the request credentials in constant time.
func (a *Authenticator) Check(r *http.Request) bool {
	if a == nil || !a.required {
		return true
	}
	token := ""
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			token = strings.TrimSpace(parts[1])
		}
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-API-Key"))
	}
	if token == "" || len(token) != len(a.key) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), a.key) == 1
}

// Mask renders a key for display without revealing it.
func Mask(key string) string {
	if len(key) <= 8 {
		return "********"
	}
	return key[:5] + strings.Repeat("*", 8) + key[len(key)-4:]
}

// IsLoopbackHost reports whether a listen address only accepts local clients.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ModeOn, "required", "true":
		return ModeOn
	case ModeOff, "none", "false":
		return ModeOff
	default:
		return ModeAuto
	}
}
