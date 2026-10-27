package gguf

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func gstr(s string) []byte {
	b := make([]byte, 8+len(s))
	binary.LittleEndian.PutUint64(b, uint64(len(s)))
	copy(b[8:], s)
	return b
}

func gu32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func testGGUF() []byte {
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(0))
	binary.Write(&b, binary.LittleEndian, uint64(6))
	kv := func(k string, typ uint32, v []byte) {
		b.Write(gstr(k))
		binary.Write(&b, binary.LittleEndian, typ)
		b.Write(v)
	}
	kv("general.architecture", tString, gstr("llama"))
	kv("general.name", tString, gstr("unit-test"))
	kv("general.file_type", tUint32, gu32(15))
	kv("llama.block_count", tUint32, gu32(32))
	kv("llama.context_length", tUint32, gu32(8192))
	kv("llama.embedding_length", tUint32, gu32(4096))
	return b.Bytes()
}

func TestParse(t *testing.T) {
	m, err := Parse(bytes.NewReader(testGGUF()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Arch != "llama" || m.Layers != 32 || m.Context != 8192 || m.Embedding != 4096 {
		t.Fatalf("unexpected metadata: %+v", m)
	}
	if m.Quant == "" || m.Name != "unit-test" {
		t.Fatalf("missing fields: %+v", m)
	}
}

func TestParseRejectsBadMagic(t *testing.T) {
	data := testGGUF()
	data[0] = 'X'
	if _, err := Parse(bytes.NewReader(data)); err == nil {
		t.Fatal("bad magic accepted")
	}
}

func TestParseRejectsTruncated(t *testing.T) {
	data := testGGUF()
	if _, err := Parse(bytes.NewReader(data[:63])); err == nil {
		t.Fatal("truncated header accepted")
	}
}
