package toml

import "testing"

func TestParseSectionsAndArrays(t *testing.T) {
	doc, err := Parse([]byte(`
# comment
name = "maio"
[cluster]
port = 47800
seeds = ["192.168.1.2:47800", "192.168.1.3:47800"]
compute = true
`))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := doc.String("name"); v != "maio" {
		t.Fatalf("name=%q", v)
	}
	if v, _ := doc.Int("cluster.port"); v != 47800 {
		t.Fatalf("port=%d", v)
	}
	if v, _ := doc.Bool("cluster.compute"); !v {
		t.Fatal("compute false")
	}
	seeds, ok := doc.Strings("cluster.seeds")
	if !ok || len(seeds) != 2 || seeds[1] != "192.168.1.3:47800" {
		t.Fatalf("seeds=%v", seeds)
	}
}

func TestParseRejectsUnknownValue(t *testing.T) {
	if _, err := Parse([]byte("x = 1.5\n")); err == nil {
		t.Fatal("float accepted")
	}
	if _, err := Parse([]byte("[a]\nx = 1\nx = 2\n")); err == nil {
		t.Fatal("duplicate accepted")
	}
}
