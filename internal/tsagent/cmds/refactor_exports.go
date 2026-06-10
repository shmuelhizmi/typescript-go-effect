package cmds

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// refactor exports (§4.4): convert default ↔ named exports per file, with all
// import sites updated, in one transaction.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "exports",
		Summary:      "Convert default <-> named exports with all import sites updated",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorExportsFlags{}
			registerRefactorTxFlags(fs, &f.tx)
			fs.StringVar(&f.to, "to", "", "target export style: named or default")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorExports(ctx, ws, flags.(*refactorExportsFlags), args)
		},
	})
}

type refactorExportsFlags struct {
	tx refactorTxFlags
	to string
}

func runRefactorExports(ctx context.Context, ws *core.Workspace, f *refactorExportsFlags, args []string) (*core.TxResult, error) {
	if f.to != "named" && f.to != "default" {
		return nil, cli.UsageErrorf("--to must be named or default")
	}
	if len(args) == 0 {
		return nil, cli.UsageErrorf("refactor exports takes at least one file path")
	}
	var es core.EditSet
	var notes []string
	for _, arg := range args {
		file, err := ws.FileOf(arg)
		if err != nil {
			return nil, err
		}
		if f.to == "named" {
			err = refactorExportsToNamed(ws, file, &es, &notes)
		} else {
			err = refactorExportsToDefault(ws, file, &es, &notes)
		}
		if err != nil {
			return nil, err
		}
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, notes)
}

// ---------------------------------------------------------------------------
// --to named

func refactorExportsToNamed(ws *core.Workspace, file *ast.SourceFile, es *core.EditSet, notes *[]string) error {
	rel := ws.RelPath(file.FileName())
	text := file.Text()

	// Refuse default re-exports up front (v1).
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != ast.KindExportDeclaration {
			continue
		}
		for _, spec := range refactorNamedExportSpecifiers(stmt) {
			if spec.Name().Text() == "default" {
				return cli.RefusedErrorf("%s: default re-exports (export { ... as default }) are not supported yet; rewrite it manually first", rel)
			}
		}
	}

	var defaultDecl *ast.Node  // function/class declaration with a `default` modifier
	var exportAssign *ast.Node // `export default <expr>;`
	for _, stmt := range file.Statements.Nodes {
		switch {
		case stmt.ModifierFlags()&ast.ModifierFlagsDefault != 0:
			defaultDecl = stmt
		case stmt.Kind == ast.KindExportAssignment && !stmt.AsExportAssignment().IsExportEquals:
			exportAssign = stmt
		}
	}
	if defaultDecl == nil && exportAssign == nil {
		return cli.NotFoundErrorf("%s has no default export", rel)
	}

	var newName string
	var fileEdits []icore.TextChange

	switch {
	case defaultDecl != nil:
		// `export default function f() {}` -> `export function f() {}`
		// Anonymous declarations get a name derived from the file name.
		mods := defaultDecl.Modifiers()
		var defaultMod *ast.Node
		for _, mod := range mods.Nodes {
			if mod.Kind == ast.KindDefaultKeyword {
				defaultMod = mod
			}
		}
		if defaultMod == nil {
			return cli.NotFoundErrorf("%s: cannot locate the `default` modifier", rel)
		}
		start := scanner.SkipTrivia(text, defaultMod.Pos())
		end := defaultMod.End()
		for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
			end++
		}
		fileEdits = append(fileEdits, icore.TextChange{TextRange: icore.NewTextRange(start, end), NewText: ""})

		if name := ast.GetNameOfDeclaration(defaultDecl); name != nil {
			newName = name.Text()
		} else {
			newName = refactorNameFromFileName(file.FileName())
			if err := refactorCheckNameCollision(ws, file, newName, defaultDecl); err != nil {
				return err
			}
			insert, err := refactorAnonymousNameInsertion(ws, file, defaultDecl, newName)
			if err != nil {
				return err
			}
			fileEdits = append(fileEdits, insert)
		}

	case exportAssign != nil:
		expr := exportAssign.AsExportAssignment().Expression
		if expr.Kind == ast.KindIdentifier {
			// `export default foo;` where foo is a same-file top-level
			// declaration: drop the statement and export the declaration.
			if decl := refactorTopLevelDeclNamed(file, expr.Text()); decl != nil {
				newName = expr.Text()
				fileEdits = append(fileEdits, icore.TextChange{TextRange: refactorDeletionRange(file, exportAssign), NewText: ""})
				if decl.ModifierFlags()&ast.ModifierFlagsExport == 0 {
					insertAt := scanner.SkipTrivia(text, decl.Pos())
					fileEdits = append(fileEdits, icore.TextChange{TextRange: icore.NewTextRange(insertAt, insertAt), NewText: "export "})
				} else {
					*notes = append(*notes, fmt.Sprintf("%s: %s was already exported as a named export; the default alias was removed", rel, newName))
				}
				break
			}
		}
		// `export default <expr>;` -> `export const <name> = <expr>;`
		newName = refactorNameFromFileName(file.FileName())
		if err := refactorCheckNameCollision(ws, file, newName, exportAssign); err != nil {
			return err
		}
		start := scanner.SkipTrivia(text, exportAssign.Pos())
		fileEdits = append(fileEdits, icore.TextChange{
			TextRange: icore.NewTextRange(start, exportAssign.End()),
			NewText:   "export const " + newName + " = " + refactorNodeText(file, expr) + ";",
		})
	}

	es.Edits = append(es.Edits, core.FileEdit{FileName: file.FileName(), Edits: fileEdits})

	// Rewrite all importers: `import X from "./f"` -> `import { N as X } from "./f"`.
	for _, importer := range ws.Program.SourceFiles() {
		if importer == file || !refactorIsProjectSourceNode(ws, importer.AsNode()) {
			continue
		}
		var edits []icore.TextChange
		for _, importDecl := range refactorImportsResolvingTo(ws, importer, file.FileName()) {
			clauseNode := importDecl.AsImportDeclaration().ImportClause
			if clauseNode == nil || clauseNode.Name() == nil {
				continue
			}
			clause := clauseNode.AsImportClause()
			localName := clauseNode.Name().Text()
			if clause.NamedBindings != nil && clause.NamedBindings.Kind == ast.KindNamespaceImport {
				return cli.RefusedErrorf("%s: `import %s, * as ns` (default + namespace) cannot be converted automatically; split the import first",
					ws.RelPath(importer.FileName()), localName)
			}
			binding := newName
			if localName != newName {
				binding = newName + " as " + localName
			}
			parts := []string{binding}
			if clause.NamedBindings != nil && clause.NamedBindings.Kind == ast.KindNamedImports {
				for _, spec := range clause.NamedBindings.AsNamedImports().Elements.Nodes {
					parts = append(parts, refactorNodeText(importer, spec))
				}
			}
			prefix := ""
			if clause.PhaseModifier == ast.KindTypeKeyword {
				prefix = "type "
			}
			start := scanner.SkipTrivia(importer.Text(), clauseNode.Pos())
			edits = append(edits, icore.TextChange{
				TextRange: icore.NewTextRange(start, clauseNode.End()),
				NewText:   prefix + "{ " + strings.Join(parts, ", ") + " }",
			})
		}
		if len(edits) > 0 {
			es.Edits = append(es.Edits, core.FileEdit{FileName: importer.FileName(), Edits: edits})
		}
	}
	return nil
}

// refactorAnonymousNameInsertion produces the edit that names an anonymous
// default function/class declaration.
func refactorAnonymousNameInsertion(ws *core.Workspace, file *ast.SourceFile, decl *ast.Node, name string) (icore.TextChange, error) {
	text := file.Text()
	kwStart := scanner.SkipTrivia(text, decl.Modifiers().End())
	rest := text[kwStart:]
	var keyword string
	switch decl.Kind {
	case ast.KindFunctionDeclaration:
		keyword = "function"
	case ast.KindClassDeclaration:
		keyword = "class"
	default:
		return icore.TextChange{}, cli.RefusedErrorf("%s: cannot name an anonymous default export of kind %s", ws.RelPath(file.FileName()), decl.Kind)
	}
	if !strings.HasPrefix(rest, keyword) {
		return icore.TextChange{}, cli.RefusedErrorf("%s: unexpected declaration shape (no `%s` keyword after modifiers)", ws.RelPath(file.FileName()), keyword)
	}
	off := len(keyword)
	// Generators: the name goes after the `*`.
	probe := off
	for probe < len(rest) && (rest[probe] == ' ' || rest[probe] == '\t') {
		probe++
	}
	if decl.Kind == ast.KindFunctionDeclaration && probe < len(rest) && rest[probe] == '*' {
		off = probe + 1
	}
	// Consume the whitespace up to the `(`/`{` so the result reads
	// `function name(` rather than `function name (`.
	wsEnd := off
	for wsEnd < len(rest) && (rest[wsEnd] == ' ' || rest[wsEnd] == '\t') {
		wsEnd++
	}
	return icore.TextChange{TextRange: icore.NewTextRange(kwStart+off, kwStart+wsEnd), NewText: " " + name}, nil
}

// refactorTopLevelDeclNamed finds a top-level declaration statement (or sole
// variable declarator) with the given name.
func refactorTopLevelDeclNamed(file *ast.SourceFile, name string) *ast.Node {
	for _, stmt := range file.Statements.Nodes {
		switch stmt.Kind {
		case ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindInterfaceDeclaration,
			ast.KindTypeAliasDeclaration, ast.KindEnumDeclaration:
			if n := ast.GetNameOfDeclaration(stmt); n != nil && n.Text() == name {
				return stmt
			}
		case ast.KindVariableStatement:
			for _, decl := range stmt.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
				if n := ast.GetNameOfDeclaration(decl); n != nil && n.Kind == ast.KindIdentifier && n.Text() == name {
					return stmt
				}
			}
		}
	}
	return nil
}

// refactorCheckNameCollision refuses when the file already declares `name`
// outside of `except`.
func refactorCheckNameCollision(ws *core.Workspace, file *ast.SourceFile, name string, except *ast.Node) error {
	for _, decl := range file.GetDeclarationMap()[name] {
		if decl.Pos() >= except.Pos() && decl.End() <= except.End() {
			continue
		}
		return cli.RefusedErrorf("%s: the derived name %q collides with an existing declaration at %s; rename one of them first",
			ws.RelPath(file.FileName()), name, refactorNodeLineCol(ws, decl))
	}
	return nil
}

// ---------------------------------------------------------------------------
// --to default

func refactorExportsToDefault(ws *core.Workspace, file *ast.SourceFile, es *core.EditSet, notes *[]string) error {
	rel := ws.RelPath(file.FileName())
	text := file.Text()

	type namedExport struct {
		name string
		stmt *ast.Node // exporting statement
		spec *ast.Node // export specifier (for `export { X }` form)
	}
	var exports []namedExport

	for _, stmt := range file.Statements.Nodes {
		flags := stmt.ModifierFlags()
		if flags&ast.ModifierFlagsDefault != 0 || (stmt.Kind == ast.KindExportAssignment && !stmt.AsExportAssignment().IsExportEquals) {
			return cli.RefusedErrorf("%s already has a default export", rel)
		}
		switch stmt.Kind {
		case ast.KindExportAssignment:
			return cli.RefusedErrorf("%s uses `export =`, which cannot be converted", rel)
		case ast.KindExportDeclaration:
			ed := stmt.AsExportDeclaration()
			if ed.ModuleSpecifier != nil {
				return cli.RefusedErrorf("%s re-exports from another module; convert the source module instead", rel)
			}
			if ed.ExportClause == nil || ed.ExportClause.Kind != ast.KindNamedExports {
				return cli.RefusedErrorf("%s uses a namespace export, which cannot be converted", rel)
			}
			for _, spec := range ed.ExportClause.AsNamedExports().Elements.Nodes {
				exports = append(exports, namedExport{name: spec.Name().Text(), stmt: stmt, spec: spec})
			}
		case ast.KindVariableStatement:
			if flags&ast.ModifierFlagsExport == 0 {
				continue
			}
			for _, decl := range stmt.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
				name := ast.GetNameOfDeclaration(decl)
				if name == nil || name.Kind != ast.KindIdentifier {
					return cli.RefusedErrorf("%s exports a destructuring declaration, which cannot be converted", rel)
				}
				exports = append(exports, namedExport{name: name.Text(), stmt: stmt})
			}
		default:
			if flags&ast.ModifierFlagsExport == 0 {
				continue
			}
			name := ast.GetNameOfDeclaration(stmt)
			if name == nil {
				return cli.RefusedErrorf("%s has an exported declaration without a name at %s", rel, refactorNodeLineCol(ws, stmt))
			}
			exports = append(exports, namedExport{name: name.Text(), stmt: stmt})
		}
	}

	if len(exports) != 1 {
		names := make([]string, 0, len(exports))
		for _, e := range exports {
			names = append(names, e.name)
		}
		return cli.RefusedErrorf("--to default needs exactly one named export; %s has %d (%s)", rel, len(exports), strings.Join(names, ", "))
	}
	exp := exports[0]
	var fileEdits []icore.TextChange

	switch {
	case exp.spec != nil:
		// `export { X };` -> `export { X as default };`
		inner := exp.spec.AsExportSpecifier()
		localName := exp.name
		if inner.PropertyName != nil {
			localName = inner.PropertyName.Text()
		}
		start := scanner.SkipTrivia(text, exp.spec.Pos())
		fileEdits = append(fileEdits, icore.TextChange{
			TextRange: icore.NewTextRange(start, exp.spec.End()),
			NewText:   localName + " as default",
		})
	case exp.stmt.Kind == ast.KindFunctionDeclaration || exp.stmt.Kind == ast.KindClassDeclaration:
		// `export function f` -> `export default function f`
		var exportMod *ast.Node
		for _, mod := range exp.stmt.Modifiers().Nodes {
			if mod.Kind == ast.KindExportKeyword {
				exportMod = mod
			}
		}
		fileEdits = append(fileEdits, icore.TextChange{
			TextRange: icore.NewTextRange(exportMod.End(), exportMod.End()),
			NewText:   " default",
		})
	default:
		// Drop the `export` modifier; append a default export statement.
		var exportMod *ast.Node
		for _, mod := range exp.stmt.Modifiers().Nodes {
			if mod.Kind == ast.KindExportKeyword {
				exportMod = mod
			}
		}
		start := scanner.SkipTrivia(text, exportMod.Pos())
		end := exportMod.End()
		for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
			end++
		}
		fileEdits = append(fileEdits, icore.TextChange{TextRange: icore.NewTextRange(start, end), NewText: ""})
		tail := "export default " + exp.name + ";\n"
		if exp.stmt.Kind == ast.KindInterfaceDeclaration || exp.stmt.Kind == ast.KindTypeAliasDeclaration {
			tail = "export { " + exp.name + " as default };\n"
		}
		insert := "\n" + tail
		if strings.HasSuffix(text, "\n") {
			insert = tail
		}
		fileEdits = append(fileEdits, icore.TextChange{TextRange: icore.NewTextRange(len(text), len(text)), NewText: insert})
	}
	es.Edits = append(es.Edits, core.FileEdit{FileName: file.FileName(), Edits: fileEdits})

	// Rewrite importers: `import { X as Y } from "./f"` -> `import Y from "./f"`.
	for _, importer := range ws.Program.SourceFiles() {
		if importer == file || !refactorIsProjectSourceNode(ws, importer.AsNode()) {
			continue
		}
		var edits []icore.TextChange
		for _, importDecl := range refactorImportsResolvingTo(ws, importer, file.FileName()) {
			clauseNode := importDecl.AsImportDeclaration().ImportClause
			if clauseNode == nil {
				continue
			}
			clause := clauseNode.AsImportClause()
			if clause.NamedBindings != nil && clause.NamedBindings.Kind == ast.KindNamespaceImport {
				*notes = append(*notes, fmt.Sprintf("%s imports * as namespace; member access of %q will break if used",
					ws.RelPath(importer.FileName()), exp.name))
				continue
			}
			var localName string
			for _, spec := range refactorNamedImportSpecifiers(importDecl) {
				if refactorImportedName(spec) == exp.name {
					localName = spec.Name().Text()
				}
			}
			if localName == "" {
				continue
			}
			prefix := ""
			if clause.PhaseModifier == ast.KindTypeKeyword {
				prefix = "type "
			}
			start := scanner.SkipTrivia(importer.Text(), clauseNode.Pos())
			edits = append(edits, icore.TextChange{
				TextRange: icore.NewTextRange(start, clauseNode.End()),
				NewText:   prefix + localName,
			})
		}
		if len(edits) > 0 {
			es.Edits = append(es.Edits, core.FileEdit{FileName: importer.FileName(), Edits: edits})
		}
	}
	return nil
}

// refactorNamedExportSpecifiers returns the specifiers of an export
// declaration's named-exports clause (nil otherwise).
func refactorNamedExportSpecifiers(stmt *ast.Node) []*ast.Node {
	ed := stmt.AsExportDeclaration()
	if ed.ExportClause == nil || ed.ExportClause.Kind != ast.KindNamedExports {
		return nil
	}
	return ed.ExportClause.AsNamedExports().Elements.Nodes
}
