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
// daemon; `serve status`/`stop`/`reload`/`snapshot`/`overlay` are thin RPC
// clients against a running daemon's unix socket. The spec's CLI
// auto-connect is available opt-in via the global `--connect auto|require`
// flag (cmd/tsagent), which routes whole commands through the daemon via
// serve.RouteCommand; `--connect` never applies to the serve family itself.
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
	cli.Register(cli.Command{
		Family:       "serve",
		Name:         "reload",
		Summary:      "Force a running daemon to rebuild its program",
		NeedsProgram: false,
		Flags:        serveClientFlagsFunc,
		Run:          serveClientRun("session/reload"),
	})
	cli.Register(cli.Command{
		Family:       "serve",
		Name:         "snapshot",
		Summary:      "Manage named overlay snapshots on a running daemon: save|restore|drop <name>, list",
		NeedsProgram: false,
		Flags:        serveClientFlagsFunc,
		Run:          runServeSnapshot,
	})
	cli.Register(cli.Command{
		Family:       "serve",
		Name:         "overlay",
		Summary:      "Manage overlays on a running daemon: set <file> (--from <path>|stdin), drop <file…>, list",
		NeedsProgram: false,
		Flags:        serveOverlayFlagsFunc,
		Run:          runServeOverlay,
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
		return callDaemon(path, method, nil)
	}
}

// runServeSnapshot implements `serve snapshot <save|restore|drop> <name>` and
// `serve snapshot list` as thin clients over session/snapshot/*.
func runServeSnapshot(_ context.Context, _ *core.Workspace, flags any, args []string) (any, error) {
	f := flags.(*serveClientFlags)
	if len(args) == 0 {
		return nil, cli.UsageErrorf("usage: serve snapshot <save|restore|drop> <name> | serve snapshot list")
	}
	path, err := f.resolveSocketPath()
	if err != nil {
		return nil, err
	}
	switch sub := args[0]; sub {
	case "list":
		if len(args) != 1 {
			return nil, cli.UsageErrorf("serve snapshot list takes no further arguments")
		}
		return callDaemon(path, "session/snapshot/list", nil)
	case "save", "restore", "drop":
		if len(args) != 2 {
			return nil, cli.UsageErrorf("usage: serve snapshot %s <name>", sub)
		}
		return callDaemon(path, "session/snapshot/"+sub, map[string]string{"name": args[1]})
	default:
		return nil, cli.UsageErrorf("unknown subcommand %q (want save, restore, drop, or list)", sub)
	}
}

type serveOverlayFlags struct {
	serveClientFlags
	from string

	// stdin overrides os.Stdin for `overlay set` without --from (tests).
	stdin io.Reader
}

func serveOverlayFlagsFunc(fs *flag.FlagSet) any {
	f := &serveOverlayFlags{}
	fs.StringVar(&f.socketPath, "socket-path", "", "daemon unix socket path (default: derived from the resolved tsconfig)")
	fs.StringVar(&f.from, "from", "", "read overlay content for `serve overlay set` from this file instead of stdin")
	return f
}

// runServeOverlay implements `serve overlay <set|drop|list>` as thin clients
// over session/overlays/*.
func runServeOverlay(_ context.Context, _ *core.Workspace, flags any, args []string) (any, error) {
	f := flags.(*serveOverlayFlags)
	if len(args) == 0 {
		return nil, cli.UsageErrorf("usage: serve overlay set <file> [--from <path>] | serve overlay drop <file…> | serve overlay list")
	}
	path, err := f.resolveSocketPath()
	if err != nil {
		return nil, err
	}
	switch sub := args[0]; sub {
	case "list":
		if len(args) != 1 {
			return nil, cli.UsageErrorf("serve overlay list takes no further arguments")
		}
		return callDaemon(path, "session/overlays/list", nil)
	case "set":
		if len(args) != 2 {
			return nil, cli.UsageErrorf("usage: serve overlay set <file> [--from <path>] (content from --from or stdin)")
		}
		content, err := f.readOverlayContent()
		if err != nil {
			return nil, err
		}
		return callDaemon(path, "session/overlays/set", map[string]string{"file": args[1], "content": content})
	case "drop":
		if len(args) < 2 {
			return nil, cli.UsageErrorf("usage: serve overlay drop <file…>")
		}
		return callDaemon(path, "session/overlays/drop", map[string]any{"files": args[1:]})
	default:
		return nil, cli.UsageErrorf("unknown subcommand %q (want set, drop, or list)", sub)
	}
}

func (f *serveOverlayFlags) readOverlayContent() (string, error) {
	if f.from != "" {
		data, err := os.ReadFile(f.from)
		if err != nil {
			return "", fmt.Errorf("reading --from %s: %w", f.from, err)
		}
		return string(data), nil
	}
	stdin := f.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading overlay content from stdin: %w", err)
	}
	return string(data), nil
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
func callDaemon(path string, method string, params any) (any, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, cli.Errorf(cli.ExitFailed,
			"cannot connect to daemon at %s: %v (is `tsagent serve --socket` running?)", path, err)
	}
	defer conn.Close()

	req := serve.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshaling params: %w", err)
		}
		req.Params = raw
	}
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
