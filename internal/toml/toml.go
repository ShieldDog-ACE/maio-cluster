// Package toml is a minimal TOML reader that is just enough for maio:
// it supports `key = "string"`, `key = integer`, `key = true/false` and # comments.
// Keys may be bare or quoted. The reader keeps line numbers so configuration
// errors can point at the exact line. maio configs are flat, and no third-party
// dependency is pulled in, to stay lightweight.
package toml

import (
	"fmt"
	"strconv"
	"strings"
)

// Document holds the parse result; keys are kept without quotes.
type Document struct {
	Values map[string]any
	Lines  map[string]int
	Order  []string
}

// Parse parses TOML text.
func Parse(data []byte) (*Document, error) {
	doc := &Document{
		Values: make(map[string]any),
		Lines:  make(map[string]int),
	}
	lines := strings.Split(string(data), "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		eq := findAssign(line)
		if eq < 0 {
			return nil, fmt.Errorf("line %d: missing '='", i+1)
		}
		key, err := parseKey(strings.TrimSpace(line[:eq]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if first, exists := doc.Lines[key]; exists {
			return nil, fmt.Errorf("line %d: duplicate key %q (first defined on line %d)", i+1, key, first)
		}
		value, err := parseValue(strings.TrimSpace(line[eq+1:]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		doc.Values[key] = value
		doc.Lines[key] = i + 1
		doc.Order = append(doc.Order, key)
	}
	return doc, nil
}

// String returns a string value.
func (d *Document) String(key string) (string, bool) {
	v, ok := d.Values[key].(string)
	return v, ok
}

// Int returns an integer value.
func (d *Document) Int(key string) (int64, bool) {
	v, ok := d.Values[key].(int64)
	return v, ok
}

// Bool returns a boolean value.
func (d *Document) Bool(key string) (bool, bool) {
	v, ok := d.Values[key].(bool)
	return v, ok
}

// Has reports whether a key exists.
func (d *Document) Has(key string) bool {
	_, ok := d.Values[key]
	return ok
}

// Line returns the 1-based source line of a key (0 when absent).
func (d *Document) Line(key string) int { return d.Lines[key] }

// Keys returns keys in source order.
func (d *Document) Keys() []string { return append([]string(nil), d.Order...) }

// TypeName describes the stored value type for diagnostics.
func (d *Document) TypeName(key string) string {
	v, ok := d.Values[key]
	if !ok {
		return "missing"
	}
	switch v.(type) {
	case string:
		return "string"
	case int64:
		return "integer"
	case bool:
		return "boolean"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func parseKey(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty key")
	}
	if strings.HasPrefix(raw, "\"") {
		if len(raw) < 2 || !strings.HasSuffix(raw, "\"") {
			return "", fmt.Errorf("key is missing the closing quote")
		}
		key, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("failed to parse key: %w", err)
		}
		if key == "" {
			return "", fmt.Errorf("empty key")
		}
		return key, nil
	}
	if strings.ContainsAny(raw, " \t") {
		return "", fmt.Errorf("key %q contains whitespace", raw)
	}
	return raw, nil
}

func stripComment(line string) string {
	inQuote, escaped := false, false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		switch {
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == '#' && !inQuote:
			return line[:i]
		}
	}
	return line
}

func findAssign(line string) int {
	inQuote, escaped := false, false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		switch {
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == '=' && !inQuote:
			return i
		}
	}
	return -1
}

func parseValue(raw string) (any, error) {
	if raw == "" {
		return nil, fmt.Errorf("value is empty")
	}
	if strings.HasPrefix(raw, "\"") {
		if len(raw) < 2 || !strings.HasSuffix(raw, "\"") {
			return nil, fmt.Errorf("string is missing the closing quote")
		}
		s, err := strconv.Unquote(raw)
		if err != nil {
			return nil, fmt.Errorf("failed to parse string: %w", err)
		}
		return s, nil
	}
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(raw, "_", ""), 10, 64)
	if err == nil {
		return n, nil
	}
	return nil, fmt.Errorf("unsupported value %q (expected: quoted string, integer, or true|false)", raw)
}
