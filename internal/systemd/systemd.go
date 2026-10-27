// Package systemd manages maio's start-on-boot service, both as a system unit
// (root) and as a per-user unit for rootless installs.
package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

const unitName = "maio"

// Mode selects the system or per-user service manager.
type Mode string

const (
	System Mode = "system"
	User   Mode = "user"
)

// Status is one observed service state.
type Status struct {
	Mode    Mode
	Unit    string
	Exists  bool
	Enabled bool
	Active  bool
	Detail  string
}

// UnitPath returns the unit file path for a mode.
func UnitPath(mode Mode) string {
	if mode == User {
		if p := os.Getenv("XDG_CONFIG_HOME"); p != "" {
			return filepath.Join(p, "systemd", "user", unitName+".service")
		}
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return filepath.Join(os.TempDir(), "maio", unitName+".service")
		}
		return filepath.Join(home, ".config", "systemd", "user", unitName+".service")
	}
	return "/etc/systemd/system/" + unitName + ".service"
}

// UnitFile renders the service unit.
func UnitFile(mode Mode, exe, confPath string) string {
	wanted := "multi-user.target"
	if mode == User {
		wanted = "default.target"
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=maio model management service\n")
	b.WriteString("Documentation=https://github.com/ShieldDog-ACE/maio-cluster\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=exec\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", systemdArg(exe))
	fmt.Fprintf(&b, "Environment=MAIO_CONF=%s\n", systemdArg(confPath))
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=3\n")
	b.WriteString("LimitNOFILE=524288\n")
	b.WriteString("TimeoutStopSec=15\n")
	b.WriteString("KillMode=control-group\n\n")
	b.WriteString("[Install]\n")
	fmt.Fprintf(&b, "WantedBy=%s\n", wanted)
	return b.String()
}

// Enable writes the unit, reloads systemd and enables + starts the service.
func Enable(mode Mode, exe, confPath string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errs.New(errs.Service, "systemctl was not found; start-on-boot cannot be managed on this system").
			WithHint("install systemd, or start the service manually with: %s serve", exe)
	}
	unit := UnitFile(mode, exe, confPath)
	path := UnitPath(mode)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create systemd unit directory %s", filepath.Dir(path)).
			WithHint("run with sudo for the system service, or use: maio boot 1 --user")
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write systemd unit %s", path).
			WithHint("run with sudo for the system service, or use: maio boot 1 --user")
	}
	if out, err := run(mode, "daemon-reload"); err != nil {
		return errs.Newf(errs.Service, "systemctl daemon-reload failed: %s", out).
			WithHint("check the unit file %s with: systemd-analyze verify %s", path, path)
	}
	if out, err := run(mode, "enable", "--now", unitName); err != nil {
		return errs.Newf(errs.Service, "systemctl enable --now failed: %s", out).
			WithHint("inspect the service with: journalctl %s -u %s -n 50", journalFlag(mode), unitName)
	}
	return nil
}

// Disable stops the service and turns start-on-boot off. Config and models are
// kept. A missing unit is not an error.
func Disable(mode Mode) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errs.New(errs.Service, "systemctl was not found; start-on-boot cannot be managed on this system")
	}
	if st := Query(mode); !st.Exists {
		return nil
	}
	if out, err := run(mode, "disable", "--now", unitName); err != nil {
		return errs.Newf(errs.Service, "systemctl disable --now failed: %s", out).
			WithHint("inspect the service with: systemctl %s status %s", modeFlag(mode), unitName)
	}
	return nil
}

// Query reports the unit state.
func Query(mode Mode) Status {
	st := Status{Mode: mode, Unit: unitName}
	path := UnitPath(mode)
	if _, err := os.Stat(path); err == nil {
		st.Exists = true
	}
	if out, err := run(mode, "is-enabled", unitName); err == nil {
		st.Detail = strings.TrimSpace(out)
		st.Enabled = st.Detail == "enabled"
	} else if out != "" {
		st.Detail = strings.TrimSpace(out)
	}
	if out, err := run(mode, "is-active", unitName); err == nil {
		st.Active = strings.TrimSpace(out) == "active"
	}
	return st
}

// Linger reports whether systemd keeps the user's service running after logout.
func Linger(user string) (bool, string) {
	if _, err := exec.LookPath("loginctl"); err != nil {
		return false, "loginctl not found"
	}
	out, err := exec.Command("loginctl", "show-user", user, "--property=Linger").CombinedOutput()
	if err != nil {
		return false, strings.TrimSpace(string(out))
	}
	val := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Linger="))
	return val == "yes", val
}

// LingerHint returns the command that enables boot-time start for a user unit.
func LingerHint(user string) string {
	return fmt.Sprintf("sudo loginctl enable-linger %s", user)
}

func run(mode Mode, args ...string) (string, error) {
	if mode == User {
		args = append([]string{"--user"}, args...)
	}
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func modeFlag(mode Mode) string {
	if mode == User {
		return "--user"
	}
	return ""
}

func journalFlag(mode Mode) string {
	if mode == User {
		return "--user"
	}
	return ""
}

// systemdArg quotes one unit argument when needed and escapes specifiers.
func systemdArg(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strconv.Quote(s)
}
