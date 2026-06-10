package cmds

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// refactor_common.go holds small helpers shared by the refactor family
// (deliberately local to refactor_*.go files; see the file-ownership note in
// refactor.go).

// refactorNodeText returns the source text of a node with leading trivia
// stripped.
func refactorNodeText(file *ast.SourceFile, node *ast.Node) string {
	return file.Text()[scanner.SkipTrivia(file.Text(), node.Pos()):node.End()]
}

// refactorCollectIdentifiers returns every identifier node within root in
// source order.
func refactorCollectIdentifiers(root *ast.Node) []*ast.Node {
	var ids []*ast.Node
	var visit func(n *ast.Node) bool
	visit = func(n *ast.Node) bool {
		if n.Kind == ast.KindIdentifier {
			ids = append(ids, n)
		}
		n.ForEachChild(visit)
		return false
	}
	root.ForEachChild(visit)
	if root.Kind == ast.KindIdentifier {
		ids = append(ids, root)
	}
	return ids
}

// refactorReferenceNodes flattens the language service's find-all-references
// result for nameNode into a deduplicated node list (in arbitrary order).
func refactorReferenceNodes(ctx context.Context, ws *core.Workspace, nameNode *ast.Node) []*ast.Node {
	entries := ws.LS.GetReferencedSymbolsForNode(ctx, nameNode.Pos(), nameNode, ws.Program.GetSourceFiles())
	seen := make(map[*ast.Node]bool)
	var nodes []*ast.Node
	for _, entry := range entries {
		for _, ref := range entry.References() {
			if !ref.IsNodeEntry() || seen[ref.Node()] {
				continue
			}
			seen[ref.Node()] = true
			nodes = append(nodes, ref.Node())
		}
	}
	return nodes
}

// refactorModuleSpecifierText computes the module specifier to import toFile
// from fromFile: a relative path without extension, "./"-prefixed when needed.
func refactorModuleSpecifierText(ws *core.Workspace, fromFile string, toFile string) string {
	rel := tspath.GetRelativePathFromDirectory(tspath.GetDirectoryPath(fromFile), toFile, tspath.ComparePathsOptions{
		CurrentDirectory:          ws.RootDir,
		UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
	})
	rel = tspath.RemoveFileExtension(rel)
	return tspath.EnsurePathIsNonModuleName(rel)
}

// refactorImportsResolvingTo returns the import declarations in file whose
// module specifier resolves to targetFileName.
func refactorImportsResolvingTo(ws *core.Workspace, file *ast.SourceFile, targetFileName string) []*ast.Node {
	var decls []*ast.Node
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != ast.KindImportDeclaration {
			continue
		}
		spec := stmt.AsImportDeclaration().ModuleSpecifier
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

// refactorNamedImportSpecifiers returns the named import specifiers of an
// import declaration (nil when it has none).
func refactorNamedImportSpecifiers(importDecl *ast.Node) []*ast.Node {
	clause := importDecl.AsImportDeclaration().ImportClause
	if clause == nil {
		return nil
	}
	bindings := clause.AsImportClause().NamedBindings
	if bindings == nil || bindings.Kind != ast.KindNamedImports {
		return nil
	}
	return bindings.AsNamedImports().Elements.Nodes
}

// refactorImportedName returns the exported name an import specifier binds
// (the property name for `import { X as Y }`, the name otherwise).
func refactorImportedName(spec *ast.Node) string {
	s := spec.AsImportSpecifier()
	if s.PropertyName != nil {
		return s.PropertyName.Text()
	}
	return spec.Name().Text()
}

// refactorRemoveImportSpecifierEdit produces the edit that removes one named
// import specifier: the whole import statement when it is the only binding,
// otherwise the specifier plus its separating comma.
func refactorRemoveImportSpecifierEdit(file *ast.SourceFile, importDecl *ast.Node, spec *ast.Node) icore.TextChange {
	clause := importDecl.AsImportDeclaration().ImportClause.AsImportClause()
	specs := refactorNamedImportSpecifiers(importDecl)
	if len(specs) == 1 && clause.Name() == nil {
		return icore.TextChange{TextRange: refactorDeletionRange(file, importDecl), NewText: ""}
	}
	text := file.Text()
	pos := scanner.SkipTrivia(text, spec.Pos())
	end := spec.End()
	j := end
	for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
		j++
	}
	if j < len(text) && text[j] == ',' {
		j++
		for j < len(text) && text[j] == ' ' {
			j++
		}
		return icore.TextChange{TextRange: icore.NewTextRange(pos, j), NewText: ""}
	}
	// Last specifier: eat the preceding comma.
	i := pos
	for i > 0 && (text[i-1] == ' ' || text[i-1] == '\t' || text[i-1] == '\n' || text[i-1] == '\r') {
		i--
	}
	if i > 0 && text[i-1] == ',' {
		i--
	}
	return icore.TextChange{TextRange: icore.NewTextRange(i, end), NewText: ""}
}

// refactorInsertImportEdit produces an edit inserting an import statement at
// the top of a file.
func refactorInsertImportEdit(names []string, specifier string) icore.TextChange {
	return icore.TextChange{
		TextRange: icore.NewTextRange(0, 0),
		NewText:   "import { " + strings.Join(names, ", ") + " } from \"" + specifier + "\";\n",
	}
}

// refactorIsSimpleExpression reports whether an expression can be substituted
// inline without parentheses: identifiers, literals, calls, property/element
// accesses, and already-parenthesized expressions.
func refactorIsSimpleExpression(node *ast.Node) bool {
	switch node.Kind {
	case ast.KindIdentifier, ast.KindThisKeyword,
		ast.KindNumericLiteral, ast.KindBigIntLiteral, ast.KindStringLiteral,
		ast.KindNoSubstitutionTemplateLiteral, ast.KindTrueKeyword, ast.KindFalseKeyword,
		ast.KindNullKeyword, ast.KindRegularExpressionLiteral,
		ast.KindCallExpression, ast.KindNewExpression,
		ast.KindPropertyAccessExpression, ast.KindElementAccessExpression,
		ast.KindParenthesizedExpression,
		ast.KindArrayLiteralExpression, ast.KindObjectLiteralExpression:
		return true
	}
	return false
}

// refactorIsSideEffectFree reports (conservatively) whether evaluating an
// expression can have no side effects: identifiers, literals, and `this`.
func refactorIsSideEffectFree(node *ast.Node) bool {
	switch node.Kind {
	case ast.KindIdentifier, ast.KindThisKeyword,
		ast.KindNumericLiteral, ast.KindBigIntLiteral, ast.KindStringLiteral,
		ast.KindNoSubstitutionTemplateLiteral, ast.KindTrueKeyword, ast.KindFalseKeyword,
		ast.KindNullKeyword:
		return true
	}
	return false
}

// refactorLineIndent returns the leading whitespace of the line containing
// pos.
func refactorLineIndent(file *ast.SourceFile, pos int) string {
	text := file.Text()
	lineStart := pos
	for lineStart > 0 && text[lineStart-1] != '\n' {
		lineStart--
	}
	i := lineStart
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return text[lineStart:i]
}

// refactorNameFromFileName derives a camelCase identifier from a file name:
// "user-profile.helper.ts" -> "userProfileHelper".
func refactorNameFromFileName(fileName string) string {
	base := tspath.RemoveFileExtension(tspath.GetBaseFileName(fileName))
	var sb strings.Builder
	upperNext := false
	for _, r := range base {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			upperNext = sb.Len() > 0
			continue
		}
		if sb.Len() == 0 && unicode.IsDigit(r) {
			sb.WriteByte('_')
		}
		if upperNext {
			sb.WriteRune(unicode.ToUpper(r))
			upperNext = false
		} else {
			sb.WriteRune(r)
		}
	}
	if sb.Len() == 0 {
		return "defaultExport"
	}
	return sb.String()
}

// refactorNodeLineCol renders "file:line:col" of a node's trivia-skipped
// start, relative to the project root.
func refactorNodeLineCol(ws *core.Workspace, node *ast.Node) string {
	file := ast.GetSourceFileOfNode(node)
	line, col := ws.PosToLineCol(file, astnav.GetStartOfNode(node, file, false /*includeJSDoc*/))
	return refactorLoc(ws.RelPath(file.FileName()), line, col)
}

func refactorLoc(file string, line int, col int) string {
	return fmt.Sprintf("%s:%d:%d", file, line, col)
}

// refactorIsProjectSourceNode reports whether a node lives in a non-lib,
// non-node_modules, non-declaration program file.
func refactorIsProjectSourceNode(ws *core.Workspace, node *ast.Node) bool {
	file := ast.GetSourceFileOfNode(node)
	if file == nil {
		return false
	}
	return !ws.Program.IsLibFile(file) && !file.IsDeclarationFile && !core.IsExternalLibraryPath(file.FileName())
}
