//go:build darwin

// Package platform implements the macOS integration surface.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	coreplatform "github.com/ShieldDog-ACE/maio-cluster/v4/core/platform"
)

// Adapter is the macOS platform adapter.
type Adapter struct{}

// New returns the macOS adapter.
func New() *Adapter { return &Adapter{} }

// Name implements platform.Adapter.
func (a *Adapter) Name() string { return "darwin" }

const label = "dev.maio.cluster"

// Paths returns user or machine-wide Application Support locations.
func (a *Adapter) Paths(system bool) config.Paths {
	if system {
		data := "/Library/Application Support/maio"
		return config.Paths{
			ConfigPath: filepath.Join(data, "cluster.toml"),
			DataDir:    data,
			ModelsDir:  filepath.Join(data, "models"),
			StateDir:   data,
			LogDir:     "/Library/Logs/maio",
		}
	}
	home, _ := os.UserHomeDir()
	data := filepath.Join(home, "Library", "Application Support", "maio")
	return config.Paths{
		ConfigPath: filepath.Join(data, "cluster.toml"),
		DataDir:    data,
		ModelsDir:  filepath.Join(data, "models"),
		StateDir:   data,
		LogDir:     filepath.Join(home, "Library", "Logs", "maio"),
	}
}

// InstallRoot returns the packaged runtime directory.
func (a *Adapter) InstallRoot(system bool) string {
	if system {
		return "/usr/local/lib/maio"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "maio", "runtime")
}

func (a *Adapter) plistPath(system bool) string {
	if system {
		return "/Library/LaunchDaemons/" + label + ".plist"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func (a *Adapter) renderPlist(spec coreplatform.ServiceSpec, logDir string) string {
	args := []string{spec.BinaryPath, "--config", spec.ConfigPath}
	if spec.System {
		args = append(args, "--system")
	}
	if spec.Coordinator {
		args = append(args, "cluster", "up")
	} else {
		args = append(args, "agent")
	}
	var argXML strings.Builder
	for _, s := range args {
		argXML.WriteString("    <string>" + xmlEscape(s) + "</string>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>WorkingDirectory</key><string>%s</string>
</dict>
</plist>
`, label, argXML.String(), xmlEscape(filepath.Join(logDir, "maio.out.log")), xmlEscape(filepath.Join(logDir, "maio.err.log")), xmlEscape(filepath.Dir(spec.ConfigPath)))
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// ServiceInstall writes a LaunchAgent/LaunchDaemon and bootstraps it.
func (a *Adapter) ServiceInstall(spec coreplatform.ServiceSpec) error {
	logDir := os.Getenv("HOME")
	if spec.System {
		logDir = "/Library/Logs/maio"
	} else {
		logDir = filepath.Join(logDir, "Library", "Logs", "maio")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("cannot create log directory: %w", err)
	}
	path := a.plistPath(spec.System)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create LaunchAgents/LaunchDaemons: %w", err)
	}
	if err := os.WriteFile(path, []byte(a.renderPlist(spec, logDir)), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if spec.System {
		_ = exec.Command("chown", "root:wheel", path).Run()
	}
	domain := a.domain(spec.System)
	_ = exec.Command("launchctl", "bootout", domain+"/"+label).Run()
	out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("launchctl", "kickstart", "-k", domain+"/"+label).Run()
	return nil
}

func (a *Adapter) domain(system bool) string {
	if system {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}

// ServiceUninstall boots out and removes the plist.
func (a *Adapter) ServiceUninstall(system bool) error {
	domain := a.domain(system)
	_ = exec.Command("launchctl", "bootout", domain+"/"+label).Run()
	path := a.plistPath(system)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove %s: %w", path, err)
	}
	return nil
}

// ServiceStatus reports the launchd state.
func (a *Adapter) ServiceStatus(system bool) (string, error) {
	domain := a.domain(system)
	out, err := exec.Command("launchctl", "print", domain+"/"+label).CombinedOutput()
	if err != nil {
		return "not loaded", nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state =") || strings.HasPrefix(line, "pid =") {
			return line, nil
		}
	}
	return "loaded", nil
}
