package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// tryUsingBind matches pattern #14 (inside effect bodies):
//
//	const c = yield* Effect.acquireRelease(acq, (p, exit?) => Effect.gen(function* () { rel }));
//	→ using c <- acq' release (p, exit) { rel' }
func (r *rewriter) tryUsingBind(node *ast.Node) (string, bool) {
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
	if name == nil || name.Kind != ast.KindIdentifier || !isValidIdentifier(name.Text()) || vd.Type != nil || vd.Initializer == nil {
		return "", false
	}
	operand, ok := yieldStarOf(vd.Initializer)
	if !ok {
		return "", false
	}
	helper, method, args, ok := r.helperCall(operand)
	if !ok || helper != "Effect" || method != "acquireRelease" || len(args) != 2 {
		return "", false
	}
	acquire := args[0]
	params, body, ok := r.finalizerArrow(args[1], 2)
	if !ok {
		return "", false
	}

	var b strings.Builder
	b.WriteString("using ")
	b.WriteString(name.Text())
	b.WriteString(" <- ")
	b.WriteString(r.emit(acquire))
	b.WriteString(" release (")
	b.WriteString(strings.Join(params, ", "))
	b.WriteString(") ")
	b.WriteString(r.emitEffectBody(body))
	r.stats.count("using-release")
	return b.String(), true
}

// tryDeferStatement matches pattern #15 (inside effect bodies):
//
//	yield* Effect.addFinalizer((exit?) => Effect.gen(function* () { b }));
//	→ defer { b' }  /  defer (exit) { b' }
func (r *rewriter) tryDeferStatement(node *ast.Node) (string, bool) {
	operand, ok := yieldStarOf(node.AsExpressionStatement().Expression)
	if !ok {
		return "", false
	}
	helper, method, args, ok := r.helperCall(operand)
	if !ok || helper != "Effect" || method != "addFinalizer" || len(args) != 1 {
		return "", false
	}
	params, body, ok := r.finalizerArrow(args[0], 1)
	if !ok {
		return "", false
	}

	var b strings.Builder
	b.WriteString("defer ")
	if len(params) == 1 {
		b.WriteString("(")
		b.WriteString(params[0])
		b.WriteString(") ")
	}
	b.WriteString(r.emitEffectBody(body))
	r.stats.count("defer")
	return b.String(), true
}

// finalizerArrow matches `(p, …) => Effect.gen(function* () { b })` with at
// most maxParams plain identifier parameters, returning the parameter names
// and the generator body block.
func (r *rewriter) finalizerArrow(node *ast.Node, maxParams int) ([]string, *ast.Node, bool) {
	if node.Kind != ast.KindArrowFunction {
		return nil, nil, false
	}
	arrow := node.AsArrowFunction()
	if arrow.Modifiers() != nil || arrow.TypeParameters != nil || arrow.Type != nil || arrow.Body == nil {
		return nil, nil, false
	}
	if len(arrow.Parameters.Nodes) > maxParams {
		return nil, nil, false
	}
	var params []string
	for _, p := range arrow.Parameters.Nodes {
		param := p.AsParameterDeclaration()
		name := p.Name()
		if name == nil || name.Kind != ast.KindIdentifier || !isValidIdentifier(name.Text()) ||
			param.Type != nil || param.Initializer != nil || param.DotDotDotToken != nil || param.QuestionToken != nil {
			return nil, nil, false
		}
		params = append(params, name.Text())
	}
	helper, method, args, ok := r.helperCall(arrow.Body)
	if !ok || helper != "Effect" || method != "gen" || len(args) != 1 {
		return nil, nil, false
	}
	gen, ok := r.genArg(args[0], true)
	if !ok {
		return nil, nil, false
	}
	return params, gen.Body, true
}
