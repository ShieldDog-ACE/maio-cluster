// Package gguf reads the GGUF header without loading tensor data.
//
// It is deliberately read-only and bounded: a hostile or truncated file can
// only waste a small, fixed amount of memory before the parser fails.
package gguf

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

// Magic is the little-endian GGUF file magic.
const Magic = 0x46554747

// Metadata is the subset of GGUF metadata maio needs for placement and UI.
type Metadata struct {
	Magic       uint32
	Version     uint32
	Name        string
	Arch        string
	Layers      int
	Context     int
	Embedding   int
	Heads       int
	Experts     int
	FileType    int
	Quant       string
	TensorCount uint64
	KVMetadata  uint64
	FileSize    int64
}

const (
	maxMetaBytes = 64 << 20
	maxCount     = 1 << 20
	maxString    = 8 << 20
)

const (
	tUint8 = iota
	tInt8
	tUint16
	tInt16
	tUint32
	tInt32
	tFloat32
	tBool
	tString
	tArray
	tUint64
	tInt64
	tFloat64
)

// ParseFile opens path and reads its GGUF header.
func ParseFile(path string) (*Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errs.Wrap(errs.NotFound, err, "cannot open model file %s", path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err, "cannot stat %s", path)
	}
	m, err := Parse(f)
	if err != nil {
		return nil, err
	}
	m.FileSize = st.Size()
	return m, nil
}

// Parse reads a GGUF header from r.
func Parse(r io.Reader) (*Metadata, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	meta := &Metadata{}
	if err := binary.Read(br, binary.LittleEndian, &meta.Magic); err != nil {
		return nil, errs.Wrap(errs.Integrity, err, "cannot read GGUF magic")
	}
	if meta.Magic != Magic {
		return nil, errs.New(errs.Integrity, "not a GGUF file (magic %#x)", meta.Magic).
			WithHint("only GGUF models are supported; convert the model to GGUF first")
	}
	if err := binary.Read(br, binary.LittleEndian, &meta.Version); err != nil {
		return nil, errs.Wrap(errs.Integrity, err, "cannot read GGUF version")
	}
	if meta.Version < 2 || meta.Version > 3 {
		return nil, errs.New(errs.Integrity, "unsupported GGUF version %d", meta.Version).
			WithHint("upgrade maio or re-convert the model with a current llama.cpp")
	}
	if err := binary.Read(br, binary.LittleEndian, &meta.TensorCount); err != nil {
		return nil, errs.Wrap(errs.Integrity, err, "cannot read tensor count")
	}
	if err := binary.Read(br, binary.LittleEndian, &meta.KVMetadata); err != nil {
		return nil, errs.Wrap(errs.Integrity, err, "cannot read metadata count")
	}
	if meta.TensorCount > maxCount || meta.KVMetadata > maxCount {
		return nil, errs.New(errs.Integrity, "GGUF header declares too many entries")
	}

	scalars := map[string]any{}
	var used int64
	for i := uint64(0); i < meta.KVMetadata; i++ {
		key, err := readString(br, &used)
		if err != nil {
			return nil, errs.Wrap(errs.Integrity, err, "cannot read metadata key %d", i)
		}
		var typ uint32
		if err := binary.Read(br, binary.LittleEndian, &typ); err != nil {
			return nil, errs.Wrap(errs.Integrity, err, "cannot read metadata type for %q", key)
		}
		v, err := readValue(br, typ, &used, 0)
		if err != nil {
			return nil, errs.Wrap(errs.Integrity, err, "cannot read metadata %q", key)
		}
		if v != nil {
			scalars[key] = v
		}
		if used > maxMetaBytes {
			return nil, errs.New(errs.Integrity, "GGUF metadata exceeds %d MiB", maxMetaBytes>>20)
		}
	}

	meta.Name = str(scalars["general.name"])
	meta.Arch = str(scalars["general.architecture"])
	prefix := meta.Arch + "."
	meta.Layers = integer(scalars[prefix+"block_count"])
	meta.Context = integer(scalars[prefix+"context_length"])
	meta.Embedding = integer(scalars[prefix+"embedding_length"])
	meta.Heads = integer(scalars[prefix+"attention.head_count"])
	meta.Experts = integer(scalars[prefix+"expert_count"])
	meta.FileType = integer(scalars["general.file_type"])
	meta.Quant = QuantName(meta.FileType)
	if meta.Arch == "" {
		return nil, errs.New(errs.Integrity, "GGUF file has no general.architecture").
			WithHint("re-download the model; the header looks incomplete")
	}
	return meta, nil
}

func readValue(r *bufio.Reader, typ uint32, used *int64, depth int) (any, error) {
	if depth > 4 {
		return nil, fmt.Errorf("array nesting is too deep")
	}
	switch typ {
	case tUint8, tInt8, tBool:
		var v uint8
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		*used += 1
		if typ == tBool {
			return v != 0, nil
		}
		return int64(v), nil
	case tUint16, tInt16:
		var v uint16
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		*used += 2
		return int64(v), nil
	case tUint32, tInt32:
		var v uint32
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		*used += 4
		return int64(v), nil
	case tFloat32:
		var v float32
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		*used += 4
		return float64(v), nil
	case tUint64, tInt64:
		var v uint64
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		*used += 8
		return int64(v), nil
	case tFloat64:
		var v float64
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		*used += 8
		return v, nil
	case tString:
		s, err := readString(r, used)
		if err != nil {
			return nil, err
		}
		return s, nil
	case tArray:
		var elem uint32
		if err := binary.Read(r, binary.LittleEndian, &elem); err != nil {
			return nil, err
		}
		var count uint64
		if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
			return nil, err
		}
		if count > maxCount {
			return nil, fmt.Errorf("array count %d is too large", count)
		}
		// Arrays are not needed for placement. Skip their payload with a
		// bounded per-element read so corrupt lengths fail early.
		for i := uint64(0); i < count; i++ {
			if _, err := readValue(r, elem, used, depth+1); err != nil {
				return nil, err
			}
			if *used > maxMetaBytes {
				return nil, fmt.Errorf("metadata exceeds budget")
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown value type %d", typ)
	}
}

func readString(r *bufio.Reader, used *int64) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > maxString {
		return "", fmt.Errorf("string length %d is too large", n)
	}
	*used += int64(n) + 8
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func integer(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// QuantName maps the GGUF general.file_type value to a short label.
func QuantName(t int) string {
	names := []string{
		"F32", "F16", "Q4_0", "Q4_1", "Q5_0", "Q5_1", "Q8_0", "Q8_1",
		"Q2_K", "Q3_K_S", "Q3_K_M", "Q3_K_L", "Q4_K_S", "Q4_K_M",
		"Q5_K_S", "Q5_K_M", "Q6_K", "IQ2_XXS", "IQ2_XS", "IQ2_S",
		"IQ3_XXS", "IQ3_XS", "IQ1_S", "IQ4_NL", "IQ3_S", "IQ3_M",
		"IQ2_S", "IQ4_XS", "IQ1_M", "BF16", "Q4_0_4_4", "Q4_0_4_8",
		"Q4_0_8_8", "TQ1_0", "TQ2_0", "IQ4_NL_4_4", "IQ4_NL_4_8",
		"IQ4_NL_8_8",
	}
	if t >= 0 && t < len(names) {
		return names[t]
	}
	return "unknown"
}

// MetadataKey returns the architecture-qualified metadata key.
func MetadataKey(arch, suffix string) string {
	if strings.HasSuffix(arch, ".") {
		return arch + suffix
	}
	return arch + "." + suffix
}
