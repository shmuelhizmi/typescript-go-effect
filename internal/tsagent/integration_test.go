// Integration tests for the tsagent CLI: they build a Workspace over the
// real on-disk fixture project (testdata/tsagent/fixture) and exercise one
// representative command per family end-to-end through the command registry,
// exactly as cmd/tsagent/main.go dispatches them.
package tsagent_test

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/repo"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	_ "github.com/microsoft/typescript-go/internal/tsagent/cmds" // register all commands
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

func fixtureDir() string {
	return filepath.Join(repo.TestDataPath(), "tsagent", "fixture")
}

// newFixtureWorkspace builds a Workspace over the real fixture directory
// using the default (OS) file system, the same way the CLI entry point does.
func newFixtureWorkspace(t *testing.T) *core.Workspace {
	t.Helper()
	dir := fixtureDir()
	ws, err := core.NewWorkspace(core.Options{
		Project:        dir,
		Cwd:            dir,
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace(%s): %v", dir, err)
	}
	return ws
}

// invoke dispatches a command through the registry with real flag parsing,
// mirroring cmd/tsagent/main.go.
func invoke(t *testing.T, ws *core.Workspace, family string, name string, args ...string) (any, error) {
	t.Helper()
	cmd, ok := cli.Lookup(family, name)
	if !ok {
		t.Fatalf("command %q is not registered", strings.TrimSpace(family+" "+name))
	}
	fs := flag.NewFlagSet(strings.TrimSpace(family+" "+name), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags any
	if cmd.Flags != nil {
		flags = cmd.Flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parsing flags for %s %s: %v", family, name, err)
	}
	return cmd.Run(context.Background(), ws, flags, fs.Args())
}

// mustInvoke is invoke that fails the test on error.
func mustInvoke(t *testing.T, ws *core.Workspace, family string, name string, args ...string) any {
	t.Helper()
	result, err := invoke(t, ws, family, name, args...)
	if err != nil {
		t.Fatalf("%s %s %v: %v", family, name, args, err)
	}
	return result
}

// render writes a command result through the same output layer the CLI uses.
func render(t *testing.T, result any, format cli.Format, limit int, offset int) string {
	t.Helper()
	var buf bytes.Buffer
	out := &cli.Output{W: &buf, Format: format, Limit: limit, Offset: offset}
	if err := out.Write(result); err != nil {
		t.Fatalf("rendering %s output: %v", format, err)
	}
	return buf.String()
}

func assertContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q; got:\n%s", want, got)
		}
	}
}

func TestFixtureIntegration(t *testing.T) {
	ws := newFixtureWorkspace(t)

	t.Run("registry has all families", func(t *testing.T) {
		families := map[string]bool{}
		for _, cmd := range cli.Commands() {
			families[cmd.Family] = true
		}
		for _, want := range []string{"map", "nav", "type", "refactor", "analyze", "diagram", "check", "api", "serve"} {
			if !families[want] {
				t.Errorf("no commands registered for family %q", want)
			}
		}
	})

	t.Run("map outline text and json", func(t *testing.T) {
		result := mustInvoke(t, ws, "map", "outline", "src/models.ts")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"interface Animal",
			"class Dog",
			"class Puppy",
			"class Container",
			"method speak",
		)
		jsonOut := render(t, result, cli.FormatJSON, 0, 0)
		assertContains(t, jsonOut, `"schemaVersion": 1`, `"Dog"`, `"Container"`)
	})

	t.Run("map search pagination", func(t *testing.T) {
		result := mustInvoke(t, ws, "map", "search", "e")
		list, ok := result.(cli.Lister)
		if !ok {
			t.Fatalf("map search result does not implement cli.Lister: %T", result)
		}
		if list.Total() < 3 {
			t.Fatalf("expected at least 3 matches for query 'e', got %d", list.Total())
		}
		text := render(t, result, cli.FormatText, 2, 1)
		if got := strings.Count(text, "\n"); got != 3 { // 2 items + truncation notice
			t.Errorf("expected 3 lines (2 items + truncation), got %d:\n%s", got, text)
		}
		assertContains(t, text, "(showing 2 of")
	})

	t.Run("nav refs", func(t *testing.T) {
		result := mustInvoke(t, ws, "nav", "refs", "--name", "Dog")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"src/index.ts",  // import + construction site
			"src/models.ts", // extends clause
			"call",
			"import",
		)
	})

	t.Run("nav refs missing name exits 3", func(t *testing.T) {
		_, err := invoke(t, ws, "nav", "refs", "--name", "TotallyBogusSymbol")
		if err == nil {
			t.Fatal("expected an error for a bogus --name")
		}
		if code := cli.ExitCode(err); code != cli.ExitNotFound {
			t.Errorf("expected exit code %d (not found), got %d (%v)", cli.ExitNotFound, code, err)
		}
	})

	t.Run("type at", func(t *testing.T) {
		result := mustInvoke(t, ws, "type", "at", "--name", "describeShape")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text, "(shape: Shape) => string", "src/shapes.ts#describeShape")
	})

	t.Run("type instantiations of generic", func(t *testing.T) {
		result := mustInvoke(t, ws, "type", "instantiations", "src/models.ts#Container")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text, "2 distinct instantiations", "Container<string>", "Container<number>")
	})

	t.Run("refactor rename dry run", func(t *testing.T) {
		result := mustInvoke(t, ws, "refactor", "rename", "--name", "Dog", "Hound")
		tx, ok := result.(*core.TxResult)
		if !ok {
			t.Fatalf("refactor rename result is %T, want *core.TxResult", result)
		}
		if tx.Applied {
			t.Error("dry-run rename must not be applied")
		}
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"-export class Dog implements Animal {",
			"+export class Hound implements Animal {",
			"-export class Puppy extends Dog {",
			"+export class Puppy extends Hound {",
			"dry-run:",
		)
		// The dry run must not touch the disk.
		onDisk, err := ws.FileOf("src/models.ts")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(onDisk.Text(), "export class Dog implements Animal") {
			t.Error("dry-run rename modified the fixture on disk")
		}
	})

	t.Run("analyze exhaustiveness", func(t *testing.T) {
		result := mustInvoke(t, ws, "analyze", "exhaustiveness")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text, "src/shapes.ts", `missing: "triangle"`)
		if strings.Contains(text, "area") {
			t.Errorf("exhaustive switch in area() was flagged:\n%s", text)
		}
	})

	t.Run("analyze dead-code", func(t *testing.T) {
		result := mustInvoke(t, ws, "analyze", "dead-code")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text, "neverCalled", "src/cycle-a.ts")
	})

	t.Run("analyze unused-deps", func(t *testing.T) {
		result := mustInvoke(t, ws, "analyze", "unused-deps")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text, "left-pad")
	})

	t.Run("diagram deps mermaid cycle", func(t *testing.T) {
		result := mustInvoke(t, ws, "diagram", "deps")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"flowchart LR",
			"src_cycle_a_ts --> src_cycle_b_ts",
			"src_cycle_b_ts --> src_cycle_a_ts",
			"linkStyle", // cycle edges are highlighted
		)
	})

	t.Run("check finds the planted error", func(t *testing.T) {
		result := mustInvoke(t, ws, "check", "")
		list, ok := result.(cli.Lister)
		if !ok {
			t.Fatalf("check result does not implement cli.Lister: %T", result)
		}
		if list.Total() != 1 {
			t.Errorf("expected exactly 1 diagnostic (the planted one), got %d", list.Total())
		}
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text, "src/broken.ts:4:14", "TS2322")
		ndjson := render(t, result, cli.FormatNDJSON, 0, 0)
		lines := strings.Split(strings.TrimSpace(ndjson), "\n")
		if len(lines) != 2 { // one diagnostic + the trailing envelope
			t.Errorf("expected 2 ndjson lines, got %d:\n%s", len(lines), ndjson)
		}
		assertContains(t, lines[len(lines)-1], `"schemaVersion":1`, `"total":1`)
	})

	t.Run("api surface", func(t *testing.T) {
		result := mustInvoke(t, ws, "api", "surface", "src/index.ts")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text, "digest sha256:", "function main", "function area")
	})
}

// TestSubprocessSmoke runs the real binary via `go run` against the fixture.
func TestSubprocessSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess smoke test in -short mode")
	}
	cmd := exec.Command("go", "run", "./cmd/tsagent",
		"map", "outline", "--project", fixtureDir(),
		filepath.Join(fixtureDir(), "src", "models.ts"))
	cmd.Dir = repo.RootPath()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run ./cmd/tsagent map outline: %v\n%s", err, out)
	}
	assertContains(t, string(out), "class Dog", "interface Animal", "[export]")
}
