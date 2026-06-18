package effectify

import "github.com/microsoft/typescript-go/internal/ast"

// trySchemaDeclaration matches the reverse of the `schema` declaration sugar
// (docs/effectscript SPEC §7.4):
//
//	[export] class S extends Schema.Class<S>("S")({ fields }) {}
//	→ [export] schema S { fields }
//
// Only the exact desugared shape converts: tag string == class name, the
// `<S>` self type argument == class name, an empty class body, and a single
// object-literal field argument (no annotations arg). Anything else — tagged
// classes/errors, named base types, body members — is left verbatim.
func (r *rewriter) trySchemaDeclaration(node *ast.Node) (string, bool) {
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
	if ext.TypeArguments != nil || ext.Expression.Kind != ast.KindCallExpression {
		return "", false
	}

	// outer: Schema.Class<S>("S")({ fields })
	outer := ext.Expression.AsCallExpression()
	if outer.QuestionDotToken != nil || outer.TypeArguments != nil || len(outer.Arguments.Nodes) != 1 {
		return "", false
	}
	fields := outer.Arguments.Nodes[0]
	if fields.Kind != ast.KindObjectLiteralExpression {
		return "", false
	}
	if outer.Expression.Kind != ast.KindCallExpression {
		return "", false
	}

	// inner: Schema.Class<S>("S")
	inner := outer.Expression.AsCallExpression()
	if inner.QuestionDotToken != nil ||
		len(inner.Arguments.Nodes) != 1 || inner.TypeArguments == nil || len(inner.TypeArguments.Nodes) != 1 {
		return "", false
	}
	helper, method, ok := r.helperMethod(inner.Expression)
	if !ok || helper != "Schema" || method != "Class" {
		return "", false
	}
	if inner.Arguments.Nodes[0].Kind != ast.KindStringLiteral || inner.Arguments.Nodes[0].Text() != name.Text() {
		return "", false
	}
	selfArg := inner.TypeArguments.Nodes[0]
	if selfArg.Kind != ast.KindTypeReference || r.text(selfArg) != name.Text() {
		return "", false
	}

	r.stats.count("schema")
	return r.modifiersText(cls.Modifiers()) + "schema " + name.Text() + " " + r.emitChildren(fields), true
}

// trySchemaErrorDeclaration matches the reverse of the `schema error`
// declaration sugar:
//
//	[export] class Name extends Schema.TaggedError<Name>()("Name", { fields }) {}
//	→ [export] schema error Name { fields }
//
// As with `schema`, only the exact desugared shape converts: the `<Name>` self
// type argument and the tag string both == class name, an empty class body, and
// a single object-literal field argument.
func (r *rewriter) trySchemaErrorDeclaration(node *ast.Node) (string, bool) {
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
	if ext.TypeArguments != nil || ext.Expression.Kind != ast.KindCallExpression {
		return "", false
	}

	// outer: Schema.TaggedError<Name>()("Name", { fields })
	outer := ext.Expression.AsCallExpression()
	if outer.QuestionDotToken != nil || outer.TypeArguments != nil || len(outer.Arguments.Nodes) != 2 {
		return "", false
	}
	tag := outer.Arguments.Nodes[0]
	fields := outer.Arguments.Nodes[1]
	if tag.Kind != ast.KindStringLiteral || tag.Text() != name.Text() {
		return "", false
	}
	if fields.Kind != ast.KindObjectLiteralExpression {
		return "", false
	}
	if outer.Expression.Kind != ast.KindCallExpression {
		return "", false
	}

	// inner: Schema.TaggedError<Name>()
	inner := outer.Expression.AsCallExpression()
	if inner.QuestionDotToken != nil ||
		len(inner.Arguments.Nodes) != 0 || inner.TypeArguments == nil || len(inner.TypeArguments.Nodes) != 1 {
		return "", false
	}
	helper, method, ok := r.helperMethod(inner.Expression)
	if !ok || helper != "Schema" || method != "TaggedError" {
		return "", false
	}
	selfArg := inner.TypeArguments.Nodes[0]
	if selfArg.Kind != ast.KindTypeReference || r.text(selfArg) != name.Text() {
		return "", false
	}

	r.stats.count("schema-error")
	return r.modifiersText(cls.Modifiers()) + "schema error " + name.Text() + " " + r.emitChildren(fields), true
}
