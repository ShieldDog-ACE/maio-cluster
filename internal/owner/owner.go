// Package owner transfers file ownership to the service account when a
// root-run CLI creates state that a non-root systemd service must later use.
package owner

import (
	"os"
	"os/user"
	"strconv"
)

// Chown changes path ownership to runAs when the current process is root and
// runAs is configured. A missing user or non-root process is a no-op.
func Chown(path, runAs string) error {
	if runAs == "" || os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup(runAs)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	return os.Chown(path, uid, gid)
}

// ChownMode changes ownership and then applies mode.
func ChownMode(path, runAs string, mode os.FileMode) {
	if runAs == "" || os.Geteuid() != 0 {
		return
	}
	if Chown(path, runAs) == nil {
		_ = os.Chmod(path, mode)
	}
}
