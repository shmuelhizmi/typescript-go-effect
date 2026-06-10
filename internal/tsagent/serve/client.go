package serve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

// RouteCommand runs one registry command ("family/name" or bare "family")
// against a running daemon at socketPath and prints the result exactly as a
// local run would: for text/ndjson the server renders through the CLI output
// layer and the {"rendered": …} bytes are printed verbatim; for json the
// structured envelope is re-indented to match local `--format json` output.
//
// The returned error is non-nil only for transport-level failures (dial,
// write, read, malformed response) so callers can fall back to a local run
// (--connect auto). Command-level failures are printed to stderr (any partial
// result to stdout) and reflected in the returned exit code with err == nil.
func RouteCommand(socketPath string, method string, params Params, stdout io.Writer, stderr io.Writer) (int, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return cli.ExitFailed, fmt.Errorf("connecting to daemon at %s: %w", socketPath, err)
	}
	defer conn.Close()

	rawParams, err := json.Marshal(params)
	if err != nil {
		return cli.ExitFailed, fmt.Errorf("marshaling params: %w", err)
	}
	req := Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: rawParams}
	data, err := json.Marshal(req)
	if err != nil {
		return cli.ExitFailed, fmt.Errorf("marshaling request: %w", err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return cli.ExitFailed, fmt.Errorf("writing to daemon: %w", err)
	}
	line, err := bufio.NewReaderSize(conn, 1<<20).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return cli.ExitFailed, fmt.Errorf("reading daemon response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return cli.ExitFailed, fmt.Errorf("invalid daemon response: %w", err)
	}

	wantRendered := params.Format != "" && params.Format != string(cli.FormatJSON)
	if resp.Error != nil {
		// Mirror the local entry point: a partial result (error data) prints
		// normally, then the error goes to stderr with the mapped exit code.
		if len(resp.Error.Data) > 0 {
			if err := printRouted(stdout, resp.Error.Data, wantRendered); err != nil {
				return cli.ExitFailed, err
			}
		}
		fmt.Fprintf(stderr, "tsagent: %s\n", resp.Error.Message)
		return ExitCodeForRPC(resp.Error.Code), nil
	}
	if err := printRouted(stdout, resp.Result, wantRendered); err != nil {
		return cli.ExitFailed, err
	}
	return cli.ExitOK, nil
}

// printRouted prints one routed result: server-rendered text/ndjson bytes
// verbatim, otherwise the JSON envelope indented exactly as the local output
// layer would emit it.
func printRouted(w io.Writer, raw json.RawMessage, wantRendered bool) error {
	if wantRendered {
		var r renderedResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("invalid rendered result from daemon: %w", err)
		}
		_, err := io.WriteString(w, r.Rendered)
		return err
	}
	// JSON envelope: the wire bytes are compacted by json.Marshal on the
	// daemon side, so re-indent to match `cli.Output{Format: json}`.
	var buf bytes.Buffer
	out := raw
	if err := json.Indent(&buf, raw, "", "  "); err == nil {
		out = buf.Bytes()
	}
	if _, err := w.Write(out); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
