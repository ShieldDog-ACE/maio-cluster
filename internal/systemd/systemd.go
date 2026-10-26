// Package systemd manages start-on-boot (maio.service).
package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	unitName = "maio"
	unitPath = "/etc/systemd/system/maio.service"
)

const unitTemplate = `[Unit]
Description=maio model service
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
ExecStart=%s serve
Environment=MAIO_CONF=%s
Restart=on-failure
RestartSec=3
LimitNOFILE=524288

[Install]
WantedBy=multi-user.target
`

// Enable writes the unit and runs enable --now.
func Enable(exe, confPath string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found; start-on-boot cannot be managed on this system")
	}
	unit := fmt.Sprintf(unitTemplate, exe, confPath)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("failed to write systemd unit (root required): %w", err)
	}
	if out, err := run("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %s", out)
	}
	if out, err := run("systemctl", "enable", "--now", unitName); err != nil {
		return fmt.Errorf("systemctl enable failed: %s", out)
	}
	return nil
}

// Disable turns start-on-boot off and stops the service (config and models are kept).
func Disable() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found; start-on-boot cannot be managed on this system")
	}
	if out, err := run("systemctl", "disable", "--now", unitName); err != nil {
		return fmt.Errorf("systemctl disable failed: %s", out)
	}
	return nil
}

// Status reports start-on-boot and service state.
func Status() (enabled, active bool) {
	if out, err := run("systemctl", "is-enabled", unitName); err == nil && strings.TrimSpace(out) == "enabled" {
		enabled = true
	}
	if out, err := run("systemctl", "is-active", unitName); err == nil && strings.TrimSpace(out) == "active" {
		active = true
	}
	return enabled, active
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
