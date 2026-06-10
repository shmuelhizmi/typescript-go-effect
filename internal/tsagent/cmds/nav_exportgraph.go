package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

func init() {
	cli.Register(cli.Command{
		Family:       "nav",
		Name:         "export-graph",
		Summary:      "Re-export/barrel flow of a symbol: every hop and public name it is reachable as",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &exportGraphFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavExportGraph(ctx, ws, flags.(*exportGraphFlags), args)
		},
	})
}

type exportGraphFlags struct {
	symbol string
	name   string
}

// ExportHop is one node of the re-export tree: file exposes the symbol under
// ExportedAs, reached Via the given mechanism.
type ExportHop struct {
	File        string       `json:"file"`
	Line        int          `json:"line,omitempty"`
	ExportedAs  string       `json:"exportedAs"`
	Via         string       `json:"via"` // declaration | named | star | namespace
	PublicEntry bool         `json:"publicEntry,omitempty"`
	Hops        []*ExportHop `json:"hops,omitempty"`
}

// ExportGraphResult is the `nav export-graph` result: the declaration and the
// tree of re-export hops.
type ExportGraphResult struct {
	SymbolID string     `json:"symbolId,omitempty"`
	Root     *ExportHop `json:"root"`
}

var _ cli.Texter = (*ExportGraphResult)(nil)

func (r *ExportGraphResult) WriteText(w io.Writer) error {
	return writeExportHopText(w, r.Root, 0)
}

func writeExportHopText(w io.Writer, hop *ExportHop, depth int) error {
	entry := ""
	if hop.PublicEntry {
		entry = "  [public-entry]"
	}
	if _, err := fmt.Fprintf(w, "%s%s:%d  %s  (%s)%s\n", cli.Indent(depth), hop.File, hop.Line, hop.ExportedAs, hop.Via, entry); err != nil {
		return err
	}
	for _, child := range hop.Hops {
		if err := writeExportHopText(w, child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// reExportRecord is one export statement edge: file re-exposes sourceName of
// the module `from` as exportedName. from == nil for `export { A as B }`
// statements that alias a local binding (applied only in the declaring file).
type reExportRecord struct {
	file         *ast.SourceFile
	from         *ast.SourceFile
	sourceName   string // "" for star (any name flows through unchanged)
	exportedName string // "" for star (same name); the namespace name for `export * as ns`
	via          string // named | star | namespace
	line         int
}

func runNavExportGraph(ctx context.Context, ws *core.Workspace, flags *exportGraphFlags, args []string) (*ExportGraphResult, error) {
	file, pos, err := resolveNavTarget(ctx, ws, flags.symbol, flags.name, args)
	if err != nil {
		return nil, err
	}
	token := astnav.GetTouchingPropertyName(file, pos)
	if token == nil || token.Kind == ast.KindSourceFile {
		return nil, cli.NotFoundErrorf("no symbol at %s:%d", ws.RelPath(file.FileName()), pos)
	}
	checker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	sym := checker.GetSymbolAtLocation(token)
	if sym != nil && sym.Flags&ast.SymbolFlagsAlias != 0 {
		sym = checker.GetAliasedSymbol(sym)
	}
	done()
	if sym == nil {
		line, col := ws.PosToLineCol(file, pos)
		return nil, cli.NotFoundErrorf("no symbol at %s:%d:%d", ws.RelPath(file.FileName()), line, col)
	}
	decl := sym.ValueDeclaration
	if decl == nil && len(sym.Declarations) > 0 {
		decl = sym.Declarations[0]
	}
	if decl == nil {
		return nil, cli.NotFoundErrorf("symbol %s has no declaration", sym.Name)
	}
	declFile := ast.GetSourceFileOfNode(decl)
	startName := sym.Name
	if rest, ok := strings.CutPrefix(startName, ast.InternalSymbolNamePrefix); ok {
		startName = rest // e.g. export default → "default"
	}

	bySource, localByFile := collectReExports(ws)

	line, _ := declLineOf(ws, decl)
	root := &ExportHop{
		File:       ws.RelPath(declFile.FileName()),
		Line:       line,
		ExportedAs: startName,
		Via:        "declaration",
	}

	type egState struct {
		file *ast.SourceFile
		name string
	}
	type queueItem struct {
		state egState
		node  *ExportHop
	}
	start := egState{file: declFile, name: startName}
	visited := map[egState]bool{start: true}
	queue := []queueItem{{state: start, node: root}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		expand := func(r *reExportRecord) {
			var childName, via string
			switch r.via {
			case "star":
				if cur.state.name == "default" || r.sourceName != "" {
					return
				}
				childName, via = cur.state.name, "star"
			case "namespace":
				if cur.state.name == "default" {
					return
				}
				// The symbol becomes a property of the namespace object; the
				// dotted name is terminal (we do not track property flow).
				cur.node.Hops = append(cur.node.Hops, &ExportHop{
					File:       ws.RelPath(r.file.FileName()),
					Line:       r.line,
					ExportedAs: r.exportedName + "." + cur.state.name,
					Via:        "namespace",
				})
				return
			default: // named
				if r.sourceName != cur.state.name {
					return
				}
				childName, via = r.exportedName, "named"
			}
			next := egState{file: r.file, name: childName}
			if visited[next] {
				return
			}
			visited[next] = true
			child := &ExportHop{
				File:       ws.RelPath(r.file.FileName()),
				Line:       r.line,
				ExportedAs: childName,
				Via:        via,
			}
			cur.node.Hops = append(cur.node.Hops, child)
			queue = append(queue, queueItem{state: next, node: child})
		}

		for _, r := range bySource[cur.state.file] {
			expand(r)
		}
		// Local `export { A as B }` aliases only re-expose the declaring
		// module's own bindings.
		if cur.state.file == declFile {
			for _, r := range localByFile[cur.state.file] {
				expand(r)
			}
		}
	}

	markPublicEntries(root)
	return &ExportGraphResult{SymbolID: core.EncodeSymbolID(ws, sym), Root: root}, nil
}

// collectReExports scans every project file's top-level statements for
// export edges: `export {X as Y} from './m'`, `export * from './m'`,
// `export * as ns from './m'`, and `import {X} from './m'; export {X as Y}`.
// Records are indexed by the source module; purely local `export {A as B}`
// statements are returned separately per file.
func collectReExports(ws *core.Workspace) (bySource map[*ast.SourceFile][]*reExportRecord, localByFile map[*ast.SourceFile][]*reExportRecord) {
	bySource = make(map[*ast.SourceFile][]*reExportRecord)
	localByFile = make(map[*ast.SourceFile][]*reExportRecord)
	files, err := projectFiles(ws, nil)
	if err != nil {
		return bySource, localByFile
	}
	for _, file := range files {
		type importOrigin struct {
			module *ast.SourceFile
			name   string
		}
		imports := make(map[string]importOrigin) // local binding → origin
		resolveModule := func(specifier *ast.Node) *ast.SourceFile {
			if specifier == nil || !ast.IsStringLiteral(specifier) {
				return nil
			}
			resolved := ws.Program.GetResolvedModuleFromModuleSpecifier(file, specifier)
			if !resolved.IsResolved() {
				return nil
			}
			return ws.Program.GetSourceFile(resolved.ResolvedFileName)
		}
		for _, statement := range file.Statements.Nodes {
			if statement.Kind != ast.KindImportDeclaration {
				continue
			}
			d := statement.AsImportDeclaration()
			module := resolveModule(d.ModuleSpecifier)
			if module == nil || d.ImportClause == nil {
				continue
			}
			clause := d.ImportClause.AsImportClause()
			if clause.Name() != nil {
				imports[clause.Name().Text()] = importOrigin{module: module, name: "default"}
			}
			if clause.NamedBindings != nil && clause.NamedBindings.Kind == ast.KindNamedImports {
				for _, element := range clause.NamedBindings.AsNamedImports().Elements.Nodes {
					spec := element.AsImportSpecifier()
					local := spec.Name().Text()
					original := local
					if spec.PropertyName != nil {
						original = spec.PropertyName.Text()
					}
					imports[local] = importOrigin{module: module, name: original}
				}
			}
		}
		for _, statement := range file.Statements.Nodes {
			if statement.Kind != ast.KindExportDeclaration {
				continue
			}
			d := statement.AsExportDeclaration()
			line, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(statement, file, false /*includeJSDoc*/))
			if d.ModuleSpecifier != nil {
				module := resolveModule(d.ModuleSpecifier)
				if module == nil {
					continue
				}
				switch {
				case d.ExportClause == nil:
					record := &reExportRecord{file: file, from: module, via: "star", line: line}
					bySource[module] = append(bySource[module], record)
				case d.ExportClause.Kind == ast.KindNamespaceExport:
					record := &reExportRecord{
						file:         file,
						from:         module,
						exportedName: d.ExportClause.AsNamespaceExport().Name().Text(),
						via:          "namespace",
						line:         line,
					}
					bySource[module] = append(bySource[module], record)
				case d.ExportClause.Kind == ast.KindNamedExports:
					for _, element := range d.ExportClause.AsNamedExports().Elements.Nodes {
						spec := element.AsExportSpecifier()
						exported := spec.Name().Text()
						source := exported
						if spec.PropertyName != nil {
							source = spec.PropertyName.Text()
						}
						record := &reExportRecord{
							file:         file,
							from:         module,
							sourceName:   source,
							exportedName: exported,
							via:          "named",
							line:         line,
						}
						bySource[module] = append(bySource[module], record)
					}
				}
				continue
			}
			// `export { A as B }` without a module specifier: the local
			// binding A is either an import (a real re-export hop) or a local
			// declaration of this file (a same-file alias).
			if d.ExportClause == nil || d.ExportClause.Kind != ast.KindNamedExports {
				continue
			}
			for _, element := range d.ExportClause.AsNamedExports().Elements.Nodes {
				spec := element.AsExportSpecifier()
				exported := spec.Name().Text()
				local := exported
				if spec.PropertyName != nil {
					local = spec.PropertyName.Text()
				}
				record := &reExportRecord{
					file:         file,
					sourceName:   local,
					exportedName: exported,
					via:          "named",
					line:         line,
				}
				if origin, ok := imports[local]; ok {
					record.from = origin.module
					record.sourceName = origin.name
					bySource[origin.module] = append(bySource[origin.module], record)
				} else {
					localByFile[file] = append(localByFile[file], record)
				}
			}
		}
	}
	return bySource, localByFile
}

// markPublicEntries flags leaf hops in files named index.* as public entry
// points (best effort; package.json mains are not consulted).
func markPublicEntries(hop *ExportHop) {
	if len(hop.Hops) == 0 {
		base := tspath.GetBaseFileName(hop.File)
		if strings.HasPrefix(base, "index.") {
			hop.PublicEntry = true
		}
		return
	}
	for _, child := range hop.Hops {
		markPublicEntries(child)
	}
}
