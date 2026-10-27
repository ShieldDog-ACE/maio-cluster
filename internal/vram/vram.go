// Package vram estimates whether a model fits in the available GPU memory
// before llama-server is started, so maio refuses early with an actionable
// context suggestion instead of running into an OOM.
package vram

import (
	"fmt"

	"github.com/ShieldDog-ACE/maio-cluster/internal/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
)

const (
	kvBytesPerElement = 2 // llama.cpp default KV cache type is f16
	minOverhead       = 1 << 30
	maxContext        = 1048576
	minContext        = 2048
)

// Budget is one VRAM estimate.
type Budget struct {
	HasGPU     bool
	Weights    uint64
	KV         uint64
	Overhead   uint64
	Total      uint64
	Free       uint64
	VRAMTotal  uint64
	Context    uint32
	MaxContext uint32
	KVKnown    bool
	KVPerToken uint64
	Shortfall  uint64
	GPUs       []hardware.GPU
}

// Fits reports whether the estimate fits in free VRAM. CPU-only systems always
// fit because the check does not apply.
func (b *Budget) Fits() bool { return !b.HasGPU || b.Total <= b.Free }

// Estimate computes the budget for a model. ctx <= 0 means the model's own
// context length; when that is unknown, 4096 is assumed.
func Estimate(info *gguf.Info, fileSize int64, ctx int, hw *hardware.Info) *Budget {
	b := &Budget{}
	if hw != nil {
		b.HasGPU = hw.HasGPU()
		b.GPUs = hw.GPUs
		b.VRAMTotal, b.Free = hw.VRAM()
	}
	if fileSize > 0 {
		b.Weights = uint64(fileSize)
	}
	if ctx <= 0 {
		if info != nil && info.ContextLength > 0 {
			ctx = int(info.ContextLength)
		} else {
			ctx = 4096
		}
	}
	b.Context = uint32(ctx)
	b.Overhead = b.Weights / 10
	if b.Overhead < minOverhead {
		b.Overhead = minOverhead
	}
	b.Overhead += uint64(len(b.GPUs)) * (256 << 20)

	if info != nil && info.BlockCount > 0 && info.HeadCountKV > 0 &&
		info.EmbeddingLength > 0 && info.HeadCount > 0 {
		headDim := uint64(info.EmbeddingLength) / uint64(info.HeadCount)
		if headDim == 0 {
			headDim = 128
		}
		b.KVPerToken = uint64(info.BlockCount) * uint64(info.HeadCountKV) * headDim * 2 * kvBytesPerElement
		b.KV = b.KVPerToken * uint64(ctx)
		b.KVKnown = true
	}
	b.Total = b.Weights + b.KV + b.Overhead
	if b.HasGPU && b.Total > b.Free {
		b.Shortfall = b.Total - b.Free
	}
	b.MaxContext = b.maxContext(info)
	return b
}

func (b *Budget) maxContext(info *gguf.Info) uint32 {
	modelMax := uint32(maxContext)
	if info != nil && info.ContextLength > 0 && info.ContextLength < modelMax {
		modelMax = info.ContextLength
	}
	if !b.HasGPU || !b.KVKnown || b.KVPerToken == 0 {
		return modelMax
	}
	fixed := b.Weights + b.Overhead
	if b.Free <= fixed {
		return 0
	}
	maxCtx := (b.Free - fixed) / b.KVPerToken
	if maxCtx > uint64(modelMax) {
		maxCtx = uint64(modelMax)
	}
	// Leave a little room for the compute buffer and round down to 1024.
	maxCtx &^= 1023
	if maxCtx < minContext {
		if maxCtx < 1024 {
			return 0
		}
		return 1024
	}
	return uint32(maxCtx)
}

// Summary renders the estimate in one line.
func (b *Budget) Summary() string {
	if !b.HasGPU {
		return fmt.Sprintf("CPU inference (no NVIDIA GPU detected); weights %s", hardware.HumanBytes(b.Weights))
	}
	kv := "unknown"
	if b.KVKnown {
		kv = hardware.HumanBytes(b.KV)
	}
	return fmt.Sprintf("needs %s (weights %s + KV %s + overhead %s), free %s",
		hardware.HumanBytes(b.Total), hardware.HumanBytes(b.Weights), kv,
		hardware.HumanBytes(b.Overhead), hardware.HumanBytes(b.Free))
}

// Suggestion returns an actionable repair for a failed budget. flag is the
// command-line switch or config key to suggest ("--ctx" or "ctx_size").
func (b *Budget) Suggestion(flag string) string {
	if !b.HasGPU {
		return "reduce ctx_size or free system memory, then retry"
	}
	if b.MaxContext == 0 {
		return "free VRAM (another process may be using it) and retry, or pass --force to skip the check"
	}
	if b.KVKnown {
		if flag == "ctx_size" {
			return fmt.Sprintf("free VRAM or lower the context: set ctx_size = %d in maio.conf, then restart the service", b.MaxContext)
		}
		return fmt.Sprintf("free VRAM or lower the context: %s %d (or --force to skip the check)", flag, b.MaxContext)
	}
	return "free VRAM and retry, or pass --force to skip the check"
}
