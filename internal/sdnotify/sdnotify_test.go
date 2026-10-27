package sdnotify

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotifySocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	n := &Notifier{addr: sock}
	if !n.Enabled() {
		t.Fatal("notifier should be enabled")
	}
	buf := make([]byte, 256)
	for _, send := range []func() error{n.Ready, n.Watchdog, func() error { return n.Status("line1\nline2") }} {
		if err := send(); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		nr, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		msg := string(buf[:nr])
		if strings.Contains(msg, "\n") {
			t.Fatalf("status was not sanitized: %q", msg)
		}
	}
}

func TestWatchdogInterval(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	if d := WatchdogInterval(); d != 0 {
		t.Fatalf("interval = %s, want 0", d)
	}
	t.Setenv("WATCHDOG_USEC", "8000000")
	if d := WatchdogInterval(); d != 4*time.Second {
		t.Fatalf("interval = %s, want 4s", d)
	}
	t.Setenv("WATCHDOG_USEC", "1000000")
	if d := WatchdogInterval(); d != time.Second {
		t.Fatalf("interval = %s, want 1s minimum", d)
	}
}

func TestDisabledNotifier(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	n := New()
	if n.Enabled() {
		t.Fatal("notifier should be disabled without NOTIFY_SOCKET")
	}
	if err := n.Ready(); err != nil {
		t.Fatalf("disabled send should be a no-op: %v", err)
	}
}
