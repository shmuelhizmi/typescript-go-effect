package cmds

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// symbolmove.go is the factored cross-file symbol-move planner shared by
// `refactor mv-symbol` and the `edit` command's cross-file move ops. The
// planner validates the move (v1 refusals: overloads, nested or default
// exported declarations, unexported file-local dependencies), then
// accumulates the edits into a caller-owned EditSet so several moves can
// batch into one transaction.

// symbolMoveDest describes where a moved declaration lands.
type symbolMoveDest struct {
	fileAbs   string          // destination file (abs path)
	file      *ast.SourceFile // nil when creating
	create    bool            // create the destination file
	insertPos int             // -1 = append at end of dest; else byte offset in the ORIGINAL dest text
}

// planSymbolMove plans moving ONE top-level declaration
// (function/class/interface/type/enum/sole-declarator const) to another
// project file. The declaration text is moved verbatim (JSDoc included),
// exported in the destination when it has external references, importers are
// rewritten to import from the new file, and the original file imports the
// symbol back if it still uses it. Edits accumulate into es; the caller runs
// the transaction. When dest.insertPos >= 0 the moved text is inserted at
// that byte offset of the original destination text (the dependency-import
// block still goes to the top of the file); -1 appends at the end.
func planSymbolMove(ctx context.Context, ws *core.Workspace, declNode *ast.Node, symbol *ast.Symbol, dest symbolMoveDest, es *core.EditSet) (notes []string, err error) {
	if len(symbol.Declarations) > 1 {
		return nil, cli.RefusedErrorf("symbol has %d declarations (overloads/merged declarations are not supported yet)", len(symbol.Declarations))
	}
	decl := symbol.Declarations[0]
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
	if dest.file == sourceFile {
		return nil, cli.UsageErrorf("the symbol already lives in %s", ws.RelPath(dest.fileAbs))
	}
	// A destination already being created by an earlier op of the same edit
	// set cannot be targeted again (two creating moves to one new file, or a
	// move into a pending create).
	for _, op := range es.Ops {
		if op.Kind == core.FileOpCreate && op.Path == dest.fileAbs {
			return nil, cli.RefusedErrorf("destination %s is being created by another op in this transaction", ws.RelPath(dest.fileAbs))
		}
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

	// (a) Remove the declaration from the source file; (d) import it back if
	// the source still references it.
	deletionRange := refactorDeletionRange(sourceFile, declNode)
	sourceEdits := []icore.TextChange{{TextRange: deletionRange, NewText: ""}}
	if sourceStillUses {
		sourceEdits = append(sourceEdits, refactorInsertImportEdit([]string{symbolName},
			refactorModuleSpecifierText(ws, sourceFile.FileName(), dest.fileAbs)))
		notes = append(notes, fmt.Sprintf("%s still uses %s and now imports it from %s",
			ws.RelPath(sourceFile.FileName()), symbolName, ws.RelPath(dest.fileAbs)))
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
	destImports := refactorMvSymbolDepImports(ws, dest.fileAbs, dest.file, deps)

	if dest.create {
		es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpCreate, Path: dest.fileAbs, Content: destImports + movedText})
	} else {
		destText := dest.file.Text()
		var destEdits []icore.TextChange
		if destImports != "" {
			destEdits = append(destEdits, icore.TextChange{TextRange: icore.NewTextRange(0, 0), NewText: destImports})
		}
		if dest.insertPos >= 0 {
			// Anchored insert: the moved text ends with a newline and goes at
			// the anchor offset as-is (zero-width change).
			destEdits = append(destEdits, icore.TextChange{TextRange: icore.NewTextRange(dest.insertPos, dest.insertPos), NewText: movedText})
		} else {
			insert := "\n" + movedText
			if strings.HasSuffix(destText, "\n") {
				insert = movedText
			}
			destEdits = append(destEdits, icore.TextChange{TextRange: icore.NewTextRange(len(destText), len(destText)), NewText: insert})
		}
		// The destination may have imported the symbol from the source file;
		// that import must go away now that the declaration is local.
		destEdits = append(destEdits, refactorDropImportOfName(ws, dest.file, sourceFile.FileName(), symbolName)...)
		es.Edits = append(es.Edits, core.FileEdit{FileName: dest.file.FileName(), Edits: destEdits})
	}

	// (e) Rewrite every other importer: drop the specifier from the old
	// import, add an import from the new file.
	newSpecRel := func(importer *ast.SourceFile) string {
		return refactorModuleSpecifierText(ws, importer.FileName(), dest.fileAbs)
	}
	for refFile := range refFiles {
		if refFile == dest.file {
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

	return notes, nil
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
