package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestExposition(t *testing.T) {
	m := New("3.0.0")
	m.ObserveRequest("generate", 200, 10*time.Millisecond)
	m.ObserveRequest("generate", 500, 2*time.Second)
	m.IncAuthFailure()
	m.IncRateLimited()
	m.IncQueueRejected()
	m.SetQueue(3, 64)
	m.SetModels(1, 2)
	var b strings.Builder
	if err := m.WritePrometheus(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`maio_build_info{version="3.0.0"} 1`,
		`maio_requests_total{endpoint="generate",status="200"} 1`,
		`maio_requests_total{endpoint="generate",status="500"} 1`,
		`maio_request_duration_seconds_bucket{endpoint="generate",le="0.01"} 1`,
		`maio_request_duration_seconds_bucket{endpoint="generate",le="2.5"} 2`,
		`maio_request_duration_seconds_count{endpoint="generate"} 2`,
		`maio_auth_failures_total 1`,
		`maio_rate_limited_total 1`,
		`maio_queue_rejected_total 1`,
		`maio_queue_depth 3`,
		`maio_queue_capacity 64`,
		`maio_loaded_models 1`,
		`maio_resident_models 2`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSnapshot(t *testing.T) {
	m := New("3.0.0")
	m.AddActive(2)
	m.AddTokens(10, 20)
	s := m.Snapshot()
	if s.ActiveRequests != 2 || s.TokensIn != 10 || s.TokensOut != 20 {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
	if s.RSSBytes == 0 {
		t.Fatal("RSS should be readable on Linux")
	}
}
