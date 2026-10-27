package errs

import (
	"bytes"
	"strings"
	"testing"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		code Code
		want int
	}{
		{Usage, 2}, {NotFound, 3}, {Conflict, 4}, {Permission, 5},
		{Download, 6}, {Verify, 7}, {Engine, 8}, {Hardware, 9},
		{Config, 10}, {Service, 11}, {Interrupted, 130},
	}
	for _, c := range cases {
		if got := ExitCode(New(c.code, "x")); got != c.want {
			t.Fatalf("ExitCode(%s) = %d, want %d", c.code, got, c.want)
		}
	}
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("ExitCode(nil) = %d", got)
	}
	if got := ExitCode(Newf(Internal, "x")); got != 1 {
		t.Fatalf("ExitCode(Internal) = %d", got)
	}
}

func TestHintChain(t *testing.T) {
	inner := New(Verify, "bad hash").WithHint("delete and retry")
	outer := Wrap(Download, inner, "download failed")
	if HintOf(outer) != "delete and retry" {
		t.Fatalf("HintOf = %q", HintOf(outer))
	}
	if !IsCode(outer, Download) {
		t.Fatal("IsCode should see the outer code")
	}
	if !IsCode(outer, Verify) {
		t.Fatal("IsCode should see the wrapped code")
	}
}

func TestPrint(t *testing.T) {
	err := New(Download, "HTTP 404").WithHint("check the URL")
	var buf bytes.Buffer
	Print(&buf, err)
	out := buf.String()
	for _, want := range []string{"MAIO-E006", "HTTP 404", "hint: check the URL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q is missing %q", out, want)
		}
	}
}
