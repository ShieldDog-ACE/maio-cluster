package machineid

import "testing"

func TestIDStableAndFormatted(t *testing.T) {
	a := ID()
	b := ID()
	if a != b {
		t.Fatalf("machine ID changed between calls: %q vs %q", a, b)
	}
	if len(a) != 20 || a[:4] != "mid_" {
		t.Fatalf("unexpected machine ID format: %q", a)
	}
	if Hash("abc") == Hash("abd") {
		t.Fatal("hash collision")
	}
}
