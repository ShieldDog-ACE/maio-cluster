package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBoundedQueue(t *testing.T) {
	q := New(2, 1, 0)
	if _, err := q.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		waited, err := q.Acquire(context.Background())
		if err == nil && !waited {
			err = errors.New("expected a queued acquire")
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for q.Depth() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if q.Depth() != 1 {
		t.Fatalf("depth = %d, want 1", q.Depth())
	}
	if _, err := q.Acquire(context.Background()); !errors.Is(err, ErrFull) {
		t.Fatalf("expected ErrFull, got %v", err)
	}
	q.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued acquire did not unblock")
	}
	q.Release()
	q.Release()
}

func TestQueueTimeoutAndCancel(t *testing.T) {
	q := New(1, 4, 30*time.Millisecond)
	if _, err := q.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := q.Acquire(context.Background()); !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("timeout fired too early")
	}
	q.Release()

	q2 := New(1, 4, 0)
	if _, err := q2.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := q2.Acquire(ctx); !errors.Is(err, ErrCanceled) {
		t.Fatalf("expected ErrCanceled, got %v", err)
	}
}
