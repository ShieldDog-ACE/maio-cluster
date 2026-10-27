package systemd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitFileSystem(t *testing.T) {
	u := UnitFile(System, "/usr/bin/maio", "/etc/maio/maio.conf")
	for _, want := range []string{
		"ExecStart=/usr/bin/maio --system serve",
		"Environment=MAIO_CONF=/etc/maio/maio.conf",
		"WantedBy=multi-user.target",
		"Type=notify",
		"Restart=always",
		"WatchdogSec=30",
		"KillMode=mixed",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=-/var/lib/maio",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit is missing %q:\n%s", want, u)
		}
	}
}

func TestUnitFileUser(t *testing.T) {
	u := UnitFile(User, "/home/u/.local/bin/maio", "/home/u/.config/maio/maio.conf")
	if !strings.Contains(u, "WantedBy=default.target") {
		t.Fatalf("user unit has the wrong WantedBy:\n%s", u)
	}
}

func TestUnitFileDedicatedUser(t *testing.T) {
	u := UnitFileOpts(Options{
		Mode: System, Exe: "/usr/bin/maio", ConfPath: "/etc/maio/maio.conf",
		StateDir: "/var/lib/maio", ModelsRoot: "/var/lib/maio/models",
		RunAs: "maio", Port: 11434,
	})
	for _, want := range []string{
		"User=maio", "Group=maio", "SupplementaryGroups=video render",
		"CapabilityBoundingSet=", "Environment=HOME=/var/lib/maio",
		"ReadOnlyPaths=-/var/lib/maio/models",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit is missing %q:\n%s", want, u)
		}
	}
}

func TestUnitPathUser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := filepath.Join(dir, "systemd", "user", "maio.service")
	if got := UnitPath(User); got != want {
		t.Fatalf("UnitPath = %q, want %q", got, want)
	}
}

func TestSystemdArg(t *testing.T) {
	if got := systemdArg("/a/b"); got != "/a/b" {
		t.Fatalf("plain path = %q", got)
	}
	if got := systemdArg("/a b"); !strings.HasPrefix(got, `"`) {
		t.Fatalf("space path not quoted: %q", got)
	}
	if got := systemdArg("100%"); !strings.Contains(got, "%%") {
		t.Fatalf("percent not escaped: %q", got)
	}
}

func TestUserUnitDoesNotSwitchUser(t *testing.T) {
	u := UnitFileOpts(Options{Mode: User, Exe: "/home/u/.local/bin/maio",
		ConfPath: "/home/u/.config/maio/maio.conf", StateDir: "/home/u/.local/state/maio",
		ModelsRoot: "/home/u/AI", RunAs: "maio"})
	if strings.Contains(u, "User=") || strings.Contains(u, "Group=") {
		t.Fatalf("user unit must not set User=/Group=:\n%s", u)
	}
}

func TestUnitPassesScope(t *testing.T) {
	base := Options{Exe: "/usr/bin/maio", ConfPath: "/etc/maio/maio.conf",
		StateDir: "/var/lib/maio", ModelsRoot: "/var/lib/maio/models"}
	sys := UnitFileOpts(withMode(base, System))
	if !strings.Contains(sys, "ExecStart=/usr/bin/maio --system serve") {
		t.Fatalf("system unit lacks --system:\n%s", sys)
	}
	usr := UnitFileOpts(withMode(base, User))
	if !strings.Contains(usr, "ExecStart=/usr/bin/maio --user serve") {
		t.Fatalf("user unit lacks --user:\n%s", usr)
	}
}

func withMode(o Options, m Mode) Options { o.Mode = m; return o }
