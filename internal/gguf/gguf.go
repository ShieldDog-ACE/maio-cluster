// Package gguf reads the GGUF metadata header without loading tensors.
//
// It is used for model capability detection (architecture, context length,
// layer/head counts, quantization, vision) and for the VRAM budget check.
package gguf

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

const (
	magic            = "GGUF"
	maxMetadataBytes = 1 << 30 // 1 GiB of metadata is already pathological
	maxStringBytes   = 64 << 20
	maxArrayCount    = 1 << 28
)

// Info is the capability summary extracted from the GGUF header.
type Info struct {
	Path            string
	Version         uint32
	TensorCount     uint64
	KVCount         uint64
	Architecture    string
	Name            string
	ContextLength   uint32
	BlockCount      uint32
	HeadCount       uint32
	HeadCountKV     uint32
	EmbeddingLength uint32
	FileType        uint32
	Quant           string
	Vision          bool
	Keys            map[string]any // small scalar metadata, kept for diagnostics
}

var fileTypes = map[uint32]string{
	0:  "F32",
	1:  "F16",
	2:  "Q4_0",
	3:  "Q4_1",
	7:  "Q8_0",
	8:  "Q5_0",
	9:  "Q5_1",
	10: "Q2_K",
	11: "Q3_K_S",
	12: "Q3_K_M",
	13: "Q3_K_L",
	14: "Q4_K_S",
	15: "Q4_K_M",
	16: "Q5_K_S",
	17: "Q5_K_M",
	18: "Q6_K",
	19: "IQ2_XXS",
	20: "IQ2_XS",
	21: "IQ3_XXS",
	22: "IQ1_S",
	23: "IQ4_NL",
	24: "IQ3_S",
	25: "IQ2_S",
	26: "IQ4_XS",
	27: "IQ1_M",
	28: "BF16",
	29: "Q4_0_4_4",
	30: "Q4_0_4_8",
	31: "Q4_0_8_8",
	32: "TQ1_0",
	33: "TQ2_0",
}

// Read parses the metadata header of a GGUF file.
func Read(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.Wrap(errs.NotFound, err, "model file not found: %s", path).
				WithHint("run maio show <name>, or download the model again")
		}
		if os.IsPermission(err) {
			return nil, errs.Wrap(errs.Permission, err, "cannot read model file %s", path).
				WithHint("check file permissions, or run with sudo")
		}
		return nil, errs.Wrap(errs.IO, err, "cannot open model file %s", path)
	}
	defer f.Close()
	info, err := ReadReader(f, path)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// ReadReader parses GGUF metadata from an arbitrary reader (used by tests).
func ReadReader(r io.Reader, path string) (*Info, error) {
	cr := &countingReader{r: r, max: maxMetadataBytes}
	br := bufio.NewReaderSize(cr, 1<<20)
	var head [4]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return nil, verifyErr(path, "file is too short to be a GGUF model", err)
	}
	if string(head[:]) != magic {
		return nil, errs.Newf(errs.Verify, "not a GGUF file: %s (magic %q)", path, string(head[:])).
			WithHint("expected a file that starts with GGUF; check the download source")
	}
	version, err := readU32(br)
	if err != nil {
		return nil, verifyErr(path, "cannot read the GGUF version", err)
	}
	if version < 1 || version > 3 {
		return nil, errs.Newf(errs.Verify, "unsupported GGUF version %d in %s", version, path).
			WithHint("convert the model with a recent llama.cpp or download a supported GGUF")
	}
	var tensorCount, kvCount uint64
	if version == 1 {
		t1, err := readU32(br)
		if err != nil {
			return nil, verifyErr(path, "cannot read the GGUF tensor count", err)
		}
		k1, err := readU32(br)
		if err != nil {
			return nil, verifyErr(path, "cannot read the GGUF metadata count", err)
		}
		tensorCount, kvCount = uint64(t1), uint64(k1)
	} else {
		tensorCount, err = readU64(br)
		if err != nil {
			return nil, verifyErr(path, "cannot read the GGUF tensor count", err)
		}
		kvCount, err = readU64(br)
		if err != nil {
			return nil, verifyErr(path, "cannot read the GGUF metadata count", err)
		}
	}
	if kvCount > 20_000_000 {
		return nil, errs.Newf(errs.Verify, "GGUF metadata count is implausible: %d (%s)", kvCount, path)
	}
	info := &Info{
		Path:        path,
		Version:     version,
		TensorCount: tensorCount,
		KVCount:     kvCount,
		Keys:        make(map[string]any),
	}
	for i := uint64(0); i < kvCount; i++ {
		key, err := readString(br, maxStringBytes)
		if err != nil {
			return nil, verifyErr(path, fmt.Sprintf("cannot read metadata key #%d", i+1), err)
		}
		typ, err := readU32(br)
		if err != nil {
			return nil, verifyErr(path, fmt.Sprintf("cannot read the type of %q", key), err)
		}
		if typ == 9 { // array: skip it, tokenizer arrays are large
			if err := skipArray(br, path); err != nil {
				return nil, err
			}
			continue
		}
		val, err := readScalar(br, typ)
		if err != nil {
			return nil, verifyErr(path, fmt.Sprintf("cannot read metadata %q", key), err)
		}
		if len(key) <= 256 {
			if s, ok := val.(string); ok && len(s) > 4096 {
				info.Keys[key] = fmt.Sprintf("[%d bytes]", len(s))
			} else {
				info.Keys[key] = val
			}
		}
		apply(info, key, val)
	}
	finalize(info)
	return info, nil
}

func finalize(info *Info) {
	if info.HeadCountKV == 0 {
		info.HeadCountKV = info.HeadCount
	}
	if info.FileType != 0 {
		if q, ok := fileTypes[info.FileType]; ok {
			info.Quant = q
		}
	}
	if info.Quant == "" {
		info.Quant = inferQuant(info.Path)
	}
	info.Vision = info.Vision || hasVisionArch(info.Architecture)
	if info.Vision {
		// Keep the flag but do not invent an architecture.
	}
}

func apply(info *Info, key string, val any) {
	switch key {
	case "general.architecture":
		info.Architecture = asString(val)
	case "general.name":
		info.Name = asString(val)
	case "general.file_type":
		if n, ok := asUint(val); ok {
			info.FileType = uint32(n)
		}
	}
	switch {
	case strings.HasSuffix(key, ".context_length"):
		if n, ok := asUint(val); ok && n <= math.MaxUint32 {
			info.ContextLength = uint32(n)
		}
	case strings.HasSuffix(key, ".block_count"):
		if n, ok := asUint(val); ok && n <= math.MaxUint32 {
			info.BlockCount = uint32(n)
		}
	case strings.HasSuffix(key, ".attention.head_count"):
		if n, ok := asUint(val); ok && n <= math.MaxUint32 {
			info.HeadCount = uint32(n)
		}
	case strings.HasSuffix(key, ".attention.head_count_kv"):
		if n, ok := asUint(val); ok && n <= math.MaxUint32 {
			info.HeadCountKV = uint32(n)
		}
	case strings.HasSuffix(key, ".embedding_length"):
		if n, ok := asUint(val); ok && n <= math.MaxUint32 {
			info.EmbeddingLength = uint32(n)
		}
	}
	if strings.Contains(strings.ToLower(key), "vision") {
		if b, ok := val.(bool); ok && b {
			info.Vision = true
		}
	}
}

func hasVisionArch(arch string) bool {
	a := strings.ToLower(arch)
	if a == "" {
		return false
	}
	for _, marker := range []string{
		"vl", "vision", "clip", "llava", "minicpmv", "glm4v", "qwen2vl",
		"qwen3vl", "granitevision", "idefics", "mllama", "molmo", "pixtral",
	} {
		if strings.Contains(a, marker) {
			return true
		}
	}
	return false
}

func inferQuant(path string) string {
	base := strings.ToUpper(filepath.Base(path))
	for _, q := range []string{
		"IQ1_M", "IQ1_S", "IQ2_XXS", "IQ2_XS", "IQ2_S", "IQ3_XXS", "IQ3_XS",
		"IQ3_S", "IQ4_XS", "IQ4_NL", "Q2_K", "Q3_K_S", "Q3_K_M", "Q3_K_L",
		"Q4_K_S", "Q4_K_M", "Q5_K_S", "Q5_K_M", "Q6_K", "Q8_0", "Q5_0",
		"Q5_1", "Q4_0", "Q4_1", "BF16", "F16", "F32",
	} {
		if strings.Contains(base, q) {
			return q
		}
	}
	return "unknown"
}

func verifyErr(path, msg string, cause error) error {
	if cause == nil {
		return errs.Newf(errs.Verify, "%s (%s)", msg, path)
	}
	return errs.Wrap(errs.Verify, cause, "%s (%s)", msg, path).
		WithHint("the file may be truncated or corrupt; re-download it")
}

func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func readU64(r io.Reader) (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

func readString(r *bufio.Reader, max uint64) (string, error) {
	n, err := readU64(r)
	if err != nil {
		return "", err
	}
	if n > max {
		return "", fmt.Errorf("string length %d exceeds the %d byte limit", n, max)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func readScalar(r *bufio.Reader, typ uint32) (any, error) {
	switch typ {
	case 0:
		b, err := r.ReadByte()
		return uint64(b), err
	case 1:
		b, err := r.ReadByte()
		return int64(int8(b)), err
	case 2:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		return uint64(binary.LittleEndian.Uint16(b[:])), nil
	case 3:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		return int64(int16(binary.LittleEndian.Uint16(b[:]))), nil
	case 4:
		v, err := readU32(r)
		return uint64(v), err
	case 5:
		v, err := readU32(r)
		return int64(int32(v)), err
	case 6:
		v, err := readU32(r)
		return float64(math.Float32frombits(v)), err
	case 7:
		b, err := r.ReadByte()
		return b != 0, err
	case 8:
		s, err := readString(r, maxStringBytes)
		return s, err
	case 10:
		return readU64(r)
	case 11:
		v, err := readU64(r)
		return int64(v), err
	case 12:
		v, err := readU64(r)
		return math.Float64frombits(v), err
	default:
		return nil, fmt.Errorf("unsupported GGUF value type %d", typ)
	}
}

func skipArray(r *bufio.Reader, path string) error {
	elem, err := readU32(r)
	if err != nil {
		return err
	}
	count, err := readU64(r)
	if err != nil {
		return err
	}
	if count > maxArrayCount {
		return errs.Newf(errs.Verify, "GGUF array is implausibly large: %d elements (%s)", count, path)
	}
	switch elem {
	case 8:
		for i := uint64(0); i < count; i++ {
			n, err := readU64(r)
			if err != nil {
				return err
			}
			if n > maxStringBytes {
				return fmt.Errorf("string length %d exceeds the limit", n)
			}
			if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
				return err
			}
		}
		return nil
	case 9:
		return fmt.Errorf("nested GGUF arrays are not supported")
	case 0, 1, 7:
		return skipN(r, count)
	case 2, 3:
		return skipN(r, count*2)
	case 4, 5, 6:
		return skipN(r, count*4)
	case 10, 11, 12:
		return skipN(r, count*8)
	default:
		return fmt.Errorf("unsupported GGUF array element type %d", elem)
	}
}

func skipN(r *bufio.Reader, n uint64) error {
	if n > maxMetadataBytes {
		return fmt.Errorf("metadata array is too large (%d bytes)", n)
	}
	_, err := io.CopyN(io.Discard, r, int64(n))
	return err
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asUint(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
}

type countingReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.max {
		return n, fmt.Errorf("GGUF metadata exceeds the %d byte limit", c.max)
	}
	return n, err
}

// FormatContext renders a context length for humans.
func FormatContext(n uint32) string {
	if n == 0 {
		return "unknown"
	}
	return strconv.FormatUint(uint64(n), 10)
}
