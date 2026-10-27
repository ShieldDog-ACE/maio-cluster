package cli

import (
	"os"
	"testing"
)

func TestExtractScopeDefaultUser(t *testing.T) {
	scope, args, err := extractScope([]string{"status", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if scope != scopeUser {
		t.Fatalf("scope = %q", scope)
	}
	if len(args) != 2 || args[0] != "status" || args[1] != "--json" {
		t.Fatalf("args = %v", args)
	}
}

func TestExtractScopeBothFlags(t *testing.T) {
	if _, _, err := extractScope([]string{"--system", "--user", "status"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestExtractScopeSystemRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	if _, _, err := extractScope([]string{"--system", "status"}); err == nil {
		t.Fatal("expected a permission error")
	}
}

func TestExtractScopeSystemHelpAllowed(t *testing.T) {
	scope, _, err := extractScope([]string{"--system", "--help"})
	if err != nil {
		t.Fatal(err)
	}
	if scope != scopeSystem {
		t.Fatalf("scope = %q", scope)
	}
}
