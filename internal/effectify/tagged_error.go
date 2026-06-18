package effectify

import "github.com/microsoft/typescript-go/internal/ast"

// tryTaggedErrorDeclaration matches the reverse of the `tagged error`
// declaration sugar (docs/effectscript SPEC §7.x):
//
//	[export] class Name extends Data.TaggedError("Name")<{ props }> {}
//	→ [export] tagged error Name { props }
//
// Only the exact desugared shape converts: tag string == class name, the prop
// shape is a single object type literal applied as the type argument, and an
// empty class body. Named base types, body members, or a tag string differing
// from the class name are left verbatim — they have no `tagged error` surface.
func (r *rewriter) tryTaggedErrorDeclaration(node *ast.Node) (string, bool) {
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

	// extends Data.TaggedError("Name")<{ props }>
	ext := heritage.Types.Nodes[0].AsExpressionWithTypeArguments()
	if ext.TypeArguments == nil || len(ext.TypeArguments.Nodes) != 1 {
		return "", false
	}
	shape := ext.TypeArguments.Nodes[0]
	if shape.Kind != ast.KindTypeLiteral {
		return "", false
	}
	if ext.Expression.Kind != ast.KindCallExpression {
		return "", false
	}
	call := ext.Expression.AsCallExpression()
	if call.QuestionDotToken != nil || call.TypeArguments != nil || len(call.Arguments.Nodes) != 1 {
		return "", false
	}
	helper, method, ok := r.helperMethod(call.Expression)
	if !ok || helper != "Data" || method != "TaggedError" {
		return "", false
	}
	tag := call.Arguments.Nodes[0]
	if tag.Kind != ast.KindStringLiteral || tag.Text() != name.Text() {
		return "", false
	}

	r.stats.count("tagged-error")
	return r.modifiersText(cls.Modifiers()) + "tagged error " + name.Text() + " " + r.text(shape), true
}
