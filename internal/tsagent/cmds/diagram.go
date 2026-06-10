package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// maxMemberSignatureLen caps class-diagram member signatures.
const maxMemberSignatureLen = 80

func init() {
	cli.Register(cli.Command{
		Family:       "diagram",
		Name:         "deps",
		Summary:      "Module dependency diagram (mermaid/dot) with cycle highlighting",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &diagramDepsFlags{}
			fs.StringVar(&f.out, "out", "mermaid", "diagram syntax: mermaid, dot, or json")
			fs.StringVar(&f.clusterBy, "cluster-by", "", "cluster nodes: dir")
			fs.BoolVar(&f.externals, "externals", false, "include node_modules nodes and edges")
			fs.BoolVar(&f.cyclesOnly, "cycles-only", false, "restrict to cycle (SCC) participants")
			fs.BoolVar(&f.neighbors, "neighbors", true, "with path filters, include direct neighbors")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runDiagramDeps(ctx, ws, flags.(*diagramDepsFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "diagram",
		Name:         "classes",
		Summary:      "Class/interface diagram: inheritance, implementation, composition",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &diagramClassesFlags{}
			fs.StringVar(&f.out, "out", "mermaid", "diagram syntax: mermaid, dot, or json")
			fs.StringVar(&f.symbol, "symbol", "", "scope to the hierarchy around this symbol id")
			fs.IntVar(&f.depth, "depth", 2, "hierarchy BFS depth when --symbol is given")
			fs.StringVar(&f.members, "members", "names", "member rendering: signatures, names, or none")
			fs.BoolVar(&f.includeAliases, "include-aliases", false, "include exported object-type aliases")
			fs.BoolVar(&f.composition, "composition", false, "add composition edges from typed properties")
			fs.BoolVar(&f.collapseExternal, "collapse-external", true, "stop expansion at lib/node_modules parents")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runDiagramClasses(ctx, ws, flags.(*diagramClassesFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "diagram",
		Name:         "calls",
		Summary:      "Call-graph diagram from a root symbol with cycles marked",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &diagramCallsFlags{}
			fs.StringVar(&f.out, "out", "mermaid", "diagram syntax: mermaid, dot, or json")
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			fs.StringVar(&f.direction, "direction", "out", "call direction: in, out, or both")
			fs.IntVar(&f.depth, "depth", 3, "maximum expansion depth")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runDiagramCalls(ctx, ws, flags.(*diagramCallsFlags), args)
		},
	})
}

// ---------------------------------------------------------------------------
// shared: result type, syntax flag, node id allocation, escaping

const (
	syntaxMermaid = "mermaid"
	syntaxDot     = "dot"
	syntaxJSON    = "json"
)

func parseDiagramSyntax(s string) (string, error) {
	switch s {
	case "", syntaxMermaid:
		return syntaxMermaid, nil
	case syntaxDot, syntaxJSON:
		return s, nil
	}
	return "", cli.UsageErrorf("invalid --out %q (want mermaid, dot, or json)", s)
}

// DiagramResult is the result of every `diagram` command: the diagram source
// text in the chosen syntax plus size stats. With --format text the raw
// diagram is printed ready to paste; --format json wraps it in the standard
// envelope.
type DiagramResult struct {
	Syntax  string         `json:"syntax"`
	Diagram string         `json:"diagram"`
	Stats   map[string]int `json:"stats"`
}

var _ cli.Texter = (*DiagramResult)(nil)

func (r *DiagramResult) WriteText(w io.Writer) error {
	text := r.Diagram
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, err := io.WriteString(w, text)
	return err
}

// diagramIDAllocator hands out deterministic node ids: keys are sanitized to
// [A-Za-z0-9_] and collisions get a numeric suffix. The same key always maps
// to the same id within one allocator.
type diagramIDAllocator struct {
	used  map[string]bool
	byKey map[string]string
}

func newDiagramIDAllocator() *diagramIDAllocator {
	return &diagramIDAllocator{used: make(map[string]bool), byKey: make(map[string]string)}
}

func (a *diagramIDAllocator) id(key string) string {
	if id, ok := a.byKey[key]; ok {
		return id
	}
	base := sanitizeDiagramID(key)
	id := base
	for n := 2; a.used[id]; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	a.used[id] = true
	a.byKey[key] = id
	return id
}

// sanitizeDiagramID maps an arbitrary label to a valid mermaid/dot identifier.
func sanitizeDiagramID(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "n" + out
	}
	return out
}

// mermaidLabel escapes a label for use inside a quoted mermaid node label.
func mermaidLabel(s string) string {
	s = strings.ReplaceAll(s, `"`, "#quot;")
	return strings.ReplaceAll(s, "\n", "<br/>")
}

// dotLabel escapes a label for use inside a quoted dot attribute.
func dotLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// ---------------------------------------------------------------------------
// flowchart model (deps, calls)

type flowNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Class is a style class: "external" (dashed) or "root" (highlighted).
	Class string `json:"class,omitempty"`
}

type flowEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
	Cycle bool   `json:"cycle,omitempty"`
}

type flowCluster struct {
	Label string   `json:"label"`
	Nodes []string `json:"nodes"`
}

type flowGraph struct {
	Nodes    []*flowNode    `json:"nodes"`
	Edges    []*flowEdge    `json:"edges"`
	Clusters []*flowCluster `json:"clusters,omitempty"`
	// Direction is the mermaid/dot layout direction ("" = LR).
	Direction string `json:"direction,omitempty"`
}

func (g *flowGraph) direction() string {
	if g.Direction == "" {
		return "LR"
	}
	return g.Direction
}

func renderFlowDiagram(g *flowGraph, syntax string, stats map[string]int) (*DiagramResult, error) {
	var text string
	switch syntax {
	case syntaxMermaid:
		text = g.emitMermaid()
	case syntaxDot:
		text = g.emitDot()
	case syntaxJSON:
		data, err := json.MarshalIndent(g, "", "  ")
		if err != nil {
			return nil, err
		}
		text = string(data) + "\n"
	}
	return &DiagramResult{Syntax: syntax, Diagram: text, Stats: stats}, nil
}

func (g *flowGraph) emitMermaid() string {
	var b strings.Builder
	fmt.Fprintf(&b, "flowchart %s\n", g.direction())
	inCluster := make(map[string]bool)
	for _, c := range g.Clusters {
		for _, id := range c.Nodes {
			inCluster[id] = true
		}
	}
	byID := make(map[string]*flowNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
		if !inCluster[n.ID] {
			fmt.Fprintf(&b, "  %s[\"%s\"]\n", n.ID, mermaidLabel(n.Label))
		}
	}
	for i, c := range g.Clusters {
		fmt.Fprintf(&b, "  subgraph cluster%d[\"%s\"]\n", i, mermaidLabel(c.Label))
		for _, id := range c.Nodes {
			if n := byID[id]; n != nil {
				fmt.Fprintf(&b, "    %s[\"%s\"]\n", n.ID, mermaidLabel(n.Label))
			}
		}
		b.WriteString("  end\n")
	}
	var cycleEdges []string
	for i, e := range g.Edges {
		if e.Label != "" {
			fmt.Fprintf(&b, "  %s -->|%s| %s\n", e.From, mermaidLabel(e.Label), e.To)
		} else {
			fmt.Fprintf(&b, "  %s --> %s\n", e.From, e.To)
		}
		if e.Cycle {
			cycleEdges = append(cycleEdges, fmt.Sprintf("%d", i))
		}
	}
	writeMermaidClasses(&b, g.Nodes)
	for _, idx := range cycleEdges {
		fmt.Fprintf(&b, "  linkStyle %s stroke:#cc0000,stroke-width:2px\n", idx)
	}
	return b.String()
}

// writeMermaidClasses emits classDef/class lines for styled nodes.
func writeMermaidClasses(b *strings.Builder, nodes []*flowNode) {
	defs := map[string]string{
		"external": "fill:#eeeeee,stroke-dasharray: 3 3",
		"root":     "fill:#fff3bf,stroke-width:3px",
	}
	for _, class := range []string{"external", "root"} {
		var ids []string
		for _, n := range nodes {
			if n.Class == class {
				ids = append(ids, n.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		fmt.Fprintf(b, "  classDef %s %s\n", class, defs[class])
		fmt.Fprintf(b, "  class %s %s\n", strings.Join(ids, ","), class)
	}
}

func (g *flowGraph) emitDot() string {
	var b strings.Builder
	b.WriteString("digraph G {\n")
	rankdir := "LR"
	if g.direction() == "TD" {
		rankdir = "TB"
	}
	fmt.Fprintf(&b, "  rankdir=%s;\n", rankdir)
	b.WriteString("  node [shape=box];\n")
	inCluster := make(map[string]bool)
	for i, c := range g.Clusters {
		fmt.Fprintf(&b, "  subgraph cluster_%d {\n", i)
		fmt.Fprintf(&b, "    label=\"%s\";\n", dotLabel(c.Label))
		for _, id := range c.Nodes {
			inCluster[id] = true
			fmt.Fprintf(&b, "    %s;\n", id)
		}
		b.WriteString("  }\n")
	}
	for _, n := range g.Nodes {
		attrs := []string{fmt.Sprintf("label=\"%s\"", dotLabel(n.Label))}
		switch n.Class {
		case "external":
			attrs = append(attrs, "style=dashed")
		case "root":
			attrs = append(attrs, "penwidth=2", "style=filled", "fillcolor=lightyellow")
		}
		fmt.Fprintf(&b, "  %s [%s];\n", n.ID, strings.Join(attrs, ", "))
	}
	for _, e := range g.Edges {
		var attrs []string
		if e.Label != "" {
			attrs = append(attrs, fmt.Sprintf("label=\"%s\"", dotLabel(e.Label)))
		}
		if e.Cycle {
			attrs = append(attrs, "color=red", "penwidth=2")
		}
		if len(attrs) > 0 {
			fmt.Fprintf(&b, "  %s -> %s [%s];\n", e.From, e.To, strings.Join(attrs, ", "))
		} else {
			fmt.Fprintf(&b, "  %s -> %s;\n", e.From, e.To)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// diagram deps

type diagramDepsFlags struct {
	out        string
	clusterBy  string
	externals  bool
	cyclesOnly bool
	neighbors  bool
}

func runDiagramDeps(ctx context.Context, ws *core.Workspace, flags *diagramDepsFlags, args []string) (*DiagramResult, error) {
	syntax, err := parseDiagramSyntax(flags.out)
	if err != nil {
		return nil, err
	}
	if flags.clusterBy != "" && flags.clusterBy != "dir" {
		return nil, cli.UsageErrorf("invalid --cluster-by %q (want dir)", flags.clusterBy)
	}

	g := core.BuildImportGraph(ws)
	if !flags.externals {
		g = g.Filter(func(n *core.ImportGraphNode) bool { return !n.External })
	}
	if len(args) > 0 {
		keep, err := depsPathSelection(ws, g, args, flags.neighbors)
		if err != nil {
			return nil, err
		}
		g = g.Filter(func(n *core.ImportGraphNode) bool { return keep[n.ID] })
	}
	if flags.cyclesOnly {
		participant := make(map[int]bool)
		for _, component := range g.Cycles() {
			for _, id := range component {
				participant[id] = true
			}
		}
		g = g.Filter(func(n *core.ImportGraphNode) bool { return participant[n.ID] })
	}

	cycles := g.Cycles()
	cycleOf := make(map[int]int) // node id -> 1-based cycle component
	for i, component := range cycles {
		for _, id := range component {
			cycleOf[id] = i + 1
		}
	}

	alloc := newDiagramIDAllocator()
	fg := &flowGraph{}
	ids := make([]string, len(g.Nodes))
	for _, n := range g.Nodes {
		label := ws.RelPath(n.FileName)
		id := alloc.id(label)
		ids[n.ID] = id
		class := ""
		if n.External {
			class = "external"
		}
		fg.Nodes = append(fg.Nodes, &flowNode{ID: id, Label: label, Class: class})
	}
	if flags.clusterBy == "dir" {
		fg.Clusters = depsDirClusters(ws, g, ids)
	}
	edgeSeen := make(map[[2]int]bool)
	for _, e := range g.Edges {
		key := [2]int{e.From, e.To}
		if edgeSeen[key] {
			continue
		}
		edgeSeen[key] = true
		cycle := cycleOf[e.From] != 0 && cycleOf[e.From] == cycleOf[e.To]
		fg.Edges = append(fg.Edges, &flowEdge{From: ids[e.From], To: ids[e.To], Cycle: cycle})
	}

	stats := map[string]int{"nodes": len(fg.Nodes), "edges": len(fg.Edges), "cycles": len(cycles)}
	return renderFlowDiagram(fg, syntax, stats)
}

// depsPathSelection returns the node-id set under the given paths, optionally
// extended by one hop of direct neighbors.
func depsPathSelection(ws *core.Workspace, g *core.ImportGraph, paths []string, neighbors bool) (map[int]bool, error) {
	abs := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		cwdRelative := ws.AbsPath(p)
		abs = append(abs, cwdRelative)
		if rootRelative := tspath.GetNormalizedAbsolutePath(p, ws.RootDir); rootRelative != cwdRelative {
			abs = append(abs, rootRelative)
		}
	}
	compareOptions := tspath.ComparePathsOptions{
		CurrentDirectory:          ws.Cwd,
		UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
	}
	selected := make(map[int]bool)
	for _, n := range g.Nodes {
		for _, p := range abs {
			if tspath.ComparePaths(n.FileName, p, compareOptions) == 0 ||
				tspath.ContainsPath(p, n.FileName, compareOptions) {
				selected[n.ID] = true
				break
			}
		}
	}
	if len(selected) == 0 {
		return nil, cli.NotFoundErrorf("no graph nodes under %s", strings.Join(paths, ", "))
	}
	if !neighbors {
		return selected, nil
	}
	keep := make(map[int]bool, len(selected))
	for id := range selected {
		keep[id] = true
	}
	for _, e := range g.Edges {
		if selected[e.From] {
			keep[e.To] = true
		}
		if selected[e.To] {
			keep[e.From] = true
		}
	}
	return keep, nil
}

// depsDirClusters groups nodes into one cluster per directory (sorted).
func depsDirClusters(ws *core.Workspace, g *core.ImportGraph, ids []string) []*flowCluster {
	byDir := make(map[string][]string)
	var dirs []string
	for _, n := range g.Nodes {
		dir := tspath.GetDirectoryPath(ws.RelPath(n.FileName))
		if dir == "" {
			dir = "."
		}
		if _, ok := byDir[dir]; !ok {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], ids[n.ID])
	}
	slices.Sort(dirs)
	clusters := make([]*flowCluster, 0, len(dirs))
	for _, dir := range dirs {
		clusters = append(clusters, &flowCluster{Label: dir, Nodes: byDir[dir]})
	}
	return clusters
}

// ---------------------------------------------------------------------------
// diagram classes

type diagramClassesFlags struct {
	out              string
	symbol           string
	depth            int
	members          string
	includeAliases   bool
	composition      bool
	collapseExternal bool
}

// classEntry is one collected class/interface/alias declaration.
type classEntry struct {
	node     *ast.Node
	file     *ast.SourceFile
	name     string
	kind     string // class | interface | type
	external bool
}

// classDiagNode is one node of the rendered class diagram.
type classDiagNode struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	File       string   `json:"file,omitempty"`
	Kind       string   `json:"kind"`
	Stereotype string   `json:"stereotype,omitempty"`
	External   bool     `json:"external,omitempty"`
	Members    []string `json:"members,omitempty"`
}

// classDiagEdge is one heritage or composition edge (From is the derived /
// owning side).
type classDiagEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // extends | implements | composition
}

type classDiagram struct {
	Nodes []*classDiagNode `json:"nodes"`
	Edges []*classDiagEdge `json:"edges"`
}

func runDiagramClasses(ctx context.Context, ws *core.Workspace, flags *diagramClassesFlags, args []string) (*DiagramResult, error) {
	syntax, err := parseDiagramSyntax(flags.out)
	if err != nil {
		return nil, err
	}
	switch flags.members {
	case "signatures", "names", "none":
	default:
		return nil, cli.UsageErrorf("invalid --members %q (want signatures, names, or none)", flags.members)
	}
	if flags.symbol != "" && len(args) > 0 {
		return nil, cli.UsageErrorf("positional paths and --symbol are mutually exclusive")
	}
	if flags.depth < 1 {
		return nil, cli.UsageErrorf("--depth must be >= 1")
	}

	// With --symbol the whole project is collected so downward (subtype)
	// edges exist before the BFS scoping pass.
	var pathArgs []string
	if flags.symbol == "" {
		pathArgs = args
	}
	files, err := projectFiles(ws, pathArgs)
	if err != nil {
		return nil, err
	}

	cg := &classGraphBuilder{
		ctx:              ctx,
		ws:               ws,
		includeAliases:   flags.includeAliases,
		collapseExternal: flags.collapseExternal,
		entries:          make(map[*ast.Node]*classEntry),
		edgeSeen:         make(map[classEdgeKey]bool),
	}
	for _, file := range files {
		cg.collectStatements(file, file.Statements.Nodes)
	}
	cg.resolveHeritage()

	kept := cg.order
	keptEdges := cg.edges
	if flags.symbol != "" {
		kept, keptEdges, err = cg.scopeToSymbol(flags.symbol, flags.depth)
		if err != nil {
			return nil, err
		}
	}
	if flags.composition {
		keptEdges = append(keptEdges, cg.compositionEdges(kept)...)
	}

	diagram := cg.buildDiagram(kept, keptEdges, flags.members)
	stats := map[string]int{"nodes": len(diagram.Nodes), "edges": len(diagram.Edges)}
	return renderClassDiagram(diagram, syntax, stats)
}

type classHeritageEdge struct {
	from *ast.Node
	to   *ast.Node
	kind string
}

type classEdgeKey struct {
	from *ast.Node
	to   *ast.Node
	kind string
}

type classGraphBuilder struct {
	ctx              context.Context
	ws               *core.Workspace
	includeAliases   bool
	collapseExternal bool

	entries  map[*ast.Node]*classEntry
	order    []*classEntry
	edges    []*classHeritageEdge
	edgeSeen map[classEdgeKey]bool
	queue    []*ast.Node
}

// collectStatements seeds entries from top-level (and namespace-nested)
// class/interface declarations, plus exported object-type aliases when
// enabled.
func (cg *classGraphBuilder) collectStatements(file *ast.SourceFile, statements []*ast.Node) {
	for _, statement := range statements {
		switch statement.Kind {
		case ast.KindClassDeclaration, ast.KindInterfaceDeclaration:
			cg.addEntry(statement, false)
		case ast.KindTypeAliasDeclaration:
			if cg.includeAliases && core.IsExportedDeclaration(statement) &&
				statement.AsTypeAliasDeclaration().Type.Kind == ast.KindTypeLiteral {
				cg.addEntry(statement, false)
			}
		case ast.KindModuleDeclaration:
			if body := statement.Body(); body != nil && body.Kind == ast.KindModuleBlock {
				cg.collectStatements(file, body.AsModuleBlock().Statements.Nodes)
			}
		}
	}
}

func (cg *classGraphBuilder) addEntry(decl *ast.Node, external bool) *classEntry {
	if e, ok := cg.entries[decl]; ok {
		return e
	}
	name := ast.GetDeclarationName(decl)
	if name == "" {
		return nil
	}
	var kind string
	switch decl.Kind {
	case ast.KindClassDeclaration, ast.KindClassExpression:
		kind = "class"
	case ast.KindInterfaceDeclaration:
		kind = "interface"
	case ast.KindTypeAliasDeclaration:
		kind = "type"
	default:
		return nil
	}
	e := &classEntry{
		node:     decl,
		file:     ast.GetSourceFileOfNode(decl),
		name:     name,
		kind:     kind,
		external: external,
	}
	cg.entries[decl] = e
	cg.order = append(cg.order, e)
	// External parents stay collapsed stubs unless expansion is requested.
	if !external || !cg.collapseExternal {
		cg.queue = append(cg.queue, decl)
	}
	return e
}

// resolveHeritage walks the worklist resolving extends/implements heritage
// clause expressions through each file's checker, so cross-file (and
// re-exported/aliased) targets resolve to their declarations.
func (cg *classGraphBuilder) resolveHeritage() {
	for len(cg.queue) > 0 {
		decl := cg.queue[0]
		cg.queue = cg.queue[1:]
		if decl.Kind != ast.KindClassDeclaration && decl.Kind != ast.KindInterfaceDeclaration {
			continue
		}
		file := ast.GetSourceFileOfNode(decl)
		c, done := cg.ws.Program.GetTypeCheckerForFile(cg.ctx, file)
		for _, token := range []ast.Kind{ast.KindExtendsKeyword, ast.KindImplementsKeyword} {
			edgeKind := "extends"
			if token == ast.KindImplementsKeyword {
				edgeKind = "implements"
			}
			for _, element := range ast.GetHeritageElements(decl, token) {
				target := cg.resolveHeritageTarget(c, element)
				if target == nil {
					continue
				}
				targetEntry := cg.addEntry(target, cg.isExternalDecl(target))
				if targetEntry == nil {
					continue
				}
				cg.addEdge(decl, target, edgeKind)
			}
		}
		done()
	}
}

// resolveHeritageTarget resolves one heritage `extends X` / `implements X`
// expression to the class/interface declaration it names.
func (cg *classGraphBuilder) resolveHeritageTarget(c *checker.Checker, element *ast.Node) *ast.Node {
	expr := element.Expression()
	symbol := c.GetSymbolAtLocation(expr)
	if symbol == nil {
		// Fall back to the type of the heritage expression (e.g. for
		// expressions like `mixin(Base)`).
		if t := c.GetTypeAtLocation(element); t != nil {
			symbol = t.Symbol()
		}
		if symbol == nil {
			return nil
		}
	}
	if symbol.Flags&ast.SymbolFlagsAlias != 0 {
		symbol = c.GetAliasedSymbol(symbol)
	}
	for _, decl := range symbol.Declarations {
		if decl.Kind == ast.KindClassDeclaration || decl.Kind == ast.KindInterfaceDeclaration {
			return decl
		}
	}
	return nil
}

func (cg *classGraphBuilder) isExternalDecl(decl *ast.Node) bool {
	file := ast.GetSourceFileOfNode(decl)
	return cg.ws.Program.IsLibFile(file) || isInNodeModules(file.FileName())
}

func (cg *classGraphBuilder) addEdge(from *ast.Node, to *ast.Node, kind string) {
	key := classEdgeKey{from: from, to: to, kind: kind}
	if cg.edgeSeen[key] {
		return
	}
	cg.edgeSeen[key] = true
	cg.edges = append(cg.edges, &classHeritageEdge{from: from, to: to, kind: kind})
}

// scopeToSymbol keeps only the hierarchy within depth hops (up or down the
// heritage edges) of the given symbol's declaration.
func (cg *classGraphBuilder) scopeToSymbol(symbolID string, depth int) ([]*classEntry, []*classHeritageEdge, error) {
	target, err := cg.ws.ResolveTarget(cg.ctx, core.TargetSpec{Symbol: symbolID})
	if err != nil {
		return nil, nil, err
	}
	var root *ast.Node
	if target.Symbol != nil {
		for _, decl := range target.Symbol.Declarations {
			if _, ok := cg.entries[decl]; ok {
				root = decl
				break
			}
		}
	}
	if root == nil {
		if _, ok := cg.entries[target.Node]; ok {
			root = target.Node
		}
	}
	if root == nil {
		return nil, nil, cli.NotFoundErrorf("symbol %s is not a class, interface, or included alias", symbolID)
	}

	adjacent := make(map[*ast.Node][]*ast.Node)
	for _, e := range cg.edges {
		adjacent[e.from] = append(adjacent[e.from], e.to)
		adjacent[e.to] = append(adjacent[e.to], e.from)
	}
	visited := map[*ast.Node]bool{root: true}
	frontier := []*ast.Node{root}
	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		var next []*ast.Node
		for _, node := range frontier {
			for _, neighbor := range adjacent[node] {
				if !visited[neighbor] {
					visited[neighbor] = true
					next = append(next, neighbor)
				}
			}
		}
		frontier = next
	}

	var kept []*classEntry
	for _, e := range cg.order {
		if visited[e.node] {
			kept = append(kept, e)
		}
	}
	var keptEdges []*classHeritageEdge
	for _, e := range cg.edges {
		if visited[e.from] && visited[e.to] {
			keptEdges = append(keptEdges, e)
		}
	}
	return kept, keptEdges, nil
}

// compositionEdges adds owner *-- part edges for typed properties whose type
// resolves to another kept class/interface/alias.
func (cg *classGraphBuilder) compositionEdges(kept []*classEntry) []*classHeritageEdge {
	keptSet := make(map[*ast.Node]bool, len(kept))
	for _, e := range kept {
		keptSet[e.node] = true
	}
	var edges []*classHeritageEdge
	for _, entry := range kept {
		if entry.external && cg.collapseExternal {
			continue
		}
		members := classEntryMembers(entry)
		if len(members) == 0 {
			continue
		}
		c, done := cg.ws.Program.GetTypeCheckerForFile(cg.ctx, entry.file)
		for _, member := range members {
			if member.Kind != ast.KindPropertyDeclaration && member.Kind != ast.KindPropertySignature {
				continue
			}
			t := c.GetTypeAtLocation(member)
			if t == nil {
				continue
			}
			symbol := t.Symbol()
			if symbol == nil {
				continue
			}
			for _, decl := range symbol.Declarations {
				if decl != entry.node && keptSet[decl] {
					key := classEdgeKey{from: entry.node, to: decl, kind: "composition"}
					if !cg.edgeSeen[key] {
						cg.edgeSeen[key] = true
						edges = append(edges, &classHeritageEdge{from: entry.node, to: decl, kind: "composition"})
					}
					break
				}
			}
		}
		done()
	}
	return edges
}

// classEntryMembers returns the member declarations of an entry (class and
// interface members, or the members of an alias's type literal).
func classEntryMembers(entry *classEntry) []*ast.Node {
	switch entry.node.Kind {
	case ast.KindClassDeclaration, ast.KindInterfaceDeclaration:
		return entry.node.Members()
	case ast.KindTypeAliasDeclaration:
		if t := entry.node.AsTypeAliasDeclaration().Type; t.Kind == ast.KindTypeLiteral {
			return t.Members()
		}
	}
	return nil
}

func (cg *classGraphBuilder) buildDiagram(kept []*classEntry, edges []*classHeritageEdge, membersMode string) *classDiagram {
	alloc := newDiagramIDAllocator()
	ids := make(map[*ast.Node]string, len(kept))
	diagram := &classDiagram{}
	for _, entry := range kept {
		id := alloc.id(entry.name)
		ids[entry.node] = id
		stereotype := ""
		switch {
		case entry.external:
			stereotype = "external"
		case entry.kind == "interface":
			stereotype = "interface"
		case entry.kind == "type":
			stereotype = "type"
		}
		node := &classDiagNode{
			ID:         id,
			Name:       entry.name,
			Kind:       entry.kind,
			Stereotype: stereotype,
			External:   entry.external,
		}
		if !entry.external {
			node.File = cg.ws.RelPath(entry.file.FileName())
		}
		if membersMode != "none" && !(entry.external && cg.collapseExternal) {
			node.Members = cg.memberLines(entry, membersMode)
		}
		diagram.Nodes = append(diagram.Nodes, node)
	}
	for _, e := range edges {
		from, okFrom := ids[e.from]
		to, okTo := ids[e.to]
		if !okFrom || !okTo {
			continue
		}
		diagram.Edges = append(diagram.Edges, &classDiagEdge{From: from, To: to, Kind: e.kind})
	}
	return diagram
}

// memberLines renders one mermaid classDiagram member line per member, with
// +/-/# visibility prefixes and optional checker-rendered signatures.
func (cg *classGraphBuilder) memberLines(entry *classEntry, mode string) []string {
	members := classEntryMembers(entry)
	if len(members) == 0 {
		return nil
	}
	var c *checker.Checker
	var done func()
	if mode == "signatures" {
		c, done = cg.ws.Program.GetTypeCheckerForFile(cg.ctx, entry.file)
		defer done()
	}
	var lines []string
	for _, member := range members {
		var callable bool
		switch member.Kind {
		case ast.KindMethodDeclaration, ast.KindMethodSignature, ast.KindConstructor,
			ast.KindGetAccessor, ast.KindSetAccessor:
			callable = true
		case ast.KindPropertyDeclaration, ast.KindPropertySignature:
		default:
			continue
		}
		name := ast.GetDeclarationName(member)
		if name == "" {
			if member.Kind == ast.KindConstructor {
				name = "constructor"
			} else {
				continue
			}
		}
		line := memberVisibilityPrefix(member) + name
		switch mode {
		case "names":
			if callable {
				line += "()"
			}
		case "signatures":
			if callable {
				if sig := c.GetSignatureFromDeclaration(member); sig != nil {
					line += cli.Truncate(c.SignatureToStringEx(sig, entry.file.AsNode(), checker.TypeFormatFlagsNone, nil), maxMemberSignatureLen)
				} else {
					line += "()"
				}
			} else if t := c.GetTypeAtLocation(member); t != nil {
				line += cli.Truncate(": "+c.TypeToString(t), maxMemberSignatureLen)
			}
		}
		lines = append(lines, strings.ReplaceAll(line, "\n", " "))
	}
	return lines
}

func memberVisibilityPrefix(member *ast.Node) string {
	flags := member.ModifierFlags()
	switch {
	case flags&ast.ModifierFlagsPrivate != 0:
		return "-"
	case flags&ast.ModifierFlagsProtected != 0:
		return "#"
	}
	if name := ast.GetNameOfDeclaration(member); name != nil && name.Kind == ast.KindPrivateIdentifier {
		return "-"
	}
	return "+"
}

func renderClassDiagram(d *classDiagram, syntax string, stats map[string]int) (*DiagramResult, error) {
	var text string
	switch syntax {
	case syntaxMermaid:
		text = d.emitMermaid()
	case syntaxDot:
		text = d.emitDot()
	case syntaxJSON:
		data, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return nil, err
		}
		text = string(data) + "\n"
	}
	return &DiagramResult{Syntax: syntax, Diagram: text, Stats: stats}, nil
}

func (d *classDiagram) emitMermaid() string {
	var b strings.Builder
	b.WriteString("classDiagram\n")
	for _, n := range d.Nodes {
		if n.Stereotype == "" && len(n.Members) == 0 {
			fmt.Fprintf(&b, "  class %s[\"%s\"]\n", n.ID, mermaidLabel(n.Name))
			continue
		}
		fmt.Fprintf(&b, "  class %s[\"%s\"] {\n", n.ID, mermaidLabel(n.Name))
		if n.Stereotype != "" {
			fmt.Fprintf(&b, "    <<%s>>\n", n.Stereotype)
		}
		for _, m := range n.Members {
			fmt.Fprintf(&b, "    %s\n", m)
		}
		b.WriteString("  }\n")
	}
	for _, e := range d.Edges {
		switch e.Kind {
		case "extends":
			fmt.Fprintf(&b, "  %s <|-- %s\n", e.To, e.From)
		case "implements":
			fmt.Fprintf(&b, "  %s <|.. %s\n", e.To, e.From)
		case "composition":
			fmt.Fprintf(&b, "  %s *-- %s\n", e.From, e.To)
		}
	}
	return b.String()
}

func (d *classDiagram) emitDot() string {
	var b strings.Builder
	b.WriteString("digraph classes {\n")
	b.WriteString("  rankdir=BT;\n")
	b.WriteString("  node [shape=box];\n")
	for _, n := range d.Nodes {
		label := n.Name
		if n.Stereotype != "" {
			label = "«" + n.Stereotype + "»\n" + label
		}
		for _, m := range n.Members {
			label += "\n" + m
		}
		attrs := []string{fmt.Sprintf("label=\"%s\"", dotLabel(label))}
		if n.External {
			attrs = append(attrs, "style=dashed")
		}
		fmt.Fprintf(&b, "  %s [%s];\n", n.ID, strings.Join(attrs, ", "))
	}
	for _, e := range d.Edges {
		switch e.Kind {
		case "extends":
			fmt.Fprintf(&b, "  %s -> %s [arrowhead=empty];\n", e.From, e.To)
		case "implements":
			fmt.Fprintf(&b, "  %s -> %s [arrowhead=empty, style=dashed];\n", e.From, e.To)
		case "composition":
			fmt.Fprintf(&b, "  %s -> %s [arrowhead=diamond];\n", e.From, e.To)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// diagram calls

type diagramCallsFlags struct {
	out       string
	symbol    string
	name      string
	direction string
	depth     int
}

func runDiagramCalls(ctx context.Context, ws *core.Workspace, flags *diagramCallsFlags, args []string) (*DiagramResult, error) {
	syntax, err := parseDiagramSyntax(flags.out)
	if err != nil {
		return nil, err
	}
	switch flags.direction {
	case "in", "out", "both":
	default:
		return nil, cli.UsageErrorf("invalid --direction %q (want in, out, or both)", flags.direction)
	}
	if flags.depth < 1 {
		return nil, cli.UsageErrorf("--depth must be >= 1")
	}
	file, pos, err := resolveNavTarget(ctx, ws, flags.symbol, flags.name, args)
	if err != nil {
		return nil, err
	}

	prepared, err := ws.LS.ProvidePrepareCallHierarchy(ctx, ws.URI(file.FileName()), lspPosition(ws, file, pos))
	if err != nil {
		return nil, err
	}
	if prepared.CallHierarchyItems == nil || len(*prepared.CallHierarchyItems) == 0 {
		line, col := ws.PosToLineCol(file, pos)
		return nil, cli.NotFoundErrorf("no callable target at %s:%d:%d", ws.RelPath(file.FileName()), line, col)
	}
	root := (*prepared.CallHierarchyItems)[0]

	b := &callGraphBuilder{
		ctx:      ctx,
		ws:       ws,
		maxDepth: flags.depth,
		alloc:    newDiagramIDAllocator(),
		byKey:    make(map[string]*flowNode),
		edgeOf:   make(map[[2]string]*flowEdge),
	}
	rootNode := b.nodeFor(root)
	rootNode.Class = "root"
	if flags.direction == "out" || flags.direction == "both" {
		if err := b.walk(root, false /*incoming*/, 0, map[string]bool{}, map[string]bool{}); err != nil {
			return nil, err
		}
	}
	if flags.direction == "in" || flags.direction == "both" {
		if err := b.walk(root, true /*incoming*/, 0, map[string]bool{}, map[string]bool{}); err != nil {
			return nil, err
		}
	}

	cycleEdges := 0
	for _, e := range b.graph.Edges {
		if e.Cycle {
			cycleEdges++
		}
	}
	stats := map[string]int{"nodes": len(b.graph.Nodes), "edges": len(b.graph.Edges), "cycles": cycleEdges}
	return renderFlowDiagram(&b.graph, syntax, stats)
}

// callGraphBuilder accumulates a deduplicated caller→callee flow graph from
// the LS call-hierarchy API.
type callGraphBuilder struct {
	ctx      context.Context
	ws       *core.Workspace
	maxDepth int

	graph  flowGraph
	alloc  *diagramIDAllocator
	byKey  map[string]*flowNode
	edgeOf map[[2]string]*flowEdge
}

func (b *callGraphBuilder) nodeFor(item *lsproto.CallHierarchyItem) *flowNode {
	key := callItemKey(item)
	if n, ok := b.byKey[key]; ok {
		return n
	}
	rel := b.ws.RelPath(item.Uri.FileName())
	line := int(item.SelectionRange.Start.Line) + 1
	node := &flowNode{
		ID:    b.alloc.id(fmt.Sprintf("%s %s %d", item.Name, rel, line)),
		Label: fmt.Sprintf("%s\n%s:%d", item.Name, rel, line),
	}
	b.byKey[key] = node
	b.graph.Nodes = append(b.graph.Nodes, node)
	return node
}

func (b *callGraphBuilder) addEdge(from string, to string, cycle bool) {
	key := [2]string{from, to}
	if e, ok := b.edgeOf[key]; ok {
		if cycle && !e.Cycle {
			e.Cycle = true
			e.Label = "cycle"
		}
		return
	}
	e := &flowEdge{From: from, To: to, Cycle: cycle}
	if cycle {
		e.Label = "cycle"
	}
	b.edgeOf[key] = e
	b.graph.Edges = append(b.graph.Edges, e)
}

// walk expands the hierarchy below item. path is the current root-to-node
// chain (closing it marks a cycle edge); expanded prevents re-expanding a
// node reached through multiple paths.
func (b *callGraphBuilder) walk(item *lsproto.CallHierarchyItem, incoming bool, depth int, path map[string]bool, expanded map[string]bool) error {
	key := callItemKey(item)
	if depth >= b.maxDepth || expanded[key] {
		return nil
	}
	expanded[key] = true
	path[key] = true
	defer delete(path, key)

	node := b.byKey[key]
	var children []*lsproto.CallHierarchyItem
	if incoming {
		// nil orchestrator is the documented single-project mode (see nav.go).
		resp, err := b.ws.LS.ProvideCallHierarchyIncomingCalls(b.ctx, item, nil)
		if err != nil {
			return err
		}
		if resp.CallHierarchyIncomingCalls != nil {
			for _, call := range *resp.CallHierarchyIncomingCalls {
				children = append(children, call.From)
			}
		}
	} else {
		resp, err := b.ws.LS.ProvideCallHierarchyOutgoingCalls(b.ctx, item)
		if err != nil {
			return err
		}
		if resp.CallHierarchyOutgoingCalls != nil {
			for _, call := range *resp.CallHierarchyOutgoingCalls {
				children = append(children, call.To)
			}
		}
	}
	for _, child := range children {
		childNode := b.nodeFor(child)
		// Edges always point caller → callee.
		from, to := node.ID, childNode.ID
		if incoming {
			from, to = childNode.ID, node.ID
		}
		cycle := path[callItemKey(child)]
		b.addEdge(from, to, cycle)
		if cycle {
			continue
		}
		if err := b.walk(child, incoming, depth+1, path, expanded); err != nil {
			return err
		}
	}
	return nil
}
