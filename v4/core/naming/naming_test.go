package naming

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"DeepSeek v4.1": "deepseek-v4.1",
		"  Qwen3.5_9B ": "qwen3.5-9b",
		"GLM 4.6V":      "glm-4.6v",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
	if _, err := Normalize("!!!"); err == nil {
		t.Fatal("invalid name accepted")
	}
}
