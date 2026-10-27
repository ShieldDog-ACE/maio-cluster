// Package errs defines maio's stable error codes, exit codes and user-facing
// error formatting. Every failure surfaced by the CLI carries a code, a short
// English message and, where possible, an actionable hint.
package errs

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Code is a stable machine-readable error identifier. New codes may be added,
// but existing codes never change meaning.
type Code string

const (
	Internal     Code = "MAIO-E000"
	Usage        Code = "MAIO-E001"
	NotFound     Code = "MAIO-E002"
	Conflict     Code = "MAIO-E003"
	Permission   Code = "MAIO-E004"
	IO           Code = "MAIO-E005"
	Download     Code = "MAIO-E006"
	Verify       Code = "MAIO-E007"
	Engine       Code = "MAIO-E008"
	Hardware     Code = "MAIO-E009"
	Config       Code = "MAIO-E010"
	Service      Code = "MAIO-E011"
	Interrupted  Code = "MAIO-E012"
	Unauthorized Code = "MAIO-E013"
	RateLimited  Code = "MAIO-E014"
	Busy         Code = "MAIO-E015"
	Timeout      Code = "MAIO-E016"
)

var exitCodes = map[Code]int{
	Internal:     1,
	Usage:        2,
	NotFound:     3,
	Conflict:     4,
	Permission:   5,
	IO:           5,
	Download:     6,
	Verify:       7,
	Engine:       8,
	Hardware:     9,
	Config:       10,
	Service:      11,
	Unauthorized: 12,
	RateLimited:  13,
	Busy:         14,
	Timeout:      15,
	Interrupted:  130,
}

// Error is a maio failure with a stable code and an optional repair hint.
type Error struct {
	Code Code
	Msg  string
	Hint string
	Err  error
}

func (e *Error) Error() string { return e.Msg }

// Unwrap exposes the underlying cause to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Newf builds an error with a formatted English message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// New builds an error from a plain message.
func New(code Code, msg string) *Error {
	return &Error{Code: code, Msg: msg}
}

// Wrap adds context to a lower-level error while keeping its cause for debug
// output. The message must be plain English and must not contain a code.
func Wrap(code Code, err error, format string, args ...any) *Error {
	msg := fmt.Sprintf(format, args...)
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return &Error{Code: code, Msg: msg, Err: err}
}

// WithHint attaches a repair hint. The first hint on the chain wins.
func (e *Error) WithHint(format string, args ...any) *Error {
	if e.Hint == "" {
		e.Hint = fmt.Sprintf(format, args...)
	}
	return e
}

// As extracts the outermost maio error.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// IsCode reports whether err (or its chain) uses the given code.
func IsCode(err error, code Code) bool {
	for err != nil {
		if e, ok := err.(*Error); ok && e.Code == code {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// ExitCode maps an error to the documented process exit code.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if e := As(err); e != nil {
		if c, ok := exitCodes[e.Code]; ok {
			return c
		}
	}
	return 1
}

// HintOf walks the error chain and returns the first non-empty hint.
func HintOf(err error) string {
	for err != nil {
		if e, ok := err.(*Error); ok {
			if e.Hint != "" {
				return e.Hint
			}
			err = e.Err
			continue
		}
		break
	}
	return ""
}

// DebugEnabled reports whether cause chains should be printed.
func DebugEnabled() bool {
	return os.Getenv("MAIO_DEBUG") == "1"
}

// Print writes one error in the stable CLI format:
//
//	maio: MAIO-E006: <message>
//	hint: <actionable repair>
func Print(w io.Writer, err error) {
	if err == nil {
		return
	}
	e := As(err)
	if e == nil {
		fmt.Fprintf(w, "maio: %v\n", err)
		return
	}
	fmt.Fprintf(w, "maio: %s: %s\n", e.Code, e.Msg)
	if h := HintOf(err); h != "" {
		fmt.Fprintf(w, "hint: %s\n", h)
	}
	if DebugEnabled() && e.Err != nil {
		fmt.Fprintf(w, "cause: %v\n", e.Err)
	}
}
