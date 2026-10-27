// Package hardware reports the compute capacity each maio node contributes.
//
// Detection is read-only and avoids third-party dependencies. Missing vendor
// tools are warnings, never fatal: a CPU-only node is still a valid worker.
package hardware

import (
	"context"
	"encoding/csv"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// GPU is one accelerator visible to llama.cpp.
type GPU struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	Backend   string `json:"backend"` // cuda, metal, rocm, vulkan, cpu
	VRAMTotal uint64 `json:"vram_total"`
	VRAMFree  uint64 `json:"vram_free"`
	Unified   bool   `json:"unified"`
	Driver    string `json:"driver,omitempty"`
	Compute   string `json:"compute,omitempty"`
}

// Info is a hardware snapshot.
type Info struct {
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Hostname     string   `json:"hostname"`
	CPUModel     string   `json:"cpu_model"`
	CPUCores     int      `json:"cpu_cores"`
	MemTotal     uint64   `json:"mem_total"`
	MemFree      uint64   `json:"mem_free"`
	SwapTotal    uint64   `json:"swap_total"`
	SwapFree     uint64   `json:"swap_free"`
	DiskPath     string   `json:"disk_path"`
	DiskFree     uint64   `json:"disk_free"`
	GPUs         []GPU    `json:"gpus"`
	Unified      bool     `json:"unified_memory"`
	Warnings     []string `json:"warnings,omitempty"`
	NvidiaSMI    string   `json:"nvidia_smi,omitempty"`
	RPCBin       string   `json:"rpc_bin,omitempty"`
	LlamaBin     string   `json:"llama_bin,omitempty"`
	ArchDetected string   `json:"-"`
}

// Detect collects a snapshot. path is used for the disk check.
func Detect(path string) Info {
	host, _ := osHostname()
	info := Info{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Hostname: host,
		DiskPath: path,
	}
	info.CPUCores = runtime.NumCPU()
	fillOS(&info, path)
	info.GPUs, info.NvidiaSMI, info.Warnings = detectGPUs(info.Warnings)
	for i := range info.GPUs {
		if info.GPUs[i].Backend == "metal" && info.GPUs[i].Unified {
			info.Unified = true
		}
	}
	return info
}

// GPUVRAM returns total and free VRAM across all GPUs.
func (i Info) GPUVRAM() (total, free uint64) {
	for _, g := range i.GPUs {
		total += g.VRAMTotal
		free += g.VRAMFree
	}
	return
}

// HasGPU reports whether an accelerator is usable.
func (i Info) HasGPU() bool { return len(i.GPUs) > 0 }

// CapacityBytes estimates the memory a maio shard may use on this node.
//
// GPUs are preferred because llama.cpp offloads layers to them. When no GPU is
// present we reserve 40% of free RAM for the OS and the frontend process.
func (i Info) CapacityBytes() uint64 {
	if total, free := i.GPUVRAM(); total > 0 {
		use := free
		if use == 0 {
			use = total * 8 / 10
		}
		return use * 92 / 100
	}
	return i.MemFree * 60 / 100
}

// DeviceSummary is used in CLI output and placement explanations.
func (i Info) DeviceSummary() string {
	if len(i.GPUs) == 0 {
		return "cpu"
	}
	var parts []string
	for _, g := range i.GPUs {
		parts = append(parts, g.Backend+":"+g.Name)
	}
	return strings.Join(parts, ", ")
}

func detectGPUs(warnings []string) ([]GPU, string, []string) {
	smi, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, "", warnings
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, smi,
		"--query-gpu=index,name,memory.total,memory.free,driver_version,compute_cap",
		"--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return nil, smi, append(warnings, "nvidia-smi failed: "+err.Error())
	}
	r := csv.NewReader(strings.NewReader(string(out)))
	r.TrimLeadingSpace = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, smi, append(warnings, "cannot parse nvidia-smi output")
	}
	var gpus []GPU
	for _, rec := range records {
		if len(rec) < 4 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(rec[0]))
		g := GPU{
			Index:   idx,
			Name:    strings.TrimSpace(rec[1]),
			Backend: "cuda",
			Driver:  strings.TrimSpace(rec[4%len(rec)]),
		}
		g.VRAMTotal = mib(rec[2])
		g.VRAMFree = mib(rec[3])
		if len(rec) > 5 {
			g.Compute = strings.TrimSpace(rec[5])
		}
		gpus = append(gpus, g)
	}
	return gpus, smi, warnings
}

func mib(s string) uint64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "MiB"))
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n << 20
}

func osHostname() (string, error) { return hostname() }

// RunOutput runs a short command and returns trimmed stdout.
func RunOutput(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return strings.TrimSpace(string(out)), err
}
