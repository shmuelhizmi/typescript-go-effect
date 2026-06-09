package core

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/tspath"
)

func importGraphFiles() map[string]any {
	return map[string]any{
		"/project/src/a.ts":          `import { b } from "./b"; export function a(): void { b(); }`,
		"/project/src/b.ts":          `import { c } from "./c"; export function b(): void { c(); }`,
		"/project/src/c.ts":          `import { a } from "./a"; export function c(): void { a(); }`,
		"/project/src/standalone.ts": `export const s = 1;`,
		"/project/src/x.ts":          "import \"./y1\";\nimport \"./y2\";\n",
		"/project/src/y1.ts":         `import "./z";`,
		"/project/src/y2.ts":         `import "./z";`,
		"/project/src/z.ts":          `export const z = 1;`,
	}
}

func nodeIDOf(t *testing.T, g *ImportGraph, fileName string) int {
	t.Helper()
	id, ok := g.NodeID(fileName)
	if !ok {
		t.Fatalf("no graph node for %s", fileName)
	}
	return id
}

func TestImportGraphBuild(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, importGraphFiles())
	g := BuildImportGraph(ws)

	aID := nodeIDOf(t, g, "/project/src/a.ts")
	bID := nodeIDOf(t, g, "/project/src/b.ts")
	nodeIDOf(t, g, "/project/src/standalone.ts")

	edges := g.OutEdges(aID)
	if len(edges) != 1 || edges[0].To != bID {
		t.Fatalf("a.ts out edges = %+v, want one edge to b.ts", edges)
	}
	if edges[0].Specifier != "./b" {
		t.Errorf("edge specifier = %q, want ./b", edges[0].Specifier)
	}
	// Pos points at the specifier, on line 1 of a.ts.
	file := ws.Program.GetSourceFile("/project/src/a.ts")
	line, _ := ws.PosToLineCol(file, edges[0].Pos)
	if line != 1 {
		t.Errorf("edge pos line = %d, want 1", line)
	}
	for _, n := range g.Nodes {
		if n.External {
			t.Errorf("unexpected external node %s", n.FileName)
		}
	}
}

func TestImportGraphCycles(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, importGraphFiles())
	g := BuildImportGraph(ws)

	cycles := g.Cycles()
	if len(cycles) != 1 {
		t.Fatalf("cycles = %d, want 1", len(cycles))
	}
	want := map[int]bool{
		nodeIDOf(t, g, "/project/src/a.ts"): true,
		nodeIDOf(t, g, "/project/src/b.ts"): true,
		nodeIDOf(t, g, "/project/src/c.ts"): true,
	}
	if len(cycles[0]) != 3 {
		t.Fatalf("cycle = %v, want 3 members", cycles[0])
	}
	for _, id := range cycles[0] {
		if !want[id] {
			t.Errorf("unexpected cycle member %s", g.Nodes[id].FileName)
		}
	}

	// Every node belongs to exactly one SCC.
	seen := map[int]bool{}
	for _, scc := range g.SCCs() {
		for _, id := range scc {
			if seen[id] {
				t.Errorf("node %d in multiple SCCs", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != len(g.Nodes) {
		t.Errorf("SCCs cover %d of %d nodes", len(seen), len(g.Nodes))
	}
}

func TestImportGraphSelfLoop(t *testing.T) {
	t.Parallel()
	files := importGraphFiles()
	files["/project/src/self.ts"] = `import "./self"; export const v = 1;`
	ws := newTestWorkspace(t, files)
	g := BuildImportGraph(ws)

	selfID := nodeIDOf(t, g, "/project/src/self.ts")
	if !g.HasSelfLoop(selfID) {
		t.Fatal("self.ts should have a self loop")
	}
	foundSelfCycle := false
	for _, cycle := range g.Cycles() {
		if len(cycle) == 1 && cycle[0] == selfID {
			foundSelfCycle = true
		}
	}
	if !foundSelfCycle {
		t.Errorf("self loop should count as a cycle: %v", g.Cycles())
	}
}

func TestImportGraphShortestPaths(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, importGraphFiles())
	g := BuildImportGraph(ws)

	xID := nodeIDOf(t, g, "/project/src/x.ts")
	zID := nodeIDOf(t, g, "/project/src/z.ts")
	aID := nodeIDOf(t, g, "/project/src/a.ts")
	standaloneID := nodeIDOf(t, g, "/project/src/standalone.ts")

	paths := g.ShortestPaths(xID, zID, 10)
	if len(paths) != 2 {
		t.Fatalf("x->z paths = %d, want 2", len(paths))
	}
	for _, path := range paths {
		if len(path) != 2 {
			t.Errorf("path length = %d, want 2", len(path))
		}
		if path[0].From != xID || path[len(path)-1].To != zID {
			t.Errorf("path endpoints wrong: %+v", path)
		}
		for i := 1; i < len(path); i++ {
			if path[i].From != path[i-1].To {
				t.Errorf("path is not contiguous: %+v", path)
			}
		}
	}

	// maxPaths truncates.
	if got := g.ShortestPaths(xID, zID, 1); len(got) != 1 {
		t.Errorf("maxPaths=1 returned %d paths", len(got))
	}

	// Unreachable and same-node queries return nil.
	if got := g.ShortestPaths(standaloneID, aID, 3); got != nil {
		t.Errorf("standalone->a should be unreachable, got %+v", got)
	}
	if got := g.ShortestPaths(aID, aID, 3); got != nil {
		t.Errorf("a->a should yield no paths, got %+v", got)
	}

	// Cycle traversal: a -> c goes a->b->c.
	cID := nodeIDOf(t, g, "/project/src/c.ts")
	cyclePaths := g.ShortestPaths(aID, cID, 3)
	if len(cyclePaths) != 1 || len(cyclePaths[0]) != 2 {
		t.Errorf("a->c = %+v, want one 2-hop path", cyclePaths)
	}
}

func TestImportGraphExternals(t *testing.T) {
	t.Parallel()
	files := importGraphFiles()
	files["/project/node_modules/foo/package.json"] = `{"name": "foo", "main": "index.js", "types": "index.d.ts"}`
	files["/project/node_modules/foo/index.d.ts"] = `export declare const foo: number;`
	files["/project/src/ext.ts"] = `import { foo } from "foo"; export const e = foo;`
	ws := newTestWorkspace(t, files)
	g := BuildImportGraph(ws)

	extID := nodeIDOf(t, g, "/project/src/ext.ts")
	edges := g.OutEdges(extID)
	if len(edges) != 1 {
		t.Fatalf("ext.ts out edges = %+v, want 1", edges)
	}
	target := g.Nodes[edges[0].To]
	if !target.External {
		t.Errorf("node_modules target %s should be external", target.FileName)
	}

	filtered := g.Filter(func(n *ImportGraphNode) bool { return !n.External })
	if _, ok := filtered.NodeID(target.FileName); ok {
		t.Error("Filter should drop external nodes")
	}
	newExtID, ok := filtered.NodeID("/project/src/ext.ts")
	if !ok {
		t.Fatal("Filter dropped a non-external node")
	}
	if got := filtered.OutEdges(newExtID); len(got) != 0 {
		t.Errorf("edges to dropped nodes must vanish, got %+v", got)
	}
	// The cycle survives filtering with reassigned IDs.
	if len(filtered.Cycles()) != 1 {
		t.Errorf("filtered cycles = %d, want 1", len(filtered.Cycles()))
	}
}

func TestImportGraphCollapse(t *testing.T) {
	t.Parallel()
	files := importGraphFiles()
	files["/project/lib/other.ts"] = `import { a } from "../src/a"; export const o = a;`
	ws := newTestWorkspace(t, files)
	g := BuildImportGraph(ws)

	collapsed := g.Collapse(func(n *ImportGraphNode) string {
		return tspath.GetDirectoryPath(ws.RelPath(n.FileName))
	})
	srcID, ok := collapsed.NodeID("src")
	libID, ok2 := collapsed.NodeID("lib")
	if !ok || !ok2 {
		t.Fatalf("collapsed nodes missing: %+v", collapsed.Nodes)
	}
	// Merge-produced self edges are dropped; lib -> src survives.
	if collapsed.HasSelfLoop(srcID) {
		t.Error("collapse must not fabricate self loops")
	}
	edges := collapsed.OutEdges(libID)
	if len(edges) != 1 || edges[0].To != srcID {
		t.Errorf("lib out edges = %+v, want one edge to src", edges)
	}
	if len(collapsed.Cycles()) != 0 {
		t.Errorf("collapsed cycles = %v, want none", collapsed.Cycles())
	}
}
