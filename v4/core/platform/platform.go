// Package platform is the small seam each operating system implements.
//
// Linux, Windows and macOS each provide one Adapter. The shared core never
// imports an OS-specific package; the platform main wires the adapter into the
// CLI. That keeps the three product trees independent.
package platform

import "github.com/ShieldDog-ACE/maio-cluster/v4/core/config"

// ServiceSpec describes the long-running process to register.
type ServiceSpec struct {
	// System installs a machine-wide service; false installs a user service.
	System bool
	// Coordinator runs the coordinator loop; false runs an agent only.
	Coordinator bool
	// ConfigPath is the absolute configuration file.
	ConfigPath string
	// BinaryPath is the absolute maio executable.
	BinaryPath string
	// ExtraArgs are appended to the service command.
	ExtraArgs []string
}

// Adapter is the per-OS integration surface.
type Adapter interface {
	// Name is "linux", "windows" or "darwin".
	Name() string
	// Paths returns scope-specific defaults.
	Paths(system bool) config.Paths
	// ServiceInstall registers and starts the service.
	ServiceInstall(spec ServiceSpec) error
	// ServiceUninstall stops and removes the service.
	ServiceUninstall(system bool) error
	// ServiceStatus returns a human-readable state.
	ServiceStatus(system bool) (string, error)
	// InstallRoot returns where the packaged app lives for this scope.
	InstallRoot(system bool) string
}
