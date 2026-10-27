// Package lockfile prevents two maio service processes from sharing one state
// directory. It uses flock(2), so the kernel releases the lock if the process
// dies and no stale lock file can block a restart.
package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Lock is a held exclusive lock.
type Lock struct {
	f    *os.File
	path string
}

// LockedError reports that another process holds the lock.
type LockedError struct {
	Path string
	PID  int
}

func (e *LockedError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("another maio service is already running (pid %d, lock %s)", e.PID, e.Path)
	}
	return fmt.Sprintf("another maio service is already running (lock %s)", e.Path)
}

// Acquire takes a non-blocking exclusive lock on path.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			pid := readPID(path)
			_ = f.Close()
			return nil, &LockedError{Path: path, PID: pid}
		}
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	l := &Lock{f: f, path: path}
	_ = f.Truncate(0)
	if _, err := f.Seek(0, 0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Sync()
	}
	return l, nil
}

// Path returns the lock file path.
func (l *Lock) Path() string { return l.path }

// Release unlocks and closes the file.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}
