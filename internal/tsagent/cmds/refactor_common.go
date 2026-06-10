package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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

// refactorDestPackage describes the npm/workspace package containing a move
// destination, for retargeting consumers whose original specifier was a bare
// package specifier (e.g. `@scope/pkg/sub`): the package directory, its
// package.json name, and the package-style specifier (name + exports subpath)
// that maps to the destination file ("" when no exports subpath — or, absent
// an exports field, no main/module field — maps to it).
type refactorDestPackage struct {
	dir  string
	name string
	spec string
}

// refactorFindDestPackage walks up from destAbs to the nearest package.json
// with a "name" field and derives the package specifier for destAbs from its
// "exports" value (string targets, conditional objects on the common
// condition keys default/import/require/types/bun, and single-`*` wildcard
// patterns on subpath and target), or from "main"/"module" when there is no
// exports field. ok=false when no named package.json sits above destAbs.
func refactorFindDestPackage(ws *core.Workspace, destAbs string) (refactorDestPackage, bool) {
	for dir := tspath.GetDirectoryPath(destAbs); ; {
		content, found := ws.FS.ReadFile(tspath.CombinePaths(dir, "package.json"))
		if found {
			var pkg struct {
				Name    string `json:"name"`
				Exports any    `json:"exports"`
				Main    string `json:"main"`
				Module  string `json:"module"`
			}
			if json.Unmarshal([]byte(content), &pkg) == nil && pkg.Name != "" {
				rel := tspath.GetRelativePathFromDirectory(dir, destAbs, tspath.ComparePathsOptions{
					CurrentDirectory:          ws.RootDir,
					UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
				})
				info := refactorDestPackage{dir: dir, name: pkg.Name}
				if subpath, ok := refactorExportsSubpath(pkg.Exports, rel); ok {
					info.spec = pkg.Name + strings.TrimPrefix(subpath, ".")
				} else if pkg.Exports == nil &&
					(refactorPkgRelPathEq(pkg.Main, rel) || refactorPkgRelPathEq(pkg.Module, rel)) {
					info.spec = pkg.Name // main/module map the dest: the "." case
				}
				return info, true
			}
		}
		parent := tspath.GetDirectoryPath(dir)
		if parent == dir {
			return refactorDestPackage{}, false
		}
		dir = parent
	}
}

// refactorExportsSubpath scans a decoded package.json "exports" value for the
// subpath whose target maps to relDest (the destination file relative to the
// package directory). A plain string exports value is the "." subpath; an
// object with no "."-prefixed key is a conditions object for ".". Exact
// subpaths win over single-`*` wildcard patterns; ties break lexically for
// determinism. ok=false when nothing maps.
func refactorExportsSubpath(exports any, relDest string) (string, bool) {
	relDest = strings.TrimPrefix(tspath.NormalizeSlashes(relDest), "./")
	switch ex := exports.(type) {
	case string, []any:
		if _, starred, ok := refactorExportTargetMatches(ex, relDest); ok && !starred {
			return ".", true
		}
	case map[string]any:
		subpathMap := false
		for key := range ex {
			if strings.HasPrefix(key, ".") {
				subpathMap = true
				break
			}
		}
		if !subpathMap {
			// Conditions object for the "." subpath.
			if _, starred, ok := refactorExportTargetMatches(ex, relDest); ok && !starred {
				return ".", true
			}
			return "", false
		}
		keys := slices.Sorted(maps.Keys(ex))
		for _, wild := range []bool{false, true} { // exact subpaths first
			for _, key := range keys {
				stars := strings.Count(key, "*")
				if !strings.HasPrefix(key, ".") || stars > 1 || (stars == 1) != wild {
					continue
				}
				star, starred, ok := refactorExportTargetMatches(ex[key], relDest)
				if !ok || starred != wild {
					continue // a starred target needs a starred subpath to substitute into (and vice versa)
				}
				if wild {
					return strings.Replace(key, "*", star, 1), true
				}
				return key, true
			}
		}
	}
	return "", false
}

// refactorExportTargetMatches matches one exports target value against
// relDest: a string (exact, or a single-`*` prefix/suffix pattern whose
// captured middle is returned with starred=true), an array of fallbacks, or
// a conditional object (any of the condition keys
// default/import/require/types/bun counts; nested conditions recurse).
func refactorExportTargetMatches(target any, relDest string) (star string, starred bool, ok bool) {
	switch t := target.(type) {
	case string:
		t = strings.TrimPrefix(tspath.NormalizeSlashes(t), "./")
		if strings.Count(t, "*") == 1 {
			i := strings.Index(t, "*")
			prefix, suffix := t[:i], t[i+1:]
			if len(relDest) >= len(prefix)+len(suffix) && strings.HasPrefix(relDest, prefix) && strings.HasSuffix(relDest, suffix) {
				return relDest[len(prefix) : len(relDest)-len(suffix)], true, true
			}
		} else if !strings.Contains(t, "*") && t == relDest {
			return "", false, true
		}
	case []any:
		for _, el := range t {
			if star, starred, ok = refactorExportTargetMatches(el, relDest); ok {
				return star, starred, true
			}
		}
	case map[string]any:
		for _, cond := range []string{"default", "import", "require", "types", "bun"} {
			if sub, present := t[cond]; present {
				if star, starred, ok = refactorExportTargetMatches(sub, relDest); ok {
					return star, starred, true
				}
			}
		}
	}
	return "", false, false
}

// refactorPkgRelPathEq reports whether a package.json path field (main/module)
// names relDest (both package-directory-relative).
func refactorPkgRelPathEq(field string, relDest string) bool {
	return field != "" && strings.TrimPrefix(tspath.NormalizeSlashes(field), "./") == strings.TrimPrefix(tspath.NormalizeSlashes(relDest), "./")
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
	return refactorRemoveListSpecifierEdit(file.Text(), spec)
}

// refactorRemoveListSpecifierEdit removes one specifier from a braced
// import/export clause along with its separating comma.
func refactorRemoveListSpecifierEdit(text string, spec *ast.Node) icore.TextChange {
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

// importInsertOffset returns the byte offset at which new import statements
// (or other prepended code) should be inserted in a file: after a shebang
// (`#!...`) and after the leading directive prologue — the consecutive
// string-literal expression statements (`"use client";`, `"use strict";`) at
// the very top — at the start of the line following the last directive.
// Returns 0 for files without either.
func importInsertOffset(file *ast.SourceFile) int {
	text := file.Text()
	offset := len(scanner.GetShebang(text))
	for _, stmt := range file.Statements.Nodes {
		if !ast.IsPrologueDirective(stmt) {
			break
		}
		offset = stmt.End()
	}
	// Advance to the start of the following line.
	for offset < len(text) && (text[offset] == ' ' || text[offset] == '\t' || text[offset] == '\r') {
		offset++
	}
	if offset < len(text) && text[offset] == '\n' {
		offset++
	}
	return offset
}

// refactorImportName is one named binding an import plan must make available
// in a file: the exported name in the target module, the local alias to bind,
// and whether the binding is type-only (interfaces, type aliases — anything
// without a value meaning — or bindings that were type-only at their origin).
type refactorImportName struct {
	name     string
	local    string
	typeOnly bool
}

// render writes the specifier text of one name. Inside a value clause,
// type-only names carry an inline `type` modifier; inside an `import type`
// clause the modifier would be redundant (and illegal).
func (n refactorImportName) render(inValueClause bool) string {
	s := n.name
	if n.local != n.name {
		s = n.name + " as " + n.local
	}
	if n.typeOnly && inValueClause {
		s = "type " + s
	}
	return s
}

// refactorImportQuote returns the quote character used by the file's first
// import/export module specifier (falling back to `"`), so synthesized import
// statements match the file's prevailing style.
func refactorImportQuote(file *ast.SourceFile) string {
	if file == nil {
		return "\""
	}
	for _, stmt := range file.Statements.Nodes {
		var spec *ast.Node
		switch stmt.Kind {
		case ast.KindImportDeclaration:
			spec = stmt.AsImportDeclaration().ModuleSpecifier
		case ast.KindExportDeclaration:
			spec = stmt.AsExportDeclaration().ModuleSpecifier
		}
		if spec != nil && ast.IsStringLiteral(spec) {
			pos := scanner.SkipTrivia(file.Text(), spec.Pos())
			if q := file.Text()[pos]; q == '\'' || q == '"' {
				return string(q)
			}
		}
	}
	return "\""
}

// refactorRenderImportStatement renders ONE new import statement binding
// names from specText — the documented convention: one statement per module,
// `import type { … }` when every name is type-only, a value import with
// inline `type` modifiers when value and type names mix.
func refactorRenderImportStatement(names []refactorImportName, specText string, quote string) string {
	allType := true
	for _, n := range names {
		if !n.typeOnly {
			allType = false
			break
		}
	}
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n.render(!allType)
	}
	keyword := "import "
	if allType {
		keyword = "import type "
	}
	return keyword + "{ " + strings.Join(parts, ", ") + " } from " + quote + specText + quote + ";\n"
}

// refactorImportClauseTypeOnly reports whether an import declaration's clause
// is type-only (`import type … from`).
func refactorImportClauseTypeOnly(importDecl *ast.Node) bool {
	clause := importDecl.AsImportDeclaration().ImportClause
	return clause != nil && clause.AsImportClause().PhaseModifier == ast.KindTypeKeyword
}

// refactorImportStatementWouldVanish reports whether removing one named
// specifier from importDecl deletes the whole statement (sole named specifier
// and no default binding) — refactorRemoveImportSpecifierEdit's rule.
func refactorImportStatementWouldVanish(importDecl *ast.Node) bool {
	clause := importDecl.AsImportDeclaration().ImportClause
	return clause != nil && clause.AsImportClause().Name() == nil && len(refactorNamedImportSpecifiers(importDecl)) == 1
}

// refactorPlanImports plans binding `names` from one module in file. Names
// the file already imports from that module (same exported name and local
// alias) are dropped; the remaining names MERGE into an existing import
// clause from the same module when one can take them, appended at the end of
// its specifier list; whatever cannot merge is returned as new import
// statement text for the caller to insert at the import-insertion offset
// (refactorPrependImports).
//
// Merge rules (the README's documented convention):
//   - value names append to an existing value clause's named-import list, or
//     extend a default-only import (`import d from "m"` → `import d, { x } from "m"`);
//   - type-only names prefer an existing `import type { … }` clause, else
//     they join a value clause with an inline `type` modifier;
//   - namespace imports (`import * as ns`) and type-only clauses never take
//     value names, and a type-only default (`import type d from "m"`) takes
//     nothing — those cases fall through to a new statement.
//
// skip marks import declarations that other edits of the same plan delete
// entirely (never merged into, never satisfying a name). targetAbs matches
// existing imports by resolved module; when "" (package or unresolved
// specifiers) the specifier text is compared instead.
func refactorPlanImports(ws *core.Workspace, file *ast.SourceFile, targetAbs string, specText string, names []refactorImportName, skip map[*ast.Node]bool) (edits []icore.TextChange, newStmt string) {
	var candidates []*ast.Node
	if targetAbs != "" {
		candidates = refactorImportsResolvingTo(ws, file, targetAbs)
	} else {
		for _, stmt := range file.Statements.Nodes {
			if stmt.Kind != ast.KindImportDeclaration {
				continue
			}
			spec := stmt.AsImportDeclaration().ModuleSpecifier
			if spec != nil && ast.IsStringLiteral(spec) && spec.Text() == specText {
				candidates = append(candidates, stmt)
			}
		}
	}
	if len(skip) > 0 {
		kept := candidates[:0]
		for _, decl := range candidates {
			if !skip[decl] {
				kept = append(kept, decl)
			}
		}
		candidates = kept
	}

	// Drop names the module already provides under the same local alias (a
	// type-only need is satisfied by any existing binding; a value need only
	// by a value binding).
	provided := func(n refactorImportName) bool {
		for _, decl := range candidates {
			clauseTypeOnly := refactorImportClauseTypeOnly(decl)
			for _, spec := range refactorNamedImportSpecifiers(decl) {
				if refactorImportedName(spec) != n.name || spec.Name().Text() != n.local {
					continue
				}
				if n.typeOnly || (!clauseTypeOnly && !spec.AsImportSpecifier().IsTypeOnly) {
					return true
				}
			}
		}
		return false
	}
	var pending []refactorImportName
	for _, n := range names {
		if !provided(n) {
			pending = append(pending, n)
		}
	}
	if len(pending) == 0 {
		return nil, ""
	}

	// Pick merge targets: one `import type { … }` clause for type names, one
	// value clause (named list, or default-only) for everything else.
	var typeTarget, valueTarget *ast.Node
	for _, decl := range candidates {
		clauseNode := decl.AsImportDeclaration().ImportClause
		if clauseNode == nil {
			continue // side-effect import `import "m"`
		}
		clause := clauseNode.AsImportClause()
		named := clause.NamedBindings != nil && clause.NamedBindings.Kind == ast.KindNamedImports &&
			len(clause.NamedBindings.AsNamedImports().Elements.Nodes) > 0
		defaultOnly := clause.NamedBindings == nil && clause.Name() != nil
		if refactorImportClauseTypeOnly(decl) {
			if named && typeTarget == nil {
				typeTarget = decl
			}
		} else if (named || defaultOnly) && valueTarget == nil {
			valueTarget = decl
		}
	}

	var intoType, intoValue, leftover []refactorImportName
	for _, n := range pending {
		switch {
		case n.typeOnly && typeTarget != nil:
			intoType = append(intoType, n)
		case valueTarget != nil:
			intoValue = append(intoValue, n)
		default:
			leftover = append(leftover, n)
		}
	}
	appendToClause := func(decl *ast.Node, ns []refactorImportName, inValueClause bool) icore.TextChange {
		parts := make([]string, len(ns))
		for i, n := range ns {
			parts[i] = n.render(inValueClause)
		}
		if specs := refactorNamedImportSpecifiers(decl); len(specs) > 0 {
			last := specs[len(specs)-1]
			return icore.TextChange{TextRange: icore.NewTextRange(last.End(), last.End()), NewText: ", " + strings.Join(parts, ", ")}
		}
		// Default-only clause: extend with a named-import list.
		nameEnd := decl.AsImportDeclaration().ImportClause.AsImportClause().Name().End()
		return icore.TextChange{TextRange: icore.NewTextRange(nameEnd, nameEnd), NewText: ", { " + strings.Join(parts, ", ") + " }"}
	}
	if len(intoType) > 0 {
		edits = append(edits, appendToClause(typeTarget, intoType, false))
	}
	if len(intoValue) > 0 {
		edits = append(edits, appendToClause(valueTarget, intoValue, true))
	}
	if len(leftover) > 0 {
		newStmt = refactorRenderImportStatement(leftover, specText, refactorImportQuote(file))
	}
	return edits, newStmt
}

// refactorPrependImports renders the insertion of a block of new import
// statements at the file's import-insertion offset: below any shebang and
// directive prologue, glued to an existing leading import block, and — when
// no import follows at that offset — separated from the first following
// statement by exactly one blank line.
func refactorPrependImports(file *ast.SourceFile, block string) icore.TextChange {
	text := file.Text()
	pos := importInsertOffset(file)
	if pos > 0 && text[pos-1] != '\n' {
		// Directive (or shebang) without a trailing newline: keep it on its
		// own line.
		block = "\n" + block
	}
	if !editBlankLineAt(text, pos) && !refactorImportFollowsAt(file, pos) {
		block += "\n"
	}
	return icore.TextChange{TextRange: icore.NewTextRange(pos, pos), NewText: block}
}

// refactorImportFollowsAt reports whether the first statement ending at or
// after pos is an import declaration (i.e. an insertion at pos joins an
// existing import block).
func refactorImportFollowsAt(file *ast.SourceFile, pos int) bool {
	for _, stmt := range file.Statements.Nodes {
		if stmt.End() <= pos {
			continue
		}
		return stmt.Kind == ast.KindImportDeclaration
	}
	return false
}

// refactorCollapseBlankAfterDeletion widens a whole-line deletion range by one
// newline when removing it would leave two consecutive blank lines (a blank
// line both before and after the removed range).
func refactorCollapseBlankAfterDeletion(text string, r icore.TextRange) icore.TextRange {
	pos, end := r.Pos(), r.End()
	blankBefore := pos >= 2 && text[pos-1] == '\n' && text[pos-2] == '\n'
	blankAfter := end < len(text) && text[end] == '\n'
	if blankBefore && blankAfter {
		return icore.NewTextRange(pos, end+1)
	}
	return r
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
