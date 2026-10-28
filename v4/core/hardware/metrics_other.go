//go:build !linux

package hardware

import "time"

// samplePlatform provides a conservative fallback on non-Linux hosts: memory
// usage from the last Detect snapshot, no CPU percentage/frequency and no
// network throughput. The UI treats zero/unknown as "—" rather than inventing
// numbers.
func samplePlatform(s *Sampler, now time.Time) Metrics {
	m := Metrics{}
	if s.Info.MemTotal > 0 {
		m.MemTotal = s.Info.MemTotal
		if s.Info.MemFree < s.Info.MemTotal {
			m.MemUsed = s.Info.MemTotal - s.Info.MemFree
			m.MemPercent = 100 * float64(m.MemUsed) / float64(m.MemTotal)
		}
	}
	return m
}
