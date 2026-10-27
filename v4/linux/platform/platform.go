//go:build linux

// Package platform implements the Linux integration surface.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	coreplatform "github.com/ShieldDog-ACE/maio-cluster/v4/core/platform"
)

// Adapter is the Linux platform adapter.
type Adapter struct{}

// New returns the Linux adapter.
func New() *Adapter { return &Adapter{} }

// Name implements platform.Adapter.
func (a *Adapter) Name() string { return "linux" }

// Paths returns XDG paths for user scope and FHS paths for system scope.
func (a *Adapter) Paths(system bool) config.Paths {
	if system {
		return config.Paths{
			ConfigPath: "/etc/maio/cluster.toml",
			DataDir:    "/var/lib/maio",
			ModelsDir:  "/var/lib/maio/models",
			StateDir:   "/var/lib/maio",
			LogDir:     "/var/log/maio",
		}
	}
	home, _ := os.UserHomeDir()
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	cfg := filepath.Join(cfgHome, "maio", "cluster.toml")
	data := filepath.Join(dataHome, "maio")
	return config.Paths{
		ConfigPath: cfg,
		DataDir:    data,
		ModelsDir:  filepath.Join(data, "models"),
		StateDir:   data,
		LogDir:     filepath.Join(data, "logs"),
	}
}

// InstallRoot returns the packaged runtime directory.
func (a *Adapter) InstallRoot(system bool) string {
	if system {
		return "/usr/lib/maio"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "lib", "maio")
}

func (a *Adapter) unitPath(system bool) string {
	if system {
		return "/etc/systemd/system/maio.service"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "maio.service")
}

// ServiceInstall writes and starts a systemd unit.
func (a *Adapter) ServiceInstall(spec coreplatform.ServiceSpec) error {
	unit := a.renderUnit(spec)
	path := a.unitPath(spec.System)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create systemd directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	args := []string{"daemon-reload"}
	if err := runSystemctl(spec.System, args...); err != nil {
		return err
	}
	if err := runSystemctl(spec.System, "enable", "--now", "maio"); err != nil {
		return fmt.Errorf("unit written to %s but starting it failed: %w", path, err)
	}
	return nil
}

func (a *Adapter) renderUnit(spec coreplatform.ServiceSpec) string {
	args := []string{spec.BinaryPath, "--config", spec.ConfigPath}
	if spec.System {
		args = append(args, "--system")
	}
	if spec.Coordinator {
		args = append(args, "cluster", "up")
	} else {
		args = append(args, "agent")
	}
	quoted := make([]string, len(args))
	for i, s := range args {
		quoted[i] = systemdQuote(s)
	}
	userLine := ""
	if spec.System {
		if u, err := user.Lookup("maio"); err == nil {
			userLine = "User=" + u.Username + "\nGroup=" + u.Username + "\n"
		}
	}
	return fmt.Sprintf(`[Unit]
Description=maio distributed llama.cpp cluster
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=always
RestartSec=3
%sNoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=false
LimitNOFILE=1048576
TimeoutStopSec=20

[Install]
WantedBy=%s
`, strings.Join(quoted, " "), userLine, map[bool]string{true: "multi-user.target", false: "default.target"}[spec.System])
}

func systemdQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"'\\$") {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func runSystemctl(system bool, args ...string) error {
	base := []string{}
	if !system {
		base = append(base, "--user")
	}
	base = append(base, args...)
	cmd := exec.Command("systemctl", base...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w (%s)", strings.Join(base, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ServiceUninstall stops and removes the unit.
func (a *Adapter) ServiceUninstall(system bool) error {
	_ = runSystemctl(system, "disable", "--now", "maio")
	path := a.unitPath(system)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove %s: %w", path, err)
	}
	_ = runSystemctl(system, "daemon-reload")
	return nil
}

// ServiceStatus reports the unit state.
func (a *Adapter) ServiceStatus(system bool) (string, error) {
	base := []string{}
	if !system {
		base = append(base, "--user")
	}
	base = append(base, "is-active", "maio")
	out, err := exec.Command("systemctl", base...).CombinedOutput()
	state := strings.TrimSpace(string(out))
	if state == "" {
		state = "inactive"
	}
	if err != nil && state != "inactive" && state != "failed" {
		return state, nil
	}
	return state, nil
}
