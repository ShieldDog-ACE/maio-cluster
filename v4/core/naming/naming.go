// Package naming normalizes model names shared by every maio platform.
package naming

import (
	"regexp"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

var valid = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// Normalize turns free-form input into a valid model name.
//
//	"DeepSeek v4.1" -> "deepseek-v4.1"
//	"  Qwen3.5_9B " -> "qwen3.5-9b"
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errs.New(errs.Usage, "model name is empty").
			WithHint(`use a lowercase name such as "qwen3.5-9b"`)
	}
	repl := strings.NewReplacer(
		" ", "-", "_", "-", "/", "-", "\\", "-", ":", "-", "@", "-",
	)
	s = repl.Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, ".-")
	if !valid.MatchString(s) {
		return "", errs.New(errs.Usage, "invalid model name %q", raw).
			WithHint("allowed characters: a-z 0-9 . - and the name must start with a letter or digit").
			WithHint(`example: "DeepSeek v4.1" becomes "deepseek-v4.1"`)
	}
	if len(s) > 96 {
		return "", errs.New(errs.Usage, "model name is too long (%d characters)", len(s))
	}
	return s, nil
}
