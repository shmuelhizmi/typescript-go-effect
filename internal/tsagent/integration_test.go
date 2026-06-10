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
	"os"
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
		for _, want := range []string{"map", "nav", "type", "refactor", "analyze", "diagram", "check", "context", "api", "serve"} {
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

	t.Run("context pack", func(t *testing.T) {
		result := mustInvoke(t, ws, "context", "pack", "describeShape")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"=== src/shapes.ts",                               // declaration slice header
			"export function describeShape",                   // the declaration itself
			"export type Shape = Circle | Square | Triangle;", // one-hop type dep
			"estimated tokens (budget 4000)",                  // budget line (default budget)
		)
		jsonOut := render(t, result, cli.FormatJSON, 0, 0)
		assertContains(t, jsonOut, `"tokenBudget": 4000`, `"estimatedTokens"`, `"slices"`)
	})

	t.Run("nav types hierarchy", func(t *testing.T) {
		result := mustInvoke(t, ws, "nav", "types", "--name", "Dog")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"src/models.ts#Dog",
			"up",
			"Animal", "implements",
			"down",
			"Puppy", "extends",
		)
	})

	t.Run("type explain-error on the planted error", func(t *testing.T) {
		result := mustInvoke(t, ws, "type", "explain-error", "src/broken.ts:4:14")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"src/broken.ts:4:14",
			"TS2322",
			"Type 'string' is not assignable to type 'number'.",
			"drilldown:",
		)
	})

	t.Run("refactor extract dry run", func(t *testing.T) {
		result := mustInvoke(t, ws, "refactor", "extract",
			"--range", "src/shapes.ts:37:20-37:47", "--into", "constant", "--name", "circleArea")
		tx, ok := result.(*core.TxResult)
		if !ok {
			t.Fatalf("refactor extract result is %T, want *core.TxResult", result)
		}
		if tx.Applied {
			t.Error("dry-run extract must not be applied")
		}
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"-            return Math.PI * shape.radius ** 2;",
			"+            const circleArea = Math.PI * shape.radius ** 2;",
			"+            return circleArea;",
			"dry-run:",
		)
		// The dry run must not touch the disk.
		onDisk, err := ws.FileOf("src/shapes.ts")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(onDisk.Text(), "circleArea") {
			t.Error("dry-run extract modified the fixture on disk")
		}
	})

	t.Run("analyze duplicates finds the planted clone pair", func(t *testing.T) {
		result := mustInvoke(t, ws, "analyze", "duplicates")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"clone class",
			"2 members",
			"src/dup.ts", "sumPositiveSquares", "sumPositiveWeights",
		)
	})

	t.Run("edit_script", func(t *testing.T) {
		// `edit` applies by default, so it runs against a throwaway copy of
		// the fixture rather than the shared on-disk workspace.
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(fixtureDir())); err != nil {
			t.Fatalf("copying fixture: %v", err)
		}
		editWs, err := core.NewWorkspace(core.Options{Project: dir, Cwd: dir, SingleThreaded: true})
		if err != nil {
			t.Fatalf("NewWorkspace(%s): %v", dir, err)
		}

		script := strings.Join([]string{
			"# tidy the fixture in one transaction",
			"move src/shapes.ts#area before src/shapes.ts#describeShape",
			"insert after src/shapes.ts#describeShape <<EOF",
			"export function perimeterHint(): string {",
			"    return \"see area\";",
			"}",
			"EOF",
			"delete src/cycle-a.ts#neverCalled",
			"",
		}, "\n")
		scriptPath := filepath.Join(dir, "tidy.edit")
		if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}

		result := mustInvoke(t, editWs, "edit", "", scriptPath)
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"ok line 2: move src/shapes.ts#area before src/shapes.ts#describeShape",
			"ok line 3: insert after src/shapes.ts#describeShape (+3 lines)",
			"ok line 8: delete src/cycle-a.ts#neverCalled",
			"applied: 2 file(s) changed, 0 new errors, 0 fixed",
		)

		shapes, err := os.ReadFile(filepath.Join(dir, "src", "shapes.ts"))
		if err != nil {
			t.Fatal(err)
		}
		areaIdx := strings.Index(string(shapes), "export function area")
		describeIdx := strings.Index(string(shapes), "export function describeShape")
		hintIdx := strings.Index(string(shapes), "export function perimeterHint")
		if areaIdx < 0 || describeIdx < 0 || hintIdx < 0 || !(areaIdx < describeIdx && describeIdx < hintIdx) {
			t.Errorf("shapes.ts op order wrong (area=%d describe=%d hint=%d):\n%s", areaIdx, describeIdx, hintIdx, shapes)
		}
		cycleA, err := os.ReadFile(filepath.Join(dir, "src", "cycle-a.ts"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(cycleA), "neverCalled") {
			t.Errorf("cycle-a.ts still contains the deleted symbol:\n%s", cycleA)
		}

		// A fresh program over the edited copy must show no new diagnostics:
		// only the deliberately planted broken.ts error remains.
		checkWs, err := core.NewWorkspace(core.Options{Project: dir, Cwd: dir, SingleThreaded: true})
		if err != nil {
			t.Fatalf("NewWorkspace after edit: %v", err)
		}
		checkResult := mustInvoke(t, checkWs, "check", "")
		list, ok := checkResult.(cli.Lister)
		if !ok {
			t.Fatalf("check result does not implement cli.Lister: %T", checkResult)
		}
		if list.Total() != 1 {
			t.Errorf("expected only the planted diagnostic after the edit, got %d:\n%s",
				list.Total(), render(t, checkResult, cli.FormatText, 0, 0))
		}
	})

	t.Run("diagram state from the Shape union", func(t *testing.T) {
		result := mustInvoke(t, ws, "diagram", "state", "--name", "Shape")
		text := render(t, result, cli.FormatText, 0, 0)
		assertContains(t, text,
			"stateDiagram-v2",
			"circle", "square", "triangle", // the union's states
			"circle --> square: squareUp", // Shape -> Square transition
			"square --> circle: roll",     // Square -> Circle transition
		)
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
