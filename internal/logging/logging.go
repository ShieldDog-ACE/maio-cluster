// Package logging writes maio's operational audit log (JSON Lines).
//
// One line is written for every meaningful action: installs, downloads,
// registrations, removals, engine starts/stops, boot changes, API errors and
// failed commands. The log lives under <state_dir>/log/maio.log and falls back
// to the per-user state directory when the system state directory is not
// writable.
package logging

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

const maxLogBytes = 10 << 20 // 10 MiB, then rotate to maio.log.1

// Logger is a concurrency-safe JSONL audit logger. A nil *Logger is a no-op.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

var (
	stdMu sync.RWMutex
	std   *Logger
)

// Init opens the audit log under stateDir/log. If that directory is not
// writable, it falls back to the per-user state directory. It never fails the
// command: when no log can be opened a warning is printed once to stderr.
func Init(stateDir string) *Logger {
	candidates := make([]string, 0, 4)
	if p := os.Getenv("MAIO_LOG"); p != "" {
		candidates = append(candidates, p)
	}
	if stateDir != "" {
		candidates = append(candidates, filepath.Join(stateDir, "log", "maio.log"))
	}
	candidates = append(candidates, filepath.Join(userStateDir(), "log", "maio.log"))

	for _, p := range candidates {
		if l, err := open(p); err == nil {
			stdMu.Lock()
			if std != nil {
				std.Close()
			}
			std = l
			stdMu.Unlock()
			return l
		}
	}
	fmt.Fprintf(os.Stderr, "maio: warning: cannot write the audit log; continuing without it\n")
	l := &Logger{}
	stdMu.Lock()
	std = l
	stdMu.Unlock()
	return l
}

// InitPath opens an exact log path (used by tests and by the installer).
func InitPath(path string) (*Logger, error) {
	l, err := open(path)
	if err != nil {
		return nil, err
	}
	stdMu.Lock()
	if std != nil {
		std.Close()
	}
	std = l
	stdMu.Unlock()
	return l, nil
}

// Current returns the active logger; it is never nil after Init.
func Current() *Logger {
	stdMu.RLock()
	defer stdMu.RUnlock()
	if std == nil {
		return &Logger{}
	}
	return std
}

// Path returns the active log file, or "" when logging is unavailable.
func Path() string {
	l := Current()
	if l == nil {
		return ""
	}
	return l.path
}

// Close flushes and closes the active logger.
func Close() {
	stdMu.Lock()
	defer stdMu.Unlock()
	if std != nil {
		std.Close()
		std = nil
	}
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// Info records a normal operation.
func Info(action string, kv ...any) { Current().log("info", action, nil, kv) }

// Warn records a recoverable problem.
func Warn(action string, kv ...any) { Current().log("warn", action, nil, kv) }

// Error records a failure with its stable code and hint.
func Error(action string, err error, kv ...any) {
	Current().log("error", action, err, kv)
}

func (l *Logger) log(level, action string, err error, kv []any) {
	if l == nil || l.f == nil {
		return
	}
	entry := map[string]any{
		"time":   time.Now().Format(time.RFC3339),
		"level":  level,
		"action": action,
	}
	if e := errs.As(err); e != nil {
		entry["code"] = string(e.Code)
		entry["error"] = e.Msg
		if h := errs.HintOf(err); h != "" {
			entry["hint"] = h
		}
	} else if err != nil {
		entry["error"] = err.Error()
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok || k == "" {
			continue
		}
		entry[k] = redact(k, kv[i+1])
	}
	if len(kv)%2 == 1 {
		entry["extra"] = fmt.Sprint(kv[len(kv)-1])
	}
	line, mErr := json.Marshal(entry)
	if mErr != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	_, _ = l.f.Write(append(line, '\n'))
}

// redact removes credentials and volatile query strings before persisting.
func redact(key string, v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	lk := strings.ToLower(key)
	switch {
	case strings.Contains(lk, "token"), strings.Contains(lk, "password"),
		strings.Contains(lk, "secret"), strings.Contains(lk, "credential"):
		return "[redacted]"
	case lk == "prompt" || lk == "content" || lk == "messages":
		return fmt.Sprintf("[%d bytes]", len(s))
	case lk == "url" || lk == "source" || strings.HasSuffix(lk, "_url"):
		return stripQuery(s)
	}
	return v
}

func stripQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	u.RawQuery = ""
	return u.String()
}

func open(path string) (*Logger, error) {
	if path == "" {
		return nil, fmt.Errorf("empty log path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Logger{f: f, path: path}, nil
}

func userStateDir() string {
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "maio")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "maio-state")
	}
	return filepath.Join(home, ".local", "state", "maio")
}
