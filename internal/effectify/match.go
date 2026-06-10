package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// tryMatchExpression matches the pure form of pattern #20 (outside effect
// bodies only — inside one, a match expression always desugars effectful):
//
//	Match.value(x).pipe(
//	  Match.when(200, (_) => "ok"),
//	  Match.tag("NotFound", (e) => e.id),
//	  Match.orElse((_) => "unknown"),   // or a final Match.exhaustive
//	)
//	→ match (x') { 200 >> "ok"  NotFound as e >> e.id  _ >> "unknown" }
func (r *rewriter) tryMatchExpression(node *ast.Node) (string, bool) {
	if r.inEffectBody {
		return "", false
	}
	return r.matchChainText(node, false)
}

// tryEffectfulMatch matches the effectful form of pattern #20 (inside effect
// bodies): `(yield* Match.value(x).pipe(…))` with every arm lowered through
// Effect.gen.
func (r *rewriter) tryEffectfulMatch(node *ast.Node) (string, bool) {
	inner, ok := yieldStarOf(node.AsParenthesizedExpression().Expression)
	if !ok {
		return "", false
	}
	return r.matchChainText(inner, true)
}

func (r *rewriter) matchChainText(node *ast.Node, effectful bool) (string, bool) {
	call, ok := pipeMethodCall(node)
	if !ok || len(call.Arguments.Nodes) == 0 {
		return "", false
	}
	helper, method, valueArgs, ok := r.helperCall(call.Expression.AsPropertyAccessExpression().Expression)
	if !ok || helper != "Match" || method != "value" || len(valueArgs) != 1 {
		return "", false
	}
	scrutinee := valueArgs[0]

	args := call.Arguments.Nodes
	hasExhaustive := false
	if h, m, ok := r.helperMethod(args[len(args)-1]); ok && h == "Match" && m == "exhaustive" {
		hasExhaustive = true
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return "", false
	}

	var arms []string
	for i, arg := range args {
		pattern, binding, body, isOrElse, ok := r.matchArmParts(arg, effectful)
		if !ok {
			return "", false
		}
		if isOrElse && (hasExhaustive || i != len(args)-1) {
			return "", false
		}
		arm := pattern
		if binding != "" {
			arm += " as " + binding
		}
		arms = append(arms, arm+" >> "+body)
	}

	indent := r.lineIndent(r.start(node))
	var b strings.Builder
	b.WriteString("match (")
	b.WriteString(r.emit(scrutinee))
	b.WriteString(") {\n")
	for _, arm := range arms {
		b.WriteString(indent)
		b.WriteString("  ")
		b.WriteString(arm)
		b.WriteString("\n")
	}
	b.WriteString(indent)
	b.WriteString("}")
	r.stats.count("match")
	return b.String(), true
}

// matchArmParts recognizes one W-form pipe argument of a match chain and
// renders its pattern, binding and body. Structural Match.when patterns and
// guard predicates are not reversed (the whole chain bails).
func (r *rewriter) matchArmParts(node *ast.Node, effectful bool) (pattern string, binding string, body string, isOrElse bool, ok bool) {
	if helper, method, args, isCall := r.helperCall(node); isCall && helper == "Match" {
		switch method {
		case "tag":
			if len(args) != 2 || args[0].Kind != ast.KindStringLiteral || !isEtsMatchTagName(args[0].Text()) {
				return "", "", "", false, false
			}
			binding, body, ok = r.matchHandlerParts(args[1], effectful)
			return args[0].Text(), binding, body, false, ok
		case "orElse":
			if len(args) != 1 {
				return "", "", "", false, false
			}
			binding, body, ok = r.matchHandlerParts(args[0], effectful)
			return "_", binding, body, true, ok
		case "when":
			if len(args) != 2 {
				return "", "", "", false, false
			}
			lit, isLit := r.literalPatternText(args[0])
			if !isLit {
				return "", "", "", false, false
			}
			binding, body, ok = r.matchHandlerParts(args[1], effectful)
			return lit, binding, body, false, ok
		case "whenOr":
			if len(args) < 3 {
				return "", "", "", false, false
			}
			var lits []string
			for _, litArg := range args[:len(args)-1] {
				lit, isLit := r.literalPatternText(litArg)
				if !isLit {
					return "", "", "", false, false
				}
				lits = append(lits, lit)
			}
			binding, body, ok = r.matchHandlerParts(args[len(args)-1], effectful)
			return strings.Join(lits, " | "), binding, body, false, ok
		}
		return "", "", "", false, false
	}

	// ((h) => Match.tags({ T1: h, T2: h }))(handler)
	tags, handler, ok := r.sharedHandlerIIFE(node, "Match", "tags")
	if !ok {
		return "", "", "", false, false
	}
	for _, tag := range tags {
		if !isEtsMatchTagName(tag) {
			return "", "", "", false, false
		}
	}
	binding, body, ok = r.matchHandlerParts(handler, effectful)
	return strings.Join(tags, " | "), binding, body, false, ok
}

// isEtsMatchTagName reports whether a tag string can appear as a match-arm
// pattern. The forward parser classifies identifier patterns by their first
// character — ASCII uppercase is a tag, anything else is a binding (i.e. a
// catch-all arm) — so lowercase tag names are inexpressible in match syntax.
func isEtsMatchTagName(name string) bool {
	return isValidIdentifier(name) && name[0] >= 'A' && name[0] <= 'Z'
}

// matchHandlerParts extracts the binding name and rendered body of a match
// arm handler. Effectful handlers are `(e) => Effect.gen(function* () { … })`
// (shared with catch arms); pure handlers are plain arrows.
func (r *rewriter) matchHandlerParts(node *ast.Node, effectful bool) (string, string, bool) {
	if effectful {
		binding, block, ok := r.catchHandler(node)
		if !ok {
			return "", "", false
		}
		return binding, r.armBodyText(block, ""), true
	}

	if node.Kind != ast.KindArrowFunction {
		return "", "", false
	}
	arrow := node.AsArrowFunction()
	if arrow.Modifiers() != nil || arrow.TypeParameters != nil || arrow.Type != nil || len(arrow.Parameters.Nodes) != 1 {
		return "", "", false
	}
	p := arrow.Parameters.Nodes[0].AsParameterDeclaration()
	name := arrow.Parameters.Nodes[0].Name()
	if name == nil || name.Kind != ast.KindIdentifier || p.Type != nil || p.Initializer != nil || p.DotDotDotToken != nil || p.QuestionToken != nil {
		return "", "", false
	}
	binding := name.Text()
	if binding == "_" {
		binding = ""
	} else if !isValidIdentifier(binding) {
		return "", "", false
	}
	if arrow.Body.Kind == ast.KindBlock {
		return binding, r.emitChildren(arrow.Body), true
	}
	return binding, r.emit(arrow.Body), true
}

// sharedHandlerIIFE matches `((h) => Helper.method({ T1: h, T2: h }))(handler)`
// — the shared-handler lowering used by both catch and match multi-tag arms —
// returning the tag list and the handler argument.
func (r *rewriter) sharedHandlerIIFE(node *ast.Node, wantHelper string, wantMethod string) ([]string, *ast.Node, bool) {
	if node.Kind != ast.KindCallExpression {
		return nil, nil, false
	}
	iife := node.AsCallExpression()
	if iife.QuestionDotToken != nil || iife.TypeArguments != nil || len(iife.Arguments.Nodes) != 1 ||
		iife.Expression.Kind != ast.KindParenthesizedExpression {
		return nil, nil, false
	}
	arrow := iife.Expression.AsParenthesizedExpression().Expression
	if arrow.Kind != ast.KindArrowFunction {
		return nil, nil, false
	}
	af := arrow.AsArrowFunction()
	if af.Modifiers() != nil || af.TypeParameters != nil || af.Type != nil || len(af.Parameters.Nodes) != 1 {
		return nil, nil, false
	}
	hName := af.Parameters.Nodes[0].Name()
	if hName == nil || hName.Kind != ast.KindIdentifier {
		return nil, nil, false
	}
	helper, method, args, ok := r.helperCall(af.Body)
	if !ok || helper != wantHelper || method != wantMethod || len(args) != 1 || args[0].Kind != ast.KindObjectLiteralExpression {
		return nil, nil, false
	}
	var tags []string
	for _, prop := range args[0].AsObjectLiteralExpression().Properties.Nodes {
		if prop.Kind != ast.KindPropertyAssignment {
			return nil, nil, false
		}
		pa := prop.AsPropertyAssignment()
		name := prop.Name()
		if name == nil || name.Kind != ast.KindIdentifier || !isValidIdentifier(name.Text()) {
			return nil, nil, false
		}
		if pa.Initializer.Kind != ast.KindIdentifier || pa.Initializer.Text() != hName.Text() {
			return nil, nil, false
		}
		tags = append(tags, name.Text())
	}
	if len(tags) < 2 {
		return nil, nil, false
	}
	return tags, iife.Arguments.Nodes[0], true
}

// literalPatternText matches the literal pattern kinds the match grammar
// accepts and returns their source text.
func (r *rewriter) literalPatternText(node *ast.Node) (string, bool) {
	switch node.Kind {
	case ast.KindStringLiteral, ast.KindNumericLiteral, ast.KindBigIntLiteral,
		ast.KindNoSubstitutionTemplateLiteral, ast.KindTrueKeyword, ast.KindFalseKeyword,
		ast.KindNullKeyword:
		return r.text(node), true
	case ast.KindPrefixUnaryExpression:
		prefix := node.AsPrefixUnaryExpression()
		if prefix.Operator == ast.KindMinusToken && prefix.Operand.Kind == ast.KindNumericLiteral {
			return r.text(node), true
		}
	}
	return "", false
}
