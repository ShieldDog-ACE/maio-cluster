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

// Options describes the service unit to generate.
type Options struct {
	Mode       Mode
	Exe        string
	ConfPath   string
	StateDir   string
	ModelsRoot string
	RunAs      string
	Port       int
}

// UnitFile renders the service unit with the V3 hardening and watchdog
// settings. It is kept for compatibility with older callers.
func UnitFile(mode Mode, exe, confPath string) string {
	return UnitFileOpts(Options{Mode: mode, Exe: exe, ConfPath: confPath})
}

// UnitFileOpts renders the service unit. The unit uses Type=notify so systemd
// only marks the service ready after the HTTP listener is up, and WatchdogSec
// lets systemd restart a wedged process.
func UnitFileOpts(o Options) string {
	mode := o.Mode
	if mode == "" {
		mode = System
	}
	stateDir := o.StateDir
	if stateDir == "" {
		stateDir = "/var/lib/maio"
	}
	modelsRoot := o.ModelsRoot
	if modelsRoot == "" {
		modelsRoot = filepath.Join(stateDir, "models")
	}
	wanted := "multi-user.target"
	if mode == User {
		wanted = "default.target"
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=maio model management service\n")
	b.WriteString("Documentation=https://github.com/ShieldDog-ACE/maio-cluster\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("StartLimitIntervalSec=300\n")
	b.WriteString("StartLimitBurst=12\n\n")
	b.WriteString("[Service]\n")
	if stateDir == "/var/lib/maio" {
		b.WriteString("StateDirectory=maio\n")
		b.WriteString("StateDirectoryMode=0750\n")
	}
	b.WriteString("Type=notify\n")
	b.WriteString("NotifyAccess=main\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", systemdArg(o.Exe))
	fmt.Fprintf(&b, "Environment=MAIO_CONF=%s\n", systemdArg(o.ConfPath))
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2\n")
	b.WriteString("WatchdogSec=30\n")
	b.WriteString("TimeoutStartSec=120\n")
	b.WriteString("TimeoutStopSec=45\n")
	b.WriteString("KillMode=mixed\n")
	b.WriteString("SendSIGKILL=yes\n")
	b.WriteString("OOMPolicy=stop\n")
	b.WriteString("LimitNOFILE=1048576\n")
	b.WriteString("LimitNPROC=8192\n")
	b.WriteString("TasksMax=8192\n")
	fmt.Fprintf(&b, "Environment=HOME=%s\n", systemdArg(stateDir))
	// User units run under the user manager and cannot switch User=/Group=.
	if mode == System && o.RunAs != "" {
		fmt.Fprintf(&b, "User=%s\n", systemdArg(o.RunAs))
		fmt.Fprintf(&b, "Group=%s\n", systemdArg(o.RunAs))
		b.WriteString("SupplementaryGroups=video render\n")
		if o.Port > 0 && o.Port < 1024 {
			b.WriteString("AmbientCapabilities=CAP_NET_BIND_SERVICE\n")
		}
		b.WriteString("CapabilityBoundingSet=\n")
	}
	b.WriteString("\n# sandbox\n")
	b.WriteString("NoNewPrivileges=yes\n")
	b.WriteString("PrivateTmp=yes\n")
	b.WriteString("ProtectSystem=strict\n")
	b.WriteString("ProtectKernelTunables=yes\n")
	b.WriteString("ProtectKernelModules=yes\n")
	b.WriteString("ProtectKernelLogs=yes\n")
	b.WriteString("ProtectControlGroups=yes\n")
	b.WriteString("ProtectClock=yes\n")
	b.WriteString("ProtectHostname=yes\n")
	b.WriteString("ProtectProc=invisible\n")
	b.WriteString("RestrictNamespaces=yes\n")
	b.WriteString("RestrictRealtime=yes\n")
	b.WriteString("RestrictSUIDSGID=yes\n")
	b.WriteString("LockPersonality=yes\n")
	b.WriteString("SystemCallArchitectures=native\n")
	b.WriteString("RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\n")
	b.WriteString("UMask=0027\n")
	fmt.Fprintf(&b, "ReadWritePaths=-%s\n", systemdArg(stateDir))
	fmt.Fprintf(&b, "ReadOnlyPaths=-%s\n", systemdArg(modelsRoot))
	b.WriteString("\n[Install]\n")
	fmt.Fprintf(&b, "WantedBy=%s\n", wanted)
	return b.String()
}

// EnableOpts writes the unit, reloads systemd and enables + starts the service.
func EnableOpts(o Options) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errs.New(errs.Service, "systemctl was not found; start-on-boot cannot be managed on this system").
			WithHint("install systemd, or start the service manually with: %s serve", o.Exe)
	}
	unit := UnitFileOpts(o)
	path := UnitPath(o.Mode)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create systemd unit directory %s", filepath.Dir(path)).
			WithHint("run with sudo for the system service, or use: maio boot 1 --user")
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write systemd unit %s", path).
			WithHint("run with sudo for the system service, or use: maio boot 1 --user")
	}
	if out, err := run(o.Mode, "daemon-reload"); err != nil {
		return errs.Newf(errs.Service, "systemctl daemon-reload failed: %s", out).
			WithHint("check the unit file %s with: systemd-analyze verify %s", path, path)
	}
	if out, err := run(o.Mode, "enable", "--now", unitName); err != nil {
		return errs.Newf(errs.Service, "systemctl enable --now failed: %s", out).
			WithHint("inspect the service with: journalctl %s -u %s -n 50", journalFlag(o.Mode), unitName)
	}
	return nil
}

// Enable writes the unit, reloads systemd and enables + starts the service.
func Enable(mode Mode, exe, confPath string) error {
	return EnableOpts(Options{Mode: mode, Exe: exe, ConfPath: confPath})
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

// Control runs a systemctl lifecycle action.
func Control(mode Mode, action string) error {
	switch action {
	case "start", "stop", "restart", "reload", "try-restart":
	default:
		return errs.Newf(errs.Usage, "unsupported service action %q", action)
	}
	out, err := run(mode, action, unitName)
	if err != nil {
		return errs.Newf(errs.Service, "systemctl %s failed: %s", action, strings.TrimSpace(out)).
			WithHint("inspect the service with: journalctl %s -u %s -n 50", journalFlag(mode), unitName)
	}
	return nil
}

// Journal returns the last lines of the service journal.
func Journal(mode Mode, lines int) (string, error) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return "", errs.New(errs.Service, "journalctl was not found")
	}
	if lines <= 0 {
		lines = 100
	}
	args := []string{}
	if mode == User {
		args = append(args, "--user")
	}
	args = append(args, "-u", unitName, "-n", strconv.Itoa(lines), "--no-pager", "-o", "cat")
	out, err := exec.Command("journalctl", args...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), errs.Newf(errs.Service, "journalctl failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
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
