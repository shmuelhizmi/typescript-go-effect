package cmds

import (
	"context"
	"strings"
	"testing"
)

const indexSource = `import { helper } from "./util";

export class Greeter {
	greeting: string = "hi";
	greet(name: string): string {
		return this.greeting + helper(name);
	}
}

export interface Config {
	verbose: boolean;
}

export function main(config: Config): void {
	new Greeter().greet("world");
}

const internalValue = 123;
`

const utilSource = `export function helper(s: string): string {
	return s.trim();
}
`

func testProjectFiles() map[string]any {
	return map[string]any{
		"/project/src/index.ts": indexSource,
		"/project/src/util.ts":  utilSource,
	}
}

func findFileOutline(t *testing.T, result *OutlineResult, file string) *FileOutline {
	t.Helper()
	for _, f := range result.Files {
		if f.File == file {
			return f
		}
	}
	t.Fatalf("no outline for %s (have %d files)", file, len(result.Files))
	return nil
}

func findEntry(entries []*OutlineEntry, name string) *OutlineEntry {
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	return nil
}

func TestMapOutline(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, testProjectFiles())
	result, err := runMapOutline(context.Background(), ws, &outlineFlags{depth: "all"}, nil)
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result.Files))
	}

	index := findFileOutline(t, result, "src/index.ts")

	greeter := findEntry(index.Entries, "Greeter")
	if greeter == nil {
		t.Fatal("missing Greeter entry")
	}
	if greeter.Kind != "class" || !greeter.Exported {
		t.Errorf("Greeter: kind=%q exported=%v, want class/true", greeter.Kind, greeter.Exported)
	}
	if greeter.SymbolID != "src/index.ts#Greeter" {
		t.Errorf("Greeter symbolId = %q", greeter.SymbolID)
	}
	if greeter.Line != 3 {
		t.Errorf("Greeter line = %d, want 3", greeter.Line)
	}
	greet := findEntry(greeter.Children, "greet")
	if greet == nil {
		t.Fatal("missing Greeter.greet member")
	}
	if greet.Kind != "method" || !strings.Contains(greet.Signature, "(name: string): string") {
		t.Errorf("greet: kind=%q signature=%q", greet.Kind, greet.Signature)
	}

	mainEntry := findEntry(index.Entries, "main")
	if mainEntry == nil || mainEntry.Kind != "function" {
		t.Fatalf("missing function main, got %+v", mainEntry)
	}
	if !strings.Contains(mainEntry.Signature, "(config: Config): void") {
		t.Errorf("main signature = %q", mainEntry.Signature)
	}

	internal := findEntry(index.Entries, "internalValue")
	if internal == nil || internal.Exported {
		t.Errorf("internalValue should be present and not exported, got %+v", internal)
	}
}

func TestMapOutlineFilters(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, testProjectFiles())
	ctx := context.Background()

	// exported-only drops internalValue.
	result, err := runMapOutline(ctx, ws, &outlineFlags{depth: "all", exportedOnly: true}, nil)
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	index := findFileOutline(t, result, "src/index.ts")
	if findEntry(index.Entries, "internalValue") != nil {
		t.Error("exported-only should drop internalValue")
	}

	// top-level depth drops members.
	result, err = runMapOutline(ctx, ws, &outlineFlags{depth: "top-level"}, nil)
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	index = findFileOutline(t, result, "src/index.ts")
	greeter := findEntry(index.Entries, "Greeter")
	if greeter == nil || len(greeter.Children) != 0 {
		t.Errorf("top-level should drop members, got %+v", greeter)
	}

	// kind filter keeps only classes.
	result, err = runMapOutline(ctx, ws, &outlineFlags{depth: "all", kind: "class"}, nil)
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	index = findFileOutline(t, result, "src/index.ts")
	if len(index.Entries) != 1 || index.Entries[0].Name != "Greeter" {
		t.Errorf("kind=class should keep only Greeter, got %d entries", len(index.Entries))
	}

	// path selection: only src/util.ts.
	result, err = runMapOutline(ctx, ws, &outlineFlags{depth: "all"}, []string{"src/util.ts"})
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].File != "src/util.ts" {
		t.Errorf("path filter failed: %+v", result.Files)
	}
}

func TestMapSearch(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, testProjectFiles())
	result, err := runMapSearch(context.Background(), ws, &searchFlags{}, []string{"greet"})
	if err != nil {
		t.Fatalf("runMapSearch: %v", err)
	}
	if result.Total() < 2 {
		t.Fatalf("expected at least 2 matches (Greeter, greet), got %d", result.Total())
	}
	var names []string
	var foundMethod bool
	for _, m := range result.Matches {
		names = append(names, m.Name)
		if m.Name == "greet" && m.Kind == "method" && m.SymbolID == "src/index.ts#Greeter.greet" {
			foundMethod = true
		}
	}
	if !foundMethod {
		t.Errorf("expected greet method with symbol id, matches: %v", names)
	}
	// Exact match sorts first.
	if result.Matches[0].Name != "greet" {
		t.Errorf("expected exact match first, got %q", result.Matches[0].Name)
	}
}

func TestMapSearchKindFilter(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, testProjectFiles())
	result, err := runMapSearch(context.Background(), ws, &searchFlags{kind: "class"}, []string{"greet"})
	if err != nil {
		t.Fatalf("runMapSearch: %v", err)
	}
	for _, m := range result.Matches {
		if m.Kind != "class" {
			t.Errorf("kind filter leaked %q (%s)", m.Name, m.Kind)
		}
	}
}

func TestMapFiles(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, testProjectFiles())
	result, err := runMapFiles(context.Background(), ws, &filesFlags{}, nil)
	if err != nil {
		t.Fatalf("runMapFiles: %v", err)
	}
	kinds := map[string]string{}
	roots := map[string]bool{}
	for _, f := range result.Files {
		kinds[f.File] = f.Kind
		roots[f.File] = f.Root
	}
	if kinds["src/index.ts"] != "source" || kinds["src/util.ts"] != "source" {
		t.Errorf("expected source files, got %v", kinds)
	}
	if !roots["src/index.ts"] {
		t.Error("src/index.ts should be a root file")
	}
	var libCount int
	for _, f := range result.Files {
		if f.Kind == "lib" {
			libCount++
		}
	}
	if libCount == 0 {
		t.Error("expected lib files in inventory")
	}
}

func TestMapStats(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, testProjectFiles())
	result, err := runMapStats(context.Background(), ws, &statsFlags{}, nil)
	if err != nil {
		t.Fatalf("runMapStats: %v", err)
	}
	if len(result.Dirs) != 1 || result.Dirs[0].Dir != "src" {
		t.Fatalf("expected single src dir, got %+v", result.Dirs)
	}
	stats := result.Dirs[0]
	if stats.Files != 2 {
		t.Errorf("files = %d, want 2", stats.Files)
	}
	if stats.SymbolsByKind["class"] != 1 || stats.SymbolsByKind["function"] < 2 {
		t.Errorf("symbol counts wrong: %v", stats.SymbolsByKind)
	}
	if stats.Exports < 4 {
		t.Errorf("exports = %d, want >= 4", stats.Exports)
	}
	if stats.Lines == 0 {
		t.Error("lines should be non-zero")
	}
}
