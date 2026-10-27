package gguf

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func kvString(buf *bytes.Buffer, key, val string) {
	writeString(buf, key)
	binary.Write(buf, binary.LittleEndian, uint32(8))
	writeString(buf, val)
}

func kvU32(buf *bytes.Buffer, key string, val uint32) {
	writeString(buf, key)
	binary.Write(buf, binary.LittleEndian, uint32(4))
	binary.Write(buf, binary.LittleEndian, val)
}

func writeString(buf *bytes.Buffer, s string) {
	binary.Write(buf, binary.LittleEndian, uint64(len(s)))
	buf.WriteString(s)
}

func buildGGUF(arch string) []byte {
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(0))
	binary.Write(&b, binary.LittleEndian, uint64(8))
	kvString(&b, "general.architecture", arch)
	kvString(&b, "general.name", "test-model")
	kvU32(&b, "general.file_type", 15)
	kvU32(&b, arch+".context_length", 8192)
	kvU32(&b, arch+".block_count", 32)
	kvU32(&b, arch+".attention.head_count", 32)
	kvU32(&b, arch+".attention.head_count_kv", 8)
	kvU32(&b, arch+".embedding_length", 4096)
	return b.Bytes()
}

func TestReadMetadata(t *testing.T) {
	info, err := ReadReader(bytes.NewReader(buildGGUF("llama")), "test.gguf")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if info.Architecture != "llama" || info.Name != "test-model" {
		t.Fatalf("unexpected identity: %+v", info)
	}
	if info.ContextLength != 8192 || info.BlockCount != 32 {
		t.Fatalf("unexpected counts: %+v", info)
	}
	if info.HeadCount != 32 || info.HeadCountKV != 8 || info.EmbeddingLength != 4096 {
		t.Fatalf("unexpected attention metadata: %+v", info)
	}
	if info.Quant != "Q4_K_M" {
		t.Fatalf("quant = %q", info.Quant)
	}
	if info.Vision {
		t.Fatal("llama should not be a vision architecture")
	}
}

func TestReadVision(t *testing.T) {
	info, err := ReadReader(bytes.NewReader(buildGGUF("qwen2vl")), "test.gguf")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Vision {
		t.Fatal("qwen2vl should be detected as vision")
	}
}

func TestReadRejectsBadMagic(t *testing.T) {
	if _, err := ReadReader(bytes.NewReader([]byte("NOPE")), "x.gguf"); err == nil {
		t.Fatal("bad magic should fail")
	}
}

func TestReadRejectsTruncated(t *testing.T) {
	data := buildGGUF("llama")
	if _, err := ReadReader(bytes.NewReader(data[:len(data)-4]), "x.gguf"); err == nil {
		t.Fatal("truncated metadata should fail")
	}
}
