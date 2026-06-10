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
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// refactor extract (§4.4), v1: extract a constant (the range must be exactly
// one expression) or a function (the range must cover whole statements;
// parameters and async-ness are computed, one output variable may be
// returned). Refused: ranges that are not exact nodes, `return` inside the
// range, more than one output variable, and writes to outer variables.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "extract",
		Summary:      "Extract an expression to a constant or statements to a function",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorExtractFlags{}
			registerRefactorTxFlags(fs, &f.tx)
			fs.StringVar(&f.rangeSpec, "range", "", "source range file:startLine:startCol-endLine:endCol (1-based, end-exclusive column)")
			fs.StringVar(&f.into, "into", "", "what to extract: function or constant")
			fs.StringVar(&f.name, "name", "", "name for the extracted function/constant")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorExtract(ctx, ws, flags.(*refactorExtractFlags), args)
		},
	})
}

type refactorExtractFlags struct {
	tx        refactorTxFlags
	rangeSpec string
	into      string
	name      string
}

func runRefactorExtract(ctx context.Context, ws *core.Workspace, f *refactorExtractFlags, args []string) (*core.TxResult, error) {
	if len(args) != 0 {
		return nil, cli.UsageErrorf("refactor extract takes no positional arguments (use --range)")
	}
	if f.into != "function" && f.into != "constant" {
		return nil, cli.UsageErrorf("--into must be function or constant")
	}
	if f.name == "" {
		return nil, cli.UsageErrorf("--name is required")
	}
	file, start, end, err := refactorParseRange(ws, f.rangeSpec)
	if err != nil {
		return nil, err
	}
	// Trim whitespace tolerance.
	text := file.Text()
	for start < end && (text[start] == ' ' || text[start] == '\t' || text[start] == '\n' || text[start] == '\r') {
		start++
	}
	for end > start && (text[end-1] == ' ' || text[end-1] == '\t' || text[end-1] == '\n' || text[end-1] == '\r') {
		end--
	}
	if start >= end {
		return nil, cli.UsageErrorf("--range is empty")
	}
	if f.into == "constant" {
		return refactorExtractConstant(ctx, ws, f, file, start, end)
	}
	return refactorExtractFunction(ctx, ws, f, file, start, end)
}

// refactorParseRange parses file:startLine:startCol-endLine:endCol into byte
// offsets.
func refactorParseRange(ws *core.Workspace, spec string) (*ast.SourceFile, int, int, error) {
	if spec == "" {
		return nil, 0, 0, cli.UsageErrorf("--range is required")
	}
	dash := strings.LastIndexByte(spec, '-')
	if dash <= 0 {
		return nil, 0, 0, cli.UsageErrorf("malformed --range %q (want file:startLine:startCol-endLine:endCol)", spec)
	}
	fileName, startLine, startCol, err := core.ParsePosition(spec[:dash])
	if err != nil {
		return nil, 0, 0, cli.UsageErrorf("malformed --range %q: %v", spec, err)
	}
	endParts := strings.Split(spec[dash+1:], ":")
	if len(endParts) != 2 {
		return nil, 0, 0, cli.UsageErrorf("malformed --range %q (want file:startLine:startCol-endLine:endCol)", spec)
	}
	var endLine, endCol int
	if _, err := fmt.Sscanf(endParts[0], "%d", &endLine); err != nil {
		return nil, 0, 0, cli.UsageErrorf("malformed --range %q", spec)
	}
	if _, err := fmt.Sscanf(endParts[1], "%d", &endCol); err != nil {
		return nil, 0, 0, cli.UsageErrorf("malformed --range %q", spec)
	}
	file, err := ws.FileOf(fileName)
	if err != nil {
		return nil, 0, 0, err
	}
	start, err := ws.LineColToPos(file, startLine, startCol)
	if err != nil {
		return nil, 0, 0, err
	}
	end, err := ws.LineColToPos(file, endLine, endCol)
	if err != nil {
		return nil, 0, 0, err
	}
	if end < start {
		return nil, 0, 0, cli.UsageErrorf("--range end precedes its start")
	}
	return file, start, end, nil
}

// refactorNodeStart is a node's position with leading trivia skipped.
func refactorNodeStart(file *ast.SourceFile, node *ast.Node) int {
	return scanner.SkipTrivia(file.Text(), node.Pos())
}

// ---------------------------------------------------------------------------
// --into constant

func refactorExtractConstant(ctx context.Context, ws *core.Workspace, f *refactorExtractFlags, file *ast.SourceFile, start int, end int) (*core.TxResult, error) {
	// Lowest node spanning exactly the trimmed range.
	node := astnav.GetTouchingToken(file, start)
	for node != nil && !(refactorNodeStart(file, node) == start && node.End() == end) {
		if node.End() > end && refactorNodeStart(file, node) < start {
			node = nil
			break
		}
		node = node.Parent
	}
	if node == nil {
		return nil, cli.RefusedErrorf("the range is not exactly one AST node; adjust it to cover a whole expression")
	}
	if !ast.IsExpressionNode(node) {
		return nil, cli.RefusedErrorf("the range covers a %s, not an expression", node.Kind)
	}

	stmt := refactorEnclosingStatement(node)
	if stmt == nil {
		return nil, cli.RefusedErrorf("cannot find an enclosing statement to insert the constant before")
	}
	text := file.Text()
	stmtStart := refactorNodeStart(file, stmt)
	indent := refactorLineIndent(file, stmtStart)
	lineStart := stmtStart - len(indent)

	exprText := text[start:end]
	es := core.EditSet{Edits: []core.FileEdit{{
		FileName: file.FileName(),
		Edits: []icore.TextChange{
			{TextRange: icore.NewTextRange(lineStart, lineStart), NewText: indent + "const " + f.name + " = " + exprText + ";\n"},
			{TextRange: icore.NewTextRange(start, end), NewText: f.name},
		},
	}}}
	return finishRefactorTx(ctx, ws, es, &f.tx, nil)
}

// refactorEnclosingStatement ascends to the statement directly inside a
// block, module block, source file, or case clause.
func refactorEnclosingStatement(node *ast.Node) *ast.Node {
	for n := node; n != nil; n = n.Parent {
		p := n.Parent
		if p == nil {
			return nil
		}
		switch p.Kind {
		case ast.KindBlock, ast.KindModuleBlock, ast.KindSourceFile, ast.KindCaseClause, ast.KindDefaultClause:
			return n
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// --into function

func refactorExtractFunction(ctx context.Context, ws *core.Workspace, f *refactorExtractFlags, file *ast.SourceFile, start int, end int) (*core.TxResult, error) {
	text := file.Text()

	// The range must cover one or more complete sibling statements.
	first := astnav.GetTouchingToken(file, start)
	for first != nil && refactorNodeStart(file, first) != start {
		first = first.Parent
	}
	for first != nil && !refactorIsStatementContext(first.Parent) {
		next := first.Parent
		if next != nil && refactorNodeStart(file, next) != start {
			first = nil
			break
		}
		first = next
	}
	if first == nil {
		return nil, cli.RefusedErrorf("the range must start at the beginning of a statement")
	}
	container := first.Parent
	containerStmts := refactorStatementsOf(container)
	idx := slices.Index(containerStmts, first)
	if idx < 0 {
		return nil, cli.RefusedErrorf("the range must start at the beginning of a statement")
	}
	var stmts []*ast.Node
	for i := idx; i < len(containerStmts); i++ {
		stmts = append(stmts, containerStmts[i])
		if containerStmts[i].End() == end {
			break
		}
		if containerStmts[i].End() > end {
			return nil, cli.RefusedErrorf("the range must end at a statement boundary")
		}
	}
	if stmts[len(stmts)-1].End() != end {
		return nil, cli.RefusedErrorf("the range must end at a statement boundary")
	}

	// v1 constraints: no `return` in the range (outside nested functions).
	hasReturn, hasAwait := refactorScanRangeControl(stmts)
	if hasReturn {
		return nil, cli.RefusedErrorf("the range contains a return statement; extracting it would change control flow (not supported yet)")
	}

	enclosingFn := ast.GetContainingFunction(first)
	checker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()

	declaredInsideRange := func(sym *ast.Symbol) bool {
		for _, d := range sym.Declarations {
			if ast.GetSourceFileOfNode(d) == file && refactorNodeStart(file, d) >= start && d.End() <= end {
				return true
			}
		}
		return false
	}

	// Inputs: identifiers used in the range whose declaration lives in the
	// enclosing function but outside the range.
	type param struct {
		name string
		typ  string
	}
	var params []param
	seenParam := make(map[*ast.Symbol]bool)
	for _, stmt := range stmts {
		for _, id := range refactorCollectIdentifiers(stmt) {
			sym := checker.GetSymbolAtLocation(id)
			if sym == nil || len(sym.Declarations) == 0 || seenParam[sym] || declaredInsideRange(sym) {
				continue
			}
			if enclosingFn == nil {
				continue // module scope stays visible to the extracted function
			}
			declInFn := false
			localKind := false
			for _, d := range sym.Declarations {
				if ast.GetSourceFileOfNode(d) == file && d.Pos() >= enclosingFn.Pos() && d.End() <= enclosingFn.End() {
					declInFn = true
				}
				switch d.Kind {
				case ast.KindVariableDeclaration, ast.KindParameter, ast.KindBindingElement:
					localKind = true
				}
			}
			if !declInFn || !localKind {
				continue
			}
			if refactorIsWriteRef(id) {
				return nil, cli.RefusedErrorf("the range writes to %q, which is declared outside it; extracting would change behavior", id.Text())
			}
			seenParam[sym] = true
			params = append(params, param{name: id.Text(), typ: checker.TypeToString(checker.GetTypeOfSymbolAtLocation(sym, id))})
		}
	}

	// Outputs: variables declared in the range and used after it.
	var outputs []string
	outputKeyword := "const"
	seenOut := make(map[*ast.Symbol]bool)
	scanAfter := refactorIdentifiersAfter(file, enclosingFn, end)
	for _, id := range scanAfter {
		sym := checker.GetSymbolAtLocation(id)
		if sym == nil || seenOut[sym] || !declaredInsideRange(sym) {
			continue
		}
		seenOut[sym] = true
		outputs = append(outputs, sym.Name)
		for _, d := range sym.Declarations {
			if d.Kind == ast.KindVariableDeclaration && d.Parent != nil && d.Parent.Kind == ast.KindVariableDeclarationList &&
				d.Parent.Flags&ast.NodeFlagsConst == 0 {
				outputKeyword = "let"
			}
		}
	}
	if len(outputs) > 1 {
		return nil, cli.RefusedErrorf("the range declares %d variables used after it (%s); extract supports at most one output",
			len(outputs), strings.Join(outputs, ", "))
	}

	// Render the new function.
	paramTexts := make([]string, len(params))
	argTexts := make([]string, len(params))
	for i, p := range params {
		paramTexts[i] = p.name + ": " + p.typ
		argTexts[i] = p.name
	}
	bodyText := text[start:end]
	asyncPrefix := ""
	awaitPrefix := ""
	if hasAwait {
		asyncPrefix = "async "
		awaitPrefix = "await "
	}
	var fnBody strings.Builder
	fnBody.WriteString("\n" + asyncPrefix + "function " + f.name + "(" + strings.Join(paramTexts, ", ") + ") {\n")
	fnBody.WriteString("\t" + strings.ReplaceAll(bodyText, "\n", "\n\t"))
	if len(outputs) == 1 {
		fnBody.WriteString("\n\treturn " + outputs[0] + ";")
	}
	fnBody.WriteString("\n}\n")

	// Insert after the enclosing function, or at the end of the file.
	insertPos := len(text)
	if enclosingFn != nil {
		insertPos = enclosingFn.End()
		// Land after the statement that contains the function (e.g. a
		// variable statement holding an arrow function).
		if stmt := refactorEnclosingStatement(enclosingFn); stmt != nil && stmt.End() > insertPos {
			insertPos = stmt.End()
		}
	}

	// Replace the range with the call.
	call := awaitPrefix + f.name + "(" + strings.Join(argTexts, ", ") + ");"
	if len(outputs) == 1 {
		call = outputKeyword + " " + outputs[0] + " = " + awaitPrefix + f.name + "(" + strings.Join(argTexts, ", ") + ");"
	}

	es := core.EditSet{Edits: []core.FileEdit{{
		FileName: file.FileName(),
		Edits: []icore.TextChange{
			{TextRange: icore.NewTextRange(start, end), NewText: call},
			{TextRange: icore.NewTextRange(insertPos, insertPos), NewText: fnBody.String()},
		},
	}}}
	notes := []string{fmt.Sprintf("extracted %d statement(s) into %s(%s)", len(stmts), f.name, strings.Join(paramTexts, ", "))}
	return finishRefactorTx(ctx, ws, es, &f.tx, notes)
}

// refactorIsStatementContext reports whether a node can directly hold
// statements.
func refactorIsStatementContext(node *ast.Node) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case ast.KindBlock, ast.KindModuleBlock, ast.KindSourceFile, ast.KindCaseClause, ast.KindDefaultClause:
		return true
	}
	return false
}

// refactorStatementsOf returns the statement list of a statement container.
func refactorStatementsOf(container *ast.Node) []*ast.Node {
	switch container.Kind {
	case ast.KindSourceFile:
		return container.AsSourceFile().Statements.Nodes
	case ast.KindCaseClause, ast.KindDefaultClause:
		return container.AsCaseOrDefaultClause().Statements.Nodes
	default:
		return container.Statements()
	}
}

// refactorScanRangeControl detects return statements and await expressions
// within the statements, not descending into nested functions.
func refactorScanRangeControl(stmts []*ast.Node) (hasReturn bool, hasAwait bool) {
	var visit func(n *ast.Node) bool
	visit = func(n *ast.Node) bool {
		if ast.IsFunctionLike(n) {
			return false
		}
		switch n.Kind {
		case ast.KindReturnStatement:
			hasReturn = true
		case ast.KindAwaitExpression, ast.KindForOfStatement:
			if n.Kind == ast.KindAwaitExpression {
				hasAwait = true
			} else if n.AsForInOrOfStatement().AwaitModifier != nil {
				hasAwait = true
			}
		}
		n.ForEachChild(visit)
		return false
	}
	for _, stmt := range stmts {
		visit(stmt)
	}
	return hasReturn, hasAwait
}

// refactorIdentifiersAfter returns the identifiers of scope (the enclosing
// function, or the whole file) positioned after pos.
func refactorIdentifiersAfter(file *ast.SourceFile, enclosingFn *ast.Node, pos int) []*ast.Node {
	root := file.AsNode()
	if enclosingFn != nil {
		root = enclosingFn
	}
	var out []*ast.Node
	for _, id := range refactorCollectIdentifiers(root) {
		if id.Pos() >= pos {
			out = append(out, id)
		}
	}
	return out
}
