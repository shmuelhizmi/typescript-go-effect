package cmds

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// The refactor family (§4.4). Every command follows the transaction rules of
// §2.4: dry-run by default (prints a unified diff), --apply to mutate
// atomically with a diagnostics-delta gate, --allow-errors to bypass the
// gate.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "rename",
		Summary:      "Rename a symbol across the project",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorRenameFlags{}
			registerRefactorTargetFlags(fs, &f.target)
			registerRefactorTxFlags(fs, &f.tx)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorRename(ctx, ws, flags.(*refactorRenameFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "mv",
		Summary:      "Move files with all imports updated",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorMvFlags{}
			registerRefactorTxFlags(fs, &f.tx)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorMv(ctx, ws, flags.(*refactorMvFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "organize-imports",
		Summary:      "Sort, merge, and remove unused imports",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorOrganizeFlags{}
			registerRefactorTxFlags(fs, &f.tx)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorOrganizeImports(ctx, ws, flags.(*refactorOrganizeFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "safe-delete",
		Summary:      "Delete a symbol only if nothing references it",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorSafeDeleteFlags{}
			registerRefactorTargetFlags(fs, &f.target)
			registerRefactorTxFlags(fs, &f.tx)
			fs.BoolVar(&f.cascade, "cascade", false, "also delete unexported top-level symbols that become dead as a result, recursively")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorSafeDelete(ctx, ws, flags.(*refactorSafeDeleteFlags), args)
		},
	})
}

// ---------------------------------------------------------------------------
// Shared flag plumbing

type refactorTxFlags struct {
	apply       bool
	allowErrors bool
	strictGate  bool
}

func registerRefactorTxFlags(fs *flag.FlagSet, f *refactorTxFlags) {
	fs.BoolVar(&f.apply, "apply", false, "apply the edits (default is a dry-run that prints a diff)")
	fs.BoolVar(&f.allowErrors, "allow-errors", false, "apply even if new diagnostics would be introduced")
	fs.BoolVar(&f.strictGate, "strict-gate", false, "also refuse on new unused-symbol diagnostics (TS6133 etc.; non-gating by default)")
}

type refactorTargetFlags struct {
	at     string
	symbol string
	name   string
	kind   string
}

func registerRefactorTargetFlags(fs *flag.FlagSet, f *refactorTargetFlags) {
	fs.StringVar(&f.at, "at", "", "target position file:line:col")
	fs.StringVar(&f.symbol, "symbol", "", "target symbol ID (path#qualified.name)")
	fs.StringVar(&f.name, "name", "", "target symbol by name")
	fs.StringVar(&f.kind, "kind", "", "kind filter for --name resolution")
}

// resolveRefactorTarget resolves the command target from flags or, when no
// target flag is given, from the first positional argument (guessing the
// addressing form). Returns the remaining positional arguments.
func resolveRefactorTarget(ctx context.Context, ws *core.Workspace, f *refactorTargetFlags, args []string) (*core.Target, []string, error) {
	if f.at != "" || f.symbol != "" || f.name != "" {
		target, err := ws.ResolveTarget(ctx, core.TargetSpec{At: f.at, Symbol: f.symbol, Name: f.name, Kind: f.kind})
		return target, args, err
	}
	if len(args) == 0 {
		return nil, nil, cli.UsageErrorf("missing target (positional, or --at/--symbol/--name)")
	}
	spec := guessRefactorTargetSpec(args[0])
	spec.Kind = f.kind
	target, err := ws.ResolveTarget(ctx, spec)
	return target, args[1:], err
}

// guessRefactorTargetSpec classifies a positional target argument:
// file:line:col positions, symbol IDs (contain # or @), or bare names.
func guessRefactorTargetSpec(arg string) core.TargetSpec {
	if _, _, _, err := core.ParsePosition(arg); err == nil {
		return core.TargetSpec{At: arg}
	}
	if strings.ContainsAny(arg, "#@") {
		return core.TargetSpec{Symbol: arg}
	}
	return core.TargetSpec{Name: arg}
}

// finishRefactorTx runs the edit set through the transaction engine and maps
// a gate refusal to exit code 4.
func finishRefactorTx(ctx context.Context, ws *core.Workspace, es core.EditSet, tx *refactorTxFlags, notes []string) (*core.TxResult, error) {
	result, err := core.Execute(ctx, ws, es, core.TxOpts{Apply: tx.apply, AllowErrors: tx.allowErrors, StrictGate: tx.strictGate})
	if err != nil {
		return nil, err
	}
	result.Notes = append(result.Notes, notes...)
	if result.Refused {
		return nil, cli.RefusedErrorf(
			"refusing to apply: %d new error(s) would be introduced (use --allow-errors to override):\n%s",
			len(result.NewErrors), refactorDiagsText(result.NewErrors))
	}
	return result, nil
}

func refactorDiagsText(diags []core.TxDiag) string {
	lines := make([]string, 0, len(diags))
	for _, d := range diags {
		lines = append(lines, fmt.Sprintf("  %s:%d:%d %s: %s", d.File, d.Line, d.Col, d.Code, d.Message))
	}
	return strings.Join(lines, "\n")
}

// refactorTargetNamePos returns the byte position to address the target's
// name: the declaration name for symbol/name targets, the resolved position
// for --at targets.
func refactorTargetNamePos(target *core.Target) int {
	if target.Node != nil {
		if name := ast.GetNameOfDeclaration(target.Node); name != nil {
			return astnav.GetStartOfNode(name, target.File, false /*includeJSDoc*/)
		}
	}
	return target.Pos
}

// ---------------------------------------------------------------------------
// refactor rename

type refactorRenameFlags struct {
	target refactorTargetFlags
	tx     refactorTxFlags
}

func runRefactorRename(ctx context.Context, ws *core.Workspace, f *refactorRenameFlags, args []string) (*core.TxResult, error) {
	target, rest, err := resolveRefactorTarget(ctx, ws, &f.target, args)
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, cli.UsageErrorf("refactor rename takes exactly one <new-name> argument after the target")
	}
	newName := rest[0]

	uri := ws.URI(target.File.FileName())
	position := ws.Conv.PositionToLineAndCharacter(target.File, icore.TextPos(refactorTargetNamePos(target)))

	info := ws.LS.GetRenameInfo(ctx, newName, uri, position)
	if !info.CanRename {
		message := info.LocalizedErrorMessage
		if message == "" {
			message = "this element cannot be renamed"
		}
		return nil, cli.RefusedErrorf("cannot rename: %s", message)
	}
	var notes []string
	if info.FileToRename != "" {
		notes = append(notes, fmt.Sprintf("renaming the containing file (%s) is not done automatically; use refactor mv", ws.RelPath(info.FileToRename)))
	}

	response, err := ws.LS.ProvideRename(ctx, &lsproto.RenameParams{
		TextDocument: lsproto.TextDocumentIdentifier{Uri: uri},
		Position:     position,
		NewName:      newName,
	}, nil /*orchestrator: single-project*/)
	if err != nil {
		return nil, err
	}
	if response.WorkspaceEdit == nil {
		return nil, cli.NotFoundErrorf("no rename locations found for the target")
	}
	var es core.EditSet
	if err := es.AddWorkspaceEdit(ws, response.WorkspaceEdit); err != nil {
		return nil, err
	}
	if es.IsEmpty() {
		return nil, cli.NotFoundErrorf("no rename locations found for the target")
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, notes)
}

// ---------------------------------------------------------------------------
// refactor mv

type refactorMvFlags struct {
	tx refactorTxFlags
}

func runRefactorMv(ctx context.Context, ws *core.Workspace, f *refactorMvFlags, args []string) (*core.TxResult, error) {
	if len(args) < 2 {
		return nil, cli.UsageErrorf("refactor mv takes <from...> <to>")
	}
	sources, to := args[:len(args)-1], args[len(args)-1]
	toAbs := ws.AbsPath(to)
	toIsDir := ws.FS.DirectoryExists(toAbs) || strings.HasSuffix(to, "/")
	if len(sources) > 1 && !toIsDir {
		return nil, cli.UsageErrorf("with multiple sources, <to> must be a directory")
	}

	var es core.EditSet
	for _, source := range sources {
		if ws.FS.DirectoryExists(ws.AbsPath(source)) {
			return nil, cli.UsageErrorf("moving directories is not supported yet (move the files individually): %s", source)
		}
		file, err := ws.FileOf(source)
		if err != nil {
			return nil, err
		}
		fromAbs := file.FileName()
		newAbs := toAbs
		if toIsDir {
			newAbs = tspath.CombinePaths(toAbs, tspath.GetBaseFileName(fromAbs))
		}
		newAbs = tspath.NormalizePath(newAbs)
		if newAbs == fromAbs {
			return nil, cli.UsageErrorf("%s already lives at %s", source, to)
		}
		if ws.FS.FileExists(newAbs) {
			return nil, cli.RefusedErrorf("destination %s already exists", ws.RelPath(newAbs))
		}

		// Import/tsconfig updates target pre-move paths; the rename op is
		// sequenced after edit application by the transaction engine.
		changes := ws.LS.GetEditsForFileRename(ctx, ws.URI(fromAbs), ws.URI(newAbs))
		if err := es.AddDocumentChanges(ws, changes); err != nil {
			return nil, err
		}
		es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpRename, Path: fromAbs, NewPath: newAbs})
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, nil)
}

// ---------------------------------------------------------------------------
// refactor organize-imports

type refactorOrganizeFlags struct {
	tx refactorTxFlags
}

func runRefactorOrganizeImports(ctx context.Context, ws *core.Workspace, f *refactorOrganizeFlags, args []string) (*core.TxResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	var es core.EditSet
	for _, file := range files {
		if file.IsDeclarationFile {
			continue
		}
		changes := ws.LS.OrganizeImports(ctx, file, ws.Program, lsproto.CodeActionKindSourceOrganizeImports)
		for _, fileName := range slices.Sorted(maps.Keys(changes)) {
			if err := es.AddTextEdits(ws, fileName, changes[fileName]); err != nil {
				return nil, err
			}
		}
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, nil)
}

// ---------------------------------------------------------------------------
// refactor safe-delete

type refactorSafeDeleteFlags struct {
	target  refactorTargetFlags
	tx      refactorTxFlags
	cascade bool
}

// refactorDeletionSpan is one node scheduled for deletion (widened via
// refactorDeletionNode).
type refactorDeletionSpan struct {
	file *ast.SourceFile
	node *ast.Node
}

// refactorSymbolSpans computes the deletion spans for a symbol's
// declarations.
func refactorSymbolSpans(symbol *ast.Symbol) []refactorDeletionSpan {
	var spans []refactorDeletionSpan
	seen := make(map[*ast.Node]bool)
	for _, decl := range symbol.Declarations {
		node := refactorDeletionNode(decl)
		if seen[node] {
			continue
		}
		seen[node] = true
		spans = append(spans, refactorDeletionSpan{file: ast.GetSourceFileOfNode(node), node: node})
	}
	return spans
}

// refactorBlockingRefs returns the locations of references to refNode's
// symbol that fall outside every span in spans (sorted, deduplicated).
func refactorBlockingRefs(ctx context.Context, ws *core.Workspace, refNode *ast.Node, spans []refactorDeletionSpan) []string {
	entries := ws.LS.GetReferencedSymbolsForNode(ctx, refNode.Pos(), refNode, ws.Program.GetSourceFiles())
	var blocking []string
	for _, entry := range entries {
		for _, ref := range entry.References() {
			node := ref.Node()
			if node == nil {
				blocking = append(blocking, "(reference without a resolvable location)")
				continue
			}
			file := ast.GetSourceFileOfNode(node)
			inside := slices.ContainsFunc(spans, func(s refactorDeletionSpan) bool {
				return s.file == file && node.Pos() >= s.node.Pos() && node.End() <= s.node.End()
			})
			if !inside {
				line, col := ws.PosToLineCol(file, astnav.GetStartOfNode(node, file, false /*includeJSDoc*/))
				blocking = append(blocking, fmt.Sprintf("%s:%d:%d", ws.RelPath(file.FileName()), line, col))
			}
		}
	}
	slices.Sort(blocking)
	return slices.Compact(blocking)
}

func runRefactorSafeDelete(ctx context.Context, ws *core.Workspace, f *refactorSafeDeleteFlags, args []string) (*core.TxResult, error) {
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
	symbolName := symbol.Name
	refNode := target.Node
	if name := ast.GetNameOfDeclaration(target.Node); name != nil {
		refNode = name
		symbolName = name.Text()
	}

	// Deletion nodes: the full declaration, widened to the enclosing
	// variable statement for sole declarators.
	spans := refactorSymbolSpans(symbol)

	// References outside the declarations themselves block the deletion.
	if blocking := refactorBlockingRefs(ctx, ws, refNode, spans); len(blocking) > 0 {
		return nil, cli.RefusedErrorf("cannot safe-delete %q: %d blocking reference(s):\n  %s",
			symbolName, len(blocking), strings.Join(blocking, "\n  "))
	}

	notes := []string{"imports that become unused after the deletion are not removed (run refactor organize-imports)"}
	if f.cascade {
		spans, notes = refactorCascadeSpans(ctx, ws, symbolName, spans, notes)
	}

	var es core.EditSet
	for _, span := range spans {
		deletionRange := refactorDeletionRange(span.file, span.node)
		es.Edits = append(es.Edits, core.FileEdit{
			FileName: span.file.FileName(),
			Edits:    []icore.TextChange{{TextRange: deletionRange, NewText: ""}},
		})
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, notes)
}

// refactorCascadeSpans grows the deletion set to a fixpoint: symbols that the
// deleted declarations referenced and that have no remaining references
// outside the deletion set are deleted too, when they are unexported
// top-level declarations of project files. Capped at 10 rounds.
func refactorCascadeSpans(ctx context.Context, ws *core.Workspace, rootName string, spans []refactorDeletionSpan, notes []string) ([]refactorDeletionSpan, []string) {
	const maxRounds = 10
	inSet := make(map[*ast.Node]bool)
	parentOf := make(map[*ast.Node]string) // deletion node -> name of the symbol whose deletion freed it
	nameOf := make(map[*ast.Node]string)
	for _, s := range spans {
		inSet[s.node] = true
		nameOf[s.node] = rootName
	}
	frontier := slices.Clone(spans)

	for round := 0; round < maxRounds && len(frontier) > 0; round++ {
		var next []refactorDeletionSpan
		for _, span := range frontier {
			checker, done := ws.Program.GetTypeCheckerForFile(ctx, span.file)
			for _, id := range refactorCollectIdentifiers(span.node) {
				sym := checker.GetSymbolAtLocation(id)
				if sym == nil || len(sym.Declarations) == 0 {
					continue
				}
				// Only unexported top-level declarations of project files are
				// cascade candidates; imports are left to organize-imports.
				candidate := refactorDeletionNode(sym.Declarations[0])
				if inSet[candidate] {
					continue
				}
				if !refactorCascadeEligible(ws, sym) {
					continue
				}
				nameNode := ast.GetNameOfDeclaration(sym.Declarations[0])
				if nameNode == nil {
					continue
				}
				candSpans := refactorSymbolSpans(sym)
				if len(refactorBlockingRefs(ctx, ws, nameNode, append(slices.Clone(spans), candSpans...))) > 0 {
					continue
				}
				for _, cs := range candSpans {
					if inSet[cs.node] {
						continue
					}
					inSet[cs.node] = true
					parentOf[cs.node] = nameOf[span.node]
					nameOf[cs.node] = nameNode.Text()
					spans = append(spans, cs)
					next = append(next, cs)
				}
			}
			done()
		}
		frontier = next
	}
	if len(frontier) > 0 {
		notes = append(notes, fmt.Sprintf("cascade stopped after %d rounds; more symbols may have become dead", maxRounds))
	}
	for _, s := range spans {
		if parent, ok := parentOf[s.node]; ok {
			notes = append(notes, fmt.Sprintf("cascade: %s (%s) became dead after deleting %s",
				nameOf[s.node], refactorNodeLineCol(ws, s.node), parent))
		}
	}
	return spans, notes
}

// refactorCascadeEligible reports whether a symbol may be swept up by
// --cascade: declared (only) at the top level of project source files, with
// no export modifier on any declaration, and not an import binding.
func refactorCascadeEligible(ws *core.Workspace, sym *ast.Symbol) bool {
	for _, decl := range sym.Declarations {
		if !refactorIsProjectSourceNode(ws, decl) {
			return false
		}
		switch decl.Kind {
		case ast.KindImportSpecifier, ast.KindImportClause, ast.KindNamespaceImport,
			ast.KindParameter, ast.KindTypeParameter, ast.KindBindingElement:
			return false
		}
		widened := refactorDeletionNode(decl)
		if widened.Parent == nil || widened.Parent.Kind != ast.KindSourceFile {
			return false
		}
		if widened.ModifierFlags()&ast.ModifierFlagsExport != 0 ||
			decl.ModifierFlags()&ast.ModifierFlagsExport != 0 {
			return false
		}
	}
	return true
}

// refactorDeletionNode widens a declaration to the node that should actually
// be deleted: a variable declarator that is the only one in its statement is
// widened to the whole statement.
func refactorDeletionNode(decl *ast.Node) *ast.Node {
	if decl.Kind == ast.KindVariableDeclaration {
		if list := decl.Parent; list != nil && list.Kind == ast.KindVariableDeclarationList &&
			len(list.AsVariableDeclarationList().Declarations.Nodes) == 1 {
			if statement := list.Parent; statement != nil && statement.Kind == ast.KindVariableStatement {
				return statement
			}
		}
	}
	return decl
}

// refactorDeletionRange computes the byte range to remove for a declaration:
// whole lines including leading JSDoc and the trailing newline; for a
// declarator within a multi-declarator list, the declarator plus its
// separating comma.
func refactorDeletionRange(file *ast.SourceFile, node *ast.Node) icore.TextRange {
	text := file.Text()
	pos, end := node.Pos(), node.End()

	if node.Kind == ast.KindVariableDeclaration {
		j := end
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j < len(text) && text[j] == ',' {
			return icore.NewTextRange(pos, j+1)
		}
		if pos > 0 && text[pos-1] == ',' {
			return icore.NewTextRange(pos-1, end)
		}
		return icore.NewTextRange(pos, end)
	}

	// Start at the beginning of the line holding the declaration (or its
	// JSDoc) when everything before it on that line is whitespace.
	start := astnav.GetStartOfNode(node, file, true /*includeJSDoc*/)
	lineStart := start
	for lineStart > 0 && text[lineStart-1] != '\n' {
		lineStart--
	}
	if strings.TrimLeft(text[lineStart:start], " \t") == "" {
		start = lineStart
	}
	// Consume the trailing newline when the rest of the line is whitespace.
	j := end
	for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
		j++
	}
	if j < len(text) && text[j] == '\r' {
		j++
	}
	if j < len(text) && text[j] == '\n' {
		end = j + 1
	}
	return icore.NewTextRange(start, end)
}
