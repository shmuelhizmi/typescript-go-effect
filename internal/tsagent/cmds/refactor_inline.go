package cmds

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// refactor inline (§4.4), v1: inline a const variable (initializer text
// substituted at every read site) or a simple function (single
// return-statement body, parameters substituted textually). The declaration
// is deleted; cross-file usages get their import specifier removed. Refused:
// write references, non-const variables, destructuring, functions with
// optional/rest/destructured params, `this`, recursion, non-call usages, and
// side-effectful arguments bound to parameters used != 1 times.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "inline",
		Summary:      "Inline a const variable or simple function at all usage sites",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorInlineFlags{}
			registerRefactorTargetFlags(fs, &f.target)
			registerRefactorTxFlags(fs, &f.tx)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorInline(ctx, ws, flags.(*refactorInlineFlags), args)
		},
	})
}

type refactorInlineFlags struct {
	target refactorTargetFlags
	tx     refactorTxFlags
}

func runRefactorInline(ctx context.Context, ws *core.Workspace, f *refactorInlineFlags, args []string) (*core.TxResult, error) {
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
		return nil, cli.RefusedErrorf("symbol has %d declarations (overloads/merged declarations cannot be inlined)", len(symbol.Declarations))
	}
	decl := symbol.Declarations[0]
	switch decl.Kind {
	case ast.KindVariableDeclaration:
		return refactorInlineVariable(ctx, ws, f, decl)
	case ast.KindFunctionDeclaration:
		return refactorInlineFunction(ctx, ws, f, decl)
	default:
		return nil, cli.RefusedErrorf("only const variables and function declarations can be inlined (target is a %s)", decl.Kind)
	}
}

// refactorInlineSites partitions the references of nameNode: read sites to
// rewrite, import/export specifiers, and the declaration's own name.
// Write references are returned for refusal.
func refactorInlineSites(ctx context.Context, ws *core.Workspace, nameNode *ast.Node, declRoot *ast.Node) (reads []*ast.Node, importSpecs []*ast.Node, writes []string, err error) {
	for _, ref := range refactorReferenceNodes(ctx, ws, nameNode) {
		if ref == nameNode {
			continue
		}
		file := ast.GetSourceFileOfNode(ref)
		if file == ast.GetSourceFileOfNode(declRoot) && ref.Pos() >= declRoot.Pos() && ref.End() <= declRoot.End() {
			continue // inside the declaration itself (e.g. recursion is caught separately)
		}
		parent := ref.Parent
		switch {
		case parent != nil && parent.Kind == ast.KindImportSpecifier:
			importSpecs = append(importSpecs, parent)
			continue
		case parent != nil && (parent.Kind == ast.KindExportSpecifier || parent.Kind == ast.KindImportClause || parent.Kind == ast.KindNamespaceImport):
			return nil, nil, nil, cli.RefusedErrorf("%q is re-exported or imported in a form that cannot be rewritten (at %s)",
				nameNode.Text(), refactorNodeLineCol(ws, ref))
		}
		if refactorIsWriteRef(ref) {
			writes = append(writes, refactorNodeLineCol(ws, ref))
			continue
		}
		reads = append(reads, ref)
	}
	return reads, importSpecs, writes, nil
}

// refactorIsWriteRef reports whether a reference node is an assignment
// target or increment/decrement operand.
func refactorIsWriteRef(ref *ast.Node) bool {
	parent := ref.Parent
	if parent == nil {
		return false
	}
	switch parent.Kind {
	case ast.KindBinaryExpression:
		bin := parent.AsBinaryExpression()
		if bin.Left != ref {
			return false
		}
		switch bin.OperatorToken.Kind {
		case ast.KindEqualsToken, ast.KindPlusEqualsToken, ast.KindMinusEqualsToken, ast.KindAsteriskEqualsToken,
			ast.KindAsteriskAsteriskEqualsToken, ast.KindSlashEqualsToken, ast.KindPercentEqualsToken,
			ast.KindLessThanLessThanEqualsToken, ast.KindGreaterThanGreaterThanEqualsToken,
			ast.KindGreaterThanGreaterThanGreaterThanEqualsToken, ast.KindAmpersandEqualsToken,
			ast.KindBarEqualsToken, ast.KindBarBarEqualsToken, ast.KindAmpersandAmpersandEqualsToken,
			ast.KindQuestionQuestionEqualsToken, ast.KindCaretEqualsToken:
			return true
		}
		return false
	case ast.KindPrefixUnaryExpression:
		op := parent.AsPrefixUnaryExpression().Operator
		return op == ast.KindPlusPlusToken || op == ast.KindMinusMinusToken
	case ast.KindPostfixUnaryExpression:
		op := parent.AsPostfixUnaryExpression().Operator
		return op == ast.KindPlusPlusToken || op == ast.KindMinusMinusToken
	}
	return false
}

// ---------------------------------------------------------------------------
// Variables

func refactorInlineVariable(ctx context.Context, ws *core.Workspace, f *refactorInlineFlags, decl *ast.Node) (*core.TxResult, error) {
	varDecl := decl.AsVariableDeclaration()
	nameNode := decl.Name()
	if nameNode == nil || nameNode.Kind != ast.KindIdentifier {
		return nil, cli.RefusedErrorf("destructured variable declarations cannot be inlined")
	}
	if varDecl.Initializer == nil {
		return nil, cli.RefusedErrorf("%q has no initializer", nameNode.Text())
	}
	list := decl.Parent
	if list == nil || list.Kind != ast.KindVariableDeclarationList || list.Flags&ast.NodeFlagsConst == 0 {
		return nil, cli.RefusedErrorf("only const variables can be inlined (let/var may be reassigned)")
	}
	declRoot := refactorDeletionNode(decl)
	file := ast.GetSourceFileOfNode(decl)
	initText := refactorNodeText(file, varDecl.Initializer)
	needsParens := !refactorIsSimpleExpression(varDecl.Initializer)

	reads, importSpecs, writes, err := refactorInlineSites(ctx, ws, nameNode, declRoot)
	if err != nil {
		return nil, err
	}
	if len(writes) > 0 {
		return nil, cli.RefusedErrorf("cannot inline %q: %d write reference(s):\n  %s", nameNode.Text(), len(writes), strings.Join(writes, "\n  "))
	}

	edits := make(map[string][]icore.TextChange)
	for _, ref := range reads {
		refFile := ast.GetSourceFileOfNode(ref)
		start := scanner.SkipTrivia(refFile.Text(), ref.Pos())
		newText := initText
		if needsParens {
			newText = "(" + initText + ")"
		}
		// Shorthand object properties need the explicit form: { x } -> { x: init }.
		if ref.Parent != nil && ref.Parent.Kind == ast.KindShorthandPropertyAssignment {
			newText = ref.Text() + ": " + newText
		}
		edits[refFile.FileName()] = append(edits[refFile.FileName()], icore.TextChange{
			TextRange: icore.NewTextRange(start, ref.End()),
			NewText:   newText,
		})
	}
	refactorInlineCleanupEdits(ws, edits, file, declRoot, importSpecs)
	return refactorInlineFinish(ctx, ws, f, edits, fmt.Sprintf("inlined const %s at %d site(s)", nameNode.Text(), len(reads)))
}

// ---------------------------------------------------------------------------
// Functions

func refactorInlineFunction(ctx context.Context, ws *core.Workspace, f *refactorInlineFlags, decl *ast.Node) (*core.TxResult, error) {
	fn := decl.AsFunctionDeclaration()
	nameNode := decl.Name()
	if nameNode == nil {
		return nil, cli.RefusedErrorf("anonymous functions cannot be inlined")
	}
	fnName := nameNode.Text()
	body := decl.Body()
	if body == nil {
		return nil, cli.RefusedErrorf("%q has no body (overload signature or ambient declaration)", fnName)
	}
	stmts := body.Statements()
	if len(stmts) != 1 || stmts[0].Kind != ast.KindReturnStatement || stmts[0].AsReturnStatement().Expression == nil {
		return nil, cli.RefusedErrorf("only simple functions (a single `return <expr>;` body) can be inlined")
	}
	returnExpr := stmts[0].AsReturnStatement().Expression
	if fn.AsteriskToken != nil {
		return nil, cli.RefusedErrorf("generators cannot be inlined")
	}

	// Parameter constraints: plain required identifiers only.
	var paramNames []string
	for _, param := range decl.Parameters() {
		p := param.AsParameterDeclaration()
		if p.DotDotDotToken != nil || p.QuestionToken != nil || p.Initializer != nil || param.Name().Kind != ast.KindIdentifier {
			return nil, cli.RefusedErrorf("only plain required identifier parameters are supported (no rest/optional/default/destructuring)")
		}
		paramNames = append(paramNames, param.Name().Text())
	}

	// Body constraints: no `this`, no recursion.
	file := ast.GetSourceFileOfNode(decl)
	bodyHasThis := false
	var checkThis func(n *ast.Node) bool
	checkThis = func(n *ast.Node) bool {
		if n.Kind == ast.KindThisKeyword {
			bodyHasThis = true
		}
		n.ForEachChild(checkThis)
		return false
	}
	returnExpr.ForEachChild(checkThis)
	if returnExpr.Kind == ast.KindThisKeyword || bodyHasThis {
		return nil, cli.RefusedErrorf("functions using `this` cannot be inlined")
	}
	for _, id := range refactorCollectIdentifiers(returnExpr) {
		if id.Text() == fnName {
			return nil, cli.RefusedErrorf("recursive functions cannot be inlined")
		}
	}

	// Parameter occurrences in the return expression (by name; shadowing
	// inside a one-expression body is treated as an occurrence, which only
	// makes the side-effect check stricter).
	paramUses := make(map[string][]*ast.Node)
	for _, id := range refactorCollectIdentifiers(returnExpr) {
		for _, p := range paramNames {
			if id.Text() == p {
				paramUses[p] = append(paramUses[p], id)
			}
		}
	}

	reads, importSpecs, writes, err := refactorInlineSites(ctx, ws, nameNode, decl)
	if err != nil {
		return nil, err
	}
	if len(writes) > 0 {
		return nil, cli.RefusedErrorf("cannot inline %q: %d write reference(s):\n  %s", fnName, len(writes), strings.Join(writes, "\n  "))
	}

	exprStart := scanner.SkipTrivia(file.Text(), returnExpr.Pos())
	exprText := file.Text()[exprStart:returnExpr.End()]

	edits := make(map[string][]icore.TextChange)
	for _, ref := range reads {
		call := ref.Parent
		if call == nil || call.Kind != ast.KindCallExpression || call.Expression() != ref {
			return nil, cli.RefusedErrorf("%q is used as a value (not a plain call) at %s; cannot inline", fnName, refactorNodeLineCol(ws, ref))
		}
		if call.AsCallExpression().QuestionDotToken != nil {
			return nil, cli.RefusedErrorf("optional-chained call at %s cannot be inlined", refactorNodeLineCol(ws, ref))
		}
		args := call.Arguments()
		if len(args) != len(paramNames) {
			return nil, cli.RefusedErrorf("call at %s passes %d argument(s) but %q has %d parameter(s)",
				refactorNodeLineCol(ws, ref), len(args), fnName, len(paramNames))
		}
		refFile := ast.GetSourceFileOfNode(ref)
		argTexts := make(map[string]string, len(args))
		for i, arg := range args {
			if arg.Kind == ast.KindSpreadElement {
				return nil, cli.RefusedErrorf("spread argument at %s cannot be inlined", refactorNodeLineCol(ws, ref))
			}
			uses := len(paramUses[paramNames[i]])
			if uses != 1 && !refactorIsSideEffectFree(arg) {
				return nil, cli.RefusedErrorf("argument %d at %s may have side effects and parameter %q is used %d times in the body; refusing to duplicate/drop its evaluation",
					i+1, refactorNodeLineCol(ws, ref), paramNames[i], uses)
			}
			text := refactorNodeText(refFile, arg)
			if !refactorIsSimpleExpression(arg) {
				text = "(" + text + ")"
			}
			argTexts[paramNames[i]] = text
		}

		// Substitute parameters into the return expression by splicing at
		// the recorded identifier offsets.
		var splices []refactorSplice
		for p, uses := range paramUses {
			for _, use := range uses {
				start := scanner.SkipTrivia(file.Text(), use.Pos())
				splices = append(splices, refactorSplice{pos: start - exprStart, end: use.End() - exprStart, text: argTexts[p]})
			}
		}
		inlined := refactorSpliceText(exprText, splices)
		if !refactorIsSimpleExpression(returnExpr) {
			inlined = "(" + inlined + ")"
		}
		start := scanner.SkipTrivia(refFile.Text(), call.Pos())
		edits[refFile.FileName()] = append(edits[refFile.FileName()], icore.TextChange{
			TextRange: icore.NewTextRange(start, call.End()),
			NewText:   inlined,
		})
	}
	refactorInlineCleanupEdits(ws, edits, file, decl, importSpecs)
	return refactorInlineFinish(ctx, ws, f, edits, fmt.Sprintf("inlined function %s at %d call site(s)", fnName, len(reads)))
}

// refactorSplice is one relative-offset text replacement.
type refactorSplice struct {
	pos, end int
	text     string
}

// refactorSpliceText applies non-overlapping replacements (relative offsets)
// to text, in descending position order.
func refactorSpliceText(text string, splices []refactorSplice) string {
	slices.SortFunc(splices, func(a, b refactorSplice) int { return b.pos - a.pos })
	for _, s := range splices {
		text = text[:s.pos] + s.text + text[s.end:]
	}
	return text
}

// refactorInlineCleanupEdits adds the declaration deletion and import
// specifier removals to the edit map.
func refactorInlineCleanupEdits(ws *core.Workspace, edits map[string][]icore.TextChange, declFile *ast.SourceFile, declRoot *ast.Node, importSpecs []*ast.Node) {
	edits[declFile.FileName()] = append(edits[declFile.FileName()], icore.TextChange{
		TextRange: refactorDeletionRange(declFile, declRoot),
		NewText:   "",
	})
	for _, spec := range importSpecs {
		specFile := ast.GetSourceFileOfNode(spec)
		importDecl := ast.FindAncestor(spec, ast.IsImportDeclaration)
		if importDecl == nil {
			continue
		}
		edits[specFile.FileName()] = append(edits[specFile.FileName()], refactorRemoveImportSpecifierEdit(specFile, importDecl, spec))
	}
}

func refactorInlineFinish(ctx context.Context, ws *core.Workspace, f *refactorInlineFlags, edits map[string][]icore.TextChange, note string) (*core.TxResult, error) {
	var es core.EditSet
	for fileName, changes := range edits {
		es.Edits = append(es.Edits, core.FileEdit{FileName: fileName, Edits: changes})
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, []string{note})
}
