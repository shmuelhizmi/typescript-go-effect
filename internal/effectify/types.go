package effectify

import (
	"github.com/microsoft/typescript-go/internal/ast"
)

// tryEffectTypeSugar matches pattern #21:
//
//	Effect.Effect<A, E, R>  →  A raises E requires R   (never-channels omitted)
//
// Applied only in positions where the sugar reads well and re-parses
// unambiguously: function/method return types (not arrows — their return
// type grammar would let a function-typed E swallow the `=>`), method and
// property signatures, and type alias right-hand sides. Type-argument
// positions are excluded, which also rules out nested sugar.
func (r *rewriter) tryEffectTypeSugar(node *ast.Node) (string, bool) {
	if !r.typeSugarPosition(node) {
		return "", false
	}
	ref := node.AsTypeReferenceNode()
	if ref.TypeArguments == nil {
		return "", false
	}
	typeArgs := ref.TypeArguments.Nodes
	if len(typeArgs) < 2 || len(typeArgs) > 3 {
		return "", false
	}
	if ref.TypeName.Kind != ast.KindQualifiedName {
		return "", false
	}
	qn := ref.TypeName.AsQualifiedName()
	if qn.Left.Kind != ast.KindIdentifier || qn.Right.Kind != ast.KindIdentifier || qn.Right.Text() != "Effect" {
		return "", false
	}
	if canon, ok := r.localToCanonical[qn.Left.Text()]; !ok || canon != "Effect" {
		return "", false
	}

	success := typeArgs[0]
	switch success.Kind {
	case ast.KindFunctionType, ast.KindConstructorType, ast.KindConditionalType:
		// `() => X raises E` would re-parse with the sugar attached to X.
		return "", false
	}
	errType := typeArgs[1]
	var reqType *ast.Node
	if len(typeArgs) == 3 {
		reqType = typeArgs[2]
	}
	raisesText, hasRaises := r.channelText(errType)
	requiresText, hasRequires := "", false
	if reqType != nil {
		requiresText, hasRequires = r.channelText(reqType)
	}
	if !hasRaises && !hasRequires {
		// Effect.Effect<A> / <A, never, never> — no sugar gain.
		return "", false
	}

	out := r.emitChildren(success)
	if hasRaises {
		out += " raises " + raisesText
	}
	if hasRequires {
		out += " requires " + requiresText
	}
	r.stats.count("type-sugar")
	return out, true
}

// channelText renders an E/R channel type, reporting false for `never`
// (an omitted clause).
func (r *rewriter) channelText(node *ast.Node) (string, bool) {
	if node.Kind == ast.KindNeverKeyword {
		return "", false
	}
	return r.emitChildren(node), true
}

// typeSugarPosition restricts the sugar to return-type and alias positions.
func (r *rewriter) typeSugarPosition(node *ast.Node) bool {
	parent := node.Parent
	if parent == nil {
		return false
	}
	switch parent.Kind {
	case ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindMethodDeclaration,
		ast.KindMethodSignature, ast.KindGetAccessor:
		fn := parent.FunctionLikeData()
		return fn != nil && fn.Type == node
	case ast.KindTypeAliasDeclaration:
		return parent.AsTypeAliasDeclaration().Type == node
	case ast.KindPropertySignature:
		return parent.AsPropertySignatureDeclaration().Type == node
	}
	return false
}
