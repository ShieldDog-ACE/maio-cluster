// Package naming normalizes and validates model names.
//
// Convention: lowercase, spaces become '-', only a-z 0-9 . - are allowed.
package naming

import (
	"fmt"
	"regexp"
	"strings"
)

var validPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// Normalize converts user input to the canonical form.
// Example: "DeepSeek v4.1" -> "deepseek-v4.1".
func Normalize(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", fmt.Errorf("model name is empty")
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '\t' || r == '_' || r == '-':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			return "", fmt.Errorf("invalid character %q in model name: only a-z 0-9 . - are allowed (spaces become -)", string(r))
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "", fmt.Errorf("model name %q is empty after normalization", raw)
	}
	if !validPattern.MatchString(out) {
		return "", fmt.Errorf("invalid model name %q, expected something like deepseek-v4.1", out)
	}
	return out, nil
}

// Validate checks a name that is already in canonical form.
func Validate(name string) error {
	if !validPattern.MatchString(name) {
		return fmt.Errorf("invalid model name %q, expected something like deepseek-v4.1", name)
	}
	return nil
}
