package effectify

import (
	"github.com/microsoft/typescript-go/internal/ast"
)

// equalModuloParens reports whether two ASTs are structurally identical after
// looking through ParenthesizedExpression nodes. Parentheses carry no runtime
// semantics once a program is parsed, but some reverse rules necessarily
// introduce them (`yield* e` → `(<- e)` re-desugars to `(yield* e)`), so the
// round-trip verifier must not treat them as a difference.
func equalModuloParens(a *ast.Node, b *ast.Node) bool {
	a = unwrapParens(a)
	b = unwrapParens(b)
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind {
		return false
	}
	// Effect.Effect<A, E> and Effect.Effect<A, E, never> are the same type
	// (omitted channels default to never); the raises/requires sugar always
	// lowers to the 3-argument form, so pad before comparing.
	if a.Kind == ast.KindTypeReference {
		if aArgs, aOk := effectEffectTypeArgs(a); aOk {
			bArgs, bOk := effectEffectTypeArgs(b)
			return bOk && equalEffectTypeArgs(aArgs, bArgs)
		}
		if _, bOk := effectEffectTypeArgs(b); bOk {
			return false
		}
	}
	at, aHasText := nodeText(a)
	bt, bHasText := nodeText(b)
	if aHasText != bHasText || at != bt {
		return false
	}
	if flagSensitiveKind(a.Kind) && a.Flags&ast.NodeFlagsBlockScoped != b.Flags&ast.NodeFlagsBlockScoped {
		return false
	}
	// Optional sub-tokens that ForEachChild may not surface.
	if a.Kind == ast.KindYieldExpression {
		if (a.AsYieldExpression().AsteriskToken == nil) != (b.AsYieldExpression().AsteriskToken == nil) {
			return false
		}
	}

	ac := childrenModuloParens(a)
	bc := childrenModuloParens(b)
	if len(ac) != len(bc) {
		return false
	}
	for i := range ac {
		if !equalModuloParens(ac[i], bc[i]) {
			return false
		}
	}
	return true
}

func unwrapParens(node *ast.Node) *ast.Node {
	for node != nil && node.Kind == ast.KindParenthesizedExpression {
		node = node.AsParenthesizedExpression().Expression
	}
	return node
}

func childrenModuloParens(node *ast.Node) []*ast.Node {
	var children []*ast.Node
	node.ForEachChild(func(child *ast.Node) bool {
		child = unwrapParens(child)
		if child != nil {
			children = append(children, child)
		}
		return false
	})
	return children
}

func flagSensitiveKind(kind ast.Kind) bool {
	return kind == ast.KindVariableDeclarationList
}

// effectEffectTypeArgs matches a `Effect.Effect<…>` type reference (by name —
// the comparison is applied uniformly to both sides) with 1–3 type arguments.
func effectEffectTypeArgs(node *ast.Node) ([]*ast.Node, bool) {
	ref := node.AsTypeReferenceNode()
	if ref.TypeName == nil || ref.TypeName.Kind != ast.KindQualifiedName || ref.TypeArguments == nil {
		return nil, false
	}
	qn := ref.TypeName.AsQualifiedName()
	if qn.Left.Kind != ast.KindIdentifier || qn.Left.Text() != "Effect" ||
		qn.Right.Kind != ast.KindIdentifier || qn.Right.Text() != "Effect" {
		return nil, false
	}
	args := ref.TypeArguments.Nodes
	if len(args) == 0 || len(args) > 3 {
		return nil, false
	}
	return args, true
}

func equalEffectTypeArgs(a []*ast.Node, b []*ast.Node) bool {
	for i := 0; i < 3; i++ {
		var ai, bi *ast.Node
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		switch {
		case ai == nil && bi == nil:
		case ai == nil:
			if bi.Kind != ast.KindNeverKeyword {
				return false
			}
		case bi == nil:
			if ai.Kind != ast.KindNeverKeyword {
				return false
			}
		default:
			if !equalModuloParens(ai, bi) {
				return false
			}
		}
	}
	return true
}

// nodeText extracts the comparable text payload of leaf-ish nodes.
func nodeText(node *ast.Node) (string, bool) {
	switch node.Kind {
	case ast.KindIdentifier, ast.KindPrivateIdentifier,
		ast.KindStringLiteral, ast.KindNumericLiteral, ast.KindBigIntLiteral,
		ast.KindNoSubstitutionTemplateLiteral, ast.KindTemplateHead,
		ast.KindTemplateMiddle, ast.KindTemplateTail,
		ast.KindRegularExpressionLiteral, ast.KindJsxText:
		return node.Text(), true
	}
	return "", false
}
