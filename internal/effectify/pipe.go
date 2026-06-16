package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// tryPipeChain matches patterns #18/#19:
//
//	pipe(a, f, g)     → a' |> f' |> g'      (tracked free `pipe` import)
//	recv.pipe(f, g)   → recv' |> f' |> g'   (every stage rooted at a tracked helper)
//
// The method form's per-stage guard keeps RxJS-style `.pipe` chains intact.
func (r *rewriter) tryPipeChain(node *ast.Node) (string, bool) {
	head, stages, ok := r.pipeChainParts(node)
	if !ok {
		return "", false
	}

	var b strings.Builder
	b.WriteString(r.pipeOperandText(head))
	for _, stage := range stages {
		b.WriteString(" |> ")
		b.WriteString(r.pipeOperandText(stage))
	}
	out := b.String()
	if !pipeSafePosition(node) {
		out = "(" + out + ")"
	}
	r.stats.count("pipeline")
	return out, true
}

// pipeChainParts reports whether tryPipeChain will rewrite this node into a
// |> chain, returning the head operand and stages. Consumers that emit the
// node at unary-operand precedence (fork/join/raise expressions) use this to
// know the emission's top-level operator is the low-precedence |>.
func (r *rewriter) pipeChainParts(node *ast.Node) (head *ast.Node, stages []*ast.Node, ok bool) {
	if !r.convertPipes {
		return nil, nil, false
	}
	if call, isMethod := pipeMethodCall(node); isMethod {
		if len(call.Arguments.Nodes) == 0 {
			return nil, nil, false
		}
		for _, arg := range call.Arguments.Nodes {
			if !r.rootedAtHelper(arg) {
				return nil, nil, false
			}
		}
		head = call.Expression.AsPropertyAccessExpression().Expression
		stages = call.Arguments.Nodes
	} else if r.bound["pipe"] && node.Kind == ast.KindCallExpression {
		call := node.AsCallExpression()
		if call.QuestionDotToken != nil || call.TypeArguments != nil ||
			call.Expression.Kind != ast.KindIdentifier || call.Expression.Text() != "pipe" ||
			len(call.Arguments.Nodes) < 2 {
			return nil, nil, false
		}
		head = call.Arguments.Nodes[0]
		stages = call.Arguments.Nodes[1:]
	} else {
		return nil, nil, false
	}
	if hasSpread(append([]*ast.Node{head}, stages...)) {
		return nil, nil, false
	}
	return head, stages, true
}

// rootedAtHelper reports whether an expression is a call/property chain whose
// leftmost name is a tracked helper namespace (Effect.retry(p), Layer.provide(x)).
func (r *rewriter) rootedAtHelper(node *ast.Node) bool {
	for {
		switch node.Kind {
		case ast.KindCallExpression:
			node = node.AsCallExpression().Expression
		case ast.KindPropertyAccessExpression:
			node = node.AsPropertyAccessExpression().Expression
		case ast.KindIdentifier:
			if _, ok := r.localToCanonical[node.Text()]; ok {
				return true
			}
			return r.barrelRoots[node.Text()]
		default:
			return false
		}
	}
}

// pipeOperandText emits one |> operand. Operands bind tighter than |>, so
// anything at or below conditional/arrow/binary level needs parentheses.
func (r *rewriter) pipeOperandText(node *ast.Node) string {
	text := r.emit(node)
	switch node.Kind {
	case ast.KindIdentifier, ast.KindCallExpression, ast.KindPropertyAccessExpression,
		ast.KindElementAccessExpression, ast.KindNewExpression, ast.KindParenthesizedExpression,
		ast.KindNonNullExpression, ast.KindStringLiteral, ast.KindNumericLiteral,
		ast.KindNoSubstitutionTemplateLiteral, ast.KindTemplateExpression,
		ast.KindArrayLiteralExpression, ast.KindThisKeyword:
		return text
	}
	return "(" + text + ")"
}

// pipeSafePosition reports whether a chain can be emitted bare: |> sits below
// member/call/unary precedence, so in tighter contexts the whole chain is
// parenthesized.
func pipeSafePosition(node *ast.Node) bool {
	parent := node.Parent
	if parent == nil {
		return true
	}
	switch parent.Kind {
	case ast.KindExpressionStatement, ast.KindReturnStatement, ast.KindParenthesizedExpression,
		ast.KindVariableDeclaration, ast.KindPropertyAssignment, ast.KindArrayLiteralExpression,
		ast.KindArrowFunction, ast.KindYieldExpression:
		return true
	case ast.KindCallExpression:
		// Safe as an argument, not as the callee.
		return parent.AsCallExpression().Expression != node
	case ast.KindConditionalExpression:
		// |> binds tighter than `?:`.
		return true
	}
	return false
}
