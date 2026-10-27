package vram

import (
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/internal/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
)

func testInfo() *gguf.Info {
	return &gguf.Info{
		ContextLength:   8192,
		BlockCount:      32,
		HeadCount:       32,
		HeadCountKV:     8,
		EmbeddingLength: 4096,
	}
}

func testHW(freeMiB uint64) *hardware.Info {
	return &hardware.Info{GPUs: []hardware.GPU{{
		Index: 0, Name: "test", MemoryTotal: 16384 * 1024 * 1024, MemoryFree: freeMiB * 1024 * 1024,
	}}}
}

func TestEstimateFits(t *testing.T) {
	b := Estimate(testInfo(), 1<<30, 4096, testHW(8192))
	if !b.KVKnown {
		t.Fatal("KV should be known")
	}
	if !b.Fits() {
		t.Fatalf("should fit: %s", b.Summary())
	}
	if b.KV != 32*8*128*2*2*4096 {
		t.Fatalf("KV = %d", b.KV)
	}
	if b.MaxContext < 4096 {
		t.Fatalf("max context = %d", b.MaxContext)
	}
}

func TestEstimateDoesNotFit(t *testing.T) {
	b := Estimate(testInfo(), 4<<30, 8192, testHW(4096))
	if b.Fits() {
		t.Fatalf("should not fit: %s", b.Summary())
	}
	if b.MaxContext != 0 && b.MaxContext > 8192 {
		t.Fatalf("unexpected max context %d", b.MaxContext)
	}
	if b.Suggestion("--ctx") == "" {
		t.Fatal("missing suggestion")
	}
}

func TestNoGPU(t *testing.T) {
	b := Estimate(testInfo(), 1<<30, 0, &hardware.Info{})
	if !b.Fits() {
		t.Fatal("CPU-only should fit")
	}
	if b.Context != 8192 {
		t.Fatalf("context = %d", b.Context)
	}
}

func TestParallelAndKVType(t *testing.T) {
	one := EstimateWith(testInfo(), 1<<30, Options{Context: 4096, Parallel: 1, KVType: "f16"}, testHW(16384))
	two := EstimateWith(testInfo(), 1<<30, Options{Context: 4096, Parallel: 2, KVType: "f16"}, testHW(16384))
	if two.KV <= one.KV {
		t.Fatalf("parallel KV should grow: %d -> %d", one.KV, two.KV)
	}
	q8 := EstimateWith(testInfo(), 1<<30, Options{Context: 4096, Parallel: 2, KVType: "q8_0"}, testHW(16384))
	if q8.KV >= two.KV {
		t.Fatalf("q8_0 KV should shrink: %d -> %d", two.KV, q8.KV)
	}
	if q8.KVType != "q8_0" || q8.Parallel != 2 {
		t.Fatalf("unexpected budget fields: %+v", q8)
	}
}
