// Package metrics is a small, dependency-free metrics registry. It exposes
// Prometheus text format and a JSON snapshot for the maio service. The
// registry is intentionally bounded: endpoints are fixed route names, not raw
// URLs, so label cardinality cannot grow with request paths.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var latencyBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600,
}

type requestKey struct {
	endpoint string
	status   int
}

type histogram struct {
	counts []uint64
	sum    float64
	total  uint64
}

func newHistogram() *histogram {
	return &histogram{counts: make([]uint64, len(latencyBuckets))}
}

func (h *histogram) observe(v float64) {
	h.sum += v
	h.total++
	for i, b := range latencyBuckets {
		if v <= b {
			h.counts[i]++
		}
	}
}

// Metrics is safe for concurrent use.
type Metrics struct {
	start   time.Time
	version string

	mu       sync.Mutex
	requests map[requestKey]uint64
	latency  map[string]*histogram

	active         atomic.Int64
	queueDepth     atomic.Int64
	queueCapacity  atomic.Int64
	loadedModels   atomic.Int64
	residentModels atomic.Int64

	authFailures      atomic.Uint64
	rateLimited       atomic.Uint64
	queueRejected     atomic.Uint64
	queueTimeouts     atomic.Uint64
	engineRestarts    atomic.Uint64
	restartFailures   atomic.Uint64
	panics            atomic.Uint64
	tokensIn          atomic.Uint64
	tokensOut         atomic.Uint64
	requestsCompleted atomic.Uint64
	bytesStreamed     atomic.Uint64
}

// New creates a registry.
func New(version string) *Metrics {
	return &Metrics{
		start:    time.Now(),
		version:  version,
		requests: make(map[requestKey]uint64),
		latency:  make(map[string]*histogram),
	}
}

// SetVersion updates the build info value.
func (m *Metrics) SetVersion(v string) {
	m.mu.Lock()
	m.version = v
	m.mu.Unlock()
}

// ObserveRequest records one completed HTTP request.
func (m *Metrics) ObserveRequest(endpoint string, status int, d time.Duration) {
	if endpoint == "" {
		endpoint = "other"
	}
	seconds := d.Seconds()
	m.mu.Lock()
	m.requests[requestKey{endpoint, status}]++
	h := m.latency[endpoint]
	if h == nil {
		h = newHistogram()
		m.latency[endpoint] = h
	}
	h.observe(seconds)
	m.mu.Unlock()
	m.requestsCompleted.Add(1)
}

// AddActive changes the active request gauge.
func (m *Metrics) AddActive(delta int64) { m.active.Add(delta) }
func (m *Metrics) SetQueue(depth, capacity int) {
	m.queueDepth.Store(int64(depth))
	m.queueCapacity.Store(int64(capacity))
}
func (m *Metrics) SetModels(loaded, resident int) {
	m.loadedModels.Store(int64(loaded))
	m.residentModels.Store(int64(resident))
}
func (m *Metrics) IncAuthFailure()           { m.authFailures.Add(1) }
func (m *Metrics) IncRateLimited()           { m.rateLimited.Add(1) }
func (m *Metrics) IncQueueRejected()         { m.queueRejected.Add(1) }
func (m *Metrics) IncQueueTimeout()          { m.queueTimeouts.Add(1) }
func (m *Metrics) IncPanic()                 { m.panics.Add(1) }
func (m *Metrics) AddTokens(in, out uint64)  { m.tokensIn.Add(in); m.tokensOut.Add(out) }
func (m *Metrics) AddStreamedBytes(n uint64) { m.bytesStreamed.Add(n) }
func (m *Metrics) IncEngineRestart(ok bool) {
	if ok {
		m.engineRestarts.Add(1)
	} else {
		m.restartFailures.Add(1)
	}
}

// Snapshot is the JSON view returned by GET /api/stats.
type Snapshot struct {
	Version           string            `json:"version"`
	UptimeSeconds     int64             `json:"uptime_seconds"`
	PID               int               `json:"pid"`
	GoVersion         string            `json:"go_version"`
	Goroutines        int               `json:"goroutines"`
	RSSBytes          uint64            `json:"rss_bytes"`
	VirtualBytes      uint64            `json:"virtual_bytes"`
	OpenFDs           int               `json:"open_fds"`
	MemoryLimitBytes  int64             `json:"memory_limit_bytes"`
	HeapAllocBytes    uint64            `json:"heap_alloc_bytes"`
	HeapSysBytes      uint64            `json:"heap_sys_bytes"`
	GCCount           uint32            `json:"gc_count"`
	ActiveRequests    int64             `json:"active_requests"`
	QueueDepth        int64             `json:"queue_depth"`
	QueueCapacity     int64             `json:"queue_capacity"`
	LoadedModels      int64             `json:"loaded_models"`
	ResidentModels    int64             `json:"resident_models"`
	RequestsCompleted uint64            `json:"requests_completed"`
	AuthFailures      uint64            `json:"auth_failures"`
	RateLimited       uint64            `json:"rate_limited"`
	QueueRejected     uint64            `json:"queue_rejected"`
	QueueTimeouts     uint64            `json:"queue_timeouts"`
	EngineRestarts    uint64            `json:"engine_restarts"`
	RestartFailures   uint64            `json:"engine_restart_failures"`
	Panics            uint64            `json:"panics"`
	TokensIn          uint64            `json:"tokens_in"`
	TokensOut         uint64            `json:"tokens_out"`
	BytesStreamed     uint64            `json:"bytes_streamed"`
	RequestsByRoute   map[string]uint64 `json:"requests_by_route"`
}

// Snapshot reads a consistent-enough view of the registry.
func (m *Metrics) Snapshot() Snapshot {
	rss, vsz := ProcessMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	m.mu.Lock()
	routes := make(map[string]uint64)
	for k, v := range m.requests {
		routes[k.endpoint] += v
	}
	version := m.version
	m.mu.Unlock()

	return Snapshot{
		Version:           version,
		UptimeSeconds:     int64(time.Since(m.start).Seconds()),
		PID:               os.Getpid(),
		GoVersion:         runtime.Version(),
		Goroutines:        runtime.NumGoroutine(),
		RSSBytes:          rss,
		VirtualBytes:      vsz,
		OpenFDs:           OpenFDs(),
		MemoryLimitBytes:  debugMemoryLimit(),
		HeapAllocBytes:    ms.HeapAlloc,
		HeapSysBytes:      ms.HeapSys,
		GCCount:           ms.NumGC,
		ActiveRequests:    m.active.Load(),
		QueueDepth:        m.queueDepth.Load(),
		QueueCapacity:     m.queueCapacity.Load(),
		LoadedModels:      m.loadedModels.Load(),
		ResidentModels:    m.residentModels.Load(),
		RequestsCompleted: m.requestsCompleted.Load(),
		AuthFailures:      m.authFailures.Load(),
		RateLimited:       m.rateLimited.Load(),
		QueueRejected:     m.queueRejected.Load(),
		QueueTimeouts:     m.queueTimeouts.Load(),
		EngineRestarts:    m.engineRestarts.Load(),
		RestartFailures:   m.restartFailures.Load(),
		Panics:            m.panics.Load(),
		TokensIn:          m.tokensIn.Load(),
		TokensOut:         m.tokensOut.Load(),
		BytesStreamed:     m.bytesStreamed.Load(),
		RequestsByRoute:   routes,
	}
}

// debugMemoryLimit mirrors runtime/debug.SetMemoryLimit without importing an
// extra package into the hot path.
func debugMemoryLimit() int64 {
	// A negative argument queries the limit without changing it.
	return debug.SetMemoryLimit(-1)
}

// Handler serves the Prometheus text exposition.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = m.WritePrometheus(w)
	})
}

// WritePrometheus writes the exposition text.
func (m *Metrics) WritePrometheus(w io.Writer) error {
	s := m.Snapshot()
	m.mu.Lock()
	requests := make([]requestKey, 0, len(m.requests))
	for k := range m.requests {
		requests = append(requests, k)
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].endpoint != requests[j].endpoint {
			return requests[i].endpoint < requests[j].endpoint
		}
		return requests[i].status < requests[j].status
	})
	endpoints := make([]string, 0, len(m.latency))
	for k := range m.latency {
		endpoints = append(endpoints, k)
	}
	sort.Strings(endpoints)
	latency := make(map[string]histogram, len(m.latency))
	for k, h := range m.latency {
		cp := *h
		cp.counts = append([]uint64(nil), h.counts...)
		latency[k] = cp
	}
	m.mu.Unlock()

	var b strings.Builder
	writeHelp(&b, "maio_build_info", "Build information for the maio service.", "gauge")
	fmt.Fprintf(&b, "maio_build_info{version=%q} 1\n", s.Version)

	writeHelp(&b, "maio_uptime_seconds", "Seconds since the service started.", "gauge")
	fmt.Fprintf(&b, "maio_uptime_seconds %d\n", s.UptimeSeconds)

	writeHelp(&b, "maio_requests_total", "Completed HTTP requests by route and status.", "counter")
	for _, k := range requests {
		fmt.Fprintf(&b, "maio_requests_total{endpoint=%q,status=%q} %d\n", k.endpoint, strconv.Itoa(k.status), m.requests[k])
	}

	writeHelp(&b, "maio_request_duration_seconds", "HTTP request latency by route.", "histogram")
	for _, ep := range endpoints {
		h := latency[ep]
		for i, bound := range latencyBuckets {
			fmt.Fprintf(&b, "maio_request_duration_seconds_bucket{endpoint=%q,le=%q} %d\n", ep, formatFloat(bound), h.counts[i])
		}
		fmt.Fprintf(&b, "maio_request_duration_seconds_bucket{endpoint=%q,le=\"+Inf\"} %d\n", ep, h.total)
		fmt.Fprintf(&b, "maio_request_duration_seconds_sum{endpoint=%q} %s\n", ep, formatFloat(h.sum))
		fmt.Fprintf(&b, "maio_request_duration_seconds_count{endpoint=%q} %d\n", ep, h.total)
	}

	gauges := []struct {
		name string
		help string
		val  int64
	}{
		{"maio_active_requests", "Requests currently being served.", s.ActiveRequests},
		{"maio_queue_depth", "Requests waiting for an inference slot.", s.QueueDepth},
		{"maio_queue_capacity", "Maximum number of waiting requests.", s.QueueCapacity},
		{"maio_loaded_models", "Models currently loaded by this service.", s.LoadedModels},
		{"maio_resident_models", "Models pinned to stay loaded.", s.ResidentModels},
	}
	for _, g := range gauges {
		writeHelp(&b, g.name, g.help, "gauge")
		fmt.Fprintf(&b, "%s %d\n", g.name, g.val)
	}

	counters := []struct {
		name string
		help string
		val  uint64
	}{
		{"maio_auth_failures_total", "Requests rejected because of a missing or invalid API key.", s.AuthFailures},
		{"maio_rate_limited_total", "Requests rejected by the per-client rate limiter.", s.RateLimited},
		{"maio_queue_rejected_total", "Requests rejected because the queue was full.", s.QueueRejected},
		{"maio_queue_timeouts_total", "Requests that timed out waiting for an inference slot.", s.QueueTimeouts},
		{"maio_engine_restarts_total", "Successful engine restarts by the supervisor.", s.EngineRestarts},
		{"maio_engine_restart_failures_total", "Failed engine restarts by the supervisor.", s.RestartFailures},
		{"maio_panics_total", "Recovered handler panics.", s.Panics},
		{"maio_tokens_total_in", "Prompt tokens reported by the engine.", s.TokensIn},
		{"maio_tokens_total_out", "Generated tokens reported by the engine.", s.TokensOut},
		{"maio_streamed_bytes_total", "Bytes written to streaming clients.", s.BytesStreamed},
	}
	for _, c := range counters {
		writeHelp(&b, c.name, c.help, "counter")
		fmt.Fprintf(&b, "%s %d\n", c.name, c.val)
	}

	writeHelp(&b, "maio_process_resident_memory_bytes", "Resident set size of the maio control plane.", "gauge")
	fmt.Fprintf(&b, "maio_process_resident_memory_bytes %d\n", s.RSSBytes)
	writeHelp(&b, "maio_process_virtual_memory_bytes", "Virtual size of the maio control plane.", "gauge")
	fmt.Fprintf(&b, "maio_process_virtual_memory_bytes %d\n", s.VirtualBytes)
	writeHelp(&b, "maio_process_open_fds", "Open file descriptors.", "gauge")
	fmt.Fprintf(&b, "maio_process_open_fds %d\n", s.OpenFDs)
	writeHelp(&b, "maio_process_memory_limit_bytes", "Go runtime soft memory limit (0 = unlimited).", "gauge")
	fmt.Fprintf(&b, "maio_process_memory_limit_bytes %d\n", s.MemoryLimitBytes)
	writeHelp(&b, "maio_go_goroutines", "Go goroutines.", "gauge")
	fmt.Fprintf(&b, "maio_go_goroutines %d\n", s.Goroutines)
	writeHelp(&b, "maio_go_heap_alloc_bytes", "Allocated Go heap bytes.", "gauge")
	fmt.Fprintf(&b, "maio_go_heap_alloc_bytes %d\n", s.HeapAllocBytes)
	writeHelp(&b, "maio_go_heap_sys_bytes", "Go heap system bytes.", "gauge")
	fmt.Fprintf(&b, "maio_go_heap_sys_bytes %d\n", s.HeapSysBytes)

	_, err := io.WriteString(w, b.String())
	return err
}

func writeHelp(w io.Writer, name, help, typ string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ProcessMemory returns RSS and virtual size in bytes on Linux.
func ProcessMemory() (rss, vsz uint64) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.Sys, ms.Sys
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, 0
	}
	vpages, _ := strconv.ParseUint(fields[0], 10, 64)
	rpages, _ := strconv.ParseUint(fields[1], 10, 64)
	page := uint64(os.Getpagesize())
	return rpages * page, vpages * page
}

// OpenFDs counts open file descriptors on Linux.
func OpenFDs() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(ents)
}
