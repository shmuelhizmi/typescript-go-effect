package cmds

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

// ---------------------------------------------------------------------------
// fixtures

func diagramDepsFiles() map[string]any {
	return map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "module": "esnext", "moduleResolution": "bundler"}}`,
		"/project/src/a.ts": `import { b } from "./b";
import { depFn } from "dep";
export function a(): void {
	b();
	depFn();
}
`,
		"/project/src/b.ts": `import { c } from "../lib/c";
export function b(): void {
	c();
}
`,
		"/project/lib/c.ts": `export function c(): void {}
`,
		"/project/src/cyc1.ts": `import "./cyc2";
export const one = 1;
`,
		"/project/src/cyc2.ts": `import "./cyc1";
export const two = 2;
`,
		"/project/node_modules/dep/package.json": `{"name": "dep", "types": "index.d.ts"}`,
		"/project/node_modules/dep/index.d.ts":   `export declare function depFn(): void;`,
	}
}

func diagramClassesFiles() map[string]any {
	return map[string]any{
		"/project/src/shapes.ts": `export interface Shape {
	area(): number;
}
`,
		"/project/src/circle.ts": `import { Shape } from "./shapes";

export class Base {
	protected id: number = 0;
}

export class Circle extends Base implements Shape {
	radius: number = 1;
	private secret: string = "";
	area(): number {
		return this.radius;
	}
}
`,
		"/project/src/err.ts": `export class MyError extends Error {
	code: number = 1;
}
`,
		"/project/src/car.ts": `export class Engine {
	hp: number = 1;
}

export class Car {
	engine: Engine = new Engine();
}
`,
		"/project/src/alias.ts": `export type Point = { x: number; y: number };
`,
	}
}

const diagramChainSource = `export function alpha(): void {
	beta();
}

export function beta(): void {
	gamma();
}

export function gamma(): void {}

export function ping(): void {
	pong();
}

export function pong(): void {
	ping();
}
`

func diagramCallsFiles() map[string]any {
	return map[string]any{
		"/project/src/chain.ts": diagramChainSource,
	}
}

// ---------------------------------------------------------------------------
// helpers

func mustContain(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q\n---\n%s", want, text)
		}
	}
}

func mustNotContain(t *testing.T, text string, rejects ...string) {
	t.Helper()
	for _, reject := range rejects {
		if strings.Contains(text, reject) {
			t.Errorf("output unexpectedly contains %q\n---\n%s", reject, text)
		}
	}
}

func checkDotStructure(t *testing.T, text string) {
	t.Helper()
	open, closed := strings.Count(text, "{"), strings.Count(text, "}")
	if open == 0 || open != closed {
		t.Errorf("dot output braces unbalanced: %d open, %d closed\n---\n%s", open, closed, text)
	}
	if !strings.HasPrefix(text, "digraph ") {
		t.Errorf("dot output does not start with digraph\n---\n%s", text)
	}
}

// ---------------------------------------------------------------------------
// diagram deps

func TestDiagramDepsMermaid(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", neighbors: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	mustContain(t, result.Diagram,
		"flowchart LR",
		`src_a_ts["src/a.ts"]`,
		"src_a_ts --> src_b_ts",
		"src_b_ts --> lib_c_ts",
		"src_cyc1_ts --> src_cyc2_ts",
		"src_cyc2_ts --> src_cyc1_ts",
		"linkStyle",
		"stroke:#cc0000",
	)
	mustNotContain(t, result.Diagram, "node_modules")
	if result.Stats["cycles"] != 1 {
		t.Errorf("stats cycles = %d, want 1", result.Stats["cycles"])
	}
	if result.Syntax != "mermaid" {
		t.Errorf("syntax = %q, want mermaid", result.Syntax)
	}
}

func TestDiagramDepsClusterByDir(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", clusterBy: "dir", neighbors: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	mustContain(t, result.Diagram,
		`subgraph cluster0["lib"]`,
		`subgraph cluster1["src"]`,
		`    lib_c_ts["lib/c.ts"]`,
		"  end",
		"src_a_ts --> src_b_ts",
	)
}

func TestDiagramDepsExternals(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", externals: true, neighbors: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	mustContain(t, result.Diagram,
		"node_modules_dep_index_d_ts",
		"classDef external",
		"src_a_ts --> node_modules_dep_index_d_ts",
	)
}

func TestDiagramDepsCyclesOnly(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", cyclesOnly: true, neighbors: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	mustContain(t, result.Diagram, "src_cyc1_ts", "src_cyc2_ts", "linkStyle")
	mustNotContain(t, result.Diagram, "src_a_ts", "lib_c_ts")
	if result.Stats["nodes"] != 2 {
		t.Errorf("stats nodes = %d, want 2", result.Stats["nodes"])
	}
}

func TestDiagramDepsPathsFilter(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())

	// With neighbors (default): a.ts plus its direct neighbor b.ts, but not
	// c.ts (two hops away) and not the unrelated cycle files.
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", neighbors: true}, []string{"src/a.ts"})
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	mustContain(t, result.Diagram, "src_a_ts --> src_b_ts")
	mustNotContain(t, result.Diagram, "lib_c_ts", "cyc1")

	// Without neighbors: only the selected file remains.
	result, err = runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", neighbors: false}, []string{"src/a.ts"})
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	mustContain(t, result.Diagram, `src_a_ts["src/a.ts"]`)
	mustNotContain(t, result.Diagram, "src_b_ts")
}

func TestDiagramDepsDot(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "dot", clusterBy: "dir", neighbors: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	checkDotStructure(t, result.Diagram)
	mustContain(t, result.Diagram,
		"rankdir=LR;",
		"subgraph cluster_0 {",
		`label="lib";`,
		"src_a_ts -> src_b_ts;",
		"color=red", // cycle edge highlighting
	)
}

func TestDiagramDepsJSONOutAndEnvelope(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "json", neighbors: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	if result.Syntax != "json" {
		t.Errorf("syntax = %q, want json", result.Syntax)
	}
	var graph struct {
		Nodes []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"nodes"`
		Edges []struct {
			From  string `json:"from"`
			To    string `json:"to"`
			Cycle bool   `json:"cycle"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(result.Diagram), &graph); err != nil {
		t.Fatalf("diagram payload is not valid JSON: %v\n---\n%s", err, result.Diagram)
	}
	if len(graph.Nodes) != 5 || len(graph.Edges) != 4 {
		t.Errorf("json graph has %d nodes, %d edges; want 5 nodes, 4 edges", len(graph.Nodes), len(graph.Edges))
	}

	// The global --format json wraps the result in the standard envelope.
	var buf bytes.Buffer
	out := &cli.Output{W: &buf, Format: cli.FormatJSON}
	if err := out.Write(result); err != nil {
		t.Fatalf("Output.Write: %v", err)
	}
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
		Result        struct {
			Syntax  string         `json:"syntax"`
			Diagram string         `json:"diagram"`
			Stats   map[string]int `json:"stats"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\n---\n%s", err, buf.String())
	}
	if envelope.SchemaVersion != cli.SchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", envelope.SchemaVersion, cli.SchemaVersion)
	}
	if envelope.Result.Syntax != "json" || envelope.Result.Diagram == "" || envelope.Result.Stats["nodes"] != 5 {
		t.Errorf("unexpected envelope result: %+v", envelope.Result)
	}
}

func TestDiagramDepsTextFormat(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramDepsFiles())
	result, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", neighbors: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramDeps: %v", err)
	}
	var buf bytes.Buffer
	out := &cli.Output{W: &buf, Format: cli.FormatText}
	if err := out.Write(result); err != nil {
		t.Fatalf("Output.Write: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "flowchart LR\n") {
		t.Errorf("text format should print the raw diagram, got:\n%s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// diagram classes

func TestDiagramClassesMermaid(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramClassesFiles())
	result, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "names", depth: 2, collapseExternal: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramClasses: %v", err)
	}
	mustContain(t, result.Diagram,
		"classDiagram",
		`class Circle["Circle"]`,
		"Base <|-- Circle",   // extends
		"Shape <|.. Circle",  // implements
		"Error <|-- MyError", // external parent edge
		"<<interface>>",      // Shape stereotype
		"<<external>>",       // Error stereotype
		"+radius",
		"-secret", // private member prefix
		"#id",     // protected member prefix
		"+area()",
	)
	// External parents stay collapsed: no members for Error.
	mustNotContain(t, result.Diagram, "+stack")
}

func TestDiagramClassesMembersModes(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramClassesFiles())

	result, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "signatures", depth: 2, collapseExternal: true}, []string{"src/circle.ts", "src/shapes.ts"})
	if err != nil {
		t.Fatalf("runDiagramClasses (signatures): %v", err)
	}
	mustContain(t, result.Diagram, "+area(): number", "+radius: number")

	result, err = runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "none", depth: 2, collapseExternal: true}, []string{"src/circle.ts", "src/shapes.ts"})
	if err != nil {
		t.Fatalf("runDiagramClasses (none): %v", err)
	}
	mustNotContain(t, result.Diagram, "+area", "+radius")
	mustContain(t, result.Diagram, "Shape <|.. Circle")
}

func TestDiagramClassesComposition(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramClassesFiles())
	result, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "names", depth: 2, composition: true, collapseExternal: true}, []string{"src/car.ts"})
	if err != nil {
		t.Fatalf("runDiagramClasses: %v", err)
	}
	mustContain(t, result.Diagram, "Car *-- Engine")
}

func TestDiagramClassesIncludeAliases(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramClassesFiles())
	result, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "names", depth: 2, includeAliases: true, collapseExternal: true}, []string{"src/alias.ts"})
	if err != nil {
		t.Fatalf("runDiagramClasses: %v", err)
	}
	mustContain(t, result.Diagram, `class Point["Point"]`, "<<type>>", "+x", "+y")

	// Without the flag the alias is absent.
	result, err = runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "names", depth: 2, collapseExternal: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramClasses: %v", err)
	}
	mustNotContain(t, result.Diagram, "Point")
}

func TestDiagramClassesSymbolScope(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramClassesFiles())
	result, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "names", symbol: "src/circle.ts#Circle", depth: 1, collapseExternal: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramClasses: %v", err)
	}
	mustContain(t, result.Diagram, "Circle", "Base <|-- Circle", "Shape <|.. Circle")
	mustNotContain(t, result.Diagram, "Car", "Engine", "MyError")
}

func TestDiagramClassesDot(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramClassesFiles())
	result, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "dot", members: "names", depth: 2, collapseExternal: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramClasses: %v", err)
	}
	checkDotStructure(t, result.Diagram)
	mustContain(t, result.Diagram,
		"Circle -> Base [arrowhead=empty];",
		"Circle -> Shape [arrowhead=empty, style=dashed];",
		"MyError -> Error [arrowhead=empty];",
		"style=dashed", // external Error node
	)
}

func TestDiagramClassesJSONOut(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramClassesFiles())
	result, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "json", members: "names", depth: 2, collapseExternal: true}, nil)
	if err != nil {
		t.Fatalf("runDiagramClasses: %v", err)
	}
	var diagram struct {
		Nodes []struct {
			Name     string `json:"name"`
			Kind     string `json:"kind"`
			External bool   `json:"external"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
			Kind string `json:"kind"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(result.Diagram), &diagram); err != nil {
		t.Fatalf("diagram payload is not valid JSON: %v\n---\n%s", err, result.Diagram)
	}
	foundExternal, foundImplements := false, false
	for _, n := range diagram.Nodes {
		if n.Name == "Error" && n.External {
			foundExternal = true
		}
	}
	for _, e := range diagram.Edges {
		if e.Kind == "implements" && e.From == "Circle" && e.To == "Shape" {
			foundImplements = true
		}
	}
	if !foundExternal || !foundImplements {
		t.Errorf("json diagram missing external Error node or Circle implements Shape edge:\n%s", result.Diagram)
	}
}

// ---------------------------------------------------------------------------
// diagram calls

func TestDiagramCallsMermaidChain(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramCallsFiles())
	result, err := runDiagramCalls(context.Background(), ws, &diagramCallsFlags{out: "mermaid", name: "alpha", direction: "out", depth: 3}, nil)
	if err != nil {
		t.Fatalf("runDiagramCalls: %v", err)
	}
	mustContain(t, result.Diagram,
		"flowchart LR",
		`alpha_src_chain_ts_1["alpha<br/>src/chain.ts:1"]`,
		"alpha_src_chain_ts_1 --> beta_src_chain_ts_5",
		"beta_src_chain_ts_5 --> gamma_src_chain_ts_9",
		"classDef root",
		"class alpha_src_chain_ts_1 root",
	)
	mustNotContain(t, result.Diagram, "ping", "cycle")
	if result.Stats["nodes"] != 3 || result.Stats["edges"] != 2 {
		t.Errorf("stats = %v, want 3 nodes / 2 edges", result.Stats)
	}
}

func TestDiagramCallsCycle(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramCallsFiles())
	result, err := runDiagramCalls(context.Background(), ws, &diagramCallsFlags{out: "mermaid", name: "ping", direction: "out", depth: 3}, nil)
	if err != nil {
		t.Fatalf("runDiagramCalls: %v", err)
	}
	mustContain(t, result.Diagram,
		"ping_src_chain_ts_11 --> pong_src_chain_ts_15",
		"pong_src_chain_ts_15 -->|cycle| ping_src_chain_ts_11",
		"linkStyle",
	)
	if result.Stats["cycles"] != 1 {
		t.Errorf("stats cycles = %d, want 1", result.Stats["cycles"])
	}
}

func TestDiagramCallsIncoming(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramCallsFiles())
	result, err := runDiagramCalls(context.Background(), ws, &diagramCallsFlags{out: "mermaid", name: "gamma", direction: "in", depth: 3}, nil)
	if err != nil {
		t.Fatalf("runDiagramCalls: %v", err)
	}
	// Edges always point caller -> callee, even for incoming expansion.
	mustContain(t, result.Diagram,
		"beta_src_chain_ts_5 --> gamma_src_chain_ts_9",
		"alpha_src_chain_ts_1 --> beta_src_chain_ts_5",
		"class gamma_src_chain_ts_9 root",
	)
}

func TestDiagramCallsDot(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramCallsFiles())
	result, err := runDiagramCalls(context.Background(), ws, &diagramCallsFlags{out: "dot", name: "ping", direction: "out", depth: 3}, nil)
	if err != nil {
		t.Fatalf("runDiagramCalls: %v", err)
	}
	checkDotStructure(t, result.Diagram)
	mustContain(t, result.Diagram,
		"ping_src_chain_ts_11 -> pong_src_chain_ts_15;",
		`pong_src_chain_ts_15 -> ping_src_chain_ts_11 [label="cycle", color=red, penwidth=2];`,
		"penwidth=2", // root highlight
	)
}

// ---------------------------------------------------------------------------
// registry and flag validation

func TestDiagramRegistry(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"deps", "classes", "calls"} {
		cmd, ok := cli.Lookup("diagram", name)
		if !ok {
			t.Errorf("diagram %s is not registered", name)
			continue
		}
		if !cmd.NeedsProgram {
			t.Errorf("diagram %s should need a program", name)
		}
	}
}

func TestDiagramInvalidFlags(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramCallsFiles())
	if _, err := runDiagramCalls(context.Background(), ws, &diagramCallsFlags{out: "svg", name: "alpha", direction: "out", depth: 3}, nil); err == nil {
		t.Error("expected error for invalid --out")
	}
	if _, err := runDiagramCalls(context.Background(), ws, &diagramCallsFlags{out: "mermaid", name: "alpha", direction: "sideways", depth: 3}, nil); err == nil {
		t.Error("expected error for invalid --direction")
	}
	if _, err := runDiagramDeps(context.Background(), ws, &diagramDepsFlags{out: "mermaid", clusterBy: "package"}, nil); err == nil {
		t.Error("expected error for invalid --cluster-by")
	}
	if _, err := runDiagramClasses(context.Background(), ws, &diagramClassesFlags{out: "mermaid", members: "full", depth: 2}, nil); err == nil {
		t.Error("expected error for invalid --members")
	}
}
