package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/systemd"
)

type upgradeState struct {
	Current         string `json:"current"`
	Previous        string `json:"previous"`
	CurrentVersion  string `json:"current_version"`
	PreviousVersion string `json:"previous_version"`
	UpdatedAt       string `json:"updated_at"`
}

func upgradeDir(conf *config.Global) string {
	return filepath.Join(conf.RuntimeDir, "upgrade")
}

func upgradeStatePath(conf *config.Global) string {
	return filepath.Join(upgradeDir(conf), "state.json")
}

func cmdUpgrade(conf *config.Global, args []string) int {
	noRestart := false
	force := false
	var pos []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio upgrade <path-to-maio-binary> [--no-restart] [--force]")
			fmt.Println("       atomically replaces the running binary and restarts the service")
			return 0
		case "--no-restart":
			noRestart = true
		case "--force":
			force = true
		default:
			if strings.HasPrefix(a, "-") {
				return usagef("unknown argument %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usagef("usage: maio upgrade <path-to-maio-binary>")
	}
	candidate, err := filepath.Abs(pos[0])
	if err != nil {
		return usagef("invalid path %q", pos[0])
	}
	if err := validateCandidate(candidate); err != nil {
		return fail(err)
	}
	newVersion, err := binaryVersion(candidate)
	if err != nil {
		return fail(err)
	}
	current, err := runningBinary()
	if err != nil {
		return fail(err)
	}
	oldVersion := Version
	if newVersion == oldVersion && !force {
		return fail(errs.Newf(errs.Conflict, "the candidate already reports version %s", newVersion).
			WithHint("pass --force to replace it anyway"))
	}
	if err := os.MkdirAll(upgradeDir(conf), 0o700); err != nil {
		return fail(errs.Wrap(errs.Permission, err, "cannot create %s", upgradeDir(conf)))
	}
	backup := filepath.Join(upgradeDir(conf),
		fmt.Sprintf("maio-%s-%s", sanitizeVersion(oldVersion), time.Now().Format("20060102-150405")))
	if err := copyFileMode(current, backup, 0o755); err != nil {
		return fail(errs.Wrap(errs.Permission, err, "cannot back up the current binary %s", current).
			WithHint("run with sudo, or fix permissions on %s", filepath.Dir(current)))
	}
	if err := replaceBinary(candidate, current); err != nil {
		return fail(err)
	}
	state := upgradeState{
		Current: current, Previous: backup, CurrentVersion: newVersion, PreviousVersion: oldVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := saveUpgradeState(conf, state); err != nil {
		fmt.Fprintf(os.Stderr, "maio: warning: could not save the rollback state: %v\n", err)
	}
	logging.Info("upgrade.done", "from", oldVersion, "to", newVersion, "path", current, "backup", backup)
	fmt.Printf("upgraded %s -> %s\n  binary : %s\n  backup : %s\n", oldVersion, newVersion, current, backup)
	if noRestart {
		return 0
	}
	if err := restartIfActive(conf); err != nil {
		fmt.Fprintf(os.Stderr, "maio: restart failed: %v\nrolling back to %s\n", err, oldVersion)
		if rerr := copyFileMode(backup, current, 0o755); rerr != nil {
			return fail(errs.Wrap(errs.IO, rerr, "restart failed and rollback also failed").
				WithHint("restore %s to %s manually", backup, current))
		}
		_ = restartIfActive(conf)
		return fail(err)
	}
	return 0
}

func cmdRollback(conf *config.Global, args []string) int {
	noRestart := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio rollback [--no-restart]")
			return 0
		case "--no-restart":
			noRestart = true
		default:
			return usagef("unknown argument %s", a)
		}
	}
	state, err := loadUpgradeState(conf)
	if err != nil {
		return fail(err)
	}
	if state.Previous == "" {
		return fail(errs.New(errs.NotFound, "no upgrade backup is recorded").
			WithHint("upgrade at least once before rolling back"))
	}
	if _, err := os.Stat(state.Previous); err != nil {
		return fail(errs.Newf(errs.NotFound, "the backup is missing: %s", state.Previous))
	}
	current, err := runningBinary()
	if err != nil {
		return fail(err)
	}
	if err := copyFileMode(state.Previous, current, 0o755); err != nil {
		return fail(errs.Wrap(errs.Permission, err, "cannot restore %s", current).
			WithHint("run with sudo, or fix permissions on %s", filepath.Dir(current)))
	}
	logging.Info("rollback.done", "version", state.PreviousVersion, "path", current, "backup", state.Previous)
	fmt.Printf("rolled back to %s\n  binary : %s\n", state.PreviousVersion, current)
	if noRestart {
		return 0
	}
	if err := restartIfActive(conf); err != nil {
		return fail(err)
	}
	return 0
}

func validateCandidate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return errs.Wrap(errs.NotFound, err, "cannot read the candidate binary %s", path)
	}
	if !st.Mode().IsRegular() {
		return errs.Newf(errs.Usage, "candidate is not a regular file: %s", path)
	}
	if st.Mode().Perm()&0o111 == 0 {
		return errs.Newf(errs.Usage, "candidate is not executable: %s", path).
			WithHint("run chmod +x %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot read %s", path)
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return errs.Newf(errs.Verify, "candidate is too small to be a Linux binary: %s", path)
	}
	if string(magic[:]) != "\x7fELF" {
		return errs.Newf(errs.Verify, "candidate is not a Linux ELF binary: %s", path).
			WithHint("build it with: make build (Linux x86-64)")
	}
	return nil
}

func binaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version", "--json")
	out, err := cmd.Output()
	if err != nil {
		return "", errs.Newf(errs.Verify, "the candidate cannot report its version: %v", err).
			WithHint("run %s version --json yourself", path)
	}
	var rec struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &rec); err != nil || rec.Version == "" {
		return "", errs.New(errs.Verify, "the candidate did not print a valid version JSON").
			WithHint("maio upgrade expects a maio 3.x binary")
	}
	return rec.Version, nil
}

func runningBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", errs.Wrap(errs.IO, err, "cannot locate the running binary")
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		real = exe
	}
	return real, nil
}

func replaceBinary(src, dst string) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".maio-upgrade-*")
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write to %s", dir).
			WithHint("run with sudo, or install maio to a user-writable directory")
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	in, err := os.Open(src)
	if err != nil {
		_ = tmp.Close()
		return errs.Wrap(errs.IO, err, "cannot read %s", src)
	}
	defer in.Close()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return errs.Wrap(errs.IO, err, "cannot copy the candidate binary")
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return errs.Wrap(errs.Permission, err, "cannot chmod the candidate binary")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errs.Wrap(errs.IO, err, "cannot flush the candidate binary")
	}
	if err := tmp.Close(); err != nil {
		return errs.Wrap(errs.IO, err, "cannot close the candidate binary")
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot replace %s", dst).
			WithHint("run with sudo, or fix permissions on %s", dir)
	}
	return nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func saveUpgradeState(conf *config.Global, state upgradeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := upgradeStatePath(conf) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, upgradeStatePath(conf))
}

func loadUpgradeState(conf *config.Global) (upgradeState, error) {
	var state upgradeState
	data, err := os.ReadFile(upgradeStatePath(conf))
	if err != nil {
		if os.IsNotExist(err) {
			return state, errs.New(errs.NotFound, "no upgrade has been recorded on this machine")
		}
		return state, errs.Wrap(errs.Permission, err, "cannot read %s", upgradeStatePath(conf))
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, errs.Wrap(errs.IO, err, "invalid upgrade state %s", upgradeStatePath(conf))
	}
	return state, nil
}

func restartIfActive(conf *config.Global) error {
	mode, installed := serviceMode(conf, false)
	if !installed {
		fmt.Println("no systemd unit is installed; start the new binary with: maio serve")
		return nil
	}
	st := systemd.Query(mode)
	if !st.Active {
		fmt.Println("the service is not running; the new binary will be used on the next start")
		return nil
	}
	if err := systemd.Control(mode, "restart"); err != nil {
		return err
	}
	fmt.Printf("restarted the %s maio service\n", mode)
	return nil
}

func sanitizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	v = strings.ReplaceAll(v, "/", "-")
	v = strings.ReplaceAll(v, " ", "-")
	if v == "" {
		return "unknown"
	}
	return v
}
