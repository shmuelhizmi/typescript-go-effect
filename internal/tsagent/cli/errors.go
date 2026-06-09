package cli

import (
	"errors"
	"fmt"

	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// Exit codes per spec §2.5.
const (
	ExitOK       = 0
	ExitFailed   = 1
	ExitUsage    = 2
	ExitNotFound = 3
	ExitRefused  = 4
	ExitPartial  = 5
)

// ExitError carries an explicit process exit code alongside an error.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Errorf creates an error that maps to the given exit code.
func Errorf(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

// UsageErrorf creates an invalid-arguments error (exit 2).
func UsageErrorf(format string, args ...any) error {
	return Errorf(ExitUsage, format, args...)
}

// NotFoundErrorf creates a target-not-found error (exit 3).
func NotFoundErrorf(format string, args ...any) error {
	return Errorf(ExitNotFound, format, args...)
}

// RefusedErrorf creates a mutation-refused error (exit 4).
func RefusedErrorf(format string, args ...any) error {
	return Errorf(ExitRefused, format, args...)
}

// PartialErrorf creates a partial-success error (exit 5).
func PartialErrorf(format string, args ...any) error {
	return Errorf(ExitPartial, format, args...)
}

// ExitCode maps an error to its process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	if errors.Is(err, core.ErrNotFound) {
		return ExitNotFound
	}
	if errors.Is(err, core.ErrInvalidArgument) {
		return ExitUsage
	}
	return ExitFailed
}
