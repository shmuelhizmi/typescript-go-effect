package core

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/scanner"
)

// ImportGraphNode is one file in the module dependency graph.
type ImportGraphNode struct {
	ID       int
	FileName string // absolute, normalized
	External bool   // under node_modules (or otherwise outside the program)
}

// ImportEdge is one importer → imported edge. Pos is the byte offset of the
// module specifier in the From file, suitable for line/col rendering of the
// import statement that creates the edge.
type ImportEdge struct {
	From      int
	To        int
	Specifier string
	Pos       int
}

// ImportGraph is the file-level module dependency graph of a program. Nodes
// are program source files (libs excluded; node_modules included and tagged
// external) plus any resolved import targets outside the program.
type ImportGraph struct {
	Nodes []*ImportGraphNode
	Edges []*ImportEdge

	byPath map[string]int
	out    map[int][]*ImportEdge
}

// BuildImportGraph enumerates every import/export/require module specifier of
// every program file and resolves it through the program's module resolution
// cache. Unresolved specifiers contribute no edge. The result is
// deterministic: nodes in program order (then first-seen order for targets
// outside the program), edges in file order.
func BuildImportGraph(ws *Workspace) *ImportGraph {
	g := &ImportGraph{
		byPath: make(map[string]int),
		out:    make(map[int][]*ImportEdge),
	}
	for _, file := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(file) {
			continue
		}
		g.addNode(file.FileName())
	}
	for _, file := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(file) {
			continue
		}
		from := g.byPath[file.FileName()]
		for _, specifier := range file.Imports() {
			resolved := ws.Program.GetResolvedModuleFromModuleSpecifier(file, specifier)
			if !resolved.IsResolved() {
				continue
			}
			to := g.addNode(resolved.ResolvedFileName)
			edge := &ImportEdge{
				From:      from,
				To:        to,
				Specifier: specifier.Text(),
				Pos:       scanner.SkipTrivia(file.Text(), specifier.Pos()),
			}
			g.Edges = append(g.Edges, edge)
			g.out[from] = append(g.out[from], edge)
		}
	}
	return g
}

func (g *ImportGraph) addNode(fileName string) int {
	if id, ok := g.byPath[fileName]; ok {
		return id
	}
	id := len(g.Nodes)
	g.byPath[fileName] = id
	g.Nodes = append(g.Nodes, &ImportGraphNode{
		ID:       id,
		FileName: fileName,
		External: IsExternalLibraryPath(fileName),
	})
	return id
}

// IsExternalLibraryPath reports whether a file lives under node_modules.
func IsExternalLibraryPath(fileName string) bool {
	return strings.Contains(fileName, "/node_modules/")
}

// NodeID returns the node for an absolute file name.
func (g *ImportGraph) NodeID(fileName string) (int, bool) {
	id, ok := g.byPath[fileName]
	return id, ok
}

// OutEdges returns the outgoing edges of a node in file order.
func (g *ImportGraph) OutEdges(id int) []*ImportEdge {
	return g.out[id]
}

// HasSelfLoop reports whether a file imports itself.
func (g *ImportGraph) HasSelfLoop(id int) bool {
	for _, e := range g.out[id] {
		if e.To == id {
			return true
		}
	}
	return false
}

// Filter returns a new graph containing only the nodes for which keep
// returns true; edges with a dropped endpoint are removed. Node IDs are
// reassigned densely, preserving order.
func (g *ImportGraph) Filter(keep func(*ImportGraphNode) bool) *ImportGraph {
	filtered := &ImportGraph{
		byPath: make(map[string]int),
		out:    make(map[int][]*ImportEdge),
	}
	oldToNew := make(map[int]int)
	for _, n := range g.Nodes {
		if !keep(n) {
			continue
		}
		id := filtered.addNode(n.FileName)
		filtered.Nodes[id].External = n.External
		oldToNew[n.ID] = id
	}
	for _, e := range g.Edges {
		from, okFrom := oldToNew[e.From]
		to, okTo := oldToNew[e.To]
		if !okFrom || !okTo {
			continue
		}
		edge := &ImportEdge{From: from, To: to, Specifier: e.Specifier, Pos: e.Pos}
		filtered.Edges = append(filtered.Edges, edge)
		filtered.out[from] = append(filtered.out[from], edge)
	}
	return filtered
}

// Collapse merges nodes sharing a key (e.g. their directory) into one node
// whose FileName is the key. Parallel edges are deduplicated and self-edges
// produced by the merge are dropped (an edge survives as a self-loop only
// when it already was one). A merged node is external when any member is.
func (g *ImportGraph) Collapse(key func(*ImportGraphNode) string) *ImportGraph {
	collapsed := &ImportGraph{
		byPath: make(map[string]int),
		out:    make(map[int][]*ImportEdge),
	}
	oldToNew := make(map[int]int)
	for _, n := range g.Nodes {
		k := key(n)
		id := collapsed.addNode(k)
		collapsed.Nodes[id].External = collapsed.Nodes[id].External || n.External
		oldToNew[n.ID] = id
	}
	seen := make(map[[2]int]bool)
	for _, e := range g.Edges {
		from, to := oldToNew[e.From], oldToNew[e.To]
		if from == to && e.From != e.To {
			continue
		}
		if seen[[2]int{from, to}] {
			continue
		}
		seen[[2]int{from, to}] = true
		edge := &ImportEdge{From: from, To: to, Specifier: e.Specifier, Pos: e.Pos}
		collapsed.Edges = append(collapsed.Edges, edge)
		collapsed.out[from] = append(collapsed.out[from], edge)
	}
	return collapsed
}

// SCCs computes all strongly connected components with Tarjan's algorithm.
// Each component lists node IDs in discovery order; components come out in
// reverse topological order (dependencies before dependents).
func (g *ImportGraph) SCCs() [][]int {
	n := len(g.Nodes)
	index := make([]int, n)
	lowLink := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	var components [][]int
	next := 0

	var strongConnect func(v int)
	strongConnect = func(v int) {
		index[v] = next
		lowLink[v] = next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, e := range g.out[v] {
			w := e.To
			if index[w] < 0 {
				strongConnect(w)
				lowLink[v] = min(lowLink[v], lowLink[w])
			} else if onStack[w] {
				lowLink[v] = min(lowLink[v], index[w])
			}
		}
		if lowLink[v] == index[v] {
			var component []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				component = append(component, w)
				if w == v {
					break
				}
			}
			// Tarjan pops in reverse discovery order; flip for readability.
			for i, j := 0, len(component)-1; i < j; i, j = i+1, j-1 {
				component[i], component[j] = component[j], component[i]
			}
			components = append(components, component)
		}
	}
	for v := range n {
		if index[v] < 0 {
			strongConnect(v)
		}
	}
	return components
}

// Cycles returns only the SCCs that are real cycles: more than one node, or a
// single node that imports itself.
func (g *ImportGraph) Cycles() [][]int {
	var cycles [][]int
	for _, component := range g.SCCs() {
		if len(component) > 1 || g.HasSelfLoop(component[0]) {
			cycles = append(cycles, component)
		}
	}
	return cycles
}

// ShortestPaths returns up to maxPaths distinct shortest import chains from
// one node to another as edge sequences. Distinctness is by node sequence;
// parallel imports of the same file contribute one edge (the first in file
// order). A from==to query returns no paths. Returns nil when unreachable.
func (g *ImportGraph) ShortestPaths(from int, to int, maxPaths int) [][]*ImportEdge {
	if maxPaths <= 0 {
		maxPaths = 1
	}
	n := len(g.Nodes)
	dist := make([]int, n)
	for i := range dist {
		dist[i] = -1
	}
	dist[from] = 0
	queue := []int{from}
	// preds[w] holds one edge per distinct predecessor on a shortest path.
	preds := make([][]*ImportEdge, n)
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		seenNeighbor := make(map[int]bool)
		for _, e := range g.out[v] {
			w := e.To
			if seenNeighbor[w] {
				continue
			}
			seenNeighbor[w] = true
			if dist[w] < 0 {
				dist[w] = dist[v] + 1
				queue = append(queue, w)
			}
			if dist[w] == dist[v]+1 {
				preds[w] = append(preds[w], e)
			}
		}
	}
	if from == to || dist[to] < 0 {
		return nil
	}
	// Enumerate paths backwards over the BFS DAG.
	var paths [][]*ImportEdge
	var suffix []*ImportEdge
	var walk func(w int)
	walk = func(w int) {
		if len(paths) >= maxPaths {
			return
		}
		if w == from {
			path := make([]*ImportEdge, len(suffix))
			for i, e := range suffix {
				path[len(suffix)-1-i] = e
			}
			paths = append(paths, path)
			return
		}
		for _, e := range preds[w] {
			suffix = append(suffix, e)
			walk(e.From)
			suffix = suffix[:len(suffix)-1]
			if len(paths) >= maxPaths {
				return
			}
		}
	}
	walk(to)
	return paths
}
