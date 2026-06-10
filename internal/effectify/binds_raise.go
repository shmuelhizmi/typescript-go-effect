package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// yieldStarOf matches a `yield* e` expression and returns its operand.
func yieldStarOf(node *ast.Node) (*ast.Node, bool) {
	if node.Kind != ast.KindYieldExpression {
		return nil, false
	}
	y := node.AsYieldExpression()
	if y.AsteriskToken == nil || y.Expression == nil {
		return nil, false
	}
	return y.Expression, true
}

// tryBindStatement matches pattern #6 (inside effect bodies):
//
//	const x = yield* e;        →  x <- e';
//	const {p} = yield* e;      →  {p} <- e';
//	const x: T = yield* e;     →  x: T <- e';
func (r *rewriter) tryBindStatement(node *ast.Node) (string, bool) {
	vs := node.AsVariableStatement()
	if vs.Modifiers() != nil {
		return "", false
	}
	declList := vs.DeclarationList.AsVariableDeclarationList()
	if declList.AsNode().Flags&ast.NodeFlagsConst == 0 || len(declList.Declarations.Nodes) != 1 {
		return "", false
	}
	decl := declList.Declarations.Nodes[0]
	vd := decl.AsVariableDeclaration()
	name := decl.Name()
	if name == nil || vd.Initializer == nil || vd.ExclamationToken != nil {
		return "", false
	}
	operand, ok := yieldStarOf(vd.Initializer)
	if !ok {
		return "", false
	}
	// A destructuring bind starts the statement with '{'/'[' — only safe when
	// the previous statement can't fuse with it under ASI re-parse rules.
	if name.Kind != ast.KindIdentifier && !r.prevStatementTerminated(node) {
		return "", false
	}
	if name.Kind == ast.KindIdentifier && effectKeywordIdents[name.Text()] {
		return "", false
	}
	// In `x: T <- e` the forward parser parses T with parseType, where a
	// trailing type reference treats the following `<` as a type-argument
	// list (`T<-1>` is legal). A type whose rightmost token is an identifier
	// therefore swallows the bind arrow — keep those statements verbatim.
	if vd.Type != nil && typeEndsWithIdentifier(vd.Type) {
		return "", false
	}

	var b strings.Builder
	b.WriteString(r.emit(name))
	if vd.Type != nil {
		b.WriteString(": ")
		b.WriteString(r.emitChildren(vd.Type))
	}
	b.WriteString(" <- ")
	b.WriteString(r.emit(operand))
	b.WriteString(";")
	r.stats.count("bind")
	return b.String(), true
}

// typeEndsWithIdentifier reports whether the rightmost leaf of a type node is
// an identifier — the position where a following `<` re-parses as the start
// of a type-argument list.
func typeEndsWithIdentifier(node *ast.Node) bool {
	for {
		var last *ast.Node
		node.ForEachChild(func(child *ast.Node) bool {
			last = child
			return false
		})
		if last == nil {
			return node.Kind == ast.KindIdentifier
		}
		node = last
	}
}

// tryDiscardBind matches pattern #7 (inside effect bodies):
//
//	yield* e;  →  <- e';
func (r *rewriter) tryDiscardBind(node *ast.Node) (string, bool) {
	operand, ok := yieldStarOf(node.AsExpressionStatement().Expression)
	if !ok {
		return "", false
	}
	// `<- e;` starts with '<', which is hazardous after more than just
	// unterminated statements: the forward parser re-scans any balanced
	// `{...}` block at statement position followed by `<-` as a destructuring
	// bind pattern (`if (c) { return; }` + `<- e;` fuses into
	// `{ return; } <- e`), and an unterminated expression statement absorbs
	// `<` as a relational operator. Only a `;`-terminated previous sibling is
	// immune to both.
	if !r.prevStatementEndsWithSemicolon(node) {
		return "", false
	}
	r.stats.count("discard-bind")
	return "<- " + r.emit(operand) + ";", true
}

// tryBindExpression matches pattern #8 in its parenthesized form:
//
//	(yield* e)  →  (<- e')
func (r *rewriter) tryBindExpression(node *ast.Node) (string, bool) {
	operand, ok := yieldStarOf(node.AsParenthesizedExpression().Expression)
	if !ok {
		return "", false
	}
	r.stats.count("bind-expression")
	return "(<- " + r.emit(operand) + ")", true
}

// tryBareYieldStarExpression matches pattern #8 for a yield* used as an
// expression without parentheses (the parenthesized and statement forms are
// handled first by their parents):
//
//	yield* e  →  (<- e')
func (r *rewriter) tryBareYieldStarExpression(node *ast.Node) (string, bool) {
	operand, ok := yieldStarOf(node)
	if !ok {
		return "", false
	}
	if keyword, raiseOperand, isRaise := r.raiseCallOf(operand); isRaise {
		text := r.emit(raiseOperand)
		if !r.unarySafeEmit(raiseOperand) {
			text = "(" + text + ")"
		}
		r.stats.count("raise-expression")
		return "(" + keyword + " " + text + ")", true
	}
	r.stats.count("bind-expression")
	return "(<- " + r.emit(operand) + ")", true
}

// raiseCallOf matches `Effect.fail(e)` / `Effect.die(e)` and returns the
// raise keyword spelling and operand.
func (r *rewriter) raiseCallOf(node *ast.Node) (keyword string, operand *ast.Node, ok bool) {
	helper, method, args, isCall := r.helperCall(node)
	if !isCall || helper != "Effect" || len(args) != 1 {
		return "", nil, false
	}
	switch method {
	case "fail":
		return "raise", args[0], true
	case "die":
		return "raise.die", args[0], true
	}
	return "", nil, false
}

// tryRaiseStatement matches patterns #9/#10 (inside effect bodies):
//
//	return yield* Effect.fail(e);  →  raise e';
//	return yield* Effect.die(e);   →  raise.die e';
//
// The exact `return yield*` shape is required — it is what `raise` desugars
// to, so the conversion round-trips; a bare `yield* Effect.fail(e);` is left
// to the discard-bind rule.
func (r *rewriter) tryRaiseStatement(node *ast.Node) (string, bool) {
	ret := node.AsReturnStatement()
	if ret.Expression == nil {
		return "", false
	}
	inner, ok := yieldStarOf(ret.Expression)
	if !ok {
		return "", false
	}
	keyword, operand, ok := r.raiseCallOf(inner)
	if !ok {
		return "", false
	}
	r.stats.count("raise")
	return keyword + " " + r.emit(operand) + ";", true
}

// tryRaiseExpression matches pattern #11 (inside effect bodies):
//
//	(yield* Effect.fail(e))  →  (raise e')
//
// The raise-expression operand parses at unary precedence, so anything wider
// gets wrapping parentheses.
func (r *rewriter) tryRaiseExpression(node *ast.Node) (string, bool) {
	inner, ok := yieldStarOf(node.AsParenthesizedExpression().Expression)
	if !ok {
		return "", false
	}
	keyword, operand, ok := r.raiseCallOf(inner)
	if !ok {
		return "", false
	}
	text := r.emit(operand)
	if !r.unarySafeEmit(operand) {
		text = "(" + text + ")"
	}
	r.stats.count("raise-expression")
	return "(" + keyword + " " + text + ")", true
}
