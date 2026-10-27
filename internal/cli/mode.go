package cli

import (
	"os"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

const (
	scopeUser   = "user"
	scopeSystem = "system"
)

// extractScope removes the global --user/--system selector. The default is
// user-level, so a plain "maio ..." never touches /etc/maio or a system unit.
// System scope is an explicit opt-in and requires root.
func extractScope(args []string) (string, []string, error) {
	scope := scopeUser
	system := false
	user := false
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--system":
			system = true
		case "--user":
			user = true
		default:
			out = append(out, a)
		}
	}
	if system && user {
		return "", nil, errs.New(errs.Usage, "--system and --user are mutually exclusive").
			WithHint("use --system only when you want the /etc/maio system service")
	}
	if system {
		scope = scopeSystem
	} else if user {
		scope = scopeUser
	}
	if len(out) == 0 {
		return scope, out, nil
	}
	switch out[0] {
	case "-h", "--help", "help":
		return scope, out, nil
	}
	if scope == scopeSystem && os.Geteuid() != 0 && os.Getenv("MAIO_SYSTEM_SERVICE") != "1" {
		return "", nil, errs.New(errs.Permission, "system scope requires root").
			WithHint("run: sudo maio --system %s", strings.Join(out, " "))
	}
	return scope, out, nil
}
