// Package toml implements the small, dependency-free TOML subset used by maio
// configuration files.
//
// Supported syntax:
//
//	key = "string"          # basic strings with escapes
//	key = 123               # decimal integers (underscores allowed)
//	key = true              # booleans
//	key = ["a", "b"]        # arrays of strings
//	[section]               # section headers; keys become "section.key"
//	# comment
//
// Unknown syntax fails loudly with the exact line number. maio never silently
// ignores a configuration key, because a typo must not change cluster safety.
package toml

import (
	"fmt"
	"strconv"
	"strings"
)

// Document is a parsed TOML file.
type Document struct {
	Values map[string]any
	Lines  map[string]int
	Order  []string
}

// Parse parses data.
func Parse(data []byte) (*Document, error) {
	doc := &Document{Values: map[string]any{}, Lines: map[string]int{}}
	section := ""
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: section header is missing ']'", i+1)
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" || strings.ContainsAny(name, "[] \t") {
				return nil, fmt.Errorf("line %d: invalid section %q", i+1, name)
			}
			section = name
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
		full := key
		if section != "" {
			full = section + "." + key
		}
		if first, exists := doc.Lines[full]; exists {
			return nil, fmt.Errorf("line %d: duplicate key %q (first defined on line %d)", i+1, full, first)
		}
		value, err := parseValue(strings.TrimSpace(line[eq+1:]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		doc.Values[full] = value
		doc.Lines[full] = i + 1
		doc.Order = append(doc.Order, full)
	}
	return doc, nil
}

// String returns a string value.
func (d *Document) String(key string) (string, bool) {
	v, ok := d.Values[key].(string)
	return v, ok
}

// Int returns an integer.
func (d *Document) Int(key string) (int64, bool) {
	v, ok := d.Values[key].(int64)
	return v, ok
}

// Bool returns a boolean.
func (d *Document) Bool(key string) (bool, bool) {
	v, ok := d.Values[key].(bool)
	return v, ok
}

// Strings returns a string array.
func (d *Document) Strings(key string) ([]string, bool) {
	v, ok := d.Values[key].([]string)
	return v, ok
}

// Has reports whether the key exists.
func (d *Document) Has(key string) bool {
	_, ok := d.Values[key]
	return ok
}

// Line returns the source line (0 when absent).
func (d *Document) Line(key string) int { return d.Lines[key] }

// Keys returns keys in source order.
func (d *Document) Keys() []string { return append([]string(nil), d.Order...) }

// TypeName is used in diagnostics.
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
	case []string:
		return "string array"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func parseKey(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty key")
	}
	if strings.HasPrefix(raw, `"`) {
		if len(raw) < 2 || !strings.HasSuffix(raw, `"`) {
			return "", fmt.Errorf("key is missing the closing quote")
		}
		key, err := strconv.Unquote(raw)
		if err != nil || key == "" {
			return "", fmt.Errorf("invalid quoted key %q", raw)
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
	if strings.HasPrefix(raw, `"`) {
		return parseString(raw)
	}
	if strings.HasPrefix(raw, "[") {
		return parseArray(raw)
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
	return nil, fmt.Errorf("unsupported value %q (expected: string, integer, boolean, or string array)", raw)
}

func parseString(raw string) (string, error) {
	if len(raw) < 2 || !strings.HasSuffix(raw, `"`) {
		return "", fmt.Errorf("string is missing the closing quote")
	}
	s, err := strconv.Unquote(raw)
	if err != nil {
		return "", fmt.Errorf("failed to parse string: %w", err)
	}
	return s, nil
}

func parseArray(raw string) ([]string, error) {
	if !strings.HasSuffix(raw, "]") {
		return nil, fmt.Errorf("array is missing ']'")
	}
	body := strings.TrimSpace(raw[1 : len(raw)-1])
	if body == "" {
		return []string{}, nil
	}
	var out []string
	for len(body) > 0 {
		body = strings.TrimSpace(body)
		if body == "" {
			break
		}
		if body[0] != '"' {
			return nil, fmt.Errorf("arrays may only contain quoted strings")
		}
		end, err := closingQuote(body)
		if err != nil {
			return nil, err
		}
		s, err := strconv.Unquote(body[:end])
		if err != nil {
			return nil, fmt.Errorf("failed to parse string: %w", err)
		}
		out = append(out, s)
		body = body[end:]
		body = strings.TrimLeft(body, " \t")
		if body == "" {
			break
		}
		if body[0] != ',' {
			return nil, fmt.Errorf("expected ',' between array items")
		}
		body = body[1:]
	}
	return out, nil
}

func closingQuote(s string) (int, error) {
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("string is missing the closing quote")
}
