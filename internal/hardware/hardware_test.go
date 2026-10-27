package hardware

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseNvidiaSMI(t *testing.T) {
	out := "0, Tesla V100-SXM2-16GB, 16384, 7, 16377, 580.178.04, 7.0\n" +
		"1, Tesla V100-SXM2-16GB, 16384, 8, 16376, 580.178.04, 7.0\n"
	gpus, err := ParseNvidiaSMI(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 2 {
		t.Fatalf("got %d GPUs", len(gpus))
	}
	if gpus[0].MemoryTotal != 16384*1024*1024 {
		t.Fatalf("total = %d", gpus[0].MemoryTotal)
	}
	if gpus[0].MemoryFree != 16377*1024*1024 {
		t.Fatalf("free = %d", gpus[0].MemoryFree)
	}
	if gpus[0].ComputeCap != "7.0" {
		t.Fatalf("compute = %q", gpus[0].ComputeCap)
	}
}

func TestReadMeminfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meminfo")
	body := "MemTotal:       1000 kB\nMemAvailable:    500 kB\nSwapTotal:       200 kB\nSwapFree:        100 kB\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	total, avail, st, sf, err := readMeminfo(p)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1000*1024 || avail != 500*1024 || st != 200*1024 || sf != 100*1024 {
		t.Fatalf("unexpected meminfo: %d %d %d %d", total, avail, st, sf)
	}
}

func TestDiskUsage(t *testing.T) {
	total, free, path, err := DiskUsage("/")
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || free > total || path != "/" {
		t.Fatalf("unexpected disk usage: total=%d free=%d path=%s", total, free, path)
	}
}
