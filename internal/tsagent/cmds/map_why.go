package cmds

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// map_why.go implements `map files --why <file>`: explain why a file is in
// the program by tracing import chains backwards from the file to the
// project's root files (spec §4.1).

// WhyStep is one hop of an inclusion chain: an importing file plus the import
// statement line that pulls the next file in.
type WhyStep struct {
	File string `json:"file"`
	Root bool   `json:"root,omitempty"`
	Line int    `json:"line"`
	// Text is the source line of the import statement creating the edge.
	Text string `json:"text"`
	// Imports is the file the import resolves to (the next hop).
	Imports string `json:"imports"`
}

// WhyChain is one root → … → file inclusion chain.
type WhyChain struct {
	Steps []WhyStep `json:"steps"`
}

// WhyResult is the `map files --why` result.
type WhyResult struct {
	File string `json:"file"`
	// Status classifies the inclusion: root | lib | imported | included.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Chains holds up to --max-chains shortest import chains (Status
	// "imported" only), each starting at a root file and ending at File.
	Chains []WhyChain `json:"chains,omitempty"`
	// TotalRoots is the number of distinct reachable root files (chains may
	// be truncated to --max-chains).
	TotalRoots int `json:"totalRoots,omitempty"`
}

var _ cli.Texter = (*WhyResult)(nil)

func (r *WhyResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s: %s", r.File, r.Status); err != nil {
		return err
	}
	if r.Reason != "" {
		if _, err := fmt.Fprintf(w, " (%s)", r.Reason); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	for i, chain := range r.Chains {
		if _, err := fmt.Fprintf(w, "chain %d:\n", i+1); err != nil {
			return err
		}
		for _, step := range chain.Steps {
			root := ""
			if step.Root {
				root = "  [root]"
			}
			if _, err := fmt.Fprintf(w, "  %s%s\n    %d: %s\n", step.File, root, step.Line, strings.TrimSpace(step.Text)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "  -> %s\n", r.File); err != nil {
			return err
		}
	}
	if r.TotalRoots > len(r.Chains) && len(r.Chains) > 0 {
		if _, err := fmt.Fprintf(w, "(%d reachable root file(s) total; showing %d chain(s), use --max-chains)\n", r.TotalRoots, len(r.Chains)); err != nil {
			return err
		}
	}
	return nil
}

func runMapFilesWhy(ctx context.Context, ws *core.Workspace, flags *filesFlags, args []string) (*WhyResult, error) {
	if len(args) > 0 {
		return nil, cli.UsageErrorf("--why takes no positional arguments (got %q)", args[0])
	}
	maxChains := flags.maxChains
	if maxChains < 1 {
		return nil, cli.UsageErrorf("--max-chains must be >= 1")
	}
	file, err := ws.FileOf(flags.why)
	if err != nil {
		return nil, err
	}
	result := &WhyResult{File: ws.RelPath(file.FileName())}

	if ws.Program.IsLibFile(file) {
		result.Status = "lib"
		result.Reason = "default library file included by the compiler (lib setting)"
		return result, nil
	}
	roots := make(map[string]bool, len(ws.Config.FileNames()))
	for _, name := range ws.Config.FileNames() {
		roots[name] = true
	}
	if roots[file.FileName()] {
		result.Status = "root"
		result.Reason = "listed as a root file by the tsconfig files/include patterns"
		return result, nil
	}

	g := core.BuildImportGraph(ws)
	target, ok := g.NodeID(file.FileName())
	if !ok {
		result.Status = "included"
		result.Reason = "in the program but not part of the import graph (e.g. /// <reference> or types inclusion)"
		return result, nil
	}

	// Reverse BFS from the target: dist[v] = import hops from v to the file.
	dist := whyReverseBFS(g, target)

	// Reachable roots, nearest (then lexicographically smallest) first.
	type rootNode struct {
		id   int
		rel  string
		dist int
	}
	var reachable []rootNode
	for _, n := range g.Nodes {
		if roots[n.FileName] && dist[n.ID] >= 0 && n.ID != target {
			reachable = append(reachable, rootNode{id: n.ID, rel: ws.RelPath(n.FileName), dist: dist[n.ID]})
		}
	}
	if len(reachable) == 0 {
		result.Status = "included"
		result.Reason = "in the program but no import chain from a root file was found"
		return result, nil
	}
	slices.SortFunc(reachable, func(a, b rootNode) int {
		if a.dist != b.dist {
			return a.dist - b.dist
		}
		return strings.Compare(a.rel, b.rel)
	})

	result.Status = "imported"
	result.TotalRoots = len(reachable)
	for _, root := range reachable[:min(len(reachable), maxChains)] {
		result.Chains = append(result.Chains, whyChainFrom(ws, g, dist, root.id, target, roots))
	}
	return result, nil
}

// whyReverseBFS returns the import-hop distance from every node to target
// (following imports forward), or -1 when target is unreachable from a node.
func whyReverseBFS(g *core.ImportGraph, target int) []int {
	rev := make(map[int][]int)
	for _, e := range g.Edges {
		rev[e.To] = append(rev[e.To], e.From)
	}
	dist := make([]int, len(g.Nodes))
	for i := range dist {
		dist[i] = -1
	}
	dist[target] = 0
	queue := []int{target}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range rev[v] {
			if dist[w] < 0 {
				dist[w] = dist[v] + 1
				queue = append(queue, w)
			}
		}
	}
	return dist
}

// whyChainFrom walks one shortest chain from a root node to the target along
// the BFS distance gradient, rendering each hop's import statement line.
func whyChainFrom(ws *core.Workspace, g *core.ImportGraph, dist []int, root int, target int, roots map[string]bool) WhyChain {
	chain := WhyChain{}
	v := root
	for v != target {
		var edge *core.ImportEdge
		for _, e := range g.OutEdges(v) {
			if dist[e.To] >= 0 && dist[e.To] == dist[v]-1 {
				edge = e
				break
			}
		}
		if edge == nil {
			break // should not happen on a BFS gradient
		}
		fromNode := g.Nodes[v]
		step := WhyStep{
			File:    ws.RelPath(fromNode.FileName),
			Root:    roots[fromNode.FileName],
			Imports: ws.RelPath(g.Nodes[edge.To].FileName),
		}
		if file := ws.Program.GetSourceFile(fromNode.FileName); file != nil {
			line, _ := ws.PosToLineCol(file, edge.Pos)
			step.Line = line
			step.Text = lineText(file, line)
		}
		chain.Steps = append(chain.Steps, step)
		v = edge.To
	}
	return chain
}
