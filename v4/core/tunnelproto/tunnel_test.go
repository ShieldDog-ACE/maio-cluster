package tunnelproto

import (
	"bytes"
	"sync"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	frames := []Frame{
		{Type: Open, Stream: 1},
		{Type: Data, Stream: 1, Data: []byte("hello")},
		{Type: Data, Stream: 2, Data: bytes.Repeat([]byte("x"), 64<<10)},
		{Type: Close, Stream: 1},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, &mu, f); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.Type != want.Type || got.Stream != want.Stream || !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("frame %d mismatch: %+v", i, got)
		}
	}
}

func TestRejectsBadFrame(t *testing.T) {
	// Unknown frame type.
	raw := []byte{99, 0, 0, 0, 1, 0, 0, 0, 0}
	if _, err := ReadFrame(bytes.NewReader(raw)); err == nil {
		t.Fatal("unknown type accepted")
	}
	// Oversized payload.
	raw = []byte{Data, 0, 0, 0, 1, 0x7f, 0xff, 0xff, 0xff}
	if _, err := ReadFrame(bytes.NewReader(raw)); err == nil {
		t.Fatal("oversized payload accepted")
	}
}
