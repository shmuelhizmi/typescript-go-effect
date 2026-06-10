package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// tryServiceDeclaration matches pattern #12:
//
//	[export] class S extends Context.Tag("S")<S, { members }>() {}
//	→ [export] service S { members }
func (r *rewriter) tryServiceDeclaration(node *ast.Node) (string, bool) {
	cls := node.AsClassDeclaration()
	name := node.Name()
	if name == nil || name.Kind != ast.KindIdentifier || !isValidIdentifier(name.Text()) {
		return "", false
	}
	if cls.TypeParameters != nil || len(cls.Members.Nodes) != 0 {
		return "", false
	}
	if mods := cls.Modifiers(); mods != nil {
		for _, m := range mods.Nodes {
			if m.Kind != ast.KindExportKeyword {
				return "", false
			}
		}
	}
	if cls.HeritageClauses == nil || len(cls.HeritageClauses.Nodes) != 1 {
		return "", false
	}
	heritage := cls.HeritageClauses.Nodes[0].AsHeritageClause()
	if heritage.Token != ast.KindExtendsKeyword || len(heritage.Types.Nodes) != 1 {
		return "", false
	}
	ext := heritage.Types.Nodes[0].AsExpressionWithTypeArguments()
	if ext.TypeArguments != nil {
		return "", false
	}

	// Context.Tag("S")<S, { members }>()
	if ext.Expression.Kind != ast.KindCallExpression {
		return "", false
	}
	inst := ext.Expression.AsCallExpression()
	if len(inst.Arguments.Nodes) != 0 || inst.TypeArguments == nil || len(inst.TypeArguments.Nodes) != 2 {
		return "", false
	}
	helper, method, tagArgs, ok := r.helperCall(inst.Expression)
	if !ok || helper != "Context" || method != "Tag" {
		return "", false
	}
	if len(tagArgs) != 1 || tagArgs[0].Kind != ast.KindStringLiteral || tagArgs[0].Text() != name.Text() {
		return "", false
	}
	selfArg := inst.TypeArguments.Nodes[0]
	if selfArg.Kind != ast.KindTypeReference || r.text(selfArg) != name.Text() {
		return "", false
	}
	shape := inst.TypeArguments.Nodes[1]
	if shape.Kind != ast.KindTypeLiteral {
		return "", false
	}

	r.stats.count("service")
	return r.modifiersText(cls.Modifiers()) + "service " + name.Text() + " " + r.emitChildren(shape), true
}

// layerShape is a matched Layer.effect/scoped/succeed initializer.
type layerShape struct {
	scoped  bool
	tag     *ast.Node
	gen     *ast.FunctionExpression // body form
	value   *ast.Node               // succeed form
	provide *ast.Node               // Layer.provide([...]) array literal, when wrapped
}

// tryLayerDeclaration matches pattern #13:
//
//	const L = Layer.effect(Tag, Effect.gen(function* () { b }));          → layer L: Tag { b' }
//	const L = Layer.scoped(Tag, Effect.gen(function* () { b }));          → scoped layer L: Tag { b' }
//	const L = Layer.succeed(Tag, e);                                      → layer L: Tag = e';
//	const L = Layer.effect(…).pipe(Layer.provide([A, B]));                → layer L: Tag provide [A, B] { b' }
func (r *rewriter) tryLayerDeclaration(node *ast.Node) (string, bool) {
	vs := node.AsVariableStatement()
	if mods := vs.Modifiers(); mods != nil {
		for _, m := range mods.Nodes {
			if ast.IsDecorator(m) {
				return "", false
			}
		}
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

	shape, ok := r.matchLayer(vd.Initializer)
	if !ok {
		return "", false
	}

	var b strings.Builder
	b.WriteString(r.modifiersText(vs.Modifiers()))
	if shape.scoped {
		b.WriteString("scoped ")
	}
	b.WriteString("layer ")
	b.WriteString(name.Text())
	b.WriteString(": ")
	b.WriteString(r.text(shape.tag))
	if shape.provide != nil {
		b.WriteString(" provide ")
		b.WriteString(r.emitChildren(shape.provide))
	}
	if shape.value != nil {
		b.WriteString(" = ")
		b.WriteString(r.emit(shape.value))
		b.WriteString(";")
	} else {
		b.WriteString(" ")
		b.WriteString(r.emitEffectBody(shape.gen.Body))
	}
	r.stats.count("layer")
	return b.String(), true
}

func (r *rewriter) matchLayer(init *ast.Node) (layerShape, bool) {
	var shape layerShape

	// Optional wrapper: <layer>.pipe(Layer.provide([…]))
	if call, ok := pipeMethodCall(init); ok {
		if len(call.Arguments.Nodes) != 1 {
			return shape, false
		}
		helper, method, args, ok := r.helperCall(call.Arguments.Nodes[0])
		if !ok || helper != "Layer" || method != "provide" || len(args) != 1 || args[0].Kind != ast.KindArrayLiteralExpression {
			return shape, false
		}
		shape.provide = args[0]
		init = call.Expression.AsPropertyAccessExpression().Expression
	}

	helper, method, args, ok := r.helperCall(init)
	if !ok || helper != "Layer" || len(args) != 2 {
		return shape, false
	}
	if !isEntityNameExpression(args[0]) {
		return shape, false
	}
	shape.tag = args[0]

	switch method {
	case "succeed":
		if shape.provide != nil {
			// `layer L: Tag = e` has no provide clause in the grammar.
			return shape, false
		}
		shape.value = args[1]
		return shape, true
	case "scoped":
		shape.scoped = true
	case "effect":
	default:
		return shape, false
	}

	genHelper, genMethod, genArgs, ok := r.helperCall(args[1])
	if !ok || genHelper != "Effect" || genMethod != "gen" || len(genArgs) != 1 {
		return shape, false
	}
	gen, ok := r.genArg(genArgs[0], true)
	if !ok {
		return shape, false
	}
	shape.gen = gen
	return shape, true
}

// pipeMethodCall matches `recv.pipe(args…)` and returns the call.
func pipeMethodCall(node *ast.Node) (*ast.CallExpression, bool) {
	if node.Kind != ast.KindCallExpression {
		return nil, false
	}
	call := node.AsCallExpression()
	if call.QuestionDotToken != nil || call.TypeArguments != nil || call.Expression.Kind != ast.KindPropertyAccessExpression {
		return nil, false
	}
	pa := call.Expression.AsPropertyAccessExpression()
	if pa.QuestionDotToken != nil {
		return nil, false
	}
	name := pa.Name()
	if name == nil || name.Kind != ast.KindIdentifier || name.Text() != "pipe" {
		return nil, false
	}
	return call, true
}

// isEntityNameExpression matches Identifier or dotted Identifier chains
// (Db, pkg.Db) — the only shapes a layer tag position parses.
func isEntityNameExpression(node *ast.Node) bool {
	switch node.Kind {
	case ast.KindIdentifier:
		return !effectKeywordIdents[node.Text()]
	case ast.KindPropertyAccessExpression:
		pa := node.AsPropertyAccessExpression()
		name := pa.Name()
		return pa.QuestionDotToken == nil && name != nil && name.Kind == ast.KindIdentifier && isEntityNameExpression(pa.Expression)
	}
	return false
}
