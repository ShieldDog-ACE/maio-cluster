//go:build linux

package hardware

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func hostname() (string, error) { return os.Hostname() }

func fillOS(info *Info, path string) {
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") {
				if i := strings.Index(line, ":"); i > 0 {
					info.CPUModel = strings.TrimSpace(line[i+1:])
					break
				}
			}
		}
	}
	if info.CPUModel == "" {
		info.CPUModel = "unknown"
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				info.MemTotal = v << 10
			case "MemAvailable:":
				info.MemFree = v << 10
			case "SwapTotal:":
				info.SwapTotal = v << 10
			case "SwapFree:":
				info.SwapFree = v << 10
			}
		}
	}
	info.DiskFree = freeSpace(path)
}

func freeSpace(path string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}

// FreeSpace returns free bytes available at path.
func FreeSpace(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
