// Package sdnotify implements the systemd readiness and watchdog protocol
// without external dependencies. All methods are no-ops when maio is not
// started by systemd (NOTIFY_SOCKET is unset).
package sdnotify

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Notifier sends datagrams to systemd's NOTIFY_SOCKET.
type Notifier struct {
	addr string
}

// New reads NOTIFY_SOCKET from the environment.
func New() *Notifier { return &Notifier{addr: os.Getenv("NOTIFY_SOCKET")} }

// Enabled reports whether systemd notification is available.
func (n *Notifier) Enabled() bool { return n != nil && n.addr != "" }

// Ready tells systemd the service finished starting.
func (n *Notifier) Ready() error { return n.send("READY=1") }

// Reloading announces that the configuration is being reloaded.
func (n *Notifier) Reloading() error { return n.send("RELOADING=1") }

// Stopping announces a graceful shutdown.
func (n *Notifier) Stopping() error { return n.send("STOPPING=1") }

// Watchdog resets the systemd watchdog timer.
func (n *Notifier) Watchdog() error { return n.send("WATCHDOG=1") }

// Status sets a short human-readable service status.
func (n *Notifier) Status(msg string) error {
	msg = strings.ReplaceAll(msg, "\n", " ")
	return n.send("STATUS=" + msg)
}

// ExtendTimeout asks systemd for more time for the current start/stop phase.
func (n *Notifier) ExtendTimeout(d time.Duration) error {
	if d <= 0 {
		return nil
	}
	return n.send(fmt.Sprintf("EXTEND_TIMEOUT_USEC=%d", d.Microseconds()))
}

// WatchdogInterval returns how often a watchdog ping should be sent. systemd
// expects a ping at least once per WatchdogSec; using half the interval gives
// comfortable margin.
func WatchdogInterval() time.Duration {
	raw := os.Getenv("WATCHDOG_USEC")
	if raw == "" {
		return 0
	}
	usec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	d := time.Duration(usec/2) * time.Microsecond
	if d < time.Second {
		d = time.Second
	}
	return d
}

// RunWatchdog pings systemd until ctx is done. It returns immediately when no
// watchdog is configured.
func (n *Notifier) RunWatchdog(ctxDone <-chan struct{}) {
	interval := WatchdogInterval()
	if interval == 0 || !n.Enabled() {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-t.C:
			_ = n.Watchdog()
		}
	}
}

func (n *Notifier) send(payload string) error {
	if !n.Enabled() {
		return nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: n.addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err = conn.Write([]byte(payload))
	return err
}
