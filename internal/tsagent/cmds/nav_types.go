package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

func init() {
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "types",
		Summary:      "Type hierarchy of a class/interface; --structural finds implicit implementers",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &typesFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			fs.StringVar(&f.direction, "direction", "both", "hierarchy direction: up, down, or both")
			fs.IntVar(&f.depth, "depth", 0, "maximum hierarchy depth (0 = unlimited)")
			fs.BoolVar(&f.structural, "structural", false, "include structural implementers (assignable without extends/implements)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavTypes(ctx, ws, flags.(*typesFlags), args)
		},
	})
}

type typesFlags struct {
	symbol     string
	name       string
	direction  string
	depth      int
	structural bool
}

// TypeHierarchyEntry is one supertype or subtype in the hierarchy. Depth is
// the number of heritage hops from the target (1 = direct).
type TypeHierarchyEntry struct {
	Name       string `json:"name"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	SymbolID   string `json:"symbolId,omitempty"`
	Relation   string `json:"relation"` // extends | implements | structural
	Depth      int    `json:"depth"`
	External   bool   `json:"external,omitempty"`
	Structural bool   `json:"structural,omitempty"`
}

// TypesResult is the `nav types` result. Up and Down are pre-order flattened
// trees (indent by Depth to reconstruct).
type TypesResult struct {
	Name      string                `json:"name"`
	File      string                `json:"file"`
	Line      int                   `json:"line"`
	SymbolID  string                `json:"symbolId,omitempty"`
	Direction string                `json:"direction"`
	Up        []*TypeHierarchyEntry `json:"up"`
	Down      []*TypeHierarchyEntry `json:"down"`
}

var _ cli.Texter = (*TypesResult)(nil)

func (r *TypesResult) WriteText(w io.Writer) error {
	id := ""
	if r.SymbolID != "" {
		id = "  " + r.SymbolID
	}
	if _, err := fmt.Fprintf(w, "%s  %s:%d%s\n", r.Name, r.File, r.Line, id); err != nil {
		return err
	}
	writeSection := func(label string, entries []*TypeHierarchyEntry) error {
		if _, err := fmt.Fprintf(w, "%s\n", label); err != nil {
			return err
		}
		if len(entries) == 0 {
			_, err := fmt.Fprintf(w, "%s(none)\n", cli.Indent(1))
			return err
		}
		for _, e := range entries {
			loc := ""
			if e.File != "" {
				loc = fmt.Sprintf("  %s:%d", e.File, e.Line)
			}
			id := ""
			if e.SymbolID != "" {
				id = "  " + e.SymbolID
			}
			external := ""
			if e.External {
				external = "  [external]"
			}
			if _, err := fmt.Fprintf(w, "%s%s%s%s  %s%s\n", cli.Indent(e.Depth), e.Name, loc, id, e.Relation, external); err != nil {
				return err
			}
		}
		return nil
	}
	if r.Direction == "up" || r.Direction == "both" {
		if err := writeSection("up", r.Up); err != nil {
			return err
		}
	}
	if r.Direction == "down" || r.Direction == "both" {
		if err := writeSection("down", r.Down); err != nil {
			return err
		}
	}
	return nil
}

func runNavTypes(ctx context.Context, ws *core.Workspace, flags *typesFlags, args []string) (*TypesResult, error) {
	switch flags.direction {
	case "up", "down", "both":
	default:
		return nil, cli.UsageErrorf("invalid --direction %q (want up, down, or both)", flags.direction)
	}
	if flags.depth < 0 {
		return nil, cli.UsageErrorf("--depth must be >= 0 (0 = unlimited)")
	}
	file, pos, err := resolveNavTarget(ctx, ws, flags.symbol, flags.name, args)
	if err != nil {
		return nil, err
	}

	// One checker for the whole command so symbols and types from different
	// files are comparable (required for --structural assignability).
	c, done := ws.Program.GetTypeChecker(ctx)
	defer done()

	token := astnav.GetTouchingPropertyName(file, pos)
	if token == nil || token.Kind == ast.KindSourceFile {
		return nil, cli.NotFoundErrorf("no symbol at %s:%d", ws.RelPath(file.FileName()), pos)
	}
	sym := c.GetSymbolAtLocation(token)
	if sym != nil && sym.Flags&ast.SymbolFlagsAlias != 0 {
		sym = c.GetAliasedSymbol(sym)
	}
	decl := firstClassOrInterfaceDecl(sym)
	if decl == nil {
		line, col := ws.PosToLineCol(file, pos)
		return nil, cli.NotFoundErrorf("target at %s:%d:%d is not a class or interface", ws.RelPath(file.FileName()), line, col)
	}

	declFile := ast.GetSourceFileOfNode(decl)
	line, _ := declLineOf(ws, decl)
	result := &TypesResult{
		Name:      ast.GetDeclarationName(decl),
		File:      ws.RelPath(declFile.FileName()),
		Line:      line,
		SymbolID:  core.EncodeSymbolID(ws, sym),
		Direction: flags.direction,
		Up:        []*TypeHierarchyEntry{},
		Down:      []*TypeHierarchyEntry{},
	}

	tw := &typesWalker{ws: ws, c: c, maxDepth: flags.depth}
	if flags.direction == "up" || flags.direction == "both" {
		tw.walkUp(sym, 1, map[*ast.Symbol]bool{sym: true}, &result.Up)
	}
	if flags.direction == "down" || flags.direction == "both" {
		down, err := tw.walkDown(ctx, sym, decl, flags.structural)
		if err != nil {
			return nil, err
		}
		result.Down = down
	}
	return result, nil
}

// firstClassOrInterfaceDecl returns the symbol's first class or interface
// declaration, or nil.
func firstClassOrInterfaceDecl(sym *ast.Symbol) *ast.Node {
	if sym == nil {
		return nil
	}
	for _, decl := range sym.Declarations {
		if decl.Kind == ast.KindClassDeclaration || decl.Kind == ast.KindInterfaceDeclaration {
			return decl
		}
	}
	return nil
}

// declLineOf returns the 1-based line/col of a declaration's name (or start).
func declLineOf(ws *core.Workspace, decl *ast.Node) (int, int) {
	file := ast.GetSourceFileOfNode(decl)
	node := decl
	if nameNode := ast.GetNameOfDeclaration(decl); nameNode != nil {
		node = nameNode
	}
	return ws.PosToLineCol(file, astnav.GetStartOfNode(node, file, false /*includeJSDoc*/))
}

type typesWalker struct {
	ws       *core.Workspace
	c        *checker.Checker
	maxDepth int // 0 = unlimited
}

// walkUp emits the supertypes of sym at the given depth (pre-order DFS):
// `extends` parents come from checker.GetBaseTypes on the declared type
// (which resolves mixin-style heritage expressions too), `implements`
// parents from class implements clauses. Diamonds are deduplicated to the
// first path that reaches a symbol.
func (tw *typesWalker) walkUp(sym *ast.Symbol, depth int, seen map[*ast.Symbol]bool, out *[]*TypeHierarchyEntry) {
	if tw.maxDepth > 0 && depth > tw.maxDepth {
		return
	}
	if t := tw.c.GetDeclaredTypeOfSymbol(sym); t != nil {
		for _, base := range tw.c.GetBaseTypes(t) {
			tw.addUp(base.Symbol(), base, "extends", depth, seen, out)
		}
	}
	for _, decl := range sym.Declarations {
		if decl.Kind != ast.KindClassDeclaration {
			continue
		}
		for _, element := range ast.GetHeritageElements(decl, ast.KindImplementsKeyword) {
			parent := resolveHeritageSymbol(tw.c, element)
			if parent == nil {
				continue
			}
			tw.addUp(parent, tw.c.GetDeclaredTypeOfSymbol(parent), "implements", depth, seen, out)
		}
	}
}

func (tw *typesWalker) addUp(sym *ast.Symbol, t *checker.Type, relation string, depth int, seen map[*ast.Symbol]bool, out *[]*TypeHierarchyEntry) {
	if sym == nil && t == nil {
		return
	}
	if sym != nil {
		if seen[sym] {
			return
		}
		seen[sym] = true
	}
	entry := &TypeHierarchyEntry{Relation: relation, Depth: depth}
	if t != nil {
		entry.Name = tw.c.TypeToString(t)
	} else {
		entry.Name = sym.Name
	}
	if decl := firstSymbolDecl(sym); decl != nil {
		declFile := ast.GetSourceFileOfNode(decl)
		entry.External = tw.ws.Program.IsLibFile(declFile) || isInNodeModules(declFile.FileName())
		if !entry.External {
			entry.File = tw.ws.RelPath(declFile.FileName())
			entry.Line, _ = declLineOf(tw.ws, decl)
			entry.SymbolID = core.EncodeSymbolID(tw.ws, sym)
		}
	}
	*out = append(*out, entry)
	if sym != nil {
		tw.walkUp(sym, depth+1, seen, out)
	}
}

// firstSymbolDecl returns the symbol's primary declaration, preferring a
// class/interface declaration.
func firstSymbolDecl(sym *ast.Symbol) *ast.Node {
	if sym == nil {
		return nil
	}
	if decl := firstClassOrInterfaceDecl(sym); decl != nil {
		return decl
	}
	if sym.ValueDeclaration != nil {
		return sym.ValueDeclaration
	}
	if len(sym.Declarations) > 0 {
		return sym.Declarations[0]
	}
	return nil
}

// resolveHeritageSymbol resolves one `extends X` / `implements X` heritage
// element to the (alias-resolved) symbol it names.
func resolveHeritageSymbol(c *checker.Checker, element *ast.Node) *ast.Symbol {
	sym := c.GetSymbolAtLocation(element.Expression())
	if sym == nil {
		if t := c.GetTypeAtLocation(element); t != nil {
			sym = t.Symbol()
		}
		if sym == nil {
			return nil
		}
	}
	if sym.Flags&ast.SymbolFlagsAlias != 0 {
		sym = c.GetAliasedSymbol(sym)
	}
	return sym
}

// typesDownEdge is one declared subtype edge: child's heritage clause names
// the parent.
type typesDownEdge struct {
	child    *ast.Node // class/interface declaration
	childSym *ast.Symbol
	relation string // extends | implements
}

// walkDown scans every project class/interface declaration, builds declared
// heritage edges, and BFS-expands subtypes of the target. With structural
// enabled, remaining candidates assignable to the target's declared type are
// appended as depth-1 `structural` entries.
func (tw *typesWalker) walkDown(ctx context.Context, targetSym *ast.Symbol, targetDecl *ast.Node, structural bool) ([]*TypeHierarchyEntry, error) {
	files, err := projectFiles(tw.ws, nil)
	if err != nil {
		return nil, err
	}

	type candidate struct {
		decl *ast.Node
		sym  *ast.Symbol
	}
	var candidates []candidate
	var collect func(statements []*ast.Node)
	collect = func(statements []*ast.Node) {
		for _, statement := range statements {
			switch statement.Kind {
			case ast.KindClassDeclaration, ast.KindInterfaceDeclaration:
				nameNode := ast.GetNameOfDeclaration(statement)
				if nameNode == nil {
					continue
				}
				sym := tw.c.GetSymbolAtLocation(nameNode)
				if sym != nil {
					candidates = append(candidates, candidate{decl: statement, sym: sym})
				}
			case ast.KindModuleDeclaration:
				if body := statement.Body(); body != nil && body.Kind == ast.KindModuleBlock {
					collect(body.AsModuleBlock().Statements.Nodes)
				}
			}
		}
	}
	for _, file := range files {
		collect(file.Statements.Nodes)
	}

	// Declared heritage edges, keyed by the parent's canonical declaration so
	// merged symbols compare consistently.
	children := make(map[*ast.Node][]typesDownEdge)
	for _, cand := range candidates {
		for _, token := range []ast.Kind{ast.KindExtendsKeyword, ast.KindImplementsKeyword} {
			relation := "extends"
			if token == ast.KindImplementsKeyword {
				relation = "implements"
			}
			for _, element := range ast.GetHeritageElements(cand.decl, token) {
				parent := resolveHeritageSymbol(tw.c, element)
				parentDecl := firstClassOrInterfaceDecl(parent)
				if parentDecl == nil {
					continue
				}
				children[parentDecl] = append(children[parentDecl], typesDownEdge{
					child:    cand.decl,
					childSym: cand.sym,
					relation: relation,
				})
			}
		}
	}

	targetKey := firstClassOrInterfaceDecl(targetSym)
	if targetKey == nil {
		targetKey = targetDecl
	}
	var down []*TypeHierarchyEntry
	visited := map[*ast.Node]bool{targetKey: true}
	frontier := []*ast.Node{targetKey}
	for depth := 1; len(frontier) > 0 && (tw.maxDepth == 0 || depth <= tw.maxDepth); depth++ {
		var next []*ast.Node
		for _, node := range frontier {
			for _, edge := range children[node] {
				if visited[edge.child] {
					continue
				}
				visited[edge.child] = true
				down = append(down, tw.downEntry(edge.child, edge.childSym, edge.relation, depth))
				next = append(next, edge.child)
			}
		}
		frontier = next
	}

	if structural {
		targetType := tw.c.GetDeclaredTypeOfSymbol(targetSym)
		if targetType == nil || targetType.Flags()&checker.TypeFlagsObject == 0 {
			return nil, cli.UsageErrorf("--structural requires an interface or object-like class target")
		}
		for _, cand := range candidates {
			if visited[cand.decl] || cand.sym == targetSym {
				continue
			}
			visited[cand.decl] = true // dedupe merged declarations
			candType := tw.c.GetDeclaredTypeOfSymbol(cand.sym)
			if candType == nil || candType == targetType {
				continue
			}
			if tw.c.IsTypeAssignableTo(candType, targetType) {
				entry := tw.downEntry(cand.decl, cand.sym, "structural", 1)
				entry.Structural = true
				down = append(down, entry)
			}
		}
	}
	if down == nil {
		down = []*TypeHierarchyEntry{}
	}
	return down, nil
}

func (tw *typesWalker) downEntry(decl *ast.Node, sym *ast.Symbol, relation string, depth int) *TypeHierarchyEntry {
	file := ast.GetSourceFileOfNode(decl)
	line, _ := declLineOf(tw.ws, decl)
	return &TypeHierarchyEntry{
		Name:     ast.GetDeclarationName(decl),
		File:     tw.ws.RelPath(file.FileName()),
		Line:     line,
		SymbolID: core.EncodeSymbolID(tw.ws, sym),
		Relation: relation,
		Depth:    depth,
	}
}
