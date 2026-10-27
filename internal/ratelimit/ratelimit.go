// Package ratelimit implements a per-client token bucket with lazy expiry. It
// is used by the HTTP service to protect the engine from request floods while
// keeping the common case (a single client at a modest rate) allocation-free.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a per-key token bucket. A nil or zero-rate limiter allows every
// request. Keys are usually client IP addresses.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64 // bucket capacity

	mu      sync.Mutex
	buckets map[string]*bucket
	sweep   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New creates a limiter for ratePerMinute requests per minute with the given
// burst. A non-positive rate disables limiting.
func New(ratePerMinute, burst int) *Limiter {
	if ratePerMinute <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		rate:    float64(ratePerMinute) / 60.0,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
		sweep:   time.Now(),
	}
}

// Allow consumes one token for key. The second result, when not allowed, is a
// conservative Retry-After delay.
func (l *Limiter) Allow(key string, now time.Time) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if key == "" {
		key = "unknown"
	}
	if now.Sub(l.sweep) > time.Minute {
		l.prune(now)
		l.sweep = now
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.rate
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
			b.last = now
		}
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing / l.rate * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// prune drops idle buckets. Called with mu held at most once a minute.
func (l *Limiter) prune(now time.Time) {
	cutoff := now.Add(-10 * time.Minute)
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

// Len reports the number of tracked clients (for tests and diagnostics).
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
