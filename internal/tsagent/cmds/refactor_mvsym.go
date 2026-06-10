package cmds

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// refactor mv-symbol (§4.4), v1 semantics: move ONE top-level declaration
// (function/class/interface/type/enum/sole-declarator const) to another
// project file. The declaration text is moved verbatim (JSDoc included),
// exported in the destination when it has external references, importers are
// rewritten to import from the new file, and the original file imports the
// symbol back if it still uses it. Refused (exit 4): declarations that
// reference file-local unexported symbols (--with-deps is not implemented),
// default-exported or overloaded declarations.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "mv-symbol",
		Summary:      "Move a top-level declaration to another file, fixing imports both ways",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorMvSymbolFlags{}
			registerRefactorTargetFlags(fs, &f.target)
			registerRefactorTxFlags(fs, &f.tx)
			fs.StringVar(&f.to, "to", "", "destination file (project-relative or absolute)")
			fs.BoolVar(&f.create, "create", false, "create the destination file if it does not exist")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorMvSymbol(ctx, ws, flags.(*refactorMvSymbolFlags), args)
		},
	})
}

type refactorMvSymbolFlags struct {
	target refactorTargetFlags
	tx     refactorTxFlags
	to     string
	create bool
}

func runRefactorMvSymbol(ctx context.Context, ws *core.Workspace, f *refactorMvSymbolFlags, args []string) (*core.TxResult, error) {
	if f.to == "" {
		return nil, cli.UsageErrorf("--to <file> is required")
	}
	target, rest, err := resolveRefactorTarget(ctx, ws, &f.target, args)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, cli.UsageErrorf("unexpected extra arguments: %s", strings.Join(rest, " "))
	}
	symbol := target.Symbol
	if symbol == nil || len(symbol.Declarations) == 0 {
		return nil, cli.NotFoundErrorf("target does not resolve to a symbol with declarations")
	}
	if len(symbol.Declarations) > 1 {
		return nil, cli.RefusedErrorf("symbol has %d declarations (overloads/merged declarations are not supported yet)", len(symbol.Declarations))
	}
	decl := symbol.Declarations[0]
	declNode := refactorDeletionNode(decl)
	if declNode.Parent == nil || declNode.Parent.Kind != ast.KindSourceFile {
		return nil, cli.RefusedErrorf("only top-level declarations can be moved (target is nested)")
	}
	if declNode.ModifierFlags()&ast.ModifierFlagsDefault != 0 {
		return nil, cli.RefusedErrorf("default-exported declarations cannot be moved; convert to a named export first (refactor exports --to named)")
	}
	nameNode := ast.GetNameOfDeclaration(decl)
	if nameNode == nil {
		return nil, cli.RefusedErrorf("the declaration has no name")
	}
	symbolName := nameNode.Text()

	sourceFile := ast.GetSourceFileOfNode(declNode)
	sourceText := sourceFile.Text()

	// Destination resolution.
	destAbs := refactorResolveEditPath(ws, f.to)
	destFile := ws.Program.GetSourceFile(destAbs)
	creating := false
	if destFile == nil {
		if ws.FS.FileExists(destAbs) {
			return nil, cli.RefusedErrorf("destination %s exists but is not part of the program", ws.RelPath(destAbs))
		}
		if !f.create {
			return nil, cli.UsageErrorf("destination %s does not exist (pass --create to create it)", ws.RelPath(destAbs))
		}
		creating = true
	}
	if destFile == sourceFile {
		return nil, cli.UsageErrorf("the symbol already lives in %s", ws.RelPath(destAbs))
	}

	// Dependency scan of the moved declaration: file-local unexported
	// top-level symbols block the move (v1, no --with-deps); exported
	// same-file symbols and imported names are re-imported in the
	// destination.
	deps, blockers := refactorMvSymbolDeps(ctx, ws, sourceFile, declNode, symbol)
	if len(blockers) > 0 {
		slices.Sort(blockers)
		return nil, cli.RefusedErrorf("cannot move %q: it references %d file-local unexported symbol(s) (--with-deps is not implemented yet):\n  %s",
			symbolName, len(blockers), strings.Join(blockers, "\n  "))
	}

	// Reference partition.
	refs := refactorReferenceNodes(ctx, ws, nameNode)
	insideDecl := func(n *ast.Node) bool {
		return ast.GetSourceFileOfNode(n) == sourceFile && n.Pos() >= declNode.Pos() && n.End() <= declNode.End()
	}
	sourceStillUses := false
	refFiles := make(map[*ast.SourceFile]bool)
	for _, ref := range refs {
		if insideDecl(ref) {
			continue
		}
		refFile := ast.GetSourceFileOfNode(ref)
		if refFile == sourceFile {
			// Ignore export specifiers (`export { X }`) — they are removed below.
			if ref.Parent != nil && ref.Parent.Kind == ast.KindExportSpecifier {
				return nil, cli.RefusedErrorf("%q is re-exported via an export clause in %s; remove or update that export first",
					symbolName, ws.RelPath(refFile.FileName()))
			}
			sourceStillUses = true
			continue
		}
		refFiles[refFile] = true
	}
	hasExternalRefs := sourceStillUses || len(refFiles) > 0
	wasExported := declNode.ModifierFlags()&ast.ModifierFlagsExport != 0

	var es core.EditSet
	var notes []string

	// (a) Remove the declaration from the source file; (d) import it back if
	// the source still references it.
	deletionRange := refactorDeletionRange(sourceFile, declNode)
	sourceEdits := []icore.TextChange{{TextRange: deletionRange, NewText: ""}}
	if sourceStillUses {
		sourceEdits = append(sourceEdits, refactorInsertImportEdit([]string{symbolName},
			refactorModuleSpecifierText(ws, sourceFile.FileName(), destAbs)))
		notes = append(notes, fmt.Sprintf("%s still uses %s and now imports it from %s",
			ws.RelPath(sourceFile.FileName()), symbolName, ws.RelPath(destAbs)))
	}
	es.Edits = append(es.Edits, core.FileEdit{FileName: sourceFile.FileName(), Edits: sourceEdits})

	// (b)+(c) Build the moved text, exporting it when anything references it.
	movedText := sourceText[deletionRange.Pos():deletionRange.End()]
	if hasExternalRefs && !wasExported {
		declStart := astnav.GetStartOfNode(declNode, sourceFile, false /*includeJSDoc*/)
		offset := declStart - deletionRange.Pos()
		movedText = movedText[:offset] + "export " + movedText[offset:]
		notes = append(notes, fmt.Sprintf("%s was exported in the destination (it has references elsewhere)", symbolName))
	}
	if !strings.HasSuffix(movedText, "\n") {
		movedText += "\n"
	}

	// Imports the moved declaration needs in the destination.
	destImports := refactorMvSymbolDepImports(ws, destAbs, destFile, deps)

	if creating {
		es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpCreate, Path: destAbs, Content: destImports + movedText})
	} else {
		destText := destFile.Text()
		var destEdits []icore.TextChange
		if destImports != "" {
			destEdits = append(destEdits, icore.TextChange{TextRange: icore.NewTextRange(0, 0), NewText: destImports})
		}
		insert := "\n" + movedText
		if strings.HasSuffix(destText, "\n") {
			insert = movedText
		}
		destEdits = append(destEdits, icore.TextChange{TextRange: icore.NewTextRange(len(destText), len(destText)), NewText: insert})
		// The destination may have imported the symbol from the source file;
		// that import must go away now that the declaration is local.
		destEdits = append(destEdits, refactorDropImportOfName(ws, destFile, sourceFile.FileName(), symbolName)...)
		es.Edits = append(es.Edits, core.FileEdit{FileName: destFile.FileName(), Edits: destEdits})
	}

	// (e) Rewrite every other importer: drop the specifier from the old
	// import, add an import from the new file.
	newSpecRel := func(importer *ast.SourceFile) string {
		return refactorModuleSpecifierText(ws, importer.FileName(), destAbs)
	}
	for refFile := range refFiles {
		if refFile == destFile {
			continue // handled above
		}
		var edits []icore.TextChange
		localName := symbolName
		found := false
		for _, importDecl := range refactorImportsResolvingTo(ws, refFile, sourceFile.FileName()) {
			for _, spec := range refactorNamedImportSpecifiers(importDecl) {
				if refactorImportedName(spec) != symbolName {
					continue
				}
				found = true
				localName = spec.Name().Text()
				edits = append(edits, refactorRemoveImportSpecifierEdit(refFile, importDecl, spec))
			}
		}
		if !found {
			return nil, cli.RefusedErrorf("%s references %q without a rewritable named import (namespace imports and re-exports are not supported yet)",
				ws.RelPath(refFile.FileName()), symbolName)
		}
		importName := symbolName
		if localName != symbolName {
			importName = symbolName + " as " + localName
		}
		edits = append(edits, refactorInsertImportEdit([]string{importName}, newSpecRel(refFile)))
		es.Edits = append(es.Edits, core.FileEdit{FileName: refFile.FileName(), Edits: edits})
	}

	return finishRefactorTx(ctx, ws, es, &f.tx, notes)
}

// refactorMvSymbolDep is one symbol the moved declaration references that
// must be importable from the destination.
type refactorMvSymbolDep struct {
	name      string // exported name to import
	localName string // local alias used inside the moved text
	fromFile  string // absolute file to import from ("" = keep original specifier)
	origSpec  string // original module specifier text (for unresolved/package imports)
}

// refactorMvSymbolDeps walks the moved declaration's identifiers and
// classifies their symbols: same-file unexported top-level declarations are
// blockers; same-file exported declarations and import bindings become
// destination imports.
func refactorMvSymbolDeps(ctx context.Context, ws *core.Workspace, sourceFile *ast.SourceFile, declNode *ast.Node, moved *ast.Symbol) (deps []refactorMvSymbolDep, blockers []string) {
	checker, done := ws.Program.GetTypeCheckerForFile(ctx, sourceFile)
	defer done()
	seen := make(map[string]bool)
	for _, id := range refactorCollectIdentifiers(declNode) {
		sym := checker.GetSymbolAtLocation(id)
		if sym == nil || sym == moved || len(sym.Declarations) == 0 {
			continue
		}
		depDecl := sym.Declarations[0]
		if ast.GetSourceFileOfNode(depDecl) != sourceFile {
			continue // globals, libs, other files reached via type references
		}
		// Declared inside the moved declaration itself: moves along.
		if depDecl.Pos() >= declNode.Pos() && depDecl.End() <= declNode.End() {
			continue
		}
		switch depDecl.Kind {
		case ast.KindImportSpecifier, ast.KindImportClause, ast.KindNamespaceImport:
			dep, ok := refactorImportBindingDep(ws, sourceFile, depDecl)
			if !ok {
				blockers = append(blockers, fmt.Sprintf("%s (namespace/default import binding, not rewritable yet)", id.Text()))
				continue
			}
			if !seen[dep.localName] {
				seen[dep.localName] = true
				deps = append(deps, dep)
			}
		default:
			widened := refactorDeletionNode(depDecl)
			if widened.Parent == nil || widened.Parent.Kind != ast.KindSourceFile {
				continue // locals of enclosing scopes cannot occur for top-level decls
			}
			exported := widened.ModifierFlags()&ast.ModifierFlagsExport != 0 || depDecl.ModifierFlags()&ast.ModifierFlagsExport != 0
			if !exported {
				loc := fmt.Sprintf("%s (%s)", id.Text(), refactorNodeLineCol(ws, depDecl))
				if !seen["!"+loc] {
					seen["!"+loc] = true
					blockers = append(blockers, loc)
				}
				continue
			}
			if !seen[id.Text()] {
				seen[id.Text()] = true
				deps = append(deps, refactorMvSymbolDep{name: id.Text(), localName: id.Text(), fromFile: sourceFile.FileName()})
			}
		}
	}
	return deps, blockers
}

// refactorImportBindingDep converts a named-import specifier used by the
// moved declaration into a destination import. Default and namespace imports
// are not rewritable in v1.
func refactorImportBindingDep(ws *core.Workspace, sourceFile *ast.SourceFile, depDecl *ast.Node) (refactorMvSymbolDep, bool) {
	if depDecl.Kind != ast.KindImportSpecifier {
		return refactorMvSymbolDep{}, false
	}
	importDecl := ast.FindAncestor(depDecl, ast.IsImportDeclaration)
	if importDecl == nil {
		return refactorMvSymbolDep{}, false
	}
	specNode := importDecl.AsImportDeclaration().ModuleSpecifier
	dep := refactorMvSymbolDep{
		name:      refactorImportedName(depDecl),
		localName: depDecl.Name().Text(),
		origSpec:  specNode.Text(),
	}
	resolved := ws.Program.GetResolvedModuleFromModuleSpecifier(sourceFile, specNode)
	if resolved.IsResolved() && !core.IsExternalLibraryPath(resolved.ResolvedFileName) {
		dep.fromFile = resolved.ResolvedFileName
	}
	return dep, true
}

// refactorMvSymbolDepImports renders the import statements the destination
// needs for the moved declaration's dependencies. Names already declared in
// the destination are skipped.
func refactorMvSymbolDepImports(ws *core.Workspace, destAbs string, destFile *ast.SourceFile, deps []refactorMvSymbolDep) string {
	bySpec := make(map[string][]string)
	var order []string
	for _, dep := range deps {
		if destFile != nil && len(destFile.GetDeclarationMap()[dep.localName]) > 0 {
			continue // already available in the destination
		}
		spec := dep.origSpec
		if dep.fromFile != "" {
			spec = refactorModuleSpecifierText(ws, destAbs, dep.fromFile)
		}
		name := dep.name
		if dep.localName != dep.name {
			name = dep.name + " as " + dep.localName
		}
		if _, ok := bySpec[spec]; !ok {
			order = append(order, spec)
		}
		bySpec[spec] = append(bySpec[spec], name)
	}
	var sb strings.Builder
	for _, spec := range order {
		sb.WriteString("import { " + strings.Join(bySpec[spec], ", ") + " } from \"" + spec + "\";\n")
	}
	return sb.String()
}

// refactorDropImportOfName removes the named-import specifier binding `name`
// from imports of fromFileName inside file (no-op when absent).
func refactorDropImportOfName(ws *core.Workspace, file *ast.SourceFile, fromFileName string, name string) []icore.TextChange {
	var edits []icore.TextChange
	for _, importDecl := range refactorImportsResolvingTo(ws, file, fromFileName) {
		for _, spec := range refactorNamedImportSpecifiers(importDecl) {
			if refactorImportedName(spec) == name {
				edits = append(edits, refactorRemoveImportSpecifierEdit(file, importDecl, spec))
			}
		}
	}
	return edits
}
