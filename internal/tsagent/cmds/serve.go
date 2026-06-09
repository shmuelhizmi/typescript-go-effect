package cmds

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/serve"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

// The serve family (spec §4.10, plan §1.11): `serve` starts the session
// daemon, `serve status`/`serve stop` are thin RPC clients against a running
// daemon's unix socket. The spec's CLI auto-connect (every command finding a
// running daemon transparently) is explicitly deferred per plan §1.11.
func init() {
	cli.Register(cli.Command{
		Family:       "serve",
		Name:         "",
		Summary:      "Start the session daemon (ndjson JSON-RPC over --stdio or a unix socket)",
		NeedsProgram: false, // the daemon builds and owns its own Workspace
		Flags: func(fs *flag.FlagSet) any {
			f := &serveDaemonFlags{}
			fs.BoolVar(&f.stdio, "stdio", false, "serve ndjson JSON-RPC over stdin/stdout (logs go to stderr)")
			fs.BoolVar(&f.socket, "socket", false, "serve over a unix socket (default mode; path printed to stdout)")
			fs.StringVar(&f.socketPath, "socket-path", "", "unix socket path (default: $TMPDIR/tsagent-<hash(tsconfig)>.sock)")
			return f
		},
		Run: runServeDaemon,
	})
	cli.Register(cli.Command{
		Family:       "serve",
		Name:         "status",
		Summary:      "Query a running session daemon (memory, program size, overlays, rebuilds)",
		NeedsProgram: false,
		Flags:        serveClientFlagsFunc,
		Run:          serveClientRun("session/status"),
	})
	cli.Register(cli.Command{
		Family:       "serve",
		Name:         "stop",
		Summary:      "Shut down a running session daemon",
		NeedsProgram: false,
		Flags:        serveClientFlagsFunc,
		Run:          serveClientRun("session/shutdown"),
	})
}

type serveDaemonFlags struct {
	stdio      bool
	socket     bool
	socketPath string

	// project is injected by the entry point from the global --project flag
	// (via the SetProject interface) — the daemon has no Workspace handed to
	// it, so it must resolve the project itself.
	project string

	// Test hooks (zero values mean real OS defaults).
	fs             vfs.FS
	cwd            string
	stat           serve.StatFunc
	singleThreaded bool
	stdin          io.Reader
	stdout         io.Writer
	stderr         io.Writer
}

// SetProject receives the global --project flag from the entry point.
func (f *serveDaemonFlags) SetProject(project string) { f.project = project }

func runServeDaemon(ctx context.Context, _ *core.Workspace, flags any, args []string) (any, error) {
	f := flags.(*serveDaemonFlags)
	if len(args) > 0 {
		return nil, cli.UsageErrorf("serve takes no positional arguments (got %q)", args[0])
	}
	if f.stdio && (f.socket || f.socketPath != "") {
		return nil, cli.UsageErrorf("--stdio and --socket/--socket-path are mutually exclusive")
	}
	stdin, stdout, stderr := f.stdin, f.stdout, f.stderr
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	session, err := serve.NewSession(serve.SessionOptions{
		Project:        f.project,
		Cwd:            f.cwd,
		FS:             f.fs,
		Stat:           f.stat,
		SingleThreaded: f.singleThreaded,
	})
	if err != nil {
		return nil, err
	}
	server := serve.NewServer(session, stderr)

	if f.stdio {
		err := server.ServeStream(ctx, stdin, stdout)
		if errors.Is(err, serve.ErrShutdown) {
			err = nil
		}
		return nil, err
	}

	path := f.socketPath
	if path == "" {
		path = serve.DefaultSocketPath(session.ConfigPath())
	}
	ln, err := serve.Listen(path)
	if err != nil {
		return nil, err
	}
	// Announce the socket path only once the listener is live.
	fmt.Fprintln(stdout, path)
	return nil, server.Serve(ctx, ln, path)
}

// ---------------------------------------------------------------------------
// Client mode (serve status / serve stop)

type serveClientFlags struct {
	socketPath string
	project    string

	// Test hooks for default-socket-path resolution.
	fs  vfs.FS
	cwd string
}

func (f *serveClientFlags) SetProject(project string) { f.project = project }

func serveClientFlagsFunc(fs *flag.FlagSet) any {
	f := &serveClientFlags{}
	fs.StringVar(&f.socketPath, "socket-path", "", "daemon unix socket path (default: derived from the resolved tsconfig)")
	return f
}

func serveClientRun(method string) func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
	return func(ctx context.Context, _ *core.Workspace, flags any, args []string) (any, error) {
		f := flags.(*serveClientFlags)
		if len(args) > 0 {
			return nil, cli.UsageErrorf("serve %s takes no positional arguments (got %q)", method, args[0])
		}
		path, err := f.resolveSocketPath()
		if err != nil {
			return nil, err
		}
		return callDaemon(path, method)
	}
}

func (f *serveClientFlags) resolveSocketPath() (string, error) {
	if f.socketPath != "" {
		return f.socketPath, nil
	}
	fs := f.fs
	if fs == nil {
		fs = osvfs.FS()
	}
	cwd := f.cwd
	if cwd == "" {
		osCwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting current directory: %w", err)
		}
		cwd = osCwd
	}
	configPath, err := serve.ResolveConfigPath(fs, cwd, f.project)
	if err != nil {
		return "", err
	}
	return serve.DefaultSocketPath(configPath), nil
}

// callDaemon performs one ndjson JSON-RPC round-trip over the unix socket
// and returns the daemon's result with its {schemaVersion, result} envelope
// unwrapped (the entry point re-wraps it, so output matches direct runs).
func callDaemon(path string, method string) (any, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, cli.Errorf(cli.ExitFailed,
			"cannot connect to daemon at %s: %v (is `tsagent serve --socket` running?)", path, err)
	}
	defer conn.Close()

	req := serve.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("writing to daemon: %w", err)
	}
	line, err := bufio.NewReaderSize(conn, 1<<20).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, fmt.Errorf("reading daemon response: %w", err)
	}
	var resp serve.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("invalid daemon response: %w", err)
	}
	if resp.Error != nil {
		return nil, cli.Errorf(serve.ExitCodeForRPC(resp.Error.Code), "daemon: %s", resp.Error.Message)
	}
	var envelope struct {
		SchemaVersion int             `json:"schemaVersion"`
		Result        json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(resp.Result, &envelope); err == nil && len(envelope.Result) > 0 {
		return envelope.Result, nil
	}
	return resp.Result, nil
}
