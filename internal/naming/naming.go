// Package naming normalizes and validates model names.
//
// Convention: lowercase, spaces become '-', only a-z 0-9 . - are allowed.
package naming

import (
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

// Normalize converts user input to the canonical form.
// Example: "DeepSeek v4.1" -> "deepseek-v4.1".
func Normalize(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", errs.New(errs.Usage, "model name is empty").
			WithHint("pass a name such as deepseek-v4.1")
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
			return "", errs.Newf(errs.Usage, "invalid character %q in model name", string(r)).
				WithHint("only a-z, 0-9, '.' and '-' are allowed; spaces become '-'")
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "", errs.Newf(errs.Usage, "model name %q is empty after normalization", raw).
			WithHint("use a name such as deepseek-v4.1")
	}
	if err := Validate(out); err != nil {
		return "", errs.Wrap(errs.Usage, err, "invalid model name %q", out).
			WithHint("use lowercase letters, digits, '.' and '-' (example: deepseek-v4.1)")
	}
	return out, nil
}

// NormalizeLoose is the best-effort form used for persisted state: it returns
// the canonical name, or "" when the input cannot be normalized. It never
// reports an error because a corrupt state file must not break startup.
func NormalizeLoose(raw string) string {
	out, err := Normalize(raw)
	if err != nil {
		return ""
	}
	return out
}

// Validate checks a name that is already in canonical form.
func Validate(name string) error {
	if name == "" {
		return errs.New(errs.Usage, "model name is empty")
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
		case i == 0:
			return errs.Newf(errs.Usage, "model name %q must start with a letter or digit", name)
		default:
			return errs.Newf(errs.Usage, "model name %q contains an unsupported character %q", name, string(r))
		}
	}
	return nil
}
