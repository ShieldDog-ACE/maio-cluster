//go:build darwin

package hardware

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func hostname() (string, error) { return os.Hostname() }

func fillOS(info *Info, path string) {
	if v, err := RunOutput("sysctl", "-n", "hw.memsize"); err == nil {
		info.MemTotal, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	}
	if v, err := RunOutput("sysctl", "-n", "hw.memsize_usable"); err == nil {
		info.MemFree, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	}
	if info.MemFree == 0 {
		// vm_stat reports pages; Apple silicon uses 16 KiB pages, Intel 4 KiB.
		if v, err := RunOutput("sh", "-c", `vm_stat | awk '/free|inactive/ {gsub("\.","",$3); s+=$3} END {print s}'`); err == nil {
			pages, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			info.MemFree = pages * 4096
		}
	}
	if v, err := RunOutput("sysctl", "-n", "machdep.cpu.brand_string"); err == nil && v != "" {
		info.CPUModel = v
	}
	if info.CPUModel == "" {
		info.CPUModel = "Apple Silicon"
	}
	info.DiskFree = freeSpace(path)
	info.GPUs = detectMetal(*info)
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

type spDisplay struct {
	SPDisplaysDataType []struct {
		Name string `json:"_name"`
		VRAM string `json:"spdisplays_vram"`
	} `json:"SPDisplaysDataType"`
}

func detectMetal(info Info) []GPU {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "system_profiler", "SPDisplaysDataType", "-json").Output()
	if err != nil {
		return []GPU{{Name: "Apple GPU", Backend: "metal", Unified: true, VRAMTotal: info.MemTotal, VRAMFree: info.MemFree}}
	}
	var parsed spDisplay
	if json.Unmarshal(out, &parsed) != nil {
		return nil
	}
	var gpus []GPU
	for i, d := range parsed.SPDisplaysDataType {
		g := GPU{Index: i, Name: d.Name, Backend: "metal", Unified: true}
		if d.VRAM != "" {
			fields := strings.Fields(d.VRAM)
			if len(fields) > 0 {
				if n, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
					// Apple reports VRAM in MB.
					g.VRAMTotal = n << 20
					g.Unified = false
				}
			}
		}
		if g.VRAMTotal == 0 {
			g.VRAMTotal = info.MemTotal
			g.VRAMFree = info.MemFree
			g.Unified = true
		}
		gpus = append(gpus, g)
	}
	return gpus
}
