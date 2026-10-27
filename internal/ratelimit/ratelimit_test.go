package ratelimit

import (
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	l := New(2, 2) // 2 per minute, burst 2
	now := time.Unix(1000, 0)
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("1.2.3.4", now); !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	ok, retry := l.Allow("1.2.3.4", now)
	if ok {
		t.Fatal("third request should be limited")
	}
	if retry < 29*time.Second || retry > 31*time.Second {
		t.Fatalf("retry = %s, want about 30s", retry)
	}
	// Another client has its own bucket.
	if ok, _ := l.Allow("5.6.7.8", now); !ok {
		t.Fatal("independent client should be allowed")
	}
	// One token refills after 30 seconds.
	if ok, _ := l.Allow("1.2.3.4", now.Add(30*time.Second)); !ok {
		t.Fatal("token should refill after 30s")
	}
}

func TestDisabled(t *testing.T) {
	l := New(0, 10)
	if l != nil {
		t.Fatal("zero rate should return a nil limiter")
	}
	if ok, _ := l.Allow("x", time.Now()); !ok {
		t.Fatal("nil limiter should allow")
	}
}
