//go:build linux

package hardware

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func samplePlatform(s *Sampler, now time.Time) Metrics {
	m := Metrics{}
	m.CPUPercent = s.cpuPercent()
	m.CPUFreqMHz = cpuFreqMHz()
	m.CPUFreqMaxMHz = cpuMaxMHz()
	m.MemTotal, m.MemUsed, m.MemPercent = memUsage()
	if !s.memFreqOK {
		s.memFreq = detectMemFreq()
		s.memFreqOK = true
	}
	m.MemFreqMHz = s.memFreq
	m.GPUs = gpuMetrics()
	m.RXBps, m.TXBps, m.NetLinkMbps = netRate(s, now)
	return m
}

func (s *Sampler) cpuPercent() float64 {
	total, idle := cpuCounters()
	if total == 0 {
		return 0
	}
	var pct float64
	if s.prevTotal > 0 && total > s.prevTotal {
		dt := float64(total - s.prevTotal)
		di := float64(idle - s.prevIdle)
		if di < 0 {
			di = 0
		}
		pct = 100 * (1 - di/dt)
	} else {
		pct = 100 * (1 - float64(idle)/float64(total))
	}
	s.prevTotal, s.prevIdle = total, idle
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

func cpuCounters() (total, idle uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var vals []uint64
		for _, f := range fields {
			v, _ := strconv.ParseUint(f, 10, 64)
			vals = append(vals, v)
		}
		if len(vals) < 4 {
			return 0, 0
		}
		for _, v := range vals {
			total += v
		}
		idle = vals[3]
		if len(vals) > 4 {
			idle += vals[4]
		}
		return total, idle
	}
	return 0, 0
}

func cpuFreqMHz() float64 {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	var sum float64
	var n int
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu MHz") {
			continue
		}
		if i := strings.Index(line, ":"); i >= 0 {
			v, err := strconv.ParseFloat(strings.TrimSpace(line[i+1:]), 64)
			if err == nil && v > 0 {
				sum += v
				n++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func cpuMaxMHz() float64 {
	paths := []string{
		"/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq",
		"/sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		kHz, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err == nil && kHz > 0 {
			return kHz / 1000
		}
	}
	// Apple/VM guests often have no cpufreq; fall back to the current value.
	return 0
}

func memUsage() (total, used uint64, percent float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	var avail uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v << 10
		case "MemAvailable:":
			avail = v << 10
		}
	}
	if total == 0 {
		return 0, 0, 0
	}
	used = total - avail
	percent = 100 * float64(used) / float64(total)
	return total, used, percent
}

func detectMemFreq() uint64 {
	out, err := runBrief(2*time.Second, "dmidecode", "-t", "memory")
	if err != nil {
		return 0
	}
	var configured, speed uint64
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Configured Memory Speed:") {
			configured = firstNumber(line)
		} else if strings.HasPrefix(line, "Speed:") {
			speed = firstNumber(line)
		}
	}
	if configured > 0 {
		return configured
	}
	return speed
}

func firstNumber(s string) uint64 {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			break
		}
	}
	v, _ := strconv.ParseUint(b.String(), 10, 64)
	return v
}

func gpuMetrics() []GPUMetrics {
	out, err := runBrief(2*time.Second, "nvidia-smi",
		"--query-gpu=index,utilization.gpu,memory.used,memory.total,clocks.mem,pcie.link.gen.current,pcie.link.width.current",
		"--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	var res []GPUMetrics
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 7 {
			continue
		}
		g := GPUMetrics{}
		g.Index = atoi(parts[0])
		g.Utilization = atof(parts[1])
		g.MemUsed = uint64(atof(parts[2])) << 20
		g.MemTotal = uint64(atof(parts[3])) << 20
		if g.MemTotal > 0 {
			g.MemPercent = 100 * float64(g.MemUsed) / float64(g.MemTotal)
		}
		g.MemClockMHz = uint64(atof(parts[4]))
		g.PCIeGen = atoi(parts[5])
		g.PCIeWidth = atoi(parts[6])
		g.PCIeGBs = pcieGBs(g.PCIeGen, g.PCIeWidth)
		res = append(res, g)
	}
	return res
}

func pcieGBs(gen, width int) float64 {
	if gen <= 0 || width <= 0 {
		return 0
	}
	perLane := map[int]float64{1: 0.25, 2: 0.5, 3: 0.985, 4: 1.969, 5: 3.938}[gen]
	return perLane * float64(width)
}

func netRate(s *Sampler, now time.Time) (rx, tx, link uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, 0
	}
	type iface struct{ name string }
	var totalRX, totalTX uint64
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, ":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		if name == "lo" {
			continue
		}
		rxBytes, _ := strconv.ParseUint(fields[1], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[9], 10, 64)
		totalRX += rxBytes
		totalTX += txBytes
		names = append(names, name)
	}
	if !s.prevAt.IsZero() && now.After(s.prevAt) {
		secs := now.Sub(s.prevAt).Seconds()
		if secs > 0 {
			if totalRX >= s.prevRX {
				rx = uint64(float64(totalRX-s.prevRX) / secs)
			}
			if totalTX >= s.prevTX {
				tx = uint64(float64(totalTX-s.prevTX) / secs)
			}
		}
	}
	s.prevAt, s.prevRX, s.prevTX = now, totalRX, totalTX
	for _, name := range names {
		if isVirtualIface(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/sys/class/net", name, "speed"))
		if err != nil {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err == nil && v > link {
			link = v
		}
	}
	if link == 0 {
		for _, name := range names {
			if isVirtualIface(name) {
				continue
			}
			if v := wifiBitrate(name); v > link {
				link = v
			}
		}
	}
	return rx, tx, link
}

func wifiBitrate(name string) uint64 {
	out, err := runBrief(1500*time.Millisecond, "iw", "dev", name, "link")
	if err != nil {
		return 0
	}
	var best float64
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "tx bitrate:") || strings.HasPrefix(line, "rx bitrate:") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				v, err := strconv.ParseFloat(fields[2], 64)
				if err == nil && v > best {
					best = v
				}
			}
		}
	}
	return uint64(best)
}

func isVirtualIface(name string) bool {
	for _, p := range []string{"lo", "docker", "veth", "br-", "virbr", "tun", "tap", "wg", "zt"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func atoi(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func runBrief(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
