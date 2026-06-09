package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	tscore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

func init() {
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "def",
		Summary:      "Go to definition (batch); --implementations, --type-definition",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &defFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			fs.BoolVar(&f.implementations, "implementations", false, "resolve to implementations instead of declarations")
			fs.BoolVar(&f.typeDefinition, "type-definition", false, "jump to the type of the expression instead")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavDef(ctx, ws, flags.(*defFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "refs",
		Summary:      "Find all references with per-reference usage kinds",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refsFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			fs.BoolVar(&f.includeDeclaration, "include-declaration", false, "include the declaration itself")
			fs.StringVar(&f.kind, "kind", "", "comma-separated usage-kind filter (declaration,import,type,call,write,read)")
			fs.StringVar(&f.groupBy, "group-by", "file", "group output by file or kind")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavRefs(ctx, ws, flags.(*refsFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "usages",
		Summary:      "References grouped per file with context excerpts",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &usagesFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			fs.IntVar(&f.contextLines, "context-lines", 2, "context lines around each usage")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavUsages(ctx, ws, flags.(*usagesFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "calls",
		Summary:      "Call hierarchy tree with cycle marking",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &callsFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			fs.StringVar(&f.direction, "direction", "in", "call direction: in, out, or both")
			fs.IntVar(&f.depth, "depth", 3, "maximum expansion depth")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavCalls(ctx, ws, flags.(*callsFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "graph",
		Summary:      "Module dependency graph with cycle detection",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &graphFlags{}
			fs.StringVar(&f.scope, "scope", "file", "graph granularity: file or dir")
			fs.BoolVar(&f.cycles, "cycles", false, "only emit strongly-connected components (cycles)")
			fs.BoolVar(&f.externals, "externals", false, "include node_modules nodes and edges")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavGraph(ctx, ws, flags.(*graphFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "path",
		Summary:      "Shortest import chain(s) between two files",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &pathFlags{}
			fs.StringVar(&f.from, "from", "", "source file (or file:line:col)")
			fs.StringVar(&f.to, "to", "", "destination file (or file:line:col)")
			fs.IntVar(&f.maxPaths, "max-paths", 1, "number of distinct shortest paths to find")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavPath(ctx, ws, flags.(*pathFlags), args)
		},
	})
}

// ---------------------------------------------------------------------------
// shared helpers

// lineText returns the content of a 1-based line without the trailing newline.
func lineText(file *ast.SourceFile, line int) string {
	starts := file.ECMALineMap()
	if line < 1 || line > len(starts) {
		return ""
	}
	start := int(starts[line-1])
	end := len(file.Text())
	if line < len(starts) {
		end = int(starts[line])
	}
	return strings.TrimRight(file.Text()[start:end], "\r\n")
}

// lspPosition converts a byte offset to an LSP position via the workspace
// converters (UTF-8 encoding: Character is a byte offset within the line).
func lspPosition(ws *core.Workspace, file *ast.SourceFile, pos int) lsproto.Position {
	return ws.Conv.PositionToLineAndCharacter(file, tscore.TextPos(pos))
}

// resolveNavTarget resolves the single target of refs/usages/calls: either a
// positional file:line:col argument, or --symbol/--name. For declarations the
// position is moved onto the declaration's name so LS lookups hit the symbol.
func resolveNavTarget(ctx context.Context, ws *core.Workspace, symbol string, name string, args []string) (*ast.SourceFile, int, error) {
	if symbol != "" || name != "" {
		if symbol != "" && name != "" {
			return nil, 0, cli.UsageErrorf("only one of --symbol and --name may be given")
		}
		if len(args) > 0 {
			return nil, 0, cli.UsageErrorf("positional target and --symbol/--name are mutually exclusive")
		}
		target, err := ws.ResolveTarget(ctx, core.TargetSpec{Symbol: symbol, Name: name})
		if err != nil {
			return nil, 0, err
		}
		node := target.Node
		if nameNode := ast.GetNameOfDeclaration(node); nameNode != nil {
			node = nameNode
		}
		return target.File, astnav.GetStartOfNode(node, target.File, false /*includeJSDoc*/), nil
	}
	if len(args) != 1 {
		return nil, 0, cli.UsageErrorf("expected exactly one target (file:line:col, or --symbol/--name)")
	}
	return resolvePositionArg(ws, args[0])
}

func resolvePositionArg(ws *core.Workspace, arg string) (*ast.SourceFile, int, error) {
	fileName, line, col, err := core.ParsePosition(arg)
	if err != nil {
		return nil, 0, err
	}
	file, err := ws.FileOf(fileName)
	if err != nil {
		return nil, 0, err
	}
	pos, err := ws.LineColToPos(file, line, col)
	if err != nil {
		return nil, 0, err
	}
	return file, pos, nil
}

// ---------------------------------------------------------------------------
// nav def

type defFlags struct {
	symbol          string
	name            string
	implementations bool
	typeDefinition  bool
}

// DefLocation is one resolved definition location.
type DefLocation struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"endLine"`
	EndCol  int    `json:"endCol"`
	Preview string `json:"preview,omitempty"`
}

// DefTarget is the definitions of one input target.
type DefTarget struct {
	Target      string         `json:"target"`
	Definitions []*DefLocation `json:"definitions"`
}

// DefResult is the `nav def` result, one item per target.
type DefResult struct {
	Targets []*DefTarget
}

var _ cli.Lister = (*DefResult)(nil)

func (r *DefResult) Total() int     { return len(r.Targets) }
func (r *DefResult) Item(i int) any { return r.Targets[i] }

func (r *DefResult) WriteItemText(w io.Writer, item any) error {
	t := item.(*DefTarget)
	if _, err := fmt.Fprintf(w, "%s\n", t.Target); err != nil {
		return err
	}
	if len(t.Definitions) == 0 {
		_, err := fmt.Fprintf(w, "  (no definition)\n")
		return err
	}
	for _, d := range t.Definitions {
		if _, err := fmt.Fprintf(w, "  %s:%d:%d  %s\n", d.File, d.Line, d.Col, d.Preview); err != nil {
			return err
		}
	}
	return nil
}

func runNavDef(ctx context.Context, ws *core.Workspace, flags *defFlags, args []string) (*DefResult, error) {
	if flags.implementations && flags.typeDefinition {
		return nil, cli.UsageErrorf("--implementations and --type-definition are mutually exclusive")
	}

	type defQuery struct {
		file *ast.SourceFile
		pos  int
	}
	var queries []defQuery
	if flags.symbol != "" || flags.name != "" {
		file, pos, err := resolveNavTarget(ctx, ws, flags.symbol, flags.name, args)
		if err != nil {
			return nil, err
		}
		queries = append(queries, defQuery{file, pos})
	} else {
		if len(args) == 0 {
			return nil, cli.UsageErrorf("expected at least one file:line:col target (or --symbol/--name)")
		}
		for _, arg := range args {
			file, pos, err := resolvePositionArg(ws, arg)
			if err != nil {
				return nil, err
			}
			queries = append(queries, defQuery{file, pos})
		}
	}

	result := &DefResult{}
	for _, q := range queries {
		uri := ws.URI(q.file.FileName())
		position := lspPosition(ws, q.file, q.pos)
		var resp lsproto.DefinitionResponse
		var err error
		switch {
		case flags.implementations:
			// nil orchestrator is the documented single-project mode of the
			// cross-project machinery (see ls.handleCrossProject).
			resp, err = ws.LS.ProvideImplementations(ctx, &lsproto.ImplementationParams{
				TextDocument: lsproto.TextDocumentIdentifier{Uri: uri},
				Position:     position,
			}, nil)
		case flags.typeDefinition:
			resp, err = ws.LS.ProvideTypeDefinition(ctx, uri, position)
		default:
			resp, err = ws.LS.ProvideDefinition(ctx, uri, position)
		}
		if err != nil {
			return nil, err
		}
		line, col := ws.PosToLineCol(q.file, q.pos)
		target := &DefTarget{Target: fmt.Sprintf("%s:%d:%d", ws.RelPath(q.file.FileName()), line, col)}
		for _, loc := range definitionResponseLocations(resp) {
			target.Definitions = append(target.Definitions, defLocation(ws, loc))
		}
		result.Targets = append(result.Targets, target)
	}
	return result, nil
}

func definitionResponseLocations(resp lsproto.DefinitionResponse) []lsproto.Location {
	switch {
	case resp.Location != nil:
		return []lsproto.Location{*resp.Location}
	case resp.Locations != nil:
		return *resp.Locations
	case resp.DefinitionLinks != nil:
		locations := make([]lsproto.Location, 0, len(*resp.DefinitionLinks))
		for _, link := range *resp.DefinitionLinks {
			locations = append(locations, lsproto.Location{Uri: link.TargetUri, Range: link.TargetSelectionRange})
		}
		return locations
	}
	return nil
}

func defLocation(ws *core.Workspace, loc lsproto.Location) *DefLocation {
	fileName := loc.Uri.FileName()
	d := &DefLocation{File: ws.RelPath(fileName)}
	if file := ws.Program.GetSourceFile(fileName); file != nil {
		textRange := ws.Conv.FromLSPRange(file, loc.Range)
		d.Line, d.Col = ws.PosToLineCol(file, textRange.Pos())
		d.EndLine, d.EndCol = ws.PosToLineCol(file, textRange.End())
		d.Preview = strings.TrimSpace(lineText(file, d.Line))
	} else {
		d.Line, d.Col = int(loc.Range.Start.Line)+1, int(loc.Range.Start.Character)+1
		d.EndLine, d.EndCol = int(loc.Range.End.Line)+1, int(loc.Range.End.Character)+1
	}
	return d
}

// ---------------------------------------------------------------------------
// nav refs

type refsFlags struct {
	symbol             string
	name               string
	includeDeclaration bool
	kind               string
	groupBy            string
}

// Ref is one classified reference.
type Ref struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Col       int    `json:"col"`
	UsageKind string `json:"usageKind"`
	Context   string `json:"context,omitempty"`
}

// RefsResult is the `nav refs` result: classified references plus per-kind
// totals (counted before the --kind filter so agents can refine queries).
type RefsResult struct {
	Refs   []*Ref         `json:"refs"`
	Counts map[string]int `json:"counts"`
	Totals int            `json:"total"`

	groupBy string
}

var _ cli.Texter = (*RefsResult)(nil)

func (r *RefsResult) WriteText(w io.Writer) error {
	if r.groupBy == "kind" {
		lastKind := ""
		for _, ref := range r.Refs {
			if ref.UsageKind != lastKind {
				lastKind = ref.UsageKind
				if _, err := fmt.Fprintf(w, "%s (%d)\n", lastKind, r.Counts[lastKind]); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "  %s:%d:%d  %s\n", ref.File, ref.Line, ref.Col, ref.Context); err != nil {
				return err
			}
		}
	} else {
		lastFile := ""
		for _, ref := range r.Refs {
			if ref.File != lastFile {
				lastFile = ref.File
				if _, err := fmt.Fprintf(w, "%s\n", lastFile); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "  %d:%d %s  %s\n", ref.Line, ref.Col, ref.UsageKind, ref.Context); err != nil {
				return err
			}
		}
	}
	kinds := make([]string, 0, len(r.Counts))
	for kind := range r.Counts {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s:%d", kind, r.Counts[kind]))
	}
	_, err := fmt.Fprintf(w, "total: %d  %s\n", r.Totals, strings.Join(parts, " "))
	return err
}

// usageKindRank fixes the order of kinds in group-by kind output.
var usageKindRank = map[string]int{
	"declaration": 0,
	"import":      1,
	"type":        2,
	"call":        3,
	"write":       4,
	"read":        5,
}

func runNavRefs(ctx context.Context, ws *core.Workspace, flags *refsFlags, args []string) (*RefsResult, error) {
	if flags.groupBy != "file" && flags.groupBy != "kind" {
		return nil, cli.UsageErrorf("invalid --group-by %q (want file or kind)", flags.groupBy)
	}
	kindFilter := cli.CommaSet(flags.kind)
	for kind := range kindFilter {
		if _, ok := usageKindRank[kind]; !ok {
			return nil, cli.UsageErrorf("invalid --kind entry %q (want declaration, import, type, call, write, or read)", kind)
		}
	}

	refs, err := collectRefs(ctx, ws, flags.symbol, flags.name, args)
	if err != nil {
		return nil, err
	}

	result := &RefsResult{Counts: make(map[string]int), groupBy: flags.groupBy}
	for _, ref := range refs {
		if ref.UsageKind == "declaration" && !flags.includeDeclaration {
			continue
		}
		result.Counts[ref.UsageKind]++
		result.Totals++
		if len(kindFilter) > 0 && !kindFilter[ref.UsageKind] {
			continue
		}
		result.Refs = append(result.Refs, ref)
	}
	if flags.groupBy == "kind" {
		slices.SortStableFunc(result.Refs, func(a, b *Ref) int {
			return usageKindRank[a.UsageKind] - usageKindRank[b.UsageKind]
		})
	}
	return result, nil
}

// collectRefs runs the raw reference search and classifies every node-backed
// entry. Results are sorted by file, line, col.
func collectRefs(ctx context.Context, ws *core.Workspace, symbol string, name string, args []string) ([]*Ref, error) {
	file, pos, err := resolveNavTarget(ctx, ws, symbol, name, args)
	if err != nil {
		return nil, err
	}
	node := astnav.GetTouchingPropertyName(file, pos)
	if node == nil || node.Kind == ast.KindSourceFile {
		return nil, cli.NotFoundErrorf("no symbol at %s:%d", ws.RelPath(file.FileName()), pos)
	}
	symbolsAndEntries := ws.LS.GetReferencedSymbolsForNode(ctx, pos, node, ws.Program.GetSourceFiles())

	// Name nodes (and declaration nodes) of the searched symbols classify as
	// "declaration".
	declNodes := make(map[*ast.Node]bool)
	for _, s := range symbolsAndEntries {
		if defNode := s.DefinitionNode(); defNode != nil {
			declNodes[defNode] = true
			if nameNode := ast.GetNameOfDeclaration(defNode); nameNode != nil {
				declNodes[nameNode] = true
			}
		}
		if defSymbol := s.DefinitionSymbol(); defSymbol != nil {
			for _, decl := range defSymbol.Declarations {
				declNodes[decl] = true
				if nameNode := ast.GetNameOfDeclaration(decl); nameNode != nil {
					declNodes[nameNode] = true
				}
			}
		}
	}

	var refs []*Ref
	seen := make(map[*ast.Node]bool)
	for _, s := range symbolsAndEntries {
		for _, entry := range s.References() {
			if !entry.IsNodeEntry() || seen[entry.Node()] {
				continue
			}
			seen[entry.Node()] = true
			refNode := entry.Node()
			refFile := ast.GetSourceFileOfNode(refNode)
			if refFile == nil {
				continue
			}
			start := astnav.GetStartOfNode(refNode, refFile, false /*includeJSDoc*/)
			line, col := ws.PosToLineCol(refFile, start)
			refs = append(refs, &Ref{
				File:      ws.RelPath(refFile.FileName()),
				Line:      line,
				Col:       col,
				UsageKind: classifyUsage(refNode, declNodes),
				Context:   strings.TrimSpace(lineText(refFile, line)),
			})
		}
	}
	slices.SortFunc(refs, func(a, b *Ref) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Col - b.Col
	})
	return refs, nil
}

// classifyUsage buckets a reference node into a usage kind. Order matters:
// import/export contexts win (an import specifier is also an alias
// declaration), then declarations, type positions, call sites, writes;
// everything else is a read.
func classifyUsage(node *ast.Node, declNodes map[*ast.Node]bool) string {
	switch {
	case isInImportOrExport(node):
		return "import"
	case declNodes[node]:
		return "declaration"
	case ast.IsPartOfTypeNode(node):
		return "type"
	case isCallee(node):
		return "call"
	case ast.IsWriteAccessForReference(node):
		return "write"
	default:
		return "read"
	}
}

func isInImportOrExport(node *ast.Node) bool {
	return ast.FindAncestor(node, func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindImportDeclaration, ast.KindImportEqualsDeclaration, ast.KindExportDeclaration, ast.KindJSDocImportTag:
			return true
		}
		return false
	}) != nil
}

// isCallee reports whether node is the callee of a call or `new` expression,
// looking through property accesses (`obj.method()` counts for `method`).
func isCallee(node *ast.Node) bool {
	target := node
	for target.Parent != nil && ast.IsPropertyAccessExpression(target.Parent) &&
		target.Parent.AsPropertyAccessExpression().Name() == target {
		target = target.Parent
	}
	parent := target.Parent
	if parent == nil {
		return false
	}
	if ast.IsCallExpression(parent) || ast.IsNewExpression(parent) {
		return parent.Expression() == target
	}
	return false
}

// ---------------------------------------------------------------------------
// nav usages

type usagesFlags struct {
	symbol       string
	name         string
	contextLines int
}

// Usage is one usage with a source excerpt.
type Usage struct {
	Line      int    `json:"line"`
	Col       int    `json:"col"`
	UsageKind string `json:"usageKind"`
	Excerpt   string `json:"excerpt"`
}

// FileUsages groups the usages within one file.
type FileUsages struct {
	File string `json:"file"`
	// Classification is "import-only" when every usage is an import,
	// "type-only" when every usage is an import or type position.
	Classification string   `json:"classification,omitempty"`
	Usages         []*Usage `json:"usages"`
}

// UsagesResult is the `nav usages` result, one item per file.
type UsagesResult struct {
	Files []*FileUsages
}

var _ cli.Lister = (*UsagesResult)(nil)

func (r *UsagesResult) Total() int     { return len(r.Files) }
func (r *UsagesResult) Item(i int) any { return r.Files[i] }

func (r *UsagesResult) WriteItemText(w io.Writer, item any) error {
	f := item.(*FileUsages)
	classification := ""
	if f.Classification != "" {
		classification = "  [" + f.Classification + "]"
	}
	if _, err := fmt.Fprintf(w, "%s (%d)%s\n", f.File, len(f.Usages), classification); err != nil {
		return err
	}
	for _, u := range f.Usages {
		if _, err := fmt.Fprintf(w, "  %d:%d %s\n", u.Line, u.Col, u.UsageKind); err != nil {
			return err
		}
		for line := range strings.SplitSeq(u.Excerpt, "\n") {
			if _, err := fmt.Fprintf(w, "    %s\n", line); err != nil {
				return err
			}
		}
	}
	return nil
}

func runNavUsages(ctx context.Context, ws *core.Workspace, flags *usagesFlags, args []string) (*UsagesResult, error) {
	if flags.contextLines < 0 {
		return nil, cli.UsageErrorf("--context-lines must be >= 0")
	}
	refs, err := collectRefs(ctx, ws, flags.symbol, flags.name, args)
	if err != nil {
		return nil, err
	}

	result := &UsagesResult{}
	byFile := make(map[string]*FileUsages)
	for _, ref := range refs {
		if ref.UsageKind == "declaration" {
			continue
		}
		group, ok := byFile[ref.File]
		if !ok {
			group = &FileUsages{File: ref.File}
			byFile[ref.File] = group
			result.Files = append(result.Files, group)
		}
		excerpt := ""
		if file, fileErr := ws.FileOf(ref.File); fileErr == nil {
			excerpt = buildExcerpt(file, ref.Line, flags.contextLines)
		}
		group.Usages = append(group.Usages, &Usage{
			Line:      ref.Line,
			Col:       ref.Col,
			UsageKind: ref.UsageKind,
			Excerpt:   excerpt,
		})
	}
	for _, group := range result.Files {
		group.Classification = classifyFileUsages(group.Usages)
	}
	return result, nil
}

func classifyFileUsages(usages []*Usage) string {
	importOnly, typeOnly := true, true
	for _, u := range usages {
		if u.UsageKind != "import" {
			importOnly = false
		}
		if u.UsageKind != "import" && u.UsageKind != "type" {
			typeOnly = false
		}
	}
	switch {
	case importOnly:
		return "import-only"
	case typeOnly:
		return "type-only"
	}
	return ""
}

func buildExcerpt(file *ast.SourceFile, line int, contextLines int) string {
	totalLines := len(file.ECMALineMap())
	var sb strings.Builder
	for l := max(1, line-contextLines); l <= min(totalLines, line+contextLines); l++ {
		marker := "  "
		if l == line {
			marker = "> "
		}
		fmt.Fprintf(&sb, "%s%d| %s\n", marker, l, lineText(file, l))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---------------------------------------------------------------------------
// nav calls

type callsFlags struct {
	symbol    string
	name      string
	direction string
	depth     int
}

// CallNode is one node in the call hierarchy tree.
type CallNode struct {
	Name     string      `json:"name"`
	File     string      `json:"file"`
	Line     int         `json:"line"`
	SymbolID string      `json:"symbolId,omitempty"`
	Cycle    bool        `json:"cycle,omitempty"`
	Calls    []*CallNode `json:"calls,omitempty"`
}

// CallsResult is the `nav calls` result.
type CallsResult struct {
	Direction string    `json:"direction"`
	Incoming  *CallNode `json:"incoming,omitempty"`
	Outgoing  *CallNode `json:"outgoing,omitempty"`
}

var _ cli.Texter = (*CallsResult)(nil)

func (r *CallsResult) WriteText(w io.Writer) error {
	if r.Incoming != nil {
		if _, err := fmt.Fprintf(w, "incoming (callers)\n"); err != nil {
			return err
		}
		if err := writeCallTreeText(w, r.Incoming, 1); err != nil {
			return err
		}
	}
	if r.Outgoing != nil {
		if _, err := fmt.Fprintf(w, "outgoing (callees)\n"); err != nil {
			return err
		}
		if err := writeCallTreeText(w, r.Outgoing, 1); err != nil {
			return err
		}
	}
	return nil
}

func writeCallTreeText(w io.Writer, node *CallNode, depth int) error {
	cycle := ""
	if node.Cycle {
		cycle = "  [cycle]"
	}
	id := ""
	if node.SymbolID != "" {
		id = "  " + node.SymbolID
	}
	if _, err := fmt.Fprintf(w, "%s%s  %s:%d%s%s\n", cli.Indent(depth), node.Name, node.File, node.Line, id, cycle); err != nil {
		return err
	}
	for _, child := range node.Calls {
		if err := writeCallTreeText(w, child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func runNavCalls(ctx context.Context, ws *core.Workspace, flags *callsFlags, args []string) (*CallsResult, error) {
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

	walker := &callWalker{ctx: ctx, ws: ws, maxDepth: flags.depth}
	result := &CallsResult{Direction: flags.direction}
	if flags.direction == "in" || flags.direction == "both" {
		result.Incoming, err = walker.expand(root, true /*incoming*/, 0, make(map[string]bool))
		if err != nil {
			return nil, err
		}
	}
	if flags.direction == "out" || flags.direction == "both" {
		result.Outgoing, err = walker.expand(root, false /*incoming*/, 0, make(map[string]bool))
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

type callWalker struct {
	ctx      context.Context
	ws       *core.Workspace
	maxDepth int
}

func callItemKey(item *lsproto.CallHierarchyItem) string {
	return fmt.Sprintf("%s:%d:%d", item.Uri, item.SelectionRange.Start.Line, item.SelectionRange.Start.Character)
}

// expand builds the call tree below item. path is the set of items on the
// current root-to-node chain; re-encountering one marks a cycle and stops.
func (w *callWalker) expand(item *lsproto.CallHierarchyItem, incoming bool, depth int, path map[string]bool) (*CallNode, error) {
	node := w.makeNode(item)
	key := callItemKey(item)
	if path[key] {
		node.Cycle = true
		return node, nil
	}
	if depth >= w.maxDepth {
		return node, nil
	}
	path[key] = true
	defer delete(path, key)

	var children []*lsproto.CallHierarchyItem
	if incoming {
		resp, err := w.ws.LS.ProvideCallHierarchyIncomingCalls(w.ctx, item, nil /*orchestrator: single-project mode*/)
		if err != nil {
			return nil, err
		}
		if resp.CallHierarchyIncomingCalls != nil {
			for _, call := range *resp.CallHierarchyIncomingCalls {
				children = append(children, call.From)
			}
		}
	} else {
		resp, err := w.ws.LS.ProvideCallHierarchyOutgoingCalls(w.ctx, item)
		if err != nil {
			return nil, err
		}
		if resp.CallHierarchyOutgoingCalls != nil {
			for _, call := range *resp.CallHierarchyOutgoingCalls {
				children = append(children, call.To)
			}
		}
	}
	for _, child := range children {
		childNode, err := w.expand(child, incoming, depth+1, path)
		if err != nil {
			return nil, err
		}
		node.Calls = append(node.Calls, childNode)
	}
	return node, nil
}

func (w *callWalker) makeNode(item *lsproto.CallHierarchyItem) *CallNode {
	fileName := item.Uri.FileName()
	node := &CallNode{
		Name: item.Name,
		File: w.ws.RelPath(fileName),
		Line: int(item.SelectionRange.Start.Line) + 1,
	}
	node.SymbolID = w.symbolIDAt(fileName, item.SelectionRange.Start)
	return node
}

// symbolIDAt resolves a position back to a symbol ID, best effort.
func (w *callWalker) symbolIDAt(fileName string, position lsproto.Position) string {
	file := w.ws.Program.GetSourceFile(fileName)
	if file == nil {
		return ""
	}
	pos := int(w.ws.Conv.LineAndCharacterToPosition(file, position))
	token := astnav.GetTouchingPropertyName(file, pos)
	if token == nil {
		return ""
	}
	checker, done := w.ws.Program.GetTypeCheckerForFile(w.ctx, file)
	defer done()
	return core.EncodeSymbolID(w.ws, checker.GetSymbolAtLocation(token))
}

// ---------------------------------------------------------------------------
// nav graph

type graphFlags struct {
	scope     string
	cycles    bool
	externals bool
}

// GraphNode is one node of the emitted dependency graph.
type GraphNode struct {
	ID       int    `json:"id"`
	Path     string `json:"path"`
	External bool   `json:"external,omitempty"`
}

// GraphEdge is one importer → imported edge.
type GraphEdge struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// GraphResult is the `nav graph` result.
type GraphResult struct {
	Nodes  []*GraphNode `json:"nodes"`
	Edges  []*GraphEdge `json:"edges"`
	Cycles [][]int      `json:"cycles"`
}

var _ cli.Texter = (*GraphResult)(nil)

func (r *GraphResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "nodes: %d  edges: %d  cycles: %d\n", len(r.Nodes), len(r.Edges), len(r.Cycles)); err != nil {
		return err
	}
	paths := make(map[int]string, len(r.Nodes))
	for _, n := range r.Nodes {
		paths[n.ID] = n.Path
	}
	for _, e := range r.Edges {
		if _, err := fmt.Fprintf(w, "%s -> %s\n", paths[e.From], paths[e.To]); err != nil {
			return err
		}
	}
	for _, cycle := range r.Cycles {
		names := make([]string, 0, len(cycle))
		for _, id := range cycle {
			names = append(names, paths[id])
		}
		if _, err := fmt.Fprintf(w, "cycle: %s\n", strings.Join(names, " -> ")); err != nil {
			return err
		}
	}
	return nil
}

func runNavGraph(ctx context.Context, ws *core.Workspace, flags *graphFlags, args []string) (*GraphResult, error) {
	if flags.scope != "file" && flags.scope != "dir" {
		return nil, cli.UsageErrorf("invalid --scope %q (want file or dir)", flags.scope)
	}
	graph := core.BuildImportGraph(ws)
	if !flags.externals {
		graph = graph.Filter(func(n *core.ImportGraphNode) bool { return !n.External })
	}
	if flags.scope == "dir" {
		graph = graph.Collapse(func(n *core.ImportGraphNode) string {
			dir := tspath.GetDirectoryPath(ws.RelPath(n.FileName))
			if dir == "" {
				return "."
			}
			return dir
		})
	}

	cycles := graph.Cycles()
	inCycle := make(map[int]bool)
	for _, cycle := range cycles {
		for _, id := range cycle {
			inCycle[id] = true
		}
	}

	result := &GraphResult{Cycles: cycles}
	if result.Cycles == nil {
		result.Cycles = [][]int{}
	}
	for _, n := range graph.Nodes {
		if flags.cycles && !inCycle[n.ID] {
			continue
		}
		path := n.FileName
		if flags.scope == "file" {
			path = ws.RelPath(n.FileName)
		}
		result.Nodes = append(result.Nodes, &GraphNode{ID: n.ID, Path: path, External: n.External})
	}
	for _, e := range graph.Edges {
		if flags.cycles && !(inCycle[e.From] && inCycle[e.To]) {
			continue
		}
		result.Edges = append(result.Edges, &GraphEdge{From: e.From, To: e.To})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// nav path

type pathFlags struct {
	from     string
	to       string
	maxPaths int
}

// PathHop is one edge of an import chain: the From file imports the To file
// at ImportLine.
type PathHop struct {
	From       string `json:"from"`
	To         string `json:"to"`
	ImportLine int    `json:"importLine"`
	ImportText string `json:"importText"`
}

// ImportChain is one shortest import path.
type ImportChain struct {
	Hops []*PathHop `json:"hops"`
}

// PathResult is the `nav path` result.
type PathResult struct {
	From  string         `json:"from"`
	To    string         `json:"to"`
	Paths []*ImportChain `json:"paths"`
}

var _ cli.Texter = (*PathResult)(nil)

func (r *PathResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s -> %s (%d path(s))\n", r.From, r.To, len(r.Paths)); err != nil {
		return err
	}
	for _, chain := range r.Paths {
		if _, err := fmt.Fprintf(w, "%s\n", r.From); err != nil {
			return err
		}
		for _, hop := range chain.Hops {
			if _, err := fmt.Fprintf(w, "  -> %s  (%s:%d %s)\n", hop.To, hop.From, hop.ImportLine, hop.ImportText); err != nil {
				return err
			}
		}
	}
	return nil
}

func runNavPath(ctx context.Context, ws *core.Workspace, flags *pathFlags, args []string) (*PathResult, error) {
	if flags.from == "" || flags.to == "" {
		return nil, cli.UsageErrorf("nav path requires --from and --to")
	}
	if flags.maxPaths < 1 {
		return nil, cli.UsageErrorf("--max-paths must be >= 1")
	}
	fromFile, err := fileOfPathArg(ws, flags.from)
	if err != nil {
		return nil, err
	}
	toFile, err := fileOfPathArg(ws, flags.to)
	if err != nil {
		return nil, err
	}
	relFrom, relTo := ws.RelPath(fromFile.FileName()), ws.RelPath(toFile.FileName())
	if fromFile == toFile {
		return nil, cli.UsageErrorf("--from and --to are the same file (%s)", relFrom)
	}

	graph := core.BuildImportGraph(ws)
	fromID, okFrom := graph.NodeID(fromFile.FileName())
	toID, okTo := graph.NodeID(toFile.FileName())
	if !okFrom || !okTo {
		return nil, cli.NotFoundErrorf("unreachable: %s or %s is not in the import graph", relFrom, relTo)
	}
	paths := graph.ShortestPaths(fromID, toID, flags.maxPaths)
	if len(paths) == 0 {
		return nil, cli.NotFoundErrorf("unreachable: no import path from %s to %s", relFrom, relTo)
	}

	result := &PathResult{From: relFrom, To: relTo}
	for _, path := range paths {
		chain := &ImportChain{}
		for _, edge := range path {
			fromNode := graph.Nodes[edge.From]
			hop := &PathHop{
				From: ws.RelPath(fromNode.FileName),
				To:   ws.RelPath(graph.Nodes[edge.To].FileName),
			}
			if file := ws.Program.GetSourceFile(fromNode.FileName); file != nil {
				line, _ := ws.PosToLineCol(file, edge.Pos)
				hop.ImportLine = line
				hop.ImportText = strings.TrimSpace(lineText(file, line))
			}
			chain.Hops = append(chain.Hops, hop)
		}
		result.Paths = append(result.Paths, chain)
	}
	return result, nil
}

// fileOfPathArg accepts either a file path or a file:line:col target and
// resolves the file.
func fileOfPathArg(ws *core.Workspace, arg string) (*ast.SourceFile, error) {
	if fileName, _, _, err := core.ParsePosition(arg); err == nil {
		return ws.FileOf(fileName)
	}
	return ws.FileOf(arg)
}
