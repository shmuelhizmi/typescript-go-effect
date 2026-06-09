package core

import "errors"

// ErrNotFound indicates a file, symbol, or position that resolves to nothing
// (exit code 3 at the CLI layer).
var ErrNotFound = errors.New("not found")

// ErrInvalidArgument indicates malformed user input (exit code 2 at the CLI layer).
var ErrInvalidArgument = errors.New("invalid argument")
