//go:build windows

// Package platform implements the Windows integration surface.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	coreplatform "github.com/ShieldDog-ACE/maio-cluster/v4/core/platform"
)

// Adapter is the Windows platform adapter.
type Adapter struct{}

// New returns the Windows adapter.
func New() *Adapter { return &Adapter{} }

// Name implements platform.Adapter.
func (a *Adapter) Name() string { return "windows" }

func appData() string {
	if v := os.Getenv("APPDATA"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
}

func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
}

func programData() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}
	return `C:\ProgramData`
}

// Paths returns per-user or machine-wide locations.
func (a *Adapter) Paths(system bool) config.Paths {
	if system {
		data := filepath.Join(programData(), "maio")
		return config.Paths{
			ConfigPath: filepath.Join(data, "cluster.toml"),
			DataDir:    data,
			ModelsDir:  filepath.Join(data, "models"),
			StateDir:   data,
			LogDir:     filepath.Join(data, "logs"),
		}
	}
	data := filepath.Join(localAppData(), "maio")
	return config.Paths{
		ConfigPath: filepath.Join(appData(), "maio", "cluster.toml"),
		DataDir:    data,
		ModelsDir:  filepath.Join(data, "models"),
		StateDir:   data,
		LogDir:     filepath.Join(data, "logs"),
	}
}

// InstallRoot returns the packaged runtime directory.
func (a *Adapter) InstallRoot(system bool) string {
	if system {
		return filepath.Join(os.Getenv("ProgramFiles"), "maio")
	}
	return filepath.Join(localAppData(), "maio")
}

func taskCommand(spec coreplatform.ServiceSpec) string {
	args := []string{quoteWin(spec.BinaryPath), "--config", quoteWin(spec.ConfigPath)}
	if spec.System {
		args = append(args, "--system")
	}
	if spec.Coordinator {
		args = append(args, "cluster", "up")
	} else {
		args = append(args, "agent")
	}
	return strings.Join(args, " ")
}

func quoteWin(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// ServiceInstall registers a scheduled task and starts it.
//
// A normal console program cannot be a Windows SCM service without reporting
// service status, so maio uses the Task Scheduler: it starts before/at logon,
// restarts are handled by the program loop, and uninstalling is a single
// schtasks command. This needs no third-party service wrapper.
func (a *Adapter) ServiceInstall(spec coreplatform.ServiceSpec) error {
	cmdline := taskCommand(spec)
	args := []string{"/Create", "/TN", "maio", "/TR", cmdline, "/F"}
	if spec.System {
		args = append(args, "/SC", "ONSTART", "/RU", "SYSTEM", "/RL", "HIGHEST")
	} else {
		args = append(args, "/SC", "ONLOGON", "/RL", "LIMITED")
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks create failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("schtasks", "/Run", "/TN", "maio").CombinedOutput(); err != nil {
		return fmt.Errorf("task created but start failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if spec.System {
		addFirewallRules()
	}
	return nil
}

func addFirewallRules() {
	rules := [][]string{
		{"advfirewall", "firewall", "add", "rule", "name=maio-tcp", "dir=in", "action=allow", "protocol=TCP", "localport=47800-47802"},
		{"advfirewall", "firewall", "add", "rule", "name=maio-udp", "dir=in", "action=allow", "protocol=UDP", "localport=47801"},
	}
	for _, r := range rules {
		_ = exec.Command("netsh", r...).Run()
	}
}

// ServiceUninstall stops and deletes the task.
func (a *Adapter) ServiceUninstall(system bool) error {
	_ = exec.Command("schtasks", "/End", "/TN", "maio").Run()
	out, err := exec.Command("schtasks", "/Delete", "/TN", "maio", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks delete failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if system {
		_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=maio-tcp").Run()
		_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=maio-udp").Run()
	}
	return nil
}

// ServiceStatus returns the task state.
func (a *Adapter) ServiceStatus(system bool) (string, error) {
	out, err := exec.Command("schtasks", "/Query", "/TN", "maio", "/FO", "LIST").CombinedOutput()
	if err != nil {
		return "not installed", nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		low := strings.ToLower(l)
		if strings.HasPrefix(low, "status:") || strings.Contains(l, "状态:") {
			return l, nil
		}
	}
	return "installed", nil
}
