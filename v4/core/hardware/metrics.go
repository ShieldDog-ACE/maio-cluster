package hardware

import (
	"sync"
	"time"
)

// GPUMetrics is a live sample for one accelerator.
type GPUMetrics struct {
	Index       int     `json:"index"`
	Utilization float64 `json:"utilization"`
	MemUsed     uint64  `json:"vram_used"`
	MemTotal    uint64  `json:"vram_total"`
	MemPercent  float64 `json:"vram_percent"`
	MemClockMHz uint64  `json:"mem_clock_mhz"`
	PCIeGen     int     `json:"pcie_gen"`
	PCIeWidth   int     `json:"pcie_width"`
	PCIeGBs     float64 `json:"pcie_gbs"`
	PowerW      float64 `json:"power_w"`
	PowerLimitW float64 `json:"power_limit_w"`
	PowerMaxW   float64 `json:"power_max_w"`
	TempC       float64 `json:"temp_c"`
	MemTempC    float64 `json:"mem_temp_c"`
}

// Metrics is a live resource sample sent with each heartbeat.
type Metrics struct {
	SampledAt     time.Time    `json:"sampled_at"`
	CPUPercent    float64      `json:"cpu_percent"`
	CPUFreqMHz    float64      `json:"cpu_freq_mhz"`
	CPUFreqMaxMHz float64      `json:"cpu_freq_max_mhz"`
	MemTotal      uint64       `json:"mem_total"`
	MemUsed       uint64       `json:"mem_used"`
	MemPercent    float64      `json:"mem_percent"`
	MemFreqMHz    uint64       `json:"mem_freq_mhz"`
	CPUTempC      float64      `json:"cpu_temp_c"`
	GPUs          []GPUMetrics `json:"gpus,omitempty"`
	RXBps         uint64       `json:"rx_bps"`
	TXBps         uint64       `json:"tx_bps"`
	NetLinkMbps   uint64       `json:"net_link_mbps"`
	LoadPercent   float64      `json:"load_percent"`
	LoadState     string       `json:"load_state"`
}

// Sampler holds the previous CPU/network counters used to compute deltas.
type Sampler struct {
	Info Info

	mu        sync.Mutex
	prevAt    time.Time
	prevTotal uint64
	prevIdle  uint64
	prevRX    uint64
	prevTX    uint64
	memFreq   uint64
	memFreqOK bool
}

// NewSampler builds a sampler for the detected hardware.
func NewSampler(info Info) *Sampler { return &Sampler{Info: info} }

// Sample returns a live snapshot. The first call returns cumulative CPU usage
// and zero network rates; later calls use deltas.
func (s *Sampler) Sample() Metrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	m := samplePlatform(s, now)
	m.SampledAt = now
	m.LoadPercent, m.LoadState = loadState(m)
	return m
}

func loadState(m Metrics) (float64, string) {
	used, total := uint64(0), uint64(0)
	for _, g := range m.GPUs {
		used += g.MemUsed
		total += g.MemTotal
	}
	vram := 0.0
	if total > 0 {
		vram = 100 * float64(used) / float64(total)
	}
	p := (m.CPUPercent + m.MemPercent + vram) / 3
	if len(m.GPUs) == 0 {
		p = (m.CPUPercent + m.MemPercent) / 2
	}
	switch {
	case p < 50:
		return p, "流畅"
	case p <= 90:
		return p, "平稳"
	default:
		return p, "繁忙"
	}
}
