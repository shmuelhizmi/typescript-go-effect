package cmds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

const navUtilSource = `export interface Options {
	verbose: boolean;
}

export function helper(s: string, opts?: Options): string {
	return s;
}

export let counter = 0;

export function bump(): void {
	counter = 1;
	counter++;
	counter += 2;
	const x = counter;
	void x;
}
`

const navIndexSource = `import { helper, Options } from "./util";

export function main(opts: Options): string {
	return helper("hi");
}
`

const navShapesSource = `export interface Shape {
	area(): number;
}

export class Circle implements Shape {
	area(): number {
		return 1;
	}
}

export function makeShape(s: Shape): number {
	return s.area();
}

export class Greeter {
	greet(name: string): string {
		return name;
	}
}

export function run(): string {
	const g = new Greeter();
	return g.greet("x");
}
`

const navChainSource = `export function a(): void {
	b();
}

export function b(): void {
	c();
}

export function c(): void {}

export function loopA(): void {
	loopB();
}

export function loopB(): void {
	loopA();
}
`

const navUnusedImportSource = `import { helper } from "./util";

export {};
`

func navProjectFiles() map[string]any {
	return map[string]any{
		"/project/src/util.ts":   navUtilSource,
		"/project/src/index.ts":  navIndexSource,
		"/project/src/shapes.ts": navShapesSource,
		"/project/src/chain.ts":  navChainSource,
		"/project/src/unused.ts": navUnusedImportSource,
	}
}

// targetAt renders the file:line:col target of the first occurrence of
// needle in the given fixture file.
func targetAt(t *testing.T, files map[string]any, path string, needle string) string {
	t.Helper()
	src, ok := files[path].(string)
	if !ok {
		t.Fatalf("no fixture file %s", path)
	}
	idx := strings.Index(src, needle)
	if idx < 0 {
		t.Fatalf("needle %q not found in %s", needle, path)
	}
	line := 1 + strings.Count(src[:idx], "\n")
	col := idx - strings.LastIndex(src[:idx], "\n")
	return fmt.Sprintf("%s:%d:%d", strings.TrimPrefix(path, "/project/"), line, col)
}

// ---------------------------------------------------------------------------
// nav def

func TestNavDefBatch(t *testing.T) {
	t.Parallel()
	files := navProjectFiles()
	ws := newTestWorkspace(t, files)
	result, err := runNavDef(context.Background(), ws, &defFlags{}, []string{
		targetAt(t, files, "/project/src/index.ts", `helper("hi")`),
		targetAt(t, files, "/project/src/index.ts", "Options): string"),
	})
	if err != nil {
		t.Fatalf("runNavDef: %v", err)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(result.Targets))
	}

	helperDefs := result.Targets[0].Definitions
	if len(helperDefs) != 1 {
		t.Fatalf("helper: expected 1 definition, got %d", len(helperDefs))
	}
	if helperDefs[0].File != "src/util.ts" || helperDefs[0].Line != 5 {
		t.Errorf("helper def = %s:%d, want src/util.ts:5", helperDefs[0].File, helperDefs[0].Line)
	}
	if !strings.Contains(helperDefs[0].Preview, "export function helper") {
		t.Errorf("helper def preview = %q", helperDefs[0].Preview)
	}
	if helperDefs[0].EndLine < helperDefs[0].Line || helperDefs[0].EndCol <= 0 {
		t.Errorf("helper def end range invalid: %+v", helperDefs[0])
	}

	optionsDefs := result.Targets[1].Definitions
	if len(optionsDefs) != 1 || optionsDefs[0].File != "src/util.ts" || optionsDefs[0].Line != 1 {
		t.Errorf("Options defs = %+v, want src/util.ts:1", optionsDefs)
	}
}

func TestNavDefBySymbol(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	result, err := runNavDef(context.Background(), ws, &defFlags{symbol: "src/util.ts#helper"}, nil)
	if err != nil {
		t.Fatalf("runNavDef: %v", err)
	}
	if len(result.Targets) != 1 || len(result.Targets[0].Definitions) != 1 {
		t.Fatalf("unexpected result shape: %+v", result.Targets)
	}
	if def := result.Targets[0].Definitions[0]; def.File != "src/util.ts" || def.Line != 5 {
		t.Errorf("def = %s:%d, want src/util.ts:5", def.File, def.Line)
	}
}

func TestNavDefTypeDefinition(t *testing.T) {
	t.Parallel()
	files := navProjectFiles()
	ws := newTestWorkspace(t, files)
	result, err := runNavDef(context.Background(), ws, &defFlags{typeDefinition: true}, []string{
		targetAt(t, files, "/project/src/shapes.ts", "g.greet"),
	})
	if err != nil {
		t.Fatalf("runNavDef: %v", err)
	}
	defs := result.Targets[0].Definitions
	if len(defs) == 0 {
		t.Fatal("expected a type definition for g")
	}
	if defs[0].File != "src/shapes.ts" || !strings.Contains(defs[0].Preview, "class Greeter") {
		t.Errorf("type def = %+v, want Greeter class", defs[0])
	}
}

func TestNavDefImplementations(t *testing.T) {
	t.Parallel()
	files := navProjectFiles()
	ws := newTestWorkspace(t, files)
	result, err := runNavDef(context.Background(), ws, &defFlags{implementations: true}, []string{
		targetAt(t, files, "/project/src/shapes.ts", "Shape): number"),
	})
	if err != nil {
		t.Fatalf("runNavDef: %v", err)
	}
	defs := result.Targets[0].Definitions
	if len(defs) == 0 {
		t.Fatal("expected implementations of Shape")
	}
	found := false
	for _, d := range defs {
		if d.File == "src/shapes.ts" && strings.Contains(d.Preview, "Circle") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Circle among implementations, got %+v", defs)
	}
}

// ---------------------------------------------------------------------------
// nav refs

func refsByKind(refs []*Ref) map[string][]*Ref {
	byKind := make(map[string][]*Ref)
	for _, r := range refs {
		byKind[r.UsageKind] = append(byKind[r.UsageKind], r)
	}
	return byKind
}

func TestNavRefsClassification(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	ctx := context.Background()

	// counter: 3 writes (=, ++, +=) and 1 read; declaration excluded by default.
	result, err := runNavRefs(ctx, ws, &refsFlags{name: "counter", groupBy: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs(counter): %v", err)
	}
	if result.Counts["write"] != 3 || result.Counts["read"] != 1 {
		t.Errorf("counter counts = %v, want write:3 read:1", result.Counts)
	}
	if result.Counts["declaration"] != 0 {
		t.Errorf("declaration should be excluded by default, counts = %v", result.Counts)
	}
	if result.Totals != 4 || len(result.Refs) != 4 {
		t.Errorf("counter total = %d refs = %d, want 4/4", result.Totals, len(result.Refs))
	}

	// helper: import in index.ts and unused.ts, call in index.ts.
	result, err = runNavRefs(ctx, ws, &refsFlags{name: "helper", groupBy: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs(helper): %v", err)
	}
	if result.Counts["import"] != 2 || result.Counts["call"] != 1 {
		t.Errorf("helper counts = %v, want import:2 call:1", result.Counts)
	}
	byKind := refsByKind(result.Refs)
	if len(byKind["call"]) != 1 || byKind["call"][0].File != "src/index.ts" {
		t.Errorf("helper call refs = %+v", byKind["call"])
	}
	if !strings.Contains(byKind["call"][0].Context, `helper("hi")`) {
		t.Errorf("call context = %q", byKind["call"][0].Context)
	}

	// Options: import + type position.
	result, err = runNavRefs(ctx, ws, &refsFlags{name: "Options", groupBy: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs(Options): %v", err)
	}
	if result.Counts["type"] != 2 || result.Counts["import"] != 1 {
		t.Errorf("Options counts = %v, want type:2 import:1", result.Counts)
	}

	// Greeter: `new Greeter()` is a call.
	result, err = runNavRefs(ctx, ws, &refsFlags{name: "Greeter", groupBy: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs(Greeter): %v", err)
	}
	if result.Counts["call"] != 1 {
		t.Errorf("Greeter counts = %v, want call:1", result.Counts)
	}

	// greet: method call through property access.
	result, err = runNavRefs(ctx, ws, &refsFlags{name: "greet", groupBy: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs(greet): %v", err)
	}
	if result.Counts["call"] != 1 {
		t.Errorf("greet counts = %v, want call:1", result.Counts)
	}
}

func TestNavRefsFlags(t *testing.T) {
	t.Parallel()
	files := navProjectFiles()
	ws := newTestWorkspace(t, files)
	ctx := context.Background()

	// include-declaration adds the declaration entry.
	result, err := runNavRefs(ctx, ws, &refsFlags{name: "counter", includeDeclaration: true, groupBy: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs: %v", err)
	}
	if result.Counts["declaration"] != 1 {
		t.Errorf("counts = %v, want declaration:1", result.Counts)
	}

	// kind filter narrows items but keeps full counts.
	result, err = runNavRefs(ctx, ws, &refsFlags{name: "counter", kind: "write", groupBy: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs: %v", err)
	}
	if len(result.Refs) != 3 {
		t.Errorf("kind=write should keep 3 refs, got %d", len(result.Refs))
	}
	for _, r := range result.Refs {
		if r.UsageKind != "write" {
			t.Errorf("kind filter leaked %+v", r)
		}
	}
	if result.Counts["read"] != 1 {
		t.Errorf("counts should be computed before --kind filter, got %v", result.Counts)
	}

	// group-by kind orders by usage kind.
	result, err = runNavRefs(ctx, ws, &refsFlags{name: "helper", groupBy: "kind"}, nil)
	if err != nil {
		t.Fatalf("runNavRefs: %v", err)
	}
	for i := 1; i < len(result.Refs); i++ {
		if usageKindRank[result.Refs[i-1].UsageKind] > usageKindRank[result.Refs[i].UsageKind] {
			t.Errorf("group-by kind not sorted: %+v", result.Refs)
		}
	}

	// positional file:line:col target.
	result, err = runNavRefs(ctx, ws, &refsFlags{groupBy: "file"}, []string{
		targetAt(t, files, "/project/src/util.ts", "counter = 0"),
	})
	if err != nil {
		t.Fatalf("runNavRefs positional: %v", err)
	}
	if result.Counts["write"] != 3 {
		t.Errorf("positional target counts = %v", result.Counts)
	}

	// invalid flag values.
	if _, err := runNavRefs(ctx, ws, &refsFlags{name: "counter", groupBy: "bogus"}, nil); err == nil {
		t.Error("expected error for invalid --group-by")
	}
	if _, err := runNavRefs(ctx, ws, &refsFlags{name: "counter", kind: "bogus", groupBy: "file"}, nil); err == nil {
		t.Error("expected error for invalid --kind")
	}
}

// ---------------------------------------------------------------------------
// nav usages

func TestNavUsages(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	result, err := runNavUsages(context.Background(), ws, &usagesFlags{name: "helper", contextLines: 1}, nil)
	if err != nil {
		t.Fatalf("runNavUsages: %v", err)
	}
	byFile := make(map[string]*FileUsages)
	for _, f := range result.Files {
		byFile[f.File] = f
	}
	index, ok := byFile["src/index.ts"]
	if !ok {
		t.Fatalf("missing src/index.ts usages, got %v", byFile)
	}
	if index.Classification != "" {
		t.Errorf("index.ts classification = %q, want runtime usage (empty)", index.Classification)
	}
	if len(index.Usages) != 2 {
		t.Errorf("index.ts usages = %d, want 2 (import + call)", len(index.Usages))
	}
	unused, ok := byFile["src/unused.ts"]
	if !ok || unused.Classification != "import-only" {
		t.Errorf("unused.ts should be import-only, got %+v", unused)
	}

	// Excerpts carry the marker line plus one context line on each side.
	var call *Usage
	for _, u := range index.Usages {
		if u.UsageKind == "call" {
			call = u
		}
	}
	if call == nil {
		t.Fatal("missing call usage in index.ts")
	}
	lines := strings.Split(call.Excerpt, "\n")
	if len(lines) != 3 {
		t.Errorf("excerpt should have 3 lines with context-lines=1, got %d: %q", len(lines), call.Excerpt)
	}
	if !strings.Contains(call.Excerpt, fmt.Sprintf("> %d| ", call.Line)) {
		t.Errorf("excerpt missing marker for line %d: %q", call.Line, call.Excerpt)
	}
}

func TestNavUsagesTypeOnly(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	result, err := runNavUsages(context.Background(), ws, &usagesFlags{name: "Options", contextLines: 0}, nil)
	if err != nil {
		t.Fatalf("runNavUsages: %v", err)
	}
	for _, f := range result.Files {
		if f.File == "src/index.ts" {
			if f.Classification != "type-only" {
				t.Errorf("index.ts should be type-only for Options, got %q", f.Classification)
			}
			return
		}
	}
	t.Fatalf("missing src/index.ts in usages: %+v", result.Files)
}

// ---------------------------------------------------------------------------
// nav calls

func TestNavCallsIncoming(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	result, err := runNavCalls(context.Background(), ws, &callsFlags{name: "c", direction: "in", depth: 3}, nil)
	if err != nil {
		t.Fatalf("runNavCalls: %v", err)
	}
	root := result.Incoming
	if root == nil || root.Name != "c" || root.File != "src/chain.ts" {
		t.Fatalf("root = %+v, want c in src/chain.ts", root)
	}
	if root.SymbolID != "src/chain.ts#c" {
		t.Errorf("root symbolId = %q, want src/chain.ts#c", root.SymbolID)
	}
	if len(root.Calls) != 1 || root.Calls[0].Name != "b" {
		t.Fatalf("callers of c = %+v, want [b]", root.Calls)
	}
	caller := root.Calls[0]
	if len(caller.Calls) != 1 || caller.Calls[0].Name != "a" {
		t.Fatalf("callers of b = %+v, want [a]", caller.Calls)
	}
	if len(caller.Calls[0].Calls) != 0 {
		t.Errorf("a should have no callers, got %+v", caller.Calls[0].Calls)
	}
}

func TestNavCallsOutgoing(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	result, err := runNavCalls(context.Background(), ws, &callsFlags{name: "a", direction: "out", depth: 3}, nil)
	if err != nil {
		t.Fatalf("runNavCalls: %v", err)
	}
	root := result.Outgoing
	if root == nil || root.Name != "a" {
		t.Fatalf("root = %+v, want a", root)
	}
	if len(root.Calls) != 1 || root.Calls[0].Name != "b" {
		t.Fatalf("callees of a = %+v, want [b]", root.Calls)
	}
	if len(root.Calls[0].Calls) != 1 || root.Calls[0].Calls[0].Name != "c" {
		t.Fatalf("callees of b = %+v, want [c]", root.Calls[0].Calls)
	}
}

func TestNavCallsCycle(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	result, err := runNavCalls(context.Background(), ws, &callsFlags{name: "loopA", direction: "out", depth: 5}, nil)
	if err != nil {
		t.Fatalf("runNavCalls: %v", err)
	}
	root := result.Outgoing
	if root == nil || len(root.Calls) != 1 || root.Calls[0].Name != "loopB" {
		t.Fatalf("loopA callees = %+v, want [loopB]", root)
	}
	back := root.Calls[0].Calls
	if len(back) != 1 || back[0].Name != "loopA" {
		t.Fatalf("loopB callees = %+v, want [loopA]", back)
	}
	if !back[0].Cycle {
		t.Error("revisited loopA should be marked as a cycle")
	}
	if len(back[0].Calls) != 0 {
		t.Error("cycle node must not be expanded further")
	}
}

func TestNavCallsBothAndDepth(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navProjectFiles())
	result, err := runNavCalls(context.Background(), ws, &callsFlags{name: "b", direction: "both", depth: 1}, nil)
	if err != nil {
		t.Fatalf("runNavCalls: %v", err)
	}
	if result.Incoming == nil || result.Outgoing == nil {
		t.Fatal("direction=both should fill both trees")
	}
	if len(result.Incoming.Calls) != 1 || len(result.Incoming.Calls[0].Calls) != 0 {
		t.Errorf("depth=1 should stop after one level, got %+v", result.Incoming)
	}
}

// ---------------------------------------------------------------------------
// nav graph / nav path

const graphASource = `import { b } from "./b";

export function a(): void {
	b();
}
`

const graphBSource = `import { c } from "./c";

export function b(): void {
	c();
}
`

const graphCSource = `import { a } from "./a";

export function c(): void {
	a();
}
`

func graphProjectFiles() map[string]any {
	return map[string]any{
		"/project/src/a.ts":          graphASource,
		"/project/src/b.ts":          graphBSource,
		"/project/src/c.ts":          graphCSource,
		"/project/src/standalone.ts": `export const s = 1;`,
		"/project/src/x.ts":          "import \"./y1\";\nimport \"./y2\";\n",
		"/project/src/y1.ts":         `import "./z";`,
		"/project/src/y2.ts":         `import "./z";`,
		"/project/src/z.ts":          `export const z = 1;`,
	}
}

func TestNavGraph(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, graphProjectFiles())
	ctx := context.Background()

	result, err := runNavGraph(ctx, ws, &graphFlags{scope: "file"}, nil)
	if err != nil {
		t.Fatalf("runNavGraph: %v", err)
	}
	paths := make(map[int]string)
	for _, n := range result.Nodes {
		paths[n.ID] = n.Path
		if n.External {
			t.Errorf("unexpected external node %q without --externals", n.Path)
		}
	}
	if len(result.Cycles) != 1 {
		t.Fatalf("cycles = %d, want 1", len(result.Cycles))
	}
	var cycleFiles []string
	for _, id := range result.Cycles[0] {
		cycleFiles = append(cycleFiles, paths[id])
	}
	for _, want := range []string{"src/a.ts", "src/b.ts", "src/c.ts"} {
		found := false
		for _, f := range cycleFiles {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("cycle %v missing %s", cycleFiles, want)
		}
	}
	if len(cycleFiles) != 3 {
		t.Errorf("cycle = %v, want exactly a,b,c", cycleFiles)
	}

	// --cycles keeps only cycle participants.
	result, err = runNavGraph(ctx, ws, &graphFlags{scope: "file", cycles: true}, nil)
	if err != nil {
		t.Fatalf("runNavGraph --cycles: %v", err)
	}
	if len(result.Nodes) != 3 || len(result.Edges) != 3 {
		t.Errorf("--cycles nodes=%d edges=%d, want 3/3", len(result.Nodes), len(result.Edges))
	}

	// dir scope collapses everything under src into one node without
	// fabricating self-loop cycles.
	result, err = runNavGraph(ctx, ws, &graphFlags{scope: "dir"}, nil)
	if err != nil {
		t.Fatalf("runNavGraph dir: %v", err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Path != "src" {
		t.Fatalf("dir nodes = %+v, want single src", result.Nodes)
	}
	if len(result.Edges) != 0 || len(result.Cycles) != 0 {
		t.Errorf("dir scope edges=%d cycles=%d, want 0/0", len(result.Edges), len(result.Cycles))
	}
}

func TestNavPath(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, graphProjectFiles())
	ctx := context.Background()

	result, err := runNavPath(ctx, ws, &pathFlags{from: "src/a.ts", to: "src/c.ts", maxPaths: 1}, nil)
	if err != nil {
		t.Fatalf("runNavPath: %v", err)
	}
	if len(result.Paths) != 1 {
		t.Fatalf("paths = %d, want 1", len(result.Paths))
	}
	hops := result.Paths[0].Hops
	if len(hops) != 2 {
		t.Fatalf("hops = %+v, want 2", hops)
	}
	if hops[0].From != "src/a.ts" || hops[0].To != "src/b.ts" || hops[1].To != "src/c.ts" {
		t.Errorf("chain = %+v, want a->b->c", hops)
	}
	if hops[0].ImportLine != 1 || !strings.Contains(hops[0].ImportText, `from "./b"`) {
		t.Errorf("hop import = line %d %q", hops[0].ImportLine, hops[0].ImportText)
	}
}

func TestNavPathMultiple(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, graphProjectFiles())
	result, err := runNavPath(context.Background(), ws, &pathFlags{from: "src/x.ts", to: "src/z.ts", maxPaths: 5}, nil)
	if err != nil {
		t.Fatalf("runNavPath: %v", err)
	}
	if len(result.Paths) != 2 {
		t.Fatalf("paths = %d, want 2 distinct shortest paths", len(result.Paths))
	}
	mids := map[string]bool{}
	for _, p := range result.Paths {
		if len(p.Hops) != 2 {
			t.Errorf("path %+v should have 2 hops", p.Hops)
		}
		mids[p.Hops[0].To] = true
	}
	if !mids["src/y1.ts"] || !mids["src/y2.ts"] {
		t.Errorf("expected paths via y1 and y2, got %v", mids)
	}
}

func TestNavPathUnreachable(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, graphProjectFiles())
	_, err := runNavPath(context.Background(), ws, &pathFlags{from: "src/standalone.ts", to: "src/a.ts", maxPaths: 1}, nil)
	if err == nil {
		t.Fatal("expected unreachable error")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error = %q, want it to mention unreachable", err)
	}
	if code := cli.ExitCode(err); code != cli.ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, cli.ExitNotFound)
	}
	var exitErr *cli.ExitError
	if !errors.As(err, &exitErr) {
		t.Error("expected a cli.ExitError")
	}
}
