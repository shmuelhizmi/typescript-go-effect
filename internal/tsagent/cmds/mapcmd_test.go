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

// ---------------------------------------------------------------------------
// map files --why

func whyTestFiles() map[string]any {
	return map[string]any{
		// Only src/a.ts is a root: the rest enter the program via imports.
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext"}, "files": ["src/a.ts"]}`,
		"/project/src/a.ts": `import { b } from "./b";
export const a = b;
`,
		"/project/src/b.ts": `import { c } from "./c";
export const b = c;
`,
		"/project/src/c.ts": `export const c = 1;
`,
	}
}

func TestMapFilesWhyChain(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, whyTestFiles())
	result, err := runMapFilesWhy(context.Background(), ws, &filesFlags{why: "src/c.ts", maxChains: 3}, nil)
	if err != nil {
		t.Fatalf("runMapFilesWhy: %v", err)
	}
	if result.Status != "imported" {
		t.Fatalf("status = %q, want imported (%+v)", result.Status, result)
	}
	if len(result.Chains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(result.Chains))
	}
	steps := result.Chains[0].Steps
	if len(steps) != 2 {
		t.Fatalf("expected a 2-hop chain a->b->c, got %d steps: %+v", len(steps), steps)
	}
	if steps[0].File != "src/a.ts" || !steps[0].Root || steps[0].Imports != "src/b.ts" {
		t.Errorf("step 0 = %+v, want root src/a.ts importing src/b.ts", steps[0])
	}
	if !strings.Contains(steps[0].Text, `"./b"`) || steps[0].Line != 1 {
		t.Errorf("step 0 line/text = %d %q, want line 1 with \"./b\"", steps[0].Line, steps[0].Text)
	}
	if steps[1].File != "src/b.ts" || steps[1].Imports != "src/c.ts" || !strings.Contains(steps[1].Text, `"./c"`) {
		t.Errorf("step 1 = %+v, want src/b.ts importing src/c.ts", steps[1])
	}
}

func TestMapFilesWhyRootAndLib(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, whyTestFiles())
	result, err := runMapFilesWhy(context.Background(), ws, &filesFlags{why: "src/a.ts", maxChains: 3}, nil)
	if err != nil {
		t.Fatalf("runMapFilesWhy(root): %v", err)
	}
	if result.Status != "root" {
		t.Errorf("status = %q, want root", result.Status)
	}

	// A lib file reports "lib"/default library.
	var libFile string
	for _, f := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(f) {
			libFile = f.FileName()
			break
		}
	}
	if libFile == "" {
		t.Fatal("no lib file in program")
	}
	result, err = runMapFilesWhy(context.Background(), ws, &filesFlags{why: libFile, maxChains: 3}, nil)
	if err != nil {
		t.Fatalf("runMapFilesWhy(lib): %v", err)
	}
	if result.Status != "lib" || !strings.Contains(result.Reason, "library") {
		t.Errorf("lib result = %+v", result)
	}
}

// ---------------------------------------------------------------------------
// map outline --detail

func outlineDetailFiles() map[string]any {
	return map[string]any{
		"/project/src/lib.ts": `/**
 * Adds two numbers.
 * Second paragraph that must not appear in the outline.
 * @param a the left operand
 */
export function add(a: number, b: number): number {
	return a + b;
}

/** The answer. */
export const answer = 42;
`,
		"/project/src/use.ts": `import { add, answer } from "./lib";
export const total = add(answer, answer);
`,
	}
}

func TestMapOutlineDetailNames(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, outlineDetailFiles())
	result, err := runMapOutline(context.Background(), ws, &outlineFlags{depth: "all", detail: "names"}, []string{"src/lib.ts"})
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	lib := findFileOutline(t, result, "src/lib.ts")
	add := findEntry(lib.Entries, "add")
	if add == nil {
		t.Fatal("missing add entry")
	}
	if add.Signature != "" {
		t.Errorf("names detail should drop signatures, got %q", add.Signature)
	}
	if add.Doc != "" {
		t.Errorf("names detail should not include docs, got %q", add.Doc)
	}
}

func TestMapOutlineDetailFull(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, outlineDetailFiles())
	ctx := context.Background()

	// --detail full adds the first JSDoc line; approxRefs only with the flag.
	result, err := runMapOutline(ctx, ws, &outlineFlags{depth: "all", detail: "full"}, []string{"src/lib.ts"})
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	lib := findFileOutline(t, result, "src/lib.ts")
	add := findEntry(lib.Entries, "add")
	if add == nil {
		t.Fatal("missing add entry")
	}
	if add.Doc != "Adds two numbers." {
		t.Errorf("add doc = %q, want first JSDoc line", add.Doc)
	}
	if answer := findEntry(lib.Entries, "answer"); answer == nil || answer.Doc != "The answer." {
		t.Errorf("answer doc = %+v, want 'The answer.'", answer)
	}
	if add.Signature == "" {
		t.Error("full detail keeps signatures")
	}
	if add.ApproxRefs != nil {
		t.Errorf("approxRefs must not be computed without --with-ref-counts, got %v", *add.ApproxRefs)
	}

	result, err = runMapOutline(ctx, ws, &outlineFlags{depth: "all", detail: "full", withRefCounts: true}, []string{"src/lib.ts"})
	if err != nil {
		t.Fatalf("runMapOutline --with-ref-counts: %v", err)
	}
	lib = findFileOutline(t, result, "src/lib.ts")
	add = findEntry(lib.Entries, "add")
	if add.ApproxRefs == nil || *add.ApproxRefs != 2 {
		t.Errorf("add approxRefs = %v, want 2 (import + call)", add.ApproxRefs)
	}
	answer := findEntry(lib.Entries, "answer")
	if answer.ApproxRefs == nil || *answer.ApproxRefs != 3 {
		t.Errorf("answer approxRefs = %v, want 3 (import + two args)", answer.ApproxRefs)
	}

	// --with-ref-counts without --detail full is a usage error.
	if _, err := runMapOutline(ctx, ws, &outlineFlags{depth: "all", withRefCounts: true}, nil); err == nil {
		t.Error("expected usage error for --with-ref-counts without --detail full")
	}
}
