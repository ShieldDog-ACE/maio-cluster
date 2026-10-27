// Package errs provides stable, actionable errors for maio v4.
//
// Every error carries a stable code, a numeric process exit status and an
// optional repair hint. The codes are part of the public contract: scripts may
// rely on the exit status even when the human text changes.
package errs

import (
	"errors"
	"fmt"
	"os"
)

// Code is a stable error class.
type Code int

const (
	Internal    Code = 1
	Usage       Code = 2
	NotFound    Code = 4
	Permission  Code = 5
	Network     Code = 6
	Integrity   Code = 7
	Conflict    Code = 8
	Hardware    Code = 9
	Config      Code = 10
	Unavailable Code = 11
	Auth        Code = 13
	Timeout     Code = 14
	Canceled    Code = 130
)

// String returns the stable MAIO-E code.
func (c Code) String() string {
	names := map[Code]string{
		Internal:    "MAIO-E000",
		Usage:       "MAIO-E001",
		NotFound:    "MAIO-E002",
		Permission:  "MAIO-E003",
		Network:     "MAIO-E004",
		Integrity:   "MAIO-E005",
		Conflict:    "MAIO-E006",
		Hardware:    "MAIO-E007",
		Config:      "MAIO-E008",
		Unavailable: "MAIO-E009",
		Auth:        "MAIO-E010",
		Timeout:     "MAIO-E011",
		Canceled:    "MAIO-E012",
	}
	if s, ok := names[c]; ok {
		return s
	}
	return "MAIO-E000"
}

// ExitCode returns the process status for a code.
func (c Code) ExitCode() int {
	if c == Canceled {
		return 130
	}
	if c < 1 || c > 14 {
		return 1
	}
	return int(c)
}

// Error is an actionable maio error.
type Error struct {
	Code  Code
	Msg   string
	Hints []string
	Cause error
}

// Error implements error.
func (e *Error) Error() string {
	msg := fmt.Sprintf("%s: %s", e.Code, e.Msg)
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.Cause }

// WithHint adds one repair hint.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.Hints = append(e.Hints, fmt.Sprintf(format, args...))
	return e
}

// New builds an error.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Newf is an alias kept for readability at call sites.
func Newf(code Code, format string, args ...any) *Error { return New(code, format, args...) }

// Wrap attaches a cause.
func Wrap(code Code, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...), Cause: cause}
}

// ExitCode extracts the process status, defaulting to 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code.ExitCode()
	}
	return 1
}

// CodeOf extracts the stable code.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Internal
}

// Print writes one error and its hints to w.
func Print(w *os.File, err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(w, "maio: "+err.Error())
	var e *Error
	if errors.As(err, &e) {
		for i, h := range e.Hints {
			if i == 0 {
				fmt.Fprintln(w, "  hint:")
			}
			fmt.Fprintln(w, "    - "+h)
		}
	}
}
