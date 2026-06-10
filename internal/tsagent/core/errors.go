package core

import (
	"errors"
	"fmt"
)

// ErrNotFound indicates a file, symbol, or position that resolves to nothing
// (exit code 3 at the CLI layer).
var ErrNotFound = errors.New("not found")

// ErrInvalidArgument indicates malformed user input (exit code 2 at the CLI layer).
var ErrInvalidArgument = errors.New("invalid argument")

// ErrBadSymbolAddress marks DecodeSymbolID failures where the ID's file and
// name resolved but the ID does not address exactly one declaration: a bare
// name over merged declarations, an ordinal out of range, or a position that
// hits no declaration. Errors created with BadAddressErrorf also satisfy
// errors.Is(err, ErrNotFound) (exit code 3); callers that substitute
// "unknown symbol; closest: …" suggestions for plain misses should print
// these self-explanatory messages verbatim instead.
var ErrBadSymbolAddress = errors.New("bad symbol address")

// BadAddressErrorf creates an ErrBadSymbolAddress error (also matching
// ErrNotFound) without appending sentinel text to the message.
func BadAddressErrorf(format string, args ...any) error {
	return &badAddressError{err: fmt.Errorf(format, args...)}
}

type badAddressError struct{ err error }

func (e *badAddressError) Error() string { return e.err.Error() }
func (e *badAddressError) Unwrap() error { return e.err }
func (e *badAddressError) Is(target error) bool {
	return target == ErrBadSymbolAddress || target == ErrNotFound
}
