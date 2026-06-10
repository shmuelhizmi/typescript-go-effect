package cmds

import (
	"context"
	"fmt"
	"slices"
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

// ---------------------------------------------------------------------------
// map outline --locals / --symbol

const serverSource = `function createApp(): { listen(port: number): string } {
	return { listen: (port) => "listening:" + port };
}
const isDev = true;

/** Starts the http server. */
export function startServer(port: number): string {
	const app = createApp();
	function logRequest(req: string): void { const tag = req; }
	if (isDev) { const banner = "dev mode"; }
	return app.listen(port);
}

export interface Route { path: string }

export class Router {
	routes: Route[] = [];
	add(route: Route): string { const key = route.path; this.routes.push(route); return key; }
}

const handler = (req: string): number => {
	function parse(body: string): number { return body.length; }
	return parse(req);
};
`

func localsTestFiles() map[string]any {
	return map[string]any{"/project/src/server.ts": serverSource}
}

func outlineText(t *testing.T, result *OutlineResult) string {
	t.Helper()
	var sb strings.Builder
	for i := range result.Files {
		if err := result.WriteItemText(&sb, result.Item(i)); err != nil {
			t.Fatalf("WriteItemText: %v", err)
		}
	}
	return sb.String()
}

func TestMapOutlineDefaultUnchangedByLocalsSupport(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, localsTestFiles())
	result, err := runMapOutline(context.Background(), ws, &outlineFlags{depth: "all"}, nil)
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	server := findFileOutline(t, result, "src/server.ts")

	// Without --locals no function entry has children and no local appears.
	if e := findEntry(server.Entries, "startServer"); e == nil || len(e.Children) != 0 {
		t.Errorf("default outline must not descend into function bodies, got %+v", e)
	}
	if add := findEntry(findEntry(server.Entries, "Router").Children, "add"); add == nil || len(add.Children) != 0 {
		t.Errorf("default outline must not descend into method bodies, got %+v", add)
	}
	text := outlineText(t, result)
	for _, local := range []string{"app", "logRequest", "banner", "key", "parse", "tag"} {
		if strings.Contains(text, " "+local+" ") {
			t.Errorf("default text output leaked local %q:\n%s", local, text)
		}
	}
	// Top-level and member symbol IDs are unchanged.
	if e := findEntry(server.Entries, "startServer"); e.SymbolID != "src/server.ts#startServer" {
		t.Errorf("startServer symbolId = %q", e.SymbolID)
	}
	if e := findEntry(findEntry(server.Entries, "Router").Children, "add"); e.SymbolID != "src/server.ts#Router.add" {
		t.Errorf("Router.add symbolId = %q", e.SymbolID)
	}
}

func TestMapOutlineLocals(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, localsTestFiles())
	result, err := runMapOutline(context.Background(), ws, &outlineFlags{depth: "all", locals: true}, nil)
	if err != nil {
		t.Fatalf("runMapOutline --locals: %v", err)
	}
	server := findFileOutline(t, result, "src/server.ts")

	start := findEntry(server.Entries, "startServer")
	if start == nil {
		t.Fatal("missing startServer entry")
	}
	wantChildren := []struct {
		name, kind, id string
	}{
		{"app", "const", "src/server.ts#startServer.app"},
		{"logRequest", "function", "src/server.ts#startServer.logRequest"},
		{"banner", "const", "src/server.ts#startServer.banner"},
	}
	if len(start.Children) != len(wantChildren) {
		t.Fatalf("startServer children = %d, want %d (%+v)", len(start.Children), len(wantChildren), start.Children)
	}
	for i, want := range wantChildren {
		got := start.Children[i]
		if got.Name != want.name || got.Kind != want.kind || got.SymbolID != want.id {
			t.Errorf("startServer child %d = %s/%s/%s, want %s/%s/%s",
				i, got.Kind, got.Name, got.SymbolID, want.kind, want.name, want.id)
		}
		if got.Exported {
			t.Errorf("local %s must not be exported", got.Name)
		}
	}
	// A local function's own locals appear one level deeper.
	logRequest := findEntry(start.Children, "logRequest")
	if tag := findEntry(logRequest.Children, "tag"); tag == nil || tag.SymbolID != "src/server.ts#startServer.logRequest.tag" {
		t.Errorf("logRequest.tag = %+v, want nested symbol id", tag)
	}

	// Method locals.
	add := findEntry(findEntry(server.Entries, "Router").Children, "add")
	if key := findEntry(add.Children, "key"); key == nil || key.SymbolID != "src/server.ts#Router.add.key" {
		t.Errorf("Router.add.key = %+v, want src/server.ts#Router.add.key", key)
	}

	// Named-arrow const declarator scopes.
	handlerEntry := findEntry(server.Entries, "handler")
	if parse := findEntry(handlerEntry.Children, "parse"); parse == nil || parse.SymbolID != "src/server.ts#handler.parse" {
		t.Errorf("handler.parse = %+v, want src/server.ts#handler.parse", parse)
	}
}

func TestMapOutlineLocalsDepth(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, localsTestFiles())
	ctx := context.Background()

	// depth 1 keeps the top level only: no locals at all.
	result, err := runMapOutline(ctx, ws, &outlineFlags{depth: "1", locals: true}, nil)
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	server := findFileOutline(t, result, "src/server.ts")
	if e := findEntry(server.Entries, "startServer"); len(e.Children) != 0 {
		t.Errorf("depth=1 must drop locals, got %+v", e.Children)
	}

	// depth 2 keeps direct locals but not their nested locals.
	result, err = runMapOutline(ctx, ws, &outlineFlags{depth: "2", locals: true}, nil)
	if err != nil {
		t.Fatalf("runMapOutline: %v", err)
	}
	server = findFileOutline(t, result, "src/server.ts")
	start := findEntry(server.Entries, "startServer")
	if len(start.Children) == 0 {
		t.Fatal("depth=2 should keep direct locals")
	}
	if logRequest := findEntry(start.Children, "logRequest"); len(logRequest.Children) != 0 {
		t.Errorf("depth=2 must drop nested locals, got %+v", logRequest.Children)
	}
}

func TestMapOutlineSymbolFunction(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, localsTestFiles())
	// --symbol implies locals for the subtree even without --locals.
	result, err := runMapOutlineSymbol(context.Background(), ws, &outlineFlags{depth: "all", symbol: "src/server.ts#startServer"}, nil)
	if err != nil {
		t.Fatalf("runMapOutlineSymbol: %v", err)
	}
	if result.SymbolID != "src/server.ts#startServer" || result.Kind != "function" || result.Name != "startServer" {
		t.Errorf("header = %s/%s/%s", result.SymbolID, result.Kind, result.Name)
	}
	var names []string
	for _, e := range result.Entries {
		names = append(names, e.Name)
	}
	if want := []string{"app", "logRequest", "banner"}; !slices.Equal(names, want) {
		t.Errorf("entries = %v, want %v", names, want)
	}
	var sb strings.Builder
	if err := result.WriteText(&sb); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	text := sb.String()
	if !strings.HasPrefix(text, fmt.Sprintf("src/server.ts#startServer  function  (%d-%d)\n", result.Line, result.EndLine)) {
		t.Errorf("text header mismatch:\n%s", text)
	}
	if !strings.Contains(text, "function logRequest") {
		t.Errorf("text body missing locals:\n%s", text)
	}
}

func TestMapOutlineSymbolClass(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, localsTestFiles())
	result, err := runMapOutlineSymbol(context.Background(), ws, &outlineFlags{depth: "all", symbol: "src/server.ts#Router"}, nil)
	if err != nil {
		t.Fatalf("runMapOutlineSymbol: %v", err)
	}
	if result.Kind != "class" {
		t.Errorf("kind = %q, want class", result.Kind)
	}
	routes := findEntry(result.Entries, "routes")
	if routes == nil || routes.Kind != "property" {
		t.Errorf("routes = %+v, want property member", routes)
	}
	add := findEntry(result.Entries, "add")
	if add == nil || add.Kind != "method" {
		t.Fatalf("add = %+v, want method member", add)
	}
	// --symbol implies locals: the method's locals are included.
	if key := findEntry(add.Children, "key"); key == nil || key.SymbolID != "src/server.ts#Router.add.key" {
		t.Errorf("add.key = %+v, want src/server.ts#Router.add.key", key)
	}
}

func TestMapOutlineSymbolDepth(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, localsTestFiles())
	result, err := runMapOutlineSymbol(context.Background(), ws, &outlineFlags{depth: "1", symbol: "src/server.ts#startServer"}, nil)
	if err != nil {
		t.Fatalf("runMapOutlineSymbol: %v", err)
	}
	if len(result.Entries) == 0 {
		t.Fatal("depth=1 should keep the symbol's direct children")
	}
	if logRequest := findEntry(result.Entries, "logRequest"); logRequest == nil || len(logRequest.Children) != 0 {
		t.Errorf("depth=1 must drop the locals of nested functions, got %+v", logRequest)
	}
}

func TestMapOutlineSymbolUsageErrors(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, localsTestFiles())
	ctx := context.Background()
	if _, err := runMapOutlineSymbol(ctx, ws, &outlineFlags{depth: "all", symbol: "src/server.ts#startServer"}, []string{"src/server.ts"}); err == nil {
		t.Error("expected usage error for --symbol with path arguments")
	}
	if _, err := runMapOutlineSymbol(ctx, ws, &outlineFlags{depth: "all", detail: "full", withRefCounts: true, symbol: "src/server.ts#startServer"}, nil); err == nil {
		t.Error("expected usage error for --symbol with --with-ref-counts")
	}
	if _, err := runMapOutlineSymbol(ctx, ws, &outlineFlags{depth: "all", symbol: "src/server.ts#noSuchThing"}, nil); err == nil {
		t.Error("expected not-found error for unknown --symbol")
	}
}
