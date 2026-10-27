// Package queue provides bounded admission control for inference requests.
// The first max requests run immediately; up to maxQueue more wait in FIFO
// order. Anything beyond that is rejected fast instead of exhausting memory.
package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// Errors returned by Acquire.
var (
	ErrFull     = errors.New("request queue is full")
	ErrTimeout  = errors.New("timed out waiting for an inference slot")
	ErrCanceled = errors.New("request cancelled while waiting for an inference slot")
)

// Limiter is a bounded counting semaphore with a waiting queue.
type Limiter struct {
	max      int
	maxQueue int
	timeout  time.Duration
	sem      chan struct{}
	waiting  atomic.Int64
}

// New creates a limiter. max must be at least 1. maxQueue < 0 is treated as 0;
// timeout <= 0 waits until the request context is cancelled.
func New(max, maxQueue int, timeout time.Duration) *Limiter {
	if max < 1 {
		max = 1
	}
	if maxQueue < 0 {
		maxQueue = 0
	}
	return &Limiter{
		max:      max,
		maxQueue: maxQueue,
		timeout:  timeout,
		sem:      make(chan struct{}, max),
	}
}

// Acquire takes a slot. It returns waited=true when the caller had to queue.
func (l *Limiter) Acquire(ctx context.Context) (waited bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, ErrCanceled
	}
	select {
	case l.sem <- struct{}{}:
		return false, nil
	default:
	}

	pending := l.waiting.Add(1)
	if pending > int64(l.maxQueue) {
		l.waiting.Add(-1)
		return false, ErrFull
	}
	defer l.waiting.Add(-1)

	var timer <-chan time.Time
	if l.timeout > 0 {
		t := time.NewTimer(l.timeout)
		defer t.Stop()
		timer = t.C
	}

	select {
	case l.sem <- struct{}{}:
		return true, nil
	case <-timer:
		return true, ErrTimeout
	case <-ctx.Done():
		return true, ErrCanceled
	}
}

// Release returns a slot.
func (l *Limiter) Release() {
	select {
	case <-l.sem:
	default:
	}
}

// Depth reports how many requests are waiting.
func (l *Limiter) Depth() int { return int(l.waiting.Load()) }

// Capacity reports the active slot count.
func (l *Limiter) Capacity() int { return l.max }

// MaxQueue reports the waiting-room capacity.
func (l *Limiter) MaxQueue() int { return l.maxQueue }

// InFlight reports how many slots are currently held.
func (l *Limiter) InFlight() int { return len(l.sem) }
