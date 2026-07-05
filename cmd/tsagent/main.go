package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	_ "github.com/microsoft/typescript-go/internal/tsagent/cmds"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/serve"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type globalFlags struct {
	project    string
	format     string
	raw        bool
	limit      int
	offset     int
	connect    string
	socketPath string
}

// registerGlobalFlags registers the flags valid on every command.
// withSocketPath is false for the serve family, whose commands define their
// own --socket-path flag (same meaning: the daemon socket to talk to).
func registerGlobalFlags(fs *flag.FlagSet, withSocketPath bool) *globalFlags {
	g := &globalFlags{}
	fs.StringVar(&g.project, "project", "", "tsconfig.json path or directory (default: discovered from cwd)")
	fs.StringVar(&g.format, "format", "", "output format: json, text, or ndjson (default: text)")
	fs.BoolVar(&g.raw, "raw", false, "emit raw JSON instead of the default pretty text output")
	fs.IntVar(&g.limit, "limit", 0, "maximum number of list items to emit (0 = unlimited)")
	fs.IntVar(&g.offset, "offset", 0, "number of list items to skip")
	fs.StringVar(&g.connect, "connect", "never", "route through a running session daemon: never (default), auto (use it if its socket exists), require (fail if unreachable)")
	if withSocketPath {
		fs.StringVar(&g.socketPath, "socket-path", "", "daemon unix socket path for --connect routing (default: derived from the resolved tsconfig)")
	}
	return g
}

// globalFlagNames are not forwarded to the daemon when routing a command
// (--connect): they configure the client side (project resolution, output
// format/windowing, routing itself) and travel via dedicated Params fields.
var globalFlagNames = map[string]bool{
	"project":     true,
	"format":      true,
	"raw":         true,
	"limit":       true,
	"offset":      true,
	"connect":     true,
	"socket-path": true,
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return cli.ExitOK
	}

	// Hidden dynamic-completion endpoint, called by the shell scripts that
	// `tsagent completion <shell>` prints. It must bypass normal flag parsing
	// (its args are a raw, partially-typed command line).
	if args[0] == "__complete" {
		return runComplete(args[1:], stdout)
	}

	family := args[0]
	cmd, cmdArgs, ok := lookupCommand(family, args[1:])
	if !ok {
		fmt.Fprintf(stderr, "tsagent: unknown command %q; run `tsagent help` for a list\n", strings.TrimSpace(family+" "+firstOrEmpty(args[1:])))
		return cli.ExitUsage
	}

	fs := flag.NewFlagSet(strings.TrimSpace(family+" "+cmd.Name), flag.ContinueOnError)
	fs.SetOutput(stderr)
	global := registerGlobalFlags(fs, cmd.Family != "serve")
	var cmdFlags any
	if cmd.Flags != nil {
		cmdFlags = cmd.Flags(fs)
	}
	positional, err := parseInterspersed(fs, cmdArgs)
	if err != nil {
		return cli.ExitUsage
	}

	// Commands that build their own Workspace (the serve daemon and its
	// clients have NeedsProgram=false but still honor --project) receive the
	// global --project value through this optional interface.
	if pa, ok := cmdFlags.(interface{ SetProject(string) }); ok {
		pa.SetProject(global.project)
	}

	format, err := resolveFormat(global.format, global.raw)
	if err != nil {
		fmt.Fprintf(stderr, "tsagent: %v\n", err)
		return cli.ExitCode(err)
	}

	// --connect routing: optionally run the command on a live session daemon
	// instead of building a local program. The serve family always runs
	// locally (the daemon and its admin clients must not recurse into RPC).
	switch global.connect {
	case "", "never":
	case "auto", "require":
		if cmd.Family != "serve" {
			if code, handled := routeViaDaemon(cmd, fs, global, format, positional, stdout, stderr); handled {
				return code
			}
		}
	default:
		fmt.Fprintf(stderr, "tsagent: invalid --connect %q (want never, auto, or require)\n", global.connect)
		return cli.ExitUsage
	}

	ctx := context.Background()
	var ws *core.Workspace
	if cmd.NeedsProgram {
		configOnly := cmd.ConfigOnly
		if chooser, ok := cmdFlags.(interface{ ConfigOnlyWorkspace() bool }); ok && chooser.ConfigOnlyWorkspace() {
			configOnly = true
		}
		ws, err = core.NewWorkspace(core.Options{Project: global.project, ConfigOnly: configOnly})
		if err != nil {
			fmt.Fprintf(stderr, "tsagent: %v\n", err)
			return cli.ExitCode(err)
		}
	}

	result, err := cmd.Run(ctx, ws, cmdFlags, positional)
	output := &cli.Output{W: stdout, Format: format, Limit: global.limit, Offset: global.offset}
	if err != nil {
		// Commands may return a result alongside an error (e.g. a threshold
		// failure that still carries the report): print the result normally,
		// then the error, and exit nonzero.
		if !isNilResult(result) {
			if writeErr := output.Write(result); writeErr != nil {
				fmt.Fprintf(stderr, "tsagent: %v\n", writeErr)
			}
		}
		fmt.Fprintf(stderr, "tsagent: %v\n", err)
		return cli.ExitCode(err)
	}

	// Commands with no result (e.g. the serve daemon after shutdown) emit
	// nothing rather than a null envelope.
	if isNilResult(result) {
		return cli.ExitOK
	}
	if err := output.Write(result); err != nil {
		fmt.Fprintf(stderr, "tsagent: %v\n", err)
		return cli.ExitFailed
	}
	return cli.ExitOK
}

// routeViaDaemon attempts to run the command on the project's session daemon
// (--connect auto|require). handled=false means the caller should run the
// command locally (auto mode fallback); handled=true means the command was
// either routed or definitively failed, and code is the process exit code.
// Output is byte-identical to a local run: text/ndjson is rendered by the
// daemon through the same output layer; json is the same envelope.
func routeViaDaemon(cmd cli.Command, fs *flag.FlagSet, global *globalFlags, format cli.Format, args []string, stdout io.Writer, stderr io.Writer) (code int, handled bool) {
	require := global.connect == "require"
	socketPath := global.socketPath // --socket-path overrides the derived path
	if socketPath == "" {
		derived, err := daemonSocketPath(global.project)
		if err != nil {
			if require {
				fmt.Fprintf(stderr, "tsagent: --connect require: %v\n", err)
				return cli.ExitFailed, true
			}
			return 0, false
		}
		socketPath = derived
	}
	if !require {
		// auto: only attempt the daemon when its socket exists.
		if _, statErr := os.Stat(socketPath); statErr != nil {
			return 0, false
		}
	}

	method := cmd.Family
	if cmd.Name != "" {
		method += "/" + cmd.Name
	}
	params := serve.Params{
		Flags:  collectSetFlags(fs),
		Args:   args,
		Format: string(format),
		Limit:  global.limit,
		Offset: global.offset,
	}
	code, err := serve.RouteCommand(socketPath, method, params, stdout, stderr)
	if err != nil {
		if require {
			fmt.Fprintf(stderr, "tsagent: --connect require: %v\n", err)
			return cli.ExitFailed, true
		}
		fmt.Fprintf(stderr, "tsagent: session daemon unreachable (%v); running locally\n", err)
		return 0, false
	}
	return code, true
}

// daemonSocketPath resolves the project's default daemon socket without
// building a program (same derivation the daemon itself uses).
func daemonSocketPath(project string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting current directory: %w", err)
	}
	configPath, err := serve.ResolveConfigPath(osvfs.FS(), cwd, project)
	if err != nil {
		return "", err
	}
	return serve.DefaultSocketPath(configPath), nil
}

// collectSetFlags gathers the command-specific flags the user explicitly set
// (flag.Visit only walks set flags) for forwarding as RPC params; global
// flags are excluded — they travel via dedicated Params fields or stay
// client-side.
func collectSetFlags(fs *flag.FlagSet) map[string]any {
	flags := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		if globalFlagNames[f.Name] {
			return
		}
		flags[f.Name] = f.Value.String()
	})
	return flags
}

// isNilResult reports whether a handler result is nil, including a typed
// nil pointer wrapped in a non-nil interface (the common `return result, err`
// shape where result is a nil *T).
func isNilResult(result any) bool {
	if result == nil {
		return true
	}
	v := reflect.ValueOf(result)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// lookupCommand resolves `<family> <name>` or single-command families
// registered with an empty name (e.g. `check`).
func lookupCommand(family string, rest []string) (cli.Command, []string, bool) {
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		if cmd, ok := cli.Lookup(family, rest[0]); ok {
			return cmd, rest[1:], true
		}
	}
	if cmd, ok := cli.Lookup(family, ""); ok {
		return cmd, rest, true
	}
	return cli.Command{}, nil, false
}

func firstOrEmpty(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// parseInterspersed parses flags that may appear before, between, or after
// positional arguments (the plain flag package stops at the first
// non-flag argument).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	return positional, nil
}

func resolveFormat(value string, raw bool) (cli.Format, error) {
	if raw {
		if value != "" {
			return "", cli.UsageErrorf("--raw and --format are mutually exclusive")
		}
		return cli.FormatJSON, nil
	}
	if value == "" {
		return cli.FormatText, nil
	}
	return cli.ParseFormat(value)
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "tsagent — TypeScript language service CLI for agents")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: tsagent <family> <command> [flags] [args…]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, cmd := range cli.Commands() {
		fmt.Fprintf(w, "  %-16s %s\n", strings.TrimSpace(cmd.Family+" "+cmd.Name), cmd.Summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags: --project <tsconfig|dir>, --raw (JSON output), --format json|text|ndjson, --limit N, --offset N,")
	fmt.Fprintln(w, "              --connect never|auto|require (route through a running `tsagent serve` daemon),")
	fmt.Fprintln(w, "              --socket-path <path> (daemon socket for --connect; default derived from the tsconfig)")
}
