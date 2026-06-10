package cmds

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// symbolmove.go is the factored cross-file symbol-move planner shared by
// `refactor mv-symbol` and the `edit` command's cross-file move ops. The
// planner validates the move (refusals: overloads, nested or default
// exported declarations, unexported file-local dependencies unless withDeps),
// then accumulates the edits into a caller-owned EditSet so several moves can
// batch into one transaction.

// symbolMoveDest describes where a moved declaration lands.
type symbolMoveDest struct {
	fileAbs   string          // destination file (abs path)
	file      *ast.SourceFile // nil when creating
	create    bool            // create the destination file
	insertPos int             // -1 = append at end of dest; else byte offset in the ORIGINAL dest text
}

// refactorMvSymbolMaxDeps caps how many file-local declarations withDeps will
// drag along before refusing.
const refactorMvSymbolMaxDeps = 25

// planSymbolMove plans moving ONE top-level declaration
// (function/class/interface/type/enum/sole-declarator const) to another
// project file. The declaration text is moved verbatim (JSDoc included),
// exported in the destination when it has external references, importers and
// `export { X } from` re-exporters are rewritten to point at the new file,
// and the original file imports the symbol back if it still uses it. With
// withDeps, file-local unexported declarations the symbol (transitively)
// references move along as one block — dependencies first, in source order,
// staying unexported. Edits accumulate into es; the caller runs the
// transaction. When dest.insertPos >= 0 the moved text is inserted at that
// byte offset of the original destination text (the dependency-import block
// still goes to the top of the file); -1 appends at the end.
func planSymbolMove(ctx context.Context, ws *core.Workspace, declNode *ast.Node, symbol *ast.Symbol, dest symbolMoveDest, withDeps bool, es *core.EditSet) (notes []string, err error) {
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
	// top-level symbols block the move unless withDeps pulls them along
	// (transitively); exported same-file symbols and imported names are
	// re-imported in the destination.
	deps, depBlockers, err := refactorMvSymbolDepClosure(ctx, ws, sourceFile, declNode, symbol, symbolName, withDeps)
	if err != nil {
		return nil, err
	}
	movedNodes := []*ast.Node{declNode}
	for _, dep := range depBlockers {
		movedNodes = append(movedNodes, dep.node)
	}
	inMoved := func(n *ast.Node) bool {
		if ast.GetSourceFileOfNode(n) != sourceFile {
			return false
		}
		for _, m := range movedNodes {
			if n.Pos() >= m.Pos() && n.End() <= m.End() {
				return true
			}
		}
		return false
	}

	// A moved local dependency must not be used by anything staying behind in
	// the source file (it cannot move without breaking the remaining user and
	// cannot be duplicated).
	for _, dep := range depBlockers {
		depName := ast.GetNameOfDeclaration(dep.decl)
		if depName == nil {
			continue
		}
		for _, ref := range refactorReferenceNodes(ctx, ws, depName) {
			if inMoved(ref) {
				continue
			}
			return nil, cli.RefusedErrorf("cannot move %q with its dependencies: %q is also used by %s, which stays behind",
				symbolName, dep.name, refactorMvSymbolUserDesc(ws, ref))
		}
	}

	// Reference partition.
	refs := refactorReferenceNodes(ctx, ws, nameNode)
	sourceStillUses := false
	refFiles := make(map[*ast.SourceFile][]*ast.Node)
	clauseSpecs := make(map[*ast.Node]bool) // export specifiers of local `export { X }` clauses in the source
	for _, ref := range refs {
		if inMoved(ref) {
			continue
		}
		refFile := ast.GetSourceFileOfNode(ref)
		if refFile == sourceFile {
			// A local `export { X }` clause (no module specifier) exporting
			// the moved declaration: the specifier is removed (the moved
			// declaration is exported in the destination instead) and
			// consumers get the normal retarget treatment. Aliased clause
			// exports (`export { X as Y }`) would change the symbol's public
			// name on the way and are refused.
			if ref.Parent != nil && ref.Parent.Kind == ast.KindExportSpecifier {
				spec := ref.Parent
				exportDecl := ast.FindAncestor(spec, ast.IsExportDeclaration)
				if exportDecl == nil || exportDecl.AsExportDeclaration().ModuleSpecifier != nil {
					return nil, cli.RefusedErrorf("%q is re-exported via an export clause in %s; remove or update that export first",
						symbolName, ws.RelPath(refFile.FileName()))
				}
				if s := spec.AsExportSpecifier(); s.PropertyName != nil && s.PropertyName.Text() != spec.Name().Text() {
					return nil, cli.RefusedErrorf("%q is exported as %q via the export clause at %s; aliased clause exports of the moved symbol are not supported — export it under its own name first",
						symbolName, spec.Name().Text(), refactorNodeLineCol(ws, exportDecl))
				}
				clauseSpecs[spec] = true
				continue
			}
			sourceStillUses = true
			continue
		}
		refFiles[refFile] = append(refFiles[refFile], ref)
	}
	hasExternalRefs := sourceStillUses || len(refFiles) > 0
	wasExported := declNode.ModifierFlags()&ast.ModifierFlagsExport != 0
	exportedViaClause := len(clauseSpecs) > 0

	// Clean up local `export { X }` clauses naming the moved declaration: the
	// specifier is dropped, or the whole statement when it empties.
	var clauseEdits []icore.TextChange
	clauseStmts := make(map[*ast.Node]bool)
	for spec := range clauseSpecs {
		exportDecl := ast.FindAncestor(spec, ast.IsExportDeclaration)
		if clauseStmts[exportDecl] {
			continue
		}
		clauseStmts[exportDecl] = true
		if len(refactorNamedReexportSpecifiers(exportDecl)) == 1 {
			clauseEdits = append(clauseEdits, icore.TextChange{TextRange: refactorDeletionRange(sourceFile, exportDecl), NewText: ""})
		} else {
			clauseEdits = append(clauseEdits, refactorRemoveListSpecifierEdit(sourceText, spec))
		}
	}

	// (a) Remove the moved declarations from the source file; (d) import the
	// symbol back if the source still references it. When the deletions leave
	// the file empty (whitespace only — no statements, no imports), delete
	// the file instead of leaving a husk. The consumer rewrites below point
	// re-exports and imports at the destination regardless, so deleting the
	// emptied source is safe.
	moveOrder := slices.Clone(movedNodes[1:]) // deps first, source order, then the target
	slices.SortFunc(moveOrder, func(a, b *ast.Node) int { return a.Pos() - b.Pos() })
	moveOrder = append(moveOrder, declNode)
	deletionRanges := make([]icore.TextRange, len(moveOrder))
	for i, n := range moveOrder {
		deletionRanges[i] = refactorDeletionRange(sourceFile, n)
	}
	movedTypeOnly := symbol.Flags&ast.SymbolFlagsValue == 0
	sourceDeleted := false
	if !sourceStillUses {
		remaining := sourceText
		descending := slices.Clone(deletionRanges)
		for _, e := range clauseEdits {
			descending = append(descending, e.TextRange)
		}
		slices.SortFunc(descending, func(a, b icore.TextRange) int { return b.Pos() - a.Pos() })
		for _, r := range descending {
			remaining = remaining[:r.Pos()] + remaining[r.End():]
		}
		if strings.TrimSpace(remaining) == "" {
			sourceDeleted = true
			es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpDelete, Path: sourceFile.FileName()})
			notes = append(notes, fmt.Sprintf("%s became empty and was deleted", ws.RelPath(sourceFile.FileName())))
		}
	}
	if !sourceDeleted {
		// Eat one extra newline when a deletion would leave a double blank
		// line behind (the moved text itself keeps the original range).
		var sourceEdits []icore.TextChange
		for _, r := range deletionRanges {
			sourceEdits = append(sourceEdits, icore.TextChange{TextRange: refactorCollapseBlankAfterDeletion(sourceText, r), NewText: ""})
		}
		sourceEdits = append(sourceEdits, clauseEdits...)
		if sourceStillUses {
			backImport := []refactorImportName{{name: symbolName, local: symbolName, typeOnly: movedTypeOnly}}
			merge, stmt := refactorPlanImports(ws, sourceFile, dest.fileAbs,
				refactorModuleSpecifierText(ws, sourceFile.FileName(), dest.fileAbs), backImport, nil)
			sourceEdits = append(sourceEdits, merge...)
			if stmt != "" {
				sourceEdits = append(sourceEdits, refactorPrependImports(sourceFile, stmt))
			}
			notes = append(notes, fmt.Sprintf("%s still uses %s and now imports it from %s",
				ws.RelPath(sourceFile.FileName()), symbolName, ws.RelPath(dest.fileAbs)))
		}
		es.Edits = append(es.Edits, core.FileEdit{FileName: sourceFile.FileName(), Edits: sourceEdits})
	}

	// (b)+(c) Build the moved block — dependencies first (source order), then
	// the target declaration, blank-line separated — exporting the target when
	// anything references it. Dependencies stay unexported.
	var blocks []string
	for i, n := range moveOrder {
		r := deletionRanges[i]
		block := sourceText[r.Pos():r.End()]
		if n == declNode && (hasExternalRefs || exportedViaClause) && !wasExported {
			declStart := astnav.GetStartOfNode(declNode, sourceFile, false /*includeJSDoc*/)
			block = block[:declStart-r.Pos()] + "export " + block[declStart-r.Pos():]
			if exportedViaClause {
				notes = append(notes, fmt.Sprintf("%s was exported via an export clause; the clause was updated and the declaration is exported in the destination", symbolName))
			} else {
				notes = append(notes, fmt.Sprintf("%s was exported in the destination (it has references elsewhere)", symbolName))
			}
		}
		if !strings.HasSuffix(block, "\n") {
			block += "\n"
		}
		blocks = append(blocks, block)
	}
	movedText := strings.Join(blocks, "\n")
	if len(depBlockers) > 0 {
		names := make([]string, len(depBlockers))
		for i, dep := range depBlockers {
			names[i] = dep.name
		}
		notes = append(notes, fmt.Sprintf("moved %d file-local dependency(ies) along: %s", len(names), strings.Join(names, ", ")))
	}

	if dest.create {
		// Imports the moved block needs in the destination.
		_, destImports := refactorMvSymbolDepImports(ws, dest.fileAbs, nil, deps, nil)
		if destImports != "" {
			destImports += "\n" // one blank line between the import block and the moved code
		}
		es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpCreate, Path: dest.fileAbs, Content: destImports + movedText})
	} else {
		destText := dest.file.Text()
		var destEdits []icore.TextChange
		// The destination may have imported or re-exported the symbol from
		// the source file; those bindings must go away now that the
		// declaration is local. Imports the statements delete entirely are
		// never merge targets for the dependency imports below.
		dropEdits, droppedImports := refactorDropImportOfName(ws, dest.file, sourceFile.FileName(), symbolName)
		// Imports the moved block needs in the destination: merged into
		// existing clauses from the same modules where possible, new
		// statements prepended otherwise.
		mergeEdits, destImports := refactorMvSymbolDepImports(ws, dest.fileAbs, dest.file, deps, droppedImports)
		destEdits = append(destEdits, mergeEdits...)
		if destImports != "" {
			destEdits = append(destEdits, refactorPrependImports(dest.file, destImports))
		}
		if dest.insertPos >= 0 {
			// Anchored insert: the moved text ends with a newline and goes at
			// the anchor offset (zero-width change), separated from adjacent
			// top-level code by one blank line on each side.
			insert := movedText
			if !editBlankLineAt(destText, dest.insertPos) {
				insert += "\n"
			}
			if !editBlankLineBefore(destText, dest.insertPos) {
				insert = "\n" + insert
			}
			destEdits = append(destEdits, icore.TextChange{TextRange: icore.NewTextRange(dest.insertPos, dest.insertPos), NewText: insert})
		} else {
			// Append at end, separated from the last declaration by one blank
			// line.
			insert := movedText
			if len(destText) > 0 && !strings.HasSuffix(destText, "\n") {
				insert = "\n" + insert
			}
			if !editBlankLineBefore(destText, len(destText)) {
				insert = "\n" + insert
			}
			destEdits = append(destEdits, icore.TextChange{TextRange: icore.NewTextRange(len(destText), len(destText)), NewText: insert})
		}
		// Re-exports of the symbol in the destination become the local
		// `export` modifier.
		destEdits = append(destEdits, dropEdits...)
		destEdits = append(destEdits, refactorDropReexportOfName(ws, dest.file, sourceFile.FileName(), symbolName)...)
		es.Edits = append(es.Edits, core.FileEdit{FileName: dest.file.FileName(), Edits: destEdits})
	}

	// (e) Rewrite every other consumer: drop the specifier from the old
	// import, add an import from the new file; retarget `export { X } from`
	// re-export clauses. Consumers that reach the symbol through a rewritten
	// named re-export (a barrel) need no change.
	newSpecRel := func(importer *ast.SourceFile) string {
		return refactorModuleSpecifierText(ws, importer.FileName(), dest.fileAbs)
	}
	for refFile, refNodes := range refFiles {
		if refFile == dest.file {
			continue // handled above
		}
		var edits []icore.TextChange
		localName := symbolName
		foundImport := false
		origTypeOnly := true // all removed specifiers were type-only
		removedDecls := make(map[*ast.Node]bool)
		for _, importDecl := range refactorImportsResolvingTo(ws, refFile, sourceFile.FileName()) {
			for _, spec := range refactorNamedImportSpecifiers(importDecl) {
				if refactorImportedName(spec) != symbolName {
					continue
				}
				foundImport = true
				localName = spec.Name().Text()
				origTypeOnly = origTypeOnly && (refactorImportClauseTypeOnly(importDecl) || spec.AsImportSpecifier().IsTypeOnly)
				if refactorImportStatementWouldVanish(importDecl) {
					removedDecls[importDecl] = true
				}
				edits = append(edits, refactorRemoveImportSpecifierEdit(refFile, importDecl, spec))
			}
		}
		foundReexport := false
		for _, exportDecl := range refactorReexportsResolvingTo(ws, refFile, sourceFile.FileName()) {
			reexportEdits, ok := refactorRewriteReexportEdits(refFile, exportDecl, symbolName, newSpecRel(refFile))
			if !ok {
				continue
			}
			foundReexport = true
			edits = append(edits, reexportEdits...)
		}
		if !foundImport && !foundReexport {
			// The references may flow through another module: a named
			// re-export (incl. aliased) being rewritten in this same plan
			// needs no local change; a star re-export cannot be rewritten.
			covered := len(refNodes) > 0
			var starStmt *ast.Node
			for _, ref := range refNodes {
				via, ok := refactorRefViaModule(ctx, ws, ref)
				if !ok {
					covered = false
					break
				}
				if _, inPlan := refFiles[via]; !inPlan && via != dest.file {
					covered = false
					starStmt = refactorStarReexportOf(ws, via, sourceFile.FileName())
					break
				}
			}
			if covered {
				continue
			}
			if starStmt != nil {
				return nil, cli.RefusedErrorf("%s reaches %q through the star re-export (`export * from`) at %s, which cannot be rewritten; re-export the name explicitly or import it directly first",
					ws.RelPath(refFile.FileName()), symbolName, refactorNodeLineCol(ws, starStmt))
			}
			return nil, cli.RefusedErrorf("%s references %q without a rewritable named import or re-export (namespace imports are not supported yet)",
				ws.RelPath(refFile.FileName()), symbolName)
		}
		if foundImport {
			name := []refactorImportName{{name: symbolName, local: localName, typeOnly: movedTypeOnly || origTypeOnly}}
			merge, stmt := refactorPlanImports(ws, refFile, dest.fileAbs, newSpecRel(refFile), name, removedDecls)
			edits = append(edits, merge...)
			if stmt != "" {
				edits = append(edits, refactorPrependImports(refFile, stmt))
			}
		}
		es.Edits = append(es.Edits, core.FileEdit{FileName: refFile.FileName(), Edits: edits})
	}

	return notes, nil
}

// refactorMvSymbolDep is one symbol the moved block references that must be
// importable from the destination.
type refactorMvSymbolDep struct {
	name      string // exported name to import (named bindings)
	localName string // local alias used inside the moved text
	fromFile  string // absolute file to import from ("" = keep original specifier)
	origSpec  string // original module specifier text (for unresolved/package imports)
	binding   string // "named" (default), "default", or "namespace"
	typeOnly  bool   // import as a type (type-only origin, or a symbol with no value meaning)
}

// refactorMvSymbolBlocker is one file-local unexported declaration the moved
// code references. With withDeps it moves along; otherwise it blocks the move.
type refactorMvSymbolBlocker struct {
	desc string    // "name (file:line:col)" or a binding explanation, for refusals
	name string    // referenced identifier text
	decl *ast.Node // the referenced declaration (nil for import bindings)
	node *ast.Node // widened deletion node that can move along (nil for import bindings)
}

// refactorMvSymbolDepClosure scans the moved declaration — and, with
// withDeps, the transitive closure of file-local unexported declarations it
// references (each dependency's body scanned the same way, capped at
// refactorMvSymbolMaxDeps) — returning the destination imports for the whole
// moved block and the extra local declarations to move. Without withDeps any
// local blocker refuses, suggesting the flag.
func refactorMvSymbolDepClosure(ctx context.Context, ws *core.Workspace, sourceFile *ast.SourceFile, declNode *ast.Node, symbol *ast.Symbol, symbolName string, withDeps bool) ([]refactorMvSymbolDep, []refactorMvSymbolBlocker, error) {
	movedNodes := []*ast.Node{declNode}
	inMoved := func(n *ast.Node) bool {
		for _, m := range movedNodes {
			if n.Pos() >= m.Pos() && n.End() <= m.End() {
				return true
			}
		}
		return false
	}
	var allDeps []refactorMvSymbolDep
	depSeen := make(map[string]bool)
	var depBlockers []refactorMvSymbolBlocker
	queue := []*ast.Node{declNode}
	for len(queue) > 0 {
		scanNode := queue[0]
		queue = queue[1:]
		deps, blockers := refactorMvSymbolDeps(ctx, ws, sourceFile, scanNode, symbol, inMoved)
		for _, dep := range deps {
			if !depSeen[dep.localName] {
				depSeen[dep.localName] = true
				allDeps = append(allDeps, dep)
			}
		}
		var localDescs []string
		for _, bl := range blockers {
			if bl.node == nil {
				return nil, nil, cli.RefusedErrorf("cannot move %q: it references %s", symbolName, bl.desc)
			}
			if slices.Contains(movedNodes, bl.node) {
				continue
			}
			if !withDeps {
				localDescs = append(localDescs, bl.desc)
				continue
			}
			movedNodes = append(movedNodes, bl.node)
			depBlockers = append(depBlockers, bl)
			queue = append(queue, bl.node)
		}
		if len(localDescs) > 0 {
			slices.Sort(localDescs)
			return nil, nil, cli.RefusedErrorf("cannot move %q: it references %d file-local unexported symbol(s) (pass --with-deps, or `with-deps` on the edit move line, to move them along):\n  %s",
				symbolName, len(localDescs), strings.Join(localDescs, "\n  "))
		}
		if len(depBlockers) > refactorMvSymbolMaxDeps {
			return nil, nil, cli.RefusedErrorf("cannot move %q: --with-deps would move more than %d file-local dependencies; split the file manually first",
				symbolName, refactorMvSymbolMaxDeps)
		}
	}
	return allDeps, depBlockers, nil
}

// refactorMvSymbolUserDesc names the top-level declaration (or statement)
// containing a reference, for shared-dependency refusal messages.
func refactorMvSymbolUserDesc(ws *core.Workspace, ref *ast.Node) string {
	top := ref
	for top.Parent != nil && top.Parent.Kind != ast.KindSourceFile {
		top = top.Parent
	}
	candidates := []*ast.Node{top}
	if top.Kind == ast.KindVariableStatement {
		if decls := top.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes; len(decls) > 0 {
			candidates = append(candidates, decls[0])
		}
	}
	for _, c := range candidates {
		if name := ast.GetNameOfDeclaration(c); name != nil {
			return fmt.Sprintf("%q (%s)", name.Text(), refactorNodeLineCol(ws, top))
		}
	}
	return fmt.Sprintf("the statement at %s", refactorNodeLineCol(ws, top))
}

// refactorMvSymbolDeps walks one moved declaration's identifiers and
// classifies their symbols: same-file unexported top-level declarations are
// blockers (movable with withDeps); same-file exported declarations and
// import bindings become destination imports. Declarations already inside the
// moved set are skipped.
func refactorMvSymbolDeps(ctx context.Context, ws *core.Workspace, sourceFile *ast.SourceFile, scanNode *ast.Node, moved *ast.Symbol, inMoved func(*ast.Node) bool) (deps []refactorMvSymbolDep, blockers []refactorMvSymbolBlocker) {
	checker, done := ws.Program.GetTypeCheckerForFile(ctx, sourceFile)
	defer done()
	seen := make(map[string]bool)
	for _, id := range refactorCollectIdentifiers(scanNode) {
		sym := checker.GetSymbolAtLocation(id)
		if sym == nil || sym == moved || len(sym.Declarations) == 0 {
			continue
		}
		depDecl := sym.Declarations[0]
		if ast.GetSourceFileOfNode(depDecl) != sourceFile {
			continue // globals, libs, other files reached via type references
		}
		// Declared inside the moved block itself: moves along.
		if inMoved(depDecl) {
			continue
		}
		switch depDecl.Kind {
		case ast.KindImportSpecifier, ast.KindImportClause, ast.KindNamespaceImport:
			dep, ok := refactorImportBindingDep(ws, sourceFile, depDecl)
			if !ok {
				desc := fmt.Sprintf("%s (import binding, not rewritable yet)", id.Text())
				if !seen["!"+desc] {
					seen["!"+desc] = true
					blockers = append(blockers, refactorMvSymbolBlocker{desc: desc, name: id.Text()})
				}
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
			typeOnly := sym.Flags&ast.SymbolFlagsValue == 0
			exported := widened.ModifierFlags()&ast.ModifierFlagsExport != 0 || depDecl.ModifierFlags()&ast.ModifierFlagsExport != 0
			exportedName := id.Text()
			if !exported {
				// A declaration without an export modifier may still be
				// exported via a trailing `export { … }` clause (incl.
				// aliased): treated exactly like an inline export — it stays
				// behind and the destination imports it back under the
				// exported name.
				if clauseSpec := refactorClauseExportSpec(sourceFile, id.Text()); clauseSpec != nil {
					exported = true
					exportedName = clauseSpec.Name().Text()
				}
			}
			if !exported {
				desc := fmt.Sprintf("%s (%s)", id.Text(), refactorNodeLineCol(ws, depDecl))
				if !seen["!"+desc] {
					seen["!"+desc] = true
					blockers = append(blockers, refactorMvSymbolBlocker{desc: desc, name: id.Text(), decl: depDecl, node: widened})
				}
				continue
			}
			if !seen[id.Text()] {
				seen[id.Text()] = true
				deps = append(deps, refactorMvSymbolDep{name: exportedName, localName: id.Text(), fromFile: sourceFile.FileName(), typeOnly: typeOnly})
			}
		}
	}
	return deps, blockers
}

// refactorImportBindingDep converts an import binding used by the moved
// declaration into a destination import: a named specifier, a default import
// (`import X from`), or a namespace import (`import * as ns from`).
func refactorImportBindingDep(ws *core.Workspace, sourceFile *ast.SourceFile, depDecl *ast.Node) (refactorMvSymbolDep, bool) {
	importDecl := ast.FindAncestor(depDecl, ast.IsImportDeclaration)
	if importDecl == nil {
		return refactorMvSymbolDep{}, false
	}
	clauseTypeOnly := refactorImportClauseTypeOnly(importDecl)
	var dep refactorMvSymbolDep
	switch depDecl.Kind {
	case ast.KindImportSpecifier:
		dep = refactorMvSymbolDep{name: refactorImportedName(depDecl), localName: depDecl.Name().Text(), binding: "named",
			typeOnly: clauseTypeOnly || depDecl.AsImportSpecifier().IsTypeOnly}
	case ast.KindImportClause: // default import binding
		name := depDecl.AsImportClause().Name()
		if name == nil {
			return refactorMvSymbolDep{}, false
		}
		dep = refactorMvSymbolDep{name: name.Text(), localName: name.Text(), binding: "default", typeOnly: clauseTypeOnly}
	case ast.KindNamespaceImport:
		dep = refactorMvSymbolDep{name: depDecl.Name().Text(), localName: depDecl.Name().Text(), binding: "namespace", typeOnly: clauseTypeOnly}
	default:
		return refactorMvSymbolDep{}, false
	}
	specNode := importDecl.AsImportDeclaration().ModuleSpecifier
	dep.origSpec = specNode.Text()
	resolved := ws.Program.GetResolvedModuleFromModuleSpecifier(sourceFile, specNode)
	if resolved.IsResolved() && !core.IsExternalLibraryPath(resolved.ResolvedFileName) {
		dep.fromFile = resolved.ResolvedFileName
	}
	return dep, true
}

// refactorMvSymbolDepImports plans the imports the destination needs for the
// moved block's dependencies. For an existing destination file it returns
// merge edits into existing import clauses from the same modules plus a block
// of new import statements; for a created file (destFile nil) only the block
// is returned. Named bindings group one statement per module (`import type`
// when every name is type-only, inline `type` modifiers when mixed); default
// and namespace bindings get their own statements. Names already declared in
// the destination are skipped; skip marks destination import declarations
// other edits of the same plan delete entirely.
func refactorMvSymbolDepImports(ws *core.Workspace, destAbs string, destFile *ast.SourceFile, deps []refactorMvSymbolDep, skip map[*ast.Node]bool) (mergeEdits []icore.TextChange, newBlock string) {
	type moduleGroup struct {
		targetAbs string
		spec      string
		names     []refactorImportName
	}
	quote := refactorImportQuote(destFile)
	groups := make(map[string]*moduleGroup)
	var order []string
	var singles []string // default/namespace bindings get their own statements
	for _, dep := range deps {
		if destFile != nil && len(destFile.GetDeclarationMap()[dep.localName]) > 0 {
			continue // already available in the destination
		}
		spec := dep.origSpec
		if dep.fromFile != "" {
			spec = refactorModuleSpecifierText(ws, destAbs, dep.fromFile)
		}
		keyword := "import "
		if dep.typeOnly {
			keyword = "import type "
		}
		switch dep.binding {
		case "default":
			singles = append(singles, keyword+dep.localName+" from "+quote+spec+quote+";\n")
			continue
		case "namespace":
			singles = append(singles, keyword+"* as "+dep.localName+" from "+quote+spec+quote+";\n")
			continue
		}
		g, ok := groups[spec]
		if !ok {
			g = &moduleGroup{targetAbs: dep.fromFile, spec: spec}
			groups[spec] = g
			order = append(order, spec)
		}
		g.names = append(g.names, refactorImportName{name: dep.name, local: dep.localName, typeOnly: dep.typeOnly})
	}
	var sb strings.Builder
	for _, spec := range order {
		g := groups[spec]
		if destFile != nil {
			edits, stmt := refactorPlanImports(ws, destFile, g.targetAbs, g.spec, g.names, skip)
			mergeEdits = append(mergeEdits, edits...)
			sb.WriteString(stmt)
			continue
		}
		sb.WriteString(refactorRenderImportStatement(g.names, g.spec, quote))
	}
	for _, single := range singles {
		sb.WriteString(single)
	}
	return mergeEdits, sb.String()
}

// refactorClauseExportSpec returns the export specifier of a module-local
// `export { … }` clause (no module specifier) in file whose LOCAL name is
// localName — `export { localName }` or `export { localName as alias }` — or
// nil when the file has none.
func refactorClauseExportSpec(file *ast.SourceFile, localName string) *ast.Node {
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != ast.KindExportDeclaration || stmt.AsExportDeclaration().ModuleSpecifier != nil {
			continue
		}
		for _, spec := range refactorNamedReexportSpecifiers(stmt) {
			if refactorReexportedSourceName(spec) == localName {
				return spec
			}
		}
	}
	return nil
}

// refactorDropImportOfName removes the named-import specifier binding `name`
// from imports of fromFileName inside file (no-op when absent). The second
// result marks the import declarations the edits delete entirely.
func refactorDropImportOfName(ws *core.Workspace, file *ast.SourceFile, fromFileName string, name string) ([]icore.TextChange, map[*ast.Node]bool) {
	var edits []icore.TextChange
	removed := make(map[*ast.Node]bool)
	for _, importDecl := range refactorImportsResolvingTo(ws, file, fromFileName) {
		for _, spec := range refactorNamedImportSpecifiers(importDecl) {
			if refactorImportedName(spec) == name {
				if refactorImportStatementWouldVanish(importDecl) {
					removed[importDecl] = true
				}
				edits = append(edits, refactorRemoveImportSpecifierEdit(file, importDecl, spec))
			}
		}
	}
	return edits, removed
}

// refactorDropReexportOfName removes the `export { name } from` specifier
// re-exporting `name` from fromFileName inside file — the whole statement
// when it is the sole specifier (no-op when absent).
func refactorDropReexportOfName(ws *core.Workspace, file *ast.SourceFile, fromFileName string, name string) []icore.TextChange {
	var edits []icore.TextChange
	for _, exportDecl := range refactorReexportsResolvingTo(ws, file, fromFileName) {
		specs := refactorNamedReexportSpecifiers(exportDecl)
		for _, spec := range specs {
			if refactorReexportedSourceName(spec) != name {
				continue
			}
			if len(specs) == 1 {
				edits = append(edits, icore.TextChange{TextRange: refactorDeletionRange(file, exportDecl), NewText: ""})
			} else {
				edits = append(edits, refactorRemoveListSpecifierEdit(file.Text(), spec))
			}
		}
	}
	return edits
}

// refactorReexportsResolvingTo returns the `export … from` declarations in
// file whose module specifier resolves to targetFileName.
func refactorReexportsResolvingTo(ws *core.Workspace, file *ast.SourceFile, targetFileName string) []*ast.Node {
	var decls []*ast.Node
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != ast.KindExportDeclaration {
			continue
		}
		spec := stmt.AsExportDeclaration().ModuleSpecifier
		if spec == nil || !ast.IsStringLiteral(spec) {
			continue
		}
		resolved := ws.Program.GetResolvedModuleFromModuleSpecifier(file, spec)
		if resolved.IsResolved() && resolved.ResolvedFileName == targetFileName {
			decls = append(decls, stmt)
		}
	}
	return decls
}

// refactorNamedReexportSpecifiers returns the named export specifiers of an
// export declaration (nil for `export * from`).
func refactorNamedReexportSpecifiers(exportDecl *ast.Node) []*ast.Node {
	clause := exportDecl.AsExportDeclaration().ExportClause
	if clause == nil || clause.Kind != ast.KindNamedExports {
		return nil
	}
	return clause.AsNamedExports().Elements.Nodes
}

// refactorReexportedSourceName returns the source-module name an export
// specifier re-exports (the property name for `export { X as Y } from`, the
// name otherwise).
func refactorReexportedSourceName(spec *ast.Node) string {
	s := spec.AsExportSpecifier()
	if s.PropertyName != nil {
		return s.PropertyName.Text()
	}
	return spec.Name().Text()
}

// refactorRewriteReexportEdits retargets the `export { X } from` specifier
// for symbolName in one export declaration to newSpec: the sole specifier
// rewrites the module specifier in place (preserving `as` aliases and
// type-only-ness); otherwise the specifier is removed and a fresh
// `export { X } from "<newSpec>"` statement is inserted after the
// declaration. Returns ok=false when the declaration does not re-export
// symbolName by name.
func refactorRewriteReexportEdits(file *ast.SourceFile, exportDecl *ast.Node, symbolName string, newSpec string) ([]icore.TextChange, bool) {
	specs := refactorNamedReexportSpecifiers(exportDecl)
	var match *ast.Node
	for _, spec := range specs {
		if refactorReexportedSourceName(spec) == symbolName {
			match = spec
			break
		}
	}
	if match == nil {
		return nil, false
	}
	ed := exportDecl.AsExportDeclaration()
	if len(specs) == 1 {
		// Replace the module specifier string in place (quote style kept).
		text := file.Text()
		pos := scanner.SkipTrivia(text, ed.ModuleSpecifier.Pos())
		quote := string(text[pos])
		return []icore.TextChange{{TextRange: icore.NewTextRange(pos, ed.ModuleSpecifier.End()), NewText: quote + newSpec + quote}}, true
	}
	spec := match.AsExportSpecifier()
	inner := match.Name().Text()
	if spec.PropertyName != nil {
		inner = spec.PropertyName.Text() + " as " + inner
	}
	if spec.IsTypeOnly {
		inner = "type " + inner
	}
	keyword := "export "
	if ed.IsTypeOnly {
		keyword = "export type "
	}
	insertAt := refactorDeletionRange(file, exportDecl).End()
	return []icore.TextChange{
		refactorRemoveListSpecifierEdit(file.Text(), match),
		{TextRange: icore.NewTextRange(insertAt, insertAt), NewText: keyword + "{ " + inner + " } from \"" + newSpec + "\";\n"},
	}, true
}

// refactorRefViaModule returns the module file a reference is bound through:
// the re-export clause containing it, or the named import declaration that
// binds the referenced (possibly aliased) name. Namespace and default
// bindings return ok=false.
func refactorRefViaModule(ctx context.Context, ws *core.Workspace, ref *ast.Node) (*ast.SourceFile, bool) {
	refFile := ast.GetSourceFileOfNode(ref)
	resolveSpec := func(specNode *ast.Node) (*ast.SourceFile, bool) {
		if specNode == nil || !ast.IsStringLiteral(specNode) {
			return nil, false
		}
		resolved := ws.Program.GetResolvedModuleFromModuleSpecifier(refFile, specNode)
		if !resolved.IsResolved() {
			return nil, false
		}
		via := ws.Program.GetSourceFile(resolved.ResolvedFileName)
		return via, via != nil
	}
	if ref.Parent != nil && ref.Parent.Kind == ast.KindExportSpecifier {
		if exportDecl := ast.FindAncestor(ref, ast.IsExportDeclaration); exportDecl != nil {
			return resolveSpec(exportDecl.AsExportDeclaration().ModuleSpecifier)
		}
		return nil, false
	}
	checker, done := ws.Program.GetTypeCheckerForFile(ctx, refFile)
	defer done()
	sym := checker.GetSymbolAtLocation(ref)
	if sym == nil || len(sym.Declarations) == 0 {
		return nil, false
	}
	decl := sym.Declarations[0]
	if decl.Kind != ast.KindImportSpecifier || ast.GetSourceFileOfNode(decl) != refFile {
		return nil, false
	}
	if importDecl := ast.FindAncestor(decl, ast.IsImportDeclaration); importDecl != nil {
		return resolveSpec(importDecl.AsImportDeclaration().ModuleSpecifier)
	}
	return nil, false
}

// refactorStarReexportOf returns the `export * from` (or `export * as ns
// from`) statement in file resolving to targetFileName, or nil.
func refactorStarReexportOf(ws *core.Workspace, file *ast.SourceFile, targetFileName string) *ast.Node {
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != ast.KindExportDeclaration {
			continue
		}
		ed := stmt.AsExportDeclaration()
		if ed.ModuleSpecifier == nil || !ast.IsStringLiteral(ed.ModuleSpecifier) {
			continue
		}
		if ed.ExportClause != nil && ed.ExportClause.Kind != ast.KindNamespaceExport {
			continue
		}
		resolved := ws.Program.GetResolvedModuleFromModuleSpecifier(file, ed.ModuleSpecifier)
		if resolved.IsResolved() && resolved.ResolvedFileName == targetFileName {
			return stmt
		}
	}
	return nil
}
