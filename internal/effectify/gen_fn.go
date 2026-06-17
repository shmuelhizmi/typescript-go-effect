package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
)

// effectFnShape is a matched `Effect.fn("name")(generator, …combinators)` or
// `Effect.fn(generator)` call.
type effectFnShape struct {
	name        string // "" for the anonymous form
	gen         *ast.FunctionExpression
	combinators []*ast.Node // trailing pipe combinator arguments (named form only)
}

// matchEffectFn recognizes both Effect.fn call shapes.
func (r *rewriter) matchEffectFn(node *ast.Node) (effectFnShape, bool) {
	if node.Kind != ast.KindCallExpression {
		return effectFnShape{}, false
	}
	outer := node.AsCallExpression()
	if outer.QuestionDotToken != nil || outer.TypeArguments != nil {
		return effectFnShape{}, false
	}

	// Anonymous: Effect.fn(function* (p) { … })
	if helper, method, args, ok := r.helperCall(node); ok && helper == "Effect" && method == "fn" {
		if len(args) != 1 {
			return effectFnShape{}, false
		}
		gen, ok := r.genArgTyped(args[0], false, true)
		if !ok {
			return effectFnShape{}, false
		}
		return effectFnShape{gen: gen}, true
	}

	// Named: Effect.fn("name")(function* (p) { … }, …combinators)
	helper, method, innerArgs, ok := r.helperCall(outer.Expression)
	if !ok || helper != "Effect" || method != "fn" {
		return effectFnShape{}, false
	}
	if len(innerArgs) != 1 || innerArgs[0].Kind != ast.KindStringLiteral {
		return effectFnShape{}, false
	}
	outerArgs := outer.Arguments.Nodes
	if len(outerArgs) == 0 {
		return effectFnShape{}, false
	}
	gen, ok := r.genArgTyped(outerArgs[0], false, true)
	if !ok {
		return effectFnShape{}, false
	}
	// Span-name validity is checked by the consumers — class fields accept
	// dotted "C.m" spans, the other forms need a plain identifier.
	return effectFnShape{name: innerArgs[0].Text(), gen: gen, combinators: outerArgs[1:]}, true
}

func isValidIdentifier(name string) bool {
	if name == "" || effectKeywordIdents[name] {
		return false
	}
	return scanner.IsIdentifierText(name, core.LanguageVariantStandard)
}

// effectFnText renders `effect name(params)[: A raises E requires R] { body }`
// for a matched shape.
func (r *rewriter) effectFnText(shape effectFnShape) string {
	var b strings.Builder
	b.WriteString("effect ")
	if shape.name != "" {
		b.WriteString(shape.name)
	}
	b.WriteString(r.paramsText(shape.gen))
	if shape.gen.Type != nil {
		// genArgTyped already validated the annotation is renderable.
		if clause, ok := r.effectFnReturnClause(shape.gen.Type); ok {
			b.WriteString(clause)
		}
	}
	b.WriteString(" ")
	b.WriteString(r.emitEffectBody(shape.gen.Body))
	return b.String()
}

// effectFnReturnClause renders an `Effect.fn.Return<A, E, R>` generator return
// type as the EffectScript `: A [raises E] [requires R]` clause. The arg-count
// mapping is the exact inverse of the forward parser's parseEffectFnReturnType,
// so the lowering round-trips:
//
//	Effect.fn.Return<A>            -> : A
//	Effect.fn.Return<A, E>        -> : A raises E
//	Effect.fn.Return<A, never, R> -> : A requires R
//	Effect.fn.Return<A, E, R>     -> : A raises E requires R
func (r *rewriter) effectFnReturnClause(typeNode *ast.Node) (string, bool) {
	if typeNode.Kind != ast.KindTypeReference {
		return "", false
	}
	ref := typeNode.AsTypeReferenceNode()
	if !r.isEffectFnReturnName(ref.TypeName) {
		return "", false
	}
	if ref.TypeArguments == nil {
		return "", false
	}
	args := ref.TypeArguments.Nodes
	if len(args) == 0 || len(args) > 3 {
		return "", false
	}
	var b strings.Builder
	b.WriteString(": ")
	b.WriteString(r.text(args[0]))
	switch len(args) {
	case 2:
		b.WriteString(" raises ")
		b.WriteString(r.text(args[1]))
	case 3:
		if args[1].Kind == ast.KindNeverKeyword {
			b.WriteString(" requires ")
			b.WriteString(r.text(args[2]))
		} else {
			b.WriteString(" raises ")
			b.WriteString(r.text(args[1]))
			b.WriteString(" requires ")
			b.WriteString(r.text(args[2]))
		}
	}
	return b.String(), true
}

// isEffectFnReturnName reports whether an entity name is `<Effect>.fn.Return`,
// where the leading helper resolves (through aliases/namespaces) to Effect.
func (r *rewriter) isEffectFnReturnName(name *ast.Node) bool {
	if name.Kind != ast.KindQualifiedName {
		return false
	}
	outer := name.AsQualifiedName() // (Effect.fn).Return
	if outer.Right.Text() != "Return" || outer.Left.Kind != ast.KindQualifiedName {
		return false
	}
	mid := outer.Left.AsQualifiedName() // Effect.fn
	if mid.Right.Text() != "fn" || mid.Left.Kind != ast.KindIdentifier {
		return false
	}
	return r.localToCanonical[mid.Left.Text()] == "Effect"
}

// tryEffectDeclaration matches pattern #1/#2:
//
//	[export] const f = Effect.fn("f")(function* (p) { b });
//	→ [export] effect f(p) { b' }
//
// Calls with extra pipe-combinator arguments are left untouched: EffectScript
// has no syntax for them on an effect declaration.
func (r *rewriter) tryEffectDeclaration(node *ast.Node) (string, bool) {
	vs := node.AsVariableStatement()
	declList := vs.DeclarationList.AsVariableDeclarationList()
	if declList.AsNode().Flags&ast.NodeFlagsConst == 0 || len(declList.Declarations.Nodes) != 1 {
		return "", false
	}
	decl := declList.Declarations.Nodes[0]
	vd := decl.AsVariableDeclaration()
	name := decl.Name()
	if name == nil || name.Kind != ast.KindIdentifier || vd.Type != nil || vd.Initializer == nil {
		return "", false
	}
	shape, ok := r.matchEffectFn(vd.Initializer)
	if !ok || shape.name == "" || shape.name != name.Text() || len(shape.combinators) > 0 {
		return "", false
	}
	if vs.Modifiers() != nil {
		for _, m := range vs.Modifiers().Nodes {
			if ast.IsDecorator(m) {
				return "", false
			}
		}
	}

	var b strings.Builder
	b.WriteString(r.modifiersText(vs.Modifiers()))
	b.WriteString(r.effectFnText(shape))
	r.stats.count("effect-declaration")
	return b.String(), true
}

// tryEffectFnExpression matches patterns #3 and the expression form of #1:
//
//	Effect.fn(function* (p) { b })          → effect (p) { b' }
//	Effect.fn("g")(function* (p) { b })     → effect g(p) { b' }
//
// Skipped at statement-expression position: `effect g(…) {…}` at statement
// head re-parses as a *declaration* (const g = …), which would change the
// program shape.
func (r *rewriter) tryEffectFnExpression(node *ast.Node) (string, bool) {
	shape, ok := r.matchEffectFn(node)
	if !ok || len(shape.combinators) > 0 {
		return "", false
	}
	if shape.name != "" && !isValidIdentifier(shape.name) {
		return "", false
	}
	if shape.name != "" && node.Parent != nil && node.Parent.Kind == ast.KindExpressionStatement {
		return "", false
	}
	out := r.effectFnText(shape)
	if shape.name != "" {
		r.stats.count("effect-fn-expression")
	} else {
		r.stats.count("effect-anonymous-fn")
	}
	return out, true
}

// tryEffectGenBlock matches pattern #4:
//
//	Effect.gen(function* () { b })  →  effect { b' }
func (r *rewriter) tryEffectGenBlock(node *ast.Node) (string, bool) {
	helper, method, args, ok := r.helperCall(node)
	if !ok || helper != "Effect" || method != "gen" || len(args) != 1 {
		return "", false
	}
	gen, ok := r.genArg(args[0], true)
	if !ok {
		return "", false
	}
	r.stats.count("effect-block")
	return "effect " + r.emitEffectBody(gen.Body), true
}

// tryEffectClassField matches pattern #5:
//
//	m = Effect.fn("C.m")(function* (p) { b });   (class field)
//	→ [static] effect m(p) { b' }
func (r *rewriter) tryEffectClassField(node *ast.Node) (string, bool) {
	prop := node.AsPropertyDeclaration()
	name := node.Name()
	if name == nil || name.Kind != ast.KindIdentifier || prop.Type != nil || prop.Initializer == nil || prop.PostfixToken != nil {
		return "", false
	}
	// Only a bare field or `static` survives the forward direction.
	if mods := prop.Modifiers(); mods != nil {
		for _, m := range mods.Nodes {
			if m.Kind != ast.KindStaticKeyword {
				return "", false
			}
		}
	}
	shape, ok := r.matchEffectFn(prop.Initializer)
	if !ok || shape.name == "" || len(shape.combinators) > 0 {
		return "", false
	}
	wantSpan := name.Text()
	if cls := enclosingClassName(node); cls != "" {
		wantSpan = cls + "." + name.Text()
	}
	if shape.name != wantSpan {
		return "", false
	}

	shape.name = name.Text()
	out := r.modifiersText(prop.Modifiers()) + r.effectFnText(shape)
	r.stats.count("effect-class-method")
	return out, true
}

func enclosingClassName(member *ast.Node) string {
	for p := member.Parent; p != nil; p = p.Parent {
		if p.Kind == ast.KindClassDeclaration || p.Kind == ast.KindClassExpression {
			if name := p.Name(); name != nil {
				return name.Text()
			}
			return ""
		}
	}
	return ""
}

// tryExportDefaultEffectFn matches `export default Effect.fn("entry")(…)`:
//
//	→ export default effect entry() { b' }
func (r *rewriter) tryExportDefaultEffectFn(node *ast.Node) (string, bool) {
	exp := node.AsExportAssignment()
	if exp.IsExportEquals || exp.Expression == nil {
		return "", false
	}
	shape, ok := r.matchEffectFn(exp.Expression)
	if !ok || shape.name == "" || !isValidIdentifier(shape.name) || len(shape.combinators) > 0 {
		return "", false
	}
	r.stats.count("effect-declaration")
	return "export default " + r.effectFnText(shape) + ";", true
}
