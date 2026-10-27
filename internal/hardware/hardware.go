// Package hardware detects CPU, memory, disk and NVIDIA GPU capacity.
//
// Detection is read-only and uses only standard Linux interfaces plus
// nvidia-smi when it is available. Missing tools are reported as warnings, not
// failures, so maio can still run on CPU-only machines.
package hardware

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// GPU describes one NVIDIA device.
type GPU struct {
	Index       int
	Name        string
	MemoryTotal uint64
	MemoryUsed  uint64
	MemoryFree  uint64
	Driver      string
	ComputeCap  string
}

// Info is a snapshot of the machine.
type Info struct {
	CPUModel     string
	CPUCores     int
	MemTotal     uint64
	MemAvailable uint64
	SwapTotal    uint64
	SwapFree     uint64
	DiskPath     string
	DiskTotal    uint64
	DiskFree     uint64
	GPUs         []GPU
	Warnings     []string
	NvidiaSMI    string
}

// Detect collects hardware information. path is used for the disk check (for
// example models_root); a missing path falls back to its nearest existing
// parent and finally to "/".
func Detect(path string) *Info {
	info := &Info{
		CPUCores: runtime.NumCPU(),
		DiskPath: path,
	}
	info.CPUModel = cpuModel()
	if total, avail, st, sf, err := readMeminfo("/proc/meminfo"); err == nil {
		info.MemTotal, info.MemAvailable, info.SwapTotal, info.SwapFree = total, avail, st, sf
	} else {
		info.Warnings = append(info.Warnings, "cannot read /proc/meminfo: "+err.Error())
	}
	if total, free, usedPath, err := DiskUsage(path); err == nil {
		info.DiskTotal, info.DiskFree, info.DiskPath = total, free, usedPath
	} else {
		info.Warnings = append(info.Warnings, "cannot read disk usage: "+err.Error())
	}
	gpus, smi, warnings := detectGPUs()
	info.GPUs = gpus
	info.NvidiaSMI = smi
	info.Warnings = append(info.Warnings, warnings...)
	return info
}

// HasGPU reports whether at least one NVIDIA GPU was detected.
func (i *Info) HasGPU() bool { return i != nil && len(i.GPUs) > 0 }

// VRAM returns total and free VRAM across all detected GPUs.
func (i *Info) VRAM() (total, free uint64) {
	if i == nil {
		return 0, 0
	}
	for _, g := range i.GPUs {
		total += g.MemoryTotal
		free += g.MemoryFree
	}
	return total, free
}

// DiskUsage returns total and free bytes for the filesystem containing path.
func DiskUsage(path string) (total, free uint64, usedPath string, err error) {
	if path == "" {
		path = "/"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	probe := abs
	for {
		if _, statErr := os.Stat(probe); statErr == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			probe = "/"
			break
		}
		probe = parent
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(probe, &st); err != nil {
		return 0, 0, probe, err
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bavail * bsize, probe, nil
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			if i := strings.IndexByte(line, ':'); i >= 0 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return "unknown"
}

func readMeminfo(path string) (total, available, swapTotal, swapFree uint64, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	vals := make(map[string]uint64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		vals[key] = n * 1024 // /proc/meminfo is in kB
	}
	return vals["MemTotal"], vals["MemAvailable"], vals["SwapTotal"], vals["SwapFree"], nil
}

func detectGPUs() ([]GPU, string, []string) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, "", []string{"nvidia-smi not found; NVIDIA GPU acceleration is unavailable"}
	}
	full := "index,name,memory.total,memory.used,memory.free,driver_version,compute_cap"
	out, err := runSMI(bin, full)
	if err != nil {
		// Older drivers do not expose compute_cap.
		out, err = runSMI(bin, "index,name,memory.total,memory.used,memory.free,driver_version")
		if err != nil {
			return nil, bin, []string{"nvidia-smi failed: " + strings.TrimSpace(err.Error())}
		}
	}
	gpus, perr := ParseNvidiaSMI(out)
	if perr != nil {
		return nil, bin, []string{"cannot parse nvidia-smi output: " + perr.Error()}
	}
	return gpus, bin, nil
}

func runSMI(bin, fields string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--query-gpu="+fields, "--format=csv,noheader,nounits")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("nvidia-smi timed out")
		}
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ParseNvidiaSMI parses noheader,nounits CSV output. MiB values are converted
// to bytes. It is exported for tests.
func ParseNvidiaSMI(out string) ([]GPU, error) {
	var result []GPU
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 6 {
			return nil, fmt.Errorf("expected 6 or 7 CSV fields, got %d: %q", len(parts), line)
		}
		idx, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid GPU index %q", parts[0])
		}
		miB := func(s string) (uint64, error) {
			s = strings.TrimSpace(s)
			if s == "" || strings.EqualFold(s, "[N/A]") || strings.EqualFold(s, "N/A") {
				return 0, nil
			}
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid memory value %q", s)
			}
			return n * 1024 * 1024, nil
		}
		total, err := miB(parts[2])
		if err != nil {
			return nil, err
		}
		used, err := miB(parts[3])
		if err != nil {
			return nil, err
		}
		free, err := miB(parts[4])
		if err != nil {
			return nil, err
		}
		g := GPU{
			Index:       idx,
			Name:        strings.TrimSpace(parts[1]),
			MemoryTotal: total,
			MemoryUsed:  used,
			MemoryFree:  free,
			Driver:      strings.TrimSpace(parts[5]),
		}
		if len(parts) >= 7 {
			g.ComputeCap = strings.TrimSpace(parts[6])
		}
		result = append(result, g)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no GPUs reported")
	}
	return result, nil
}

// HumanBytes formats a byte count with binary units.
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
