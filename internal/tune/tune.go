// Package tune applies process-level limits that matter for a long-running
// server: a Go soft memory limit, a bounded scheduler width, and a generous
// file-descriptor limit. Failures are reported but never fatal: a service with
// default limits is still better than a service that refuses to start.
package tune

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"syscall"
)

// Info describes the limits that were actually applied.
type Info struct {
	MemoryLimitMB int
	MaxProcs      int
	NoFileSoft    uint64
	NoFileHard    uint64
	NoFileRaised  bool
}

// Apply sets the Go memory limit (0 = leave alone), GOMAXPROCS (0 = leave
// alone) and tries to raise RLIMIT_NOFILE to maxFiles.
func Apply(memoryLimitMB, maxProcs, maxFiles int) (Info, error) {
	info := Info{MemoryLimitMB: memoryLimitMB, MaxProcs: maxProcs}
	if memoryLimitMB > 0 {
		debug.SetMemoryLimit(int64(memoryLimitMB) << 20)
	}
	if maxProcs > 0 {
		runtime.GOMAXPROCS(maxProcs)
		info.MaxProcs = runtime.GOMAXPROCS(0)
	}
	soft, hard, err := RaiseNoFile(maxFiles)
	info.NoFileSoft, info.NoFileHard = soft, hard
	if err != nil {
		return info, fmt.Errorf("raise RLIMIT_NOFILE: %w", err)
	}
	if maxFiles > 0 && soft >= uint64(maxFiles) {
		info.NoFileRaised = true
	}
	return info, nil
}

// RaiseNoFile raises the soft RLIMIT_NOFILE toward target without lowering a
// higher existing limit. It returns the resulting soft and hard limits.
func RaiseNoFile(target int) (soft, hard uint64, err error) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, 0, err
	}
	soft, hard = lim.Cur, lim.Max
	if target <= 0 {
		return soft, hard, nil
	}
	want := uint64(target)
	if hard != ^uint64(0) && want > hard {
		want = hard
	}
	if soft >= want {
		return soft, hard, nil
	}
	lim.Cur = want
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return soft, hard, err
	}
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return soft, hard, err
	}
	return lim.Cur, lim.Max, nil
}
