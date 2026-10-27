//go:build windows

package inference

import (
	"os/exec"
	"time"
)

func setGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = time.Second
}
