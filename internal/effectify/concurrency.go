package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// tryConcurrency matches pattern #17 (inside effect bodies):
//
//	Effect.fork(e)                                  → fork e'
//	Fiber.join(f)                                   → join f'
//	Effect.all([…], { concurrency: "unbounded" })   → par […]
//	Effect.all({…}, { concurrency: "unbounded" })   → par {…}
//	Effect.all(coll, { concurrency: n })            → par(n) coll'
//	Effect.race(a, b)                               → race [a', b']
//	Effect.raceAll([a, b, c, …])                    → race [a', b', c', …]   (≥3 only)
//
// The keyword forms bind their operand at unary precedence and cannot take
// postfix member/call accessors, so a chain like Effect.fork(e).pipe(…) is
// left alone.
func (r *rewriter) tryConcurrency(node *ast.Node) (string, bool) {
	if isMemberOrCalleeBase(node) {
		return "", false
	}
	helper, method, args, ok := r.helperCall(node)
	if !ok {
		return "", false
	}
	switch {
	case helper == "Effect" && method == "fork" && len(args) == 1:
		r.stats.count("fork")
		return "fork " + r.unaryOperandText(args[0]), true
	case helper == "Fiber" && method == "join" && len(args) == 1:
		r.stats.count("join")
		return "join " + r.unaryOperandText(args[0]), true
	case helper == "Effect" && method == "all" && len(args) == 2:
		return r.tryPar(args[0], args[1])
	case helper == "Effect" && method == "race" && len(args) == 2:
		r.stats.count("race")
		return "race [" + r.emit(args[0]) + ", " + r.emit(args[1]) + "]", true
	case helper == "Effect" && method == "raceAll" && len(args) == 1 && args[0].Kind == ast.KindArrayLiteralExpression:
		elements := args[0].AsArrayLiteralExpression().Elements.Nodes
		if len(elements) < 3 || hasSpread(elements) {
			// race [a, b] desugars to Effect.race(a, b), so a 2-element
			// raceAll would not round-trip.
			return "", false
		}
		r.stats.count("race")
		return "race " + r.emitChildren(args[0]), true
	}
	return "", false
}

func (r *rewriter) tryPar(collection *ast.Node, options *ast.Node) (string, bool) {
	if collection.Kind != ast.KindArrayLiteralExpression && collection.Kind != ast.KindObjectLiteralExpression {
		return "", false
	}
	if options.Kind != ast.KindObjectLiteralExpression {
		return "", false
	}
	props := options.AsObjectLiteralExpression().Properties.Nodes
	if len(props) != 1 || props[0].Kind != ast.KindPropertyAssignment {
		return "", false
	}
	name := props[0].Name()
	if name == nil || name.Kind != ast.KindIdentifier || name.Text() != "concurrency" {
		return "", false
	}
	concurrency := props[0].AsPropertyAssignment().Initializer

	var b strings.Builder
	b.WriteString("par")
	if concurrency.Kind != ast.KindStringLiteral || concurrency.Text() != "unbounded" {
		b.WriteString("(")
		b.WriteString(r.emit(concurrency))
		b.WriteString(")")
	}
	b.WriteString(" ")
	b.WriteString(r.emitChildren(collection))
	r.stats.count("par")
	return b.String(), true
}

// unaryOperandText emits a fork/join operand; the re-parse requires the
// operand to start with an identifier, `this`, `new` or `(`, so anything
// else is parenthesized (the extra parens are transparent to verification).
func (r *rewriter) unaryOperandText(arg *ast.Node) string {
	text := r.emit(arg)
	if text != "" {
		c := text[0]
		if c == '(' || c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if r.unarySafeEmit(arg) {
				return text
			}
		}
	}
	return "(" + text + ")"
}

// unarySafeEmit reports whether the EMITTED text of arg can stand as a
// unary-precedence operand. unarySafe judges the original node, but a node
// rewritten into a |> chain has the low-precedence pipeline operator at its
// top level regardless of its original kind.
func (r *rewriter) unarySafeEmit(arg *ast.Node) bool {
	if !unarySafe(arg) {
		return false
	}
	if _, _, isChain := r.pipeChainParts(arg); isChain {
		return false
	}
	return true
}

// isMemberOrCalleeBase reports whether node is the base of a member access or
// the callee of a call/new — positions where replacing it with a unary
// keyword form would re-associate the postfix operators into the operand.
func isMemberOrCalleeBase(node *ast.Node) bool {
	parent := node.Parent
	if parent == nil {
		return false
	}
	switch parent.Kind {
	case ast.KindPropertyAccessExpression:
		return parent.AsPropertyAccessExpression().Expression == node
	case ast.KindElementAccessExpression:
		return parent.AsElementAccessExpression().Expression == node
	case ast.KindCallExpression:
		return parent.AsCallExpression().Expression == node
	case ast.KindNewExpression:
		return parent.AsNewExpression().Expression == node
	case ast.KindNonNullExpression, ast.KindTaggedTemplateExpression:
		return true
	}
	return false
}

func hasSpread(elements []*ast.Node) bool {
	for _, el := range elements {
		if el.Kind == ast.KindSpreadElement {
			return true
		}
	}
	return false
}
