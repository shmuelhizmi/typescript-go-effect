package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	_ "github.com/microsoft/typescript-go/internal/tsagent/cmds"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type globalFlags struct {
	project string
	format  string
	limit   int
	offset  int
}

func registerGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	fs.StringVar(&g.project, "project", "", "tsconfig.json path or directory (default: discovered from cwd)")
	fs.StringVar(&g.format, "format", "", "output format: json, text, or ndjson (default: text on TTY, else json)")
	fs.IntVar(&g.limit, "limit", 0, "maximum number of list items to emit (0 = unlimited)")
	fs.IntVar(&g.offset, "offset", 0, "number of list items to skip")
	return g
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return cli.ExitOK
	}

	family := args[0]
	cmd, cmdArgs, ok := lookupCommand(family, args[1:])
	if !ok {
		fmt.Fprintf(stderr, "tsagent: unknown command %q; run `tsagent help` for a list\n", strings.TrimSpace(family+" "+firstOrEmpty(args[1:])))
		return cli.ExitUsage
	}

	fs := flag.NewFlagSet(strings.TrimSpace(family+" "+cmd.Name), flag.ContinueOnError)
	fs.SetOutput(stderr)
	global := registerGlobalFlags(fs)
	var cmdFlags any
	if cmd.Flags != nil {
		cmdFlags = cmd.Flags(fs)
	}
	positional, err := parseInterspersed(fs, cmdArgs)
	if err != nil {
		return cli.ExitUsage
	}

	format, err := resolveFormat(global.format)
	if err != nil {
		fmt.Fprintf(stderr, "tsagent: %v\n", err)
		return cli.ExitCode(err)
	}

	ctx := context.Background()
	var ws *core.Workspace
	if cmd.NeedsProgram {
		ws, err = core.NewWorkspace(core.Options{Project: global.project})
		if err != nil {
			fmt.Fprintf(stderr, "tsagent: %v\n", err)
			return cli.ExitCode(err)
		}
	}

	result, err := cmd.Run(ctx, ws, cmdFlags, positional)
	if err != nil {
		fmt.Fprintf(stderr, "tsagent: %v\n", err)
		return cli.ExitCode(err)
	}

	output := &cli.Output{W: stdout, Format: format, Limit: global.limit, Offset: global.offset}
	if err := output.Write(result); err != nil {
		fmt.Fprintf(stderr, "tsagent: %v\n", err)
		return cli.ExitFailed
	}
	return cli.ExitOK
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

func resolveFormat(value string) (cli.Format, error) {
	if value == "" {
		if term.IsTerminal(int(os.Stdout.Fd())) {
			return cli.FormatText, nil
		}
		return cli.FormatJSON, nil
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
	fmt.Fprintln(w, "Global flags: --project <tsconfig|dir>, --format json|text|ndjson, --limit N, --offset N")
}
