package naming

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"DeepSeek v4.1", "deepseek-v4.1"},
		{"deepseek-v4.1", "deepseek-v4.1"},
		{"  Qwen3.5  9B  ", "qwen3.5-9b"},
		{"glm_4_9b_chat", "glm-4-9b-chat"},
		{"a--b", "a-b"},
		{"fara-7b", "fara-7b"},
		{"-abc", "abc"},
		{".abc", "abc"},
	}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if err != nil {
			t.Fatalf("Normalize(%q) returned error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	bad := []string{"", "   ", "a/b", "a$b", "a+b"}
	for _, in := range bad {
		if got, err := Normalize(in); err == nil {
			t.Fatalf("Normalize(%q) should fail, got %q", in, got)
		}
	}
}
