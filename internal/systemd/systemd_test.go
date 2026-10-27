package systemd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitFileSystem(t *testing.T) {
	u := UnitFile(System, "/usr/bin/maio", "/etc/maio/maio.conf")
	for _, want := range []string{
		"ExecStart=/usr/bin/maio serve",
		"Environment=MAIO_CONF=/etc/maio/maio.conf",
		"WantedBy=multi-user.target",
		"Restart=on-failure",
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
