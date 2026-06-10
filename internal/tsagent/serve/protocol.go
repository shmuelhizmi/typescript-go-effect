// Package serve implements the tsagent session daemon (Phase 7, plan §1.11):
// a JSON-RPC 2.0 "lite" server over newline-delimited JSON (ndjson) that
// keeps a Workspace warm and dispatches the same command registry the CLI
// uses, plus session admin methods (overlays, status, reload, shutdown).
package serve

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

// JSON-RPC error codes. Standard codes for protocol-level failures, then
// implementation-defined codes mirroring the CLI exit codes (cli/errors.go).
const (
	CodeParseError     = -32700 // line is not valid JSON
	CodeMethodNotFound = -32601 // method not in the registry
	CodeInvalidParams  = -32602 // bad flags/args (exit 2)
	CodeInternal       = -32000 // generic command failure (exit 1)
	CodeNotFound       = -32001 // target not found (exit 3)
	CodeRefused        = -32002 // mutation refused (exit 4)
	CodePartial        = -32003 // partial success (exit 5)
)

// Request is one ndjson JSON-RPC request line.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Params is the params shape for registry-dispatched methods
// ("<family>/<name>"): flag values by flag name plus positional args.
// Admin methods ("session/…") use method-specific param shapes instead.
//
// Format/Limit/Offset support `--connect` routing: when Format is "text" or
// "ndjson" the server renders the result through the same output layer the
// local CLI uses and returns {"rendered": "<bytes>"} so the client can print
// it verbatim; "json"/"" keeps the structured envelope result. Limit/Offset
// apply the CLI's list windowing server-side.
type Params struct {
	Flags  map[string]any `json:"flags,omitempty"`
	Args   []string       `json:"args,omitempty"`
	Format string         `json:"format,omitempty"`
	Limit  int            `json:"limit,omitempty"`
	Offset int            `json:"offset,omitempty"`
}

// Response is one ndjson JSON-RPC response line. Exactly one of Result and
// Error is set. Results are always structured JSON in the same envelope the
// CLI emits with --format json ({schemaVersion, result|items…}).
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object. When a handler returned a result
// alongside its error (e.g. a threshold failure that still carries the
// report), Data holds that result in the standard envelope.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// errorFromExit maps a command error to a JSON-RPC error using the CLI exit
// code mapping. Errors that already are *RPCError pass through unchanged.
func errorFromExit(err error, data json.RawMessage) *RPCError {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	code := CodeInternal
	switch cli.ExitCode(err) {
	case cli.ExitUsage:
		code = CodeInvalidParams
	case cli.ExitNotFound:
		code = CodeNotFound
	case cli.ExitRefused:
		code = CodeRefused
	case cli.ExitPartial:
		code = CodePartial
	}
	return &RPCError{Code: code, Message: err.Error(), Data: data}
}

// ExitCodeForRPC maps a JSON-RPC error code back to the CLI exit code, for
// client commands (`serve status`/`serve stop`) relaying daemon errors.
func ExitCodeForRPC(code int) int {
	switch code {
	case CodeInvalidParams:
		return cli.ExitUsage
	case CodeNotFound, CodeMethodNotFound:
		return cli.ExitNotFound
	case CodeRefused:
		return cli.ExitRefused
	case CodePartial:
		return cli.ExitPartial
	default:
		return cli.ExitFailed
	}
}
