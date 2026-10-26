package toml

import "testing"

func TestParseFlat(t *testing.T) {
	text := `
# comment
boot = 1
models_root = "/var/lib/maio/models"   # trailing comment
host = "0.0.0.0"
enabled = true
dash = "a # not comment"
`
	doc, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if v, ok := doc.Int("boot"); !ok || v != 1 {
		t.Fatalf("boot = %v, ok=%v", v, ok)
	}
	if v, ok := doc.String("models_root"); !ok || v != "/var/lib/maio/models" {
		t.Fatalf("models_root = %q", v)
	}
	if v, ok := doc.String("host"); !ok || v != "0.0.0.0" {
		t.Fatalf("host = %q", v)
	}
	if v, ok := doc.Bool("enabled"); !ok || !v {
		t.Fatalf("enabled = %v", v)
	}
	if v, ok := doc.String("dash"); !ok || v != "a # not comment" {
		t.Fatalf("dash = %q", v)
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"boot = ",
		"boot 1",
		`host = "127.0.0.1`,
		"port = abc",
		"= 1",
	}
	for _, text := range bad {
		if _, err := Parse([]byte(text)); err == nil {
			t.Fatalf("Parse(%q) should fail", text)
		}
	}
}
