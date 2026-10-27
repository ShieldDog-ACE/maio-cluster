package model

import (
	"bytes"
	"encoding/binary"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func writeTestGGUF(t *testing.T, dir, name string) string {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(0))
	binary.Write(&b, binary.LittleEndian, uint64(4))
	kv := func(k string, typ uint32, v []byte) {
		binary.Write(&b, binary.LittleEndian, uint64(len(k)))
		b.WriteString(k)
		binary.Write(&b, binary.LittleEndian, typ)
		b.Write(v)
	}
	s := func(v string) []byte {
		x := make([]byte, 8+len(v))
		binary.LittleEndian.PutUint64(x, uint64(len(v)))
		copy(x[8:], v)
		return x
	}
	u := func(v uint32) []byte {
		x := make([]byte, 4)
		binary.LittleEndian.PutUint32(x, v)
		return x
	}
	kv("general.architecture", 8, s("llama"))
	kv("general.name", 8, s("test"))
	kv("llama.block_count", 4, u(16))
	kv("llama.context_length", 4, u(2048))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreInstallListRemove(t *testing.T) {
	root := t.TempDir()
	src := writeTestGGUF(t, t.TempDir(), "model.gguf")
	s := NewStore(root)
	if _, err := s.Install("Tiny Model", src, "", nil); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get("tiny-model")
	if err != nil {
		t.Fatal(err)
	}
	if e.Meta == nil || e.Meta.Layers != 16 {
		t.Fatalf("metadata: %+v", e.Meta)
	}
	if _, err := s.Install("tiny-model", src, "", nil); err == nil {
		t.Fatal("duplicate install accepted")
	}
	entries, problems := s.List()
	if len(entries) != 1 || len(problems) != 0 {
		t.Fatalf("entries=%v problems=%v", entries, problems)
	}
	if err := s.Remove("tiny-model"); err != nil {
		t.Fatal(err)
	}
	if s.Has("tiny-model") {
		t.Fatal("model still present")
	}
}

func TestStoreRejectsNonGGUF(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(t.TempDir(), "bad.gguf")
	if err := os.WriteFile(bad, []byte("not a model"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(root)
	if _, err := s.Install("bad", bad, "", nil); err == nil {
		t.Fatal("non-GGUF accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "bad")); !os.IsNotExist(err) {
		t.Fatal("failed install left a directory")
	}
}

func TestTokenOnlySentToCoordinatorHost(t *testing.T) {
	s := &Store{Token: "secret", TokenHosts: []string{"192.168.1.10"}}
	in := func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return s.tokenAllowed(u)
	}
	if !in("http://192.168.1.10:47800/v1/models/x/file") {
		t.Fatal("coordinator host rejected")
	}
	if in("https://www.modelscope.cn/models/x.gguf") {
		t.Fatal("token would leak to a model mirror")
	}
	if in("http://192.168.1.11:47800/v1/models/x/file") {
		t.Fatal("token sent to a non-coordinator LAN host")
	}
}
