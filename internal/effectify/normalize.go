package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
)

// flattenPipes rewrites every `pipe(a, f, g)` and `recv.pipe(f, g)` call in a
// TypeScript text into nested calls `g(f(a))`. It is the canonicalization
// used by the round-trip verifier: the `|>` operator desugars to nested
// calls, so both sides of the comparison are flattened uniformly (by name,
// without import tracking — what matters is that the same canonical form is
// applied to both).
func flattenPipes(fileName string, src string) string {
	file := parseFile(fileName, src, core.ScriptKindTS)
	f := &pipeFlattener{rewriter{src: src, file: file}}
	return f.emit(file.AsNode())
}

type pipeFlattener struct {
	rewriter
}

func (f *pipeFlattener) emit(node *ast.Node) string {
	if out, ok := f.tryFlatten(node); ok {
		return out
	}
	return f.emitChildrenWith(node, f.emit)
}

func (f *pipeFlattener) tryFlatten(node *ast.Node) (string, bool) {
	if node.Kind != ast.KindCallExpression {
		return "", false
	}
	call := node.AsCallExpression()
	if call.QuestionDotToken != nil || call.TypeArguments != nil {
		return "", false
	}

	var head *ast.Node
	var stages []*ast.Node
	if pm, ok := pipeMethodCall(node); ok && len(pm.Arguments.Nodes) > 0 {
		head = pm.Expression.AsPropertyAccessExpression().Expression
		stages = pm.Arguments.Nodes
	} else if call.Expression.Kind == ast.KindIdentifier && call.Expression.Text() == "pipe" && len(call.Arguments.Nodes) >= 2 {
		head = call.Arguments.Nodes[0]
		stages = call.Arguments.Nodes[1:]
	} else {
		return "", false
	}
	if hasSpread(append([]*ast.Node{head}, stages...)) {
		return "", false
	}

	out := f.parenthesizedOperand(head)
	for _, stage := range stages {
		out = f.parenthesizedOperand(stage) + "(" + out + ")"
	}
	return out, true
}

func (f *pipeFlattener) parenthesizedOperand(node *ast.Node) string {
	text := f.emit(node)
	switch node.Kind {
	case ast.KindIdentifier, ast.KindCallExpression, ast.KindPropertyAccessExpression,
		ast.KindElementAccessExpression, ast.KindParenthesizedExpression, ast.KindNonNullExpression:
		return text
	}
	return "(" + text + ")"
}

// emitChildrenWith is emitChildren parameterized over the recursion target so
// the flattener reuses the span-splicing machinery with its own dispatch.
func (r *rewriter) emitChildrenWith(node *ast.Node, emit func(*ast.Node) string) string {
	begin := r.start(node)
	end := node.End()
	if node.Kind == ast.KindSourceFile {
		begin = 0
		end = len(r.src)
	}
	var b strings.Builder
	cursor := begin
	ok := true
	node.ForEachChild(func(child *ast.Node) bool {
		if child == nil || child.Pos() < 0 || child.End() > end {
			ok = false
			return true
		}
		cs := r.start(child)
		if cs < cursor || child.End() < cs {
			ok = false
			return true
		}
		b.WriteString(r.src[cursor:cs])
		b.WriteString(emit(child))
		cursor = child.End()
		return false
	})
	if !ok {
		return r.src[begin:end]
	}
	b.WriteString(r.src[cursor:end])
	return b.String()
}
