package parser

import (
	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/diagnostics"
)

// EffectScript (.ets/.etsx) parsing. See docs/effectscript/SPEC.md.
//
// v0 strategy ("parse and lower in one step"): EffectScript constructs are
// recognized here and immediately lowered to plain TypeScript AST per
// docs/effectscript/TRANSPILATION.md, the way the JSDoc reparser synthesizes
// nodes. The binder, checker and emitter only ever see standard TS. Dedicated
// AST kinds (for preserve mode and richer tooling) come later and slot in
// behind the same hooks.

func (p *Parser) isEffectScript() bool {
	return p.scriptKind == core.ScriptKindETS || p.scriptKind == core.ScriptKindETSX
}

// tryParseEffectScriptStatement is the statement-level hook, called from
// parseStatement before the expression-statement fallthrough. It returns nil
// when the statement is not an EffectScript construct.
func (p *Parser) tryParseEffectScriptStatement() *ast.Statement {
	switch p.token {
	case ast.KindIdentifier:
		switch p.scanner.TokenValue() {
		case "effect":
			if p.lookAhead((*Parser).nextIsEffectDeclarationStart) {
				return p.parseEffectDeclaration(p.nodePos(), nil /*modifiers*/)
			}
		case "service":
			if p.lookAhead((*Parser).nextIsServiceDeclarationStart) {
				return p.parseServiceDeclaration(p.nodePos(), nil /*modifiers*/)
			}
		case "layer":
			if p.lookAhead((*Parser).nextIsLayerDeclarationStart) {
				return p.parseLayerDeclaration(p.nodePos(), nil /*modifiers*/, false /*scoped*/)
			}
		case "scoped":
			if p.lookAhead((*Parser).nextIsScopedLayerDeclarationStart) {
				return p.parseLayerDeclaration(p.nodePos(), nil /*modifiers*/, true /*scoped*/)
			}
		case "raise":
			if p.lookAhead((*Parser).nextIsRaiseOperandStart) {
				if !p.inEffectBody {
					p.parseErrorAt(p.nodePos(), p.nodePos()+len("raise"), diagnostics.X_raise_is_only_allowed_inside_an_effect_body)
				}
				return p.parseRaiseStatement()
			}
		case "defer":
			if p.inEffectBody && p.lookAhead((*Parser).nextIsDeferBodyStart) {
				return p.parseDeferStatement()
			}
		}
		if p.lookAhead((*Parser).nextIsBindArrow) {
			if !p.inEffectBody {
				p.parseErrorAt(p.nodePos(), p.nodePos()+1, diagnostics.X_binds_are_only_allowed_inside_an_effect_body)
			}
			return p.parseBindStatement()
		}
	case ast.KindDeferKeyword:
		if p.inEffectBody && p.lookAhead((*Parser).nextIsDeferBodyStart) {
			return p.parseDeferStatement()
		}
	case ast.KindOpenBracketToken, ast.KindOpenBraceToken:
		if p.inEffectBody && p.lookAhead((*Parser).nextIsBindingPatternBind) {
			return p.parseBindStatement()
		}
	case ast.KindLessThanToken:
		if p.isAtBindArrow() {
			if !p.inEffectBody {
				p.parseErrorAt(p.nodePos(), p.nodePos()+2, diagnostics.X_binds_are_only_allowed_inside_an_effect_body)
			}
			return p.parseDiscardBindStatement()
		}
	default:
		// Contextual keywords (out, async, yield, of, ...) scan as their own
		// token kind rather than KindIdentifier, but are still legal bind
		// targets: `out <- e`.
		if p.inEffectBody && p.isBindingIdentifier() && p.lookAhead((*Parser).nextIsBindArrow) {
			return p.parseBindStatement()
		}
	}
	return nil
}

// nextIsBindingPatternBind scans a balanced binding pattern ({...} or [...])
// from the current token and reports whether a BindArrow follows it, which
// makes the statement a destructuring bind rather than a block / expression.
func (p *Parser) nextIsBindingPatternBind() bool {
	open := p.token
	closeKind := ast.KindCloseBraceToken
	if open == ast.KindOpenBracketToken {
		closeKind = ast.KindCloseBracketToken
	}
	depth := 0
	for {
		switch p.token {
		case open:
			depth++
		case closeKind:
			depth--
			if depth == 0 {
				p.nextToken()
				return p.isAtBindArrow()
			}
		case ast.KindEndOfFile:
			return false
		}
		p.nextToken()
	}
}

func (p *Parser) nextIsServiceDeclarationStart() bool {
	p.nextToken()
	if p.token != ast.KindIdentifier || p.hasPrecedingLineBreak() {
		return false
	}
	return p.nextToken() == ast.KindOpenBraceToken
}

func (p *Parser) nextIsLayerDeclarationStart() bool {
	p.nextToken()
	if p.token != ast.KindIdentifier || p.hasPrecedingLineBreak() {
		return false
	}
	return p.nextToken() == ast.KindColonToken
}

func (p *Parser) nextIsScopedLayerDeclarationStart() bool {
	p.nextToken()
	if p.token != ast.KindIdentifier || p.scanner.TokenValue() != "layer" || p.hasPrecedingLineBreak() {
		return false
	}
	return p.nextIsLayerDeclarationStart()
}

func (p *Parser) nextIsDeferBodyStart() bool {
	switch p.nextToken() {
	case ast.KindOpenBraceToken:
		return true
	case ast.KindOpenParenToken:
		if p.nextToken() != ast.KindIdentifier {
			return false
		}
		if p.nextToken() != ast.KindCloseParenToken {
			return false
		}
		return p.nextToken() == ast.KindOpenBraceToken
	}
	return false
}

// tryParseEffectScriptDeclaration handles the modifier-prefixed forms
// (export effect/service/layer, export scoped layer), called from
// parseDeclarationWorker once modifiers have been consumed.
func (p *Parser) tryParseEffectScriptDeclaration(pos int, modifiers *ast.ModifierList) *ast.Statement {
	if p.token != ast.KindIdentifier {
		return nil
	}
	switch p.scanner.TokenValue() {
	case "effect":
		if p.lookAhead((*Parser).nextIsEffectDeclarationStart) {
			return p.parseEffectDeclaration(pos, modifiers)
		}
	case "service":
		if p.lookAhead((*Parser).nextIsServiceDeclarationStart) {
			return p.parseServiceDeclaration(pos, modifiers)
		}
	case "layer":
		if p.lookAhead((*Parser).nextIsLayerDeclarationStart) {
			return p.parseLayerDeclaration(pos, modifiers, false /*scoped*/)
		}
	case "scoped":
		if p.lookAhead((*Parser).nextIsScopedLayerDeclarationStart) {
			return p.parseLayerDeclaration(pos, modifiers, true /*scoped*/)
		}
	}
	return nil
}

// scanStartOfEffectScriptDeclaration extends scanStartOfDeclaration for the
// EffectScript declaration keywords (so `export effect f() {}` routes into
// parseDeclaration).
func (p *Parser) scanStartOfEffectScriptDeclaration() bool {
	if p.token != ast.KindIdentifier {
		return false
	}
	switch p.scanner.TokenValue() {
	case "effect":
		return p.nextIsEffectDeclarationStart()
	case "service":
		return p.nextIsServiceDeclarationStart()
	case "layer":
		return p.nextIsLayerDeclarationStart()
	case "scoped":
		return p.nextIsScopedLayerDeclarationStart()
	}
	return false
}

func (p *Parser) nextIsEffectDeclarationStart() bool {
	p.nextToken()
	// No line terminator is permitted between 'effect' and the name.
	if p.token != ast.KindIdentifier || p.hasPrecedingLineBreak() {
		return false
	}
	p.nextToken()
	return p.token == ast.KindOpenParenToken
}

func (p *Parser) nextIsRaiseOperandStart() bool {
	p.nextToken()
	if p.hasPrecedingLineBreak() {
		return false
	}
	// Reject shapes that suggest 'raise' is used as a plain identifier
	// (assignment, label, call of a user function named raise, etc.).
	switch p.token {
	case ast.KindEqualsToken, ast.KindColonToken, ast.KindSemicolonToken, ast.KindCommaToken,
		ast.KindOpenParenToken, ast.KindCloseParenToken, ast.KindCloseBraceToken, ast.KindCloseBracketToken,
		ast.KindEqualsEqualsToken, ast.KindEqualsEqualsEqualsToken, ast.KindEqualsGreaterThanToken, ast.KindQuestionToken:
		return false
	case ast.KindDotToken:
		// Only 'raise.die <operand>' is the keyword form.
		p.nextToken()
		return p.token == ast.KindIdentifier && p.scanner.TokenValue() == "die"
	}
	return true
}

// isAtBindArrow reports whether the current '<' token is immediately followed
// by '-' in the source text, forming the BindArrow '<-'. The '-' must not
// start '--' or '-=' (those keep their usual meaning).
func (p *Parser) isAtBindArrow() bool {
	if p.token != ast.KindLessThanToken {
		return false
	}
	end := p.scanner.TokenEnd()
	if end >= len(p.sourceText) || p.sourceText[end] != '-' {
		return false
	}
	if end+1 < len(p.sourceText) && (p.sourceText[end+1] == '-' || p.sourceText[end+1] == '=') {
		return false
	}
	return true
}

func (p *Parser) nextIsBindArrow() bool {
	p.nextToken()
	if p.token == ast.KindColonToken {
		// typed bind: x: T <- e   (the lookahead wrapper restores all state)
		p.nextToken()
		p.parseType()
		return p.isAtBindArrow()
	}
	return p.isAtBindArrow()
}

// consumeBindArrow consumes the '<' '-' token pair of a BindArrow. The caller
// must have verified isAtBindArrow.
func (p *Parser) consumeBindArrow() {
	p.parseExpected(ast.KindLessThanToken)
	p.parseExpected(ast.KindMinusToken)
}

// x <- e;  /  {p} <- e;  /  [p] <- e;   ==>   const <target> = yield* e;
func (p *Parser) parseBindStatement() *ast.Statement {
	pos := p.nodePos()
	var name *ast.Node
	switch p.token {
	case ast.KindOpenBraceToken:
		name = p.parseObjectBindingPattern()
	case ast.KindOpenBracketToken:
		name = p.parseArrayBindingPattern()
	default:
		name = p.parseIdentifier()
	}
	var typeNode *ast.TypeNode
	if p.token == ast.KindColonToken {
		p.nextToken()
		typeNode = p.parseType()
	}
	p.consumeBindArrow()
	operandPos := p.nodePos()
	operand := p.parseAssignmentExpressionOrHigher()
	p.parseSemicolon()
	end := p.nodePos()

	yieldExpr := p.makeYieldStar(operand, operandPos, end)
	decl := p.finishNodeWithEnd(p.factory.NewVariableDeclaration(name, nil, typeNode, yieldExpr), pos, end)
	declList := p.finishNodeWithEnd(p.factory.NewVariableDeclarationList(p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{decl}), ast.NodeFlagsConst), pos, end)
	return p.finishNodeWithEnd(p.factory.NewVariableStatement(nil, declList), pos, end)
}

// <- e;   ==>   yield* e;
func (p *Parser) parseDiscardBindStatement() *ast.Statement {
	pos := p.nodePos()
	p.consumeBindArrow()
	operandPos := p.nodePos()
	operand := p.parseAssignmentExpressionOrHigher()
	p.parseSemicolon()
	end := p.nodePos()
	yieldExpr := p.makeYieldStar(operand, operandPos, end)
	return p.finishNodeWithEnd(p.factory.NewExpressionStatement(yieldExpr), pos, end)
}

// raise e;       ==>   return yield* Effect.fail(e);
// raise.die e;   ==>   return yield* Effect.die(e);
func (p *Parser) parseRaiseStatement() *ast.Statement {
	pos := p.nodePos()
	p.nextToken() // consume 'raise'
	method := "fail"
	if p.token == ast.KindDotToken {
		p.nextToken()
		p.nextToken() // consume 'die' (validated by lookahead)
		method = "die"
	}
	operandPos := p.nodePos()
	operand := p.parseAssignmentExpressionOrHigher()
	p.parseSemicolon()
	end := p.nodePos()

	call := p.makeEffectCall(method, []*ast.Node{operand}, pos, end)
	yieldExpr := p.makeYieldStar(call, operandPos, end)
	return p.finishNodeWithEnd(p.factory.NewReturnStatement(yieldExpr), pos, end)
}

// effect name(params)[: T] { body }
//
//	==>  const name = Effect.fn("name")(function* (params) { body });
func (p *Parser) parseEffectDeclaration(pos int, modifiers *ast.ModifierList) *ast.Statement {
	p.nextToken() // consume 'effect'
	name := p.parseIdentifier()
	nameText := name.Text()
	parameters := p.parseParameters(ParseFlagsYield)
	// The annotation describes the resulting Effect, not the generator; it is
	// parsed and dropped here (checking against it is a later phase).
	p.parseReturnType(ast.KindColonToken, false /*isType*/)
	body := p.parseEffectFunctionBlock()
	end := p.nodePos()

	// Decorators become Effect.fn pipe combinators (top decorator outermost,
	// i.e. last argument); non-decorator modifiers (export, ...) stay on the
	// emitted variable statement.
	keptModifiers := modifiers
	var combinators []*ast.Node
	if modifiers != nil {
		var kept []*ast.Node
		var decorators []*ast.Node
		for _, m := range modifiers.Nodes {
			if ast.IsDecorator(m) {
				decorators = append(decorators, m)
			} else {
				kept = append(kept, m)
			}
		}
		for i := len(decorators) - 1; i >= 0; i-- {
			combinators = append(combinators, p.effectCombinatorFromDecorator(decorators[i]))
		}
		if len(decorators) > 0 {
			if len(kept) == 0 {
				keptModifiers = nil
			} else {
				keptModifiers = p.newModifierList(modifiers.Loc, kept)
			}
		}
	}

	funcExpr := p.makeGeneratorExpression(parameters, body, pos, end)
	fn := p.makeEffectCall("fn", []*ast.Node{p.makeStringLiteral(nameText, pos)}, pos, end)
	// Effect.fn("name")(generator, ...combinators) — pipe combinators follow
	// the generator in the second call (top decorator last = outermost).
	outerArgs := append([]*ast.Node{funcExpr}, combinators...)
	outer := p.finishNodeWithEnd(p.factory.NewCallExpression(fn, nil, nil, p.newNodeList(core.NewTextRange(pos, end), outerArgs), ast.NodeFlagsNone), pos, end)
	decl := p.finishNodeWithEnd(p.factory.NewVariableDeclaration(name, nil, nil, outer), pos, end)
	declList := p.finishNodeWithEnd(p.factory.NewVariableDeclarationList(p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{decl}), ast.NodeFlagsConst), pos, end)
	return p.finishNodeWithEnd(p.factory.NewVariableStatement(keptModifiers, declList), pos, end)
}

// effectCombinatorFromDecorator maps a decorator on an effect declaration to a
// pipe combinator: well-known bare names resolve to Effect.*; anything else is
// used as-is.
var effectWellKnownCombinators = map[string]bool{
	"retry": true, "timeout": true, "withSpan": true, "uninterruptible": true,
	"interruptible": true, "annotateLogs": true, "tapError": true, "provide": true,
	"ensuring": true,
}

func (p *Parser) effectCombinatorFromDecorator(decorator *ast.Node) *ast.Expression {
	expr := decorator.Expression()
	root := expr
	for root.Kind == ast.KindCallExpression {
		root = root.AsCallExpression().Expression
	}
	if root.Kind == ast.KindIdentifier && effectWellKnownCombinators[root.Text()] {
		p.useEffectHelper("Effect")
		effectIdent := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("Effect")))
		methodIdent := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(root.Text())))
		access := p.finishSynthesized(p.factory.NewPropertyAccessExpression(effectIdent, nil, methodIdent, ast.NodeFlagsNone))
		if expr.Kind == ast.KindCallExpression {
			call := expr.AsCallExpression()
			return p.finishSynthesized(p.factory.NewCallExpression(access, nil, nil, call.Arguments, ast.NodeFlagsNone))
		}
		return access
	}
	return expr
}

// effect name(params) { body } (expression position)
//
//	==>  Effect.fn("name")(function* (params) { body })
func (p *Parser) parseNamedEffectFnExpression() *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'effect'
	nameText := p.parseIdentifier().Text()
	parameters := p.parseParameters(ParseFlagsYield)
	p.parseReturnType(ast.KindColonToken, false /*isType*/)
	body := p.parseEffectFunctionBlock()
	end := p.nodePos()
	funcExpr := p.makeGeneratorExpression(parameters, body, pos, end)
	fn := p.makeEffectCall("fn", []*ast.Node{p.makeStringLiteral(nameText, pos)}, pos, end)
	return p.finishNodeWithEnd(p.factory.NewCallExpression(fn, nil, nil, p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{funcExpr}), ast.NodeFlagsNone), pos, end)
}

// effect { body }   ==>   Effect.gen(function* () { body })
func (p *Parser) parseEffectBlockExpression() *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'effect'
	body := p.parseEffectFunctionBlock()
	end := p.nodePos()

	emptyParams := p.newNodeList(core.NewTextRange(pos, pos), nil)
	funcExpr := p.makeGeneratorExpression(emptyParams, body, pos, end)
	return p.makeEffectCall("gen", []*ast.Node{funcExpr}, pos, end)
}

// (<- e)   ==>   (yield* e)   [hooked from parseParenthesizedExpression]
func (p *Parser) parseBindParenExpression() *ast.Expression {
	pos := p.nodePos()
	p.parseExpected(ast.KindOpenParenToken)
	p.consumeBindArrow()
	operandPos := p.nodePos()
	operand := p.parseAssignmentExpressionOrHigher()
	p.parseExpected(ast.KindCloseParenToken)
	end := p.nodePos()
	yieldExpr := p.makeYieldStar(operand, operandPos, end)
	return p.finishNodeWithEnd(p.factory.NewParenthesizedExpression(yieldExpr), pos, end)
}

// parseEffectFunctionBlock parses a block as an effect body: yield context is
// active (the body lowers into a generator) and the EffectScript statement
// forms are enabled. Mirrors parseFunctionBlock, which by contrast *clears*
// inEffectBody for ordinary nested functions.
func (p *Parser) parseEffectFunctionBlock() *ast.Node {
	saveContextFlags := p.contextFlags
	saveHasAwaitIdentifier := p.statementHasAwaitIdentifier
	saveInEffectBody := p.inEffectBody
	p.inEffectBody = true
	p.setContextFlags(ast.NodeFlagsYieldContext, true)
	p.setContextFlags(ast.NodeFlagsAwaitContext, false)
	p.setContextFlags(ast.NodeFlagsDecoratorContext, false)
	block := p.parseBlock(false /*ignoreMissingOpenBrace*/, nil /*diagnosticMessage*/)
	p.contextFlags = saveContextFlags
	p.statementHasAwaitIdentifier = saveHasAwaitIdentifier
	p.inEffectBody = saveInEffectBody
	return block
}

// ---- synthesis helpers ----

// finishSynthesized marks a desugarer-created leaf node as synthesized
// (negative positions) so the printer emits its text instead of slicing the
// original source, and wires up parent pointers.
func (p *Parser) finishSynthesized(node *ast.Node) *ast.Node {
	node.Loc = core.NewTextRange(-1, -1)
	// NodeFlagsReparsed makes position-driven machinery (astnav token search,
	// LSP navigation) skip these nodes, exactly like JSDoc reparse nodes.
	node.Flags |= p.contextFlags | ast.NodeFlagsReparsed
	p.overrideParentInImmediateChildren(node)
	return node
}

func (p *Parser) makeYieldStar(operand *ast.Expression, pos int, end int) *ast.Expression {
	asterisk := p.finishNodeWithEnd(p.factory.NewToken(ast.KindAsteriskToken), pos, pos)
	return p.finishNodeWithEnd(p.factory.NewYieldExpression(asterisk, operand), pos, end)
}

// makeEffectCall builds Effect.<method>(args...) and records that the helper
// import is needed.
func (p *Parser) makeEffectCall(method string, args []*ast.Node, pos int, end int) *ast.Expression {
	return p.makeHelperCall("Effect", method, args, pos, end)
}

// makeHelperCall builds <helper>.<method>(args...) for one of the auto-imported
// helper namespaces (Effect, Layer, Context, Fiber) and records the usage.
//
// When every argument is itself synthesized the whole call is marked
// synthesized so position-driven tooling skips it; when a real subtree is
// among the arguments the call spans the construct so navigation can descend
// into it.
func (p *Parser) makeHelperCall(helper string, method string, args []*ast.Node, pos int, end int) *ast.Expression {
	access := p.makeHelperAccess(helper, method)
	if !anyHasRealPositions(args) {
		return p.finishSynthesized(p.factory.NewCallExpression(access, nil, nil, p.newNodeList(core.NewTextRange(-1, -1), args), ast.NodeFlagsNone))
	}
	return p.finishNodeWithEnd(p.factory.NewCallExpression(access, nil, nil, p.newNodeList(core.NewTextRange(pos, end), args), ast.NodeFlagsNone), pos, end)
}

func anyHasRealPositions(nodes []*ast.Node) bool {
	for _, n := range nodes {
		if n != nil && !ast.NodeIsSynthesized(n) {
			return true
		}
	}
	return false
}

// makePipeAccess builds receiver.pipe spanning the receiver, so navigation
// still descends into real receiver subtrees.
func (p *Parser) makePipeAccess(receiver *ast.Expression) *ast.Expression {
	pipeName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("pipe")))
	access := p.factory.NewPropertyAccessExpression(receiver, nil, pipeName, ast.NodeFlagsNone)
	if ast.NodeIsSynthesized(receiver) {
		return p.finishSynthesized(access)
	}
	return p.finishNodeWithEnd(access, receiver.Pos(), receiver.End())
}

func (p *Parser) makeHelperAccess(helper string, method string) *ast.Expression {
	p.useEffectHelper(helper)
	helperIdent := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(helper)))
	methodIdent := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(method)))
	return p.finishSynthesized(p.factory.NewPropertyAccessExpression(helperIdent, nil, methodIdent, ast.NodeFlagsNone))
}

func (p *Parser) useEffectHelper(helper string) {
	if p.effectHelpersUsed == nil {
		p.effectHelpersUsed = make(map[string]struct{}, 4)
	}
	p.effectHelpersUsed[helper] = struct{}{}
}

func (p *Parser) makeStringLiteral(text string, pos int) *ast.Expression {
	_ = pos
	return p.finishSynthesized(p.factory.NewStringLiteral(text, ast.TokenFlagsNone))
}

func (p *Parser) makeGeneratorExpression(parameters *ast.NodeList, body *ast.Node, pos int, end int) *ast.Expression {
	asterisk := p.finishNodeWithEnd(p.factory.NewToken(ast.KindAsteriskToken), pos, pos)
	return p.finishNodeWithEnd(p.factory.NewFunctionExpression(nil, asterisk, nil, nil, parameters, nil, nil, body), pos, end)
}

// effectHelperImportOrder fixes the specifier order of the synthesized import.
var effectHelperImportOrder = []string{"Effect", "Layer", "Context", "Fiber", "Match"}

// injectEffectScriptImports prepends `import { Effect, Layer, ... } from
// "effect";` for every helper namespace the lowering referenced that the user
// has not bound via a top-level import already.
func (p *Parser) injectEffectScriptImports(statements []*ast.Node) []*ast.Node {
	if len(p.effectHelpersUsed) == 0 {
		return statements
	}
	var specs []*ast.Node
	for _, helper := range effectHelperImportOrder {
		if _, used := p.effectHelpersUsed[helper]; !used {
			continue
		}
		if p.hasTopLevelImportOf(statements, helper) {
			continue
		}
		specName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(helper)))
		specs = append(specs, p.finishSynthesized(p.factory.NewImportSpecifier(false /*isTypeOnly*/, nil, specName)))
	}
	if len(specs) == 0 {
		return statements
	}
	named := p.finishSynthesized(p.factory.NewNamedImports(p.newNodeList(core.NewTextRange(-1, -1), specs)))
	clause := p.finishSynthesized(p.factory.NewImportClause(ast.KindUnknown, nil, named))
	module := p.finishSynthesized(p.factory.NewStringLiteral("effect", ast.TokenFlagsNone))
	imp := p.finishNodeWithEnd(p.factory.NewImportDeclaration(nil, clause, module, nil), 0, 0)
	return append([]*ast.Node{imp}, statements...)
}

func (p *Parser) hasTopLevelImportOf(statements []*ast.Node, name string) bool {
	for _, s := range statements {
		if s.Kind != ast.KindImportDeclaration {
			continue
		}
		clause := s.AsImportDeclaration().ImportClause
		if clause == nil {
			continue
		}
		if n := clause.Name(); n != nil && n.Text() == name {
			return true
		}
		if bindings := clause.AsImportClause().NamedBindings; bindings != nil {
			if bindings.Kind == ast.KindNamespaceImport && bindings.Name() != nil && bindings.Name().Text() == name {
				return true
			}
			if bindings.Kind == ast.KindNamedImports {
				for _, el := range bindings.AsNamedImports().Elements.Nodes {
					if el.Name() != nil && el.Name().Text() == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// ---- Cycle 5 constructs: service, layer, concurrency, defer, pipeline ----

// service Name { members }
//
//	==>  class Name extends Context.Tag("Name")<Name, { members }>() {}
func (p *Parser) parseServiceDeclaration(pos int, modifiers *ast.ModifierList) *ast.Statement {
	p.nextToken() // consume 'service'
	name := p.parseIdentifier()
	nameText := name.Text()
	shape := p.parseTypeLiteral()
	end := p.nodePos()

	tagCall := p.makeHelperCall("Context", "Tag", []*ast.Node{p.makeStringLiteral(nameText, pos)}, pos, end)
	selfRef := p.finishSynthesized(p.factory.NewTypeReferenceNode(p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(nameText))), nil))
	typeArgs := p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{selfRef, shape})
	instantiated := p.finishSynthesized(p.factory.NewCallExpression(tagCall, nil, typeArgs, p.newNodeList(core.NewTextRange(-1, -1), nil), ast.NodeFlagsNone))
	withTypeArgs := p.finishSynthesized(p.factory.NewExpressionWithTypeArguments(instantiated, nil))
	heritage := p.finishSynthesized(p.factory.NewHeritageClause(ast.KindExtendsKeyword, p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{withTypeArgs})))
	heritageList := p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{heritage})
	members := p.newNodeList(core.NewTextRange(-1, -1), nil)
	return p.finishNodeWithEnd(p.factory.NewClassDeclaration(modifiers, name, nil, heritageList, members), pos, end)
}

// [scoped] layer Name: Tag [provide [a, b]] { body }   /   layer Name: Tag = expr
//
//	==>  const Name = Layer.effect(Tag, Effect.gen(function* () { body }));
//	     const Name = Layer.scoped(Tag, Effect.gen(function* () { body }));
//	     const Name = Layer.succeed(Tag, expr);
//	     ... .pipe(Layer.provide([a, b])) when a provide clause is present
func (p *Parser) parseLayerDeclaration(pos int, modifiers *ast.ModifierList, scoped bool) *ast.Statement {
	if scoped {
		p.nextToken() // consume 'scoped'
	}
	p.nextToken() // consume 'layer'
	name := p.parseIdentifier()
	p.parseExpected(ast.KindColonToken)
	tag := p.parseEntityNameExpression()

	var provided *ast.Expression
	if p.token == ast.KindIdentifier && p.scanner.TokenValue() == "provide" && p.lookAhead((*Parser).nextTokenIsOpenBracket) {
		p.nextToken() // consume 'provide'
		provided = p.parseArrayLiteralExpression()
	}

	var layerExpr *ast.Expression
	if p.token == ast.KindEqualsToken {
		p.nextToken()
		value := p.parseAssignmentExpressionOrHigher()
		p.parseSemicolon()
		layerExpr = p.makeHelperCall("Layer", "succeed", []*ast.Node{tag, value}, pos, p.nodePos())
	} else {
		body := p.parseEffectFunctionBlock()
		end := p.nodePos()
		emptyParams := p.newNodeList(core.NewTextRange(-1, -1), nil)
		gen := p.makeEffectCall("gen", []*ast.Node{p.makeGeneratorExpression(emptyParams, body, pos, end)}, pos, end)
		method := "effect"
		if scoped {
			method = "scoped"
		}
		layerExpr = p.makeHelperCall("Layer", method, []*ast.Node{tag, gen}, pos, end)
	}
	end := p.nodePos()

	if provided != nil {
		provideCall := p.makeHelperCall("Layer", "provide", []*ast.Node{provided}, pos, end)
		pipeAccess := p.makePipeAccess(layerExpr)
		layerExpr = p.finishNodeWithEnd(p.factory.NewCallExpression(pipeAccess, nil, nil, p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{provideCall}), ast.NodeFlagsNone), pos, end)
	}

	decl := p.finishNodeWithEnd(p.factory.NewVariableDeclaration(name, nil, nil, layerExpr), pos, end)
	declList := p.finishNodeWithEnd(p.factory.NewVariableDeclarationList(p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{decl}), ast.NodeFlagsConst), pos, end)
	return p.finishNodeWithEnd(p.factory.NewVariableStatement(modifiers, declList), pos, end)
}

// parseEntityNameExpression parses a dotted name (Db / pkg.Db) as an
// expression, for positions where a service tag is referenced as a value.
func (p *Parser) parseEntityNameExpression() *ast.Expression {
	pos := p.nodePos()
	expr := p.parseIdentifier()
	for p.token == ast.KindDotToken {
		p.nextToken()
		expr = p.finishNodeWithEnd(p.factory.NewPropertyAccessExpression(expr, nil, p.parseIdentifier(), ast.NodeFlagsNone), pos, p.nodePos())
	}
	return expr
}

func (p *Parser) nextTokenIsOpenBracket() bool {
	return p.nextToken() == ast.KindOpenBracketToken
}

// defer { body } / defer (exit) { body }
//
//	==>  yield* Effect.addFinalizer((exit?) => Effect.gen(function* () { body }));
func (p *Parser) parseDeferStatement() *ast.Statement {
	pos := p.nodePos()
	p.nextToken() // consume 'defer'
	var params []*ast.Node
	if p.token == ast.KindOpenParenToken {
		p.nextToken()
		exitName := p.parseIdentifier()
		param := p.finishSynthesized(p.factory.NewParameterDeclaration(nil, nil, exitName, nil, nil, nil))
		params = append(params, param)
		p.parseExpected(ast.KindCloseParenToken)
	}
	body := p.parseEffectFunctionBlock()
	end := p.nodePos()

	emptyParams := p.newNodeList(core.NewTextRange(-1, -1), nil)
	gen := p.makeEffectCall("gen", []*ast.Node{p.makeGeneratorExpression(emptyParams, body, pos, end)}, pos, end)
	arrowToken := p.finishSynthesized(p.factory.NewToken(ast.KindEqualsGreaterThanToken))
	arrow := p.finishNodeWithEnd(p.factory.NewArrowFunction(nil, nil, p.newNodeList(core.NewTextRange(-1, -1), params), nil, nil, arrowToken, gen), pos, end)
	call := p.makeEffectCall("addFinalizer", []*ast.Node{arrow}, pos, end)
	yieldExpr := p.makeYieldStar(call, pos, end)
	p.parseSemicolon()
	return p.finishNodeWithEnd(p.factory.NewExpressionStatement(yieldExpr), pos, p.nodePos())
}

// raise e (expression)  ==>  (yield* Effect.fail(e)), which has type never
func (p *Parser) parseRaiseExpression() *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'raise'
	method := "fail"
	if p.token == ast.KindDotToken {
		p.nextToken()
		p.nextToken() // consume 'die' (validated by lookahead)
		method = "die"
	}
	operand := p.parseSimpleUnaryExpression()
	end := p.nodePos()
	call := p.makeEffectCall(method, []*ast.Node{operand}, pos, end)
	yieldExpr := p.makeYieldStar(call, pos, end)
	return p.finishNodeWithEnd(p.factory.NewParenthesizedExpression(yieldExpr), pos, end)
}

// fork e  ==>  Effect.fork(e)        join e  ==>  Fiber.join(e)
func (p *Parser) parseForkOrJoinExpression(helper string, method string) *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'fork' / 'join'
	operand := p.parseSimpleUnaryExpression()
	end := p.nodePos()
	return p.makeHelperCall(helper, method, []*ast.Node{operand}, pos, end)
}

// par [a, b] / par { k: a } / par(n) [...]  ==>  Effect.all(..., { concurrency })
// race [a, b]                               ==>  Effect.race / Effect.raceAll
func (p *Parser) parseParExpression() *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'par'
	var concurrency *ast.Expression
	if p.token == ast.KindOpenParenToken {
		p.nextToken()
		concurrency = p.parseAssignmentExpressionOrHigher()
		p.parseExpected(ast.KindCloseParenToken)
	}
	var collection *ast.Expression
	if p.token == ast.KindOpenBracketToken {
		collection = p.parseArrayLiteralExpression()
	} else {
		collection = p.parseObjectLiteralExpression()
	}
	end := p.nodePos()

	if concurrency == nil {
		concurrency = p.makeStringLiteral("unbounded", pos)
	}
	concName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("concurrency")))
	concProp := p.finishSynthesized(p.factory.NewPropertyAssignment(nil, concName, nil, nil, concurrency))
	options := p.finishSynthesized(p.factory.NewObjectLiteralExpression(p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{concProp}), false))
	return p.makeEffectCall("all", []*ast.Node{collection, options}, pos, end)
}

func (p *Parser) parseRaceExpression() *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'race'
	array := p.parseArrayLiteralExpression()
	end := p.nodePos()
	elements := array.AsArrayLiteralExpression().Elements.Nodes
	if len(elements) == 2 {
		return p.makeEffectCall("race", []*ast.Node{elements[0], elements[1]}, pos, end)
	}
	return p.makeEffectCall("raceAll", []*ast.Node{array}, pos, end)
}

// tryParseEffectScriptExpression is the primary-expression hook for keyword-led
// effect expressions ('effect {', 'fork', 'join', 'par', 'race').
func (p *Parser) tryParseEffectScriptExpression() *ast.Expression {
	if p.token != ast.KindIdentifier {
		return nil
	}
	switch p.scanner.TokenValue() {
	case "effect":
		if !p.lookAhead((*Parser).nextTokenHasPrecedingLineBreak) && p.lookAhead((*Parser).nextTokenIsOpenBrace) {
			return p.parseEffectBlockExpression()
		}
		if p.lookAhead((*Parser).nextIsEffectFunctionExpression) {
			return p.parseEffectFunctionExpression()
		}
		if p.lookAhead((*Parser).nextIsEffectDeclarationStart) {
			// named effect fn in expression position (e.g. export default
			// effect entry() {...}): the name becomes the tracing span only.
			return p.parseNamedEffectFnExpression()
		}
	case "raise":
		if p.inEffectBody && p.lookAhead((*Parser).nextIsRaiseOperandStart) {
			return p.parseRaiseExpression()
		}
	case "match":
		if p.lookAhead((*Parser).nextIsMatchExpressionStart) {
			return p.parseMatchExpression()
		}
	case "fork":
		if p.inEffectBody && p.lookAhead((*Parser).nextIsUnaryOperandStart) {
			return p.parseForkOrJoinExpression("Effect", "fork")
		}
	case "join":
		if p.inEffectBody && p.lookAhead((*Parser).nextIsUnaryOperandStart) {
			return p.parseForkOrJoinExpression("Fiber", "join")
		}
	case "par":
		if p.inEffectBody && p.lookAhead((*Parser).nextIsParCollectionStart) {
			return p.parseParExpression()
		}
	case "race":
		if p.inEffectBody && !p.lookAhead((*Parser).nextTokenHasPrecedingLineBreak) && p.lookAhead((*Parser).nextTokenIsOpenBracket) {
			return p.parseRaceExpression()
		}
	}
	return nil
}

func (p *Parser) nextIsUnaryOperandStart() bool {
	p.nextToken()
	if p.hasPrecedingLineBreak() {
		return false
	}
	switch p.token {
	case ast.KindIdentifier, ast.KindThisKeyword, ast.KindNewKeyword, ast.KindOpenParenToken:
		return true
	}
	return false
}

func (p *Parser) nextIsParCollectionStart() bool {
	p.nextToken()
	if p.hasPrecedingLineBreak() {
		return false
	}
	switch p.token {
	case ast.KindOpenBracketToken, ast.KindOpenBraceToken:
		return true
	case ast.KindOpenParenToken:
		// par(n) [...] / par(n) {...}: scan past the balanced parens.
		depth := 0
		for {
			switch p.token {
			case ast.KindOpenParenToken:
				depth++
			case ast.KindCloseParenToken:
				depth--
				if depth == 0 {
					next := p.nextToken()
					return next == ast.KindOpenBracketToken || next == ast.KindOpenBraceToken
				}
			case ast.KindEndOfFile:
				return false
			}
			p.nextToken()
		}
	}
	return false
}

// isAtPipeOperator reports whether the current '|' token starts the
// EffectScript pipeline operator '|>'.
func (p *Parser) isAtPipeOperator() bool {
	if !p.isEffectScript() || p.token != ast.KindBarToken {
		return false
	}
	end := p.scanner.TokenEnd()
	if end >= len(p.sourceText) || p.sourceText[end] != '>' {
		return false
	}
	if end+1 < len(p.sourceText) && p.sourceText[end+1] == '=' {
		return false
	}
	return true
}

// ---- Cycle 6: postfix catch arms ----

// expr catch { Tag as e >> handler  Tag1 | Tag2 as e >> handler  _ as e >> handler }
//
//	==>  expr.pipe(
//	       Effect.catchTag("Tag", (e) => Effect.gen(function* () { ... })),
//	       (h => Effect.catchTags({ Tag1: h, Tag2: h }))((e) => ...),  // shared handler
//	       Effect.catchAll((e) => ...),
//	     )
func (p *Parser) parseCatchArmsPostfix(expr *ast.Expression, pos int) *ast.Expression {
	p.parseExpected(ast.KindCatchKeyword)
	p.parseExpected(ast.KindOpenBraceToken)

	var handlers []*ast.Node
	sawCatchAll := false
	for p.token != ast.KindCloseBraceToken && p.token != ast.KindEndOfFile {
		startTok := p.nodePos()
		if sawCatchAll {
			p.parseErrorAt(p.nodePos(), p.nodePos()+1, diagnostics.Unreachable_match_arm_Colon_it_follows_a_catch_all_arm)
		}
		handler, isCatchAll := p.parseCatchArm()
		sawCatchAll = sawCatchAll || isCatchAll
		handlers = append(handlers, handler)
		if p.nodePos() == startTok {
			p.nextToken() // progress guard
		}
	}
	p.parseExpected(ast.KindCloseBraceToken)
	end := p.nodePos()

	pipeAccess := p.makePipeAccess(expr)
	return p.finishNodeWithEnd(p.factory.NewCallExpression(pipeAccess, nil, nil, p.newNodeList(core.NewTextRange(pos, end), handlers), ast.NodeFlagsNone), pos, end)
}

func (p *Parser) parseCatchArm() (arm *ast.Node, isCatchAll bool) {
	armPos := p.nodePos()

	// Pattern: '_' or Tag { '|' Tag }
	var tags []string
	first := p.parseIdentifier()
	if first.Text() == "_" {
		isCatchAll = true
	} else {
		tags = append(tags, first.Text())
		for p.token == ast.KindBarToken && !p.isAtPipeOperator() {
			p.nextToken()
			tags = append(tags, p.parseIdentifier().Text())
		}
	}

	// Optional binding: 'as e'
	bindingName := "_"
	if p.token == ast.KindAsKeyword {
		p.nextToken()
		bindingName = p.parseIdentifier().Text()
	}

	// '>>'
	p.reScanGreaterThanToken()
	p.parseExpected(ast.KindGreaterThanGreaterThanToken)

	// Handler body: effect block or expression (lowered into Effect.gen).
	bodyPos := p.nodePos()
	var block *ast.Node
	if p.token == ast.KindOpenBraceToken {
		block = p.parseEffectFunctionBlock()
	} else {
		saveInEffectBody := p.inEffectBody
		p.inEffectBody = true
		value := p.parseAssignmentExpressionOrHigher()
		p.inEffectBody = saveInEffectBody
		ret := p.finishNodeWithEnd(p.factory.NewReturnStatement(value), bodyPos, p.nodePos())
		block = p.finishNodeWithEnd(p.factory.NewBlock(p.newNodeList(core.NewTextRange(bodyPos, p.nodePos()), []*ast.Node{ret}), false), bodyPos, p.nodePos())
	}
	armEnd := p.nodePos()

	emptyParams := p.newNodeList(core.NewTextRange(-1, -1), nil)
	gen := p.makeEffectCall("gen", []*ast.Node{p.makeGeneratorExpression(emptyParams, block, armPos, armEnd)}, armPos, armEnd)
	handler := p.makeSingleParamArrow(bindingName, gen, armPos, armEnd)

	switch {
	case isCatchAll:
		return p.makeEffectCall("catchAll", []*ast.Node{handler}, armPos, armEnd), true
	case len(tags) == 1:
		return p.makeEffectCall("catchTag", []*ast.Node{p.makeStringLiteral(tags[0], armPos), handler}, armPos, armEnd), false
	default:
		// (h => Effect.catchTags({ Tag1: h, Tag2: h }))(handler) — one shared
		// handler function without needing a statement position.
		var props []*ast.Node
		for _, tag := range tags {
			tagName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(tag)))
			href := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("h")))
			props = append(props, p.finishSynthesized(p.factory.NewPropertyAssignment(nil, tagName, nil, nil, href)))
		}
		obj := p.finishSynthesized(p.factory.NewObjectLiteralExpression(p.newNodeList(core.NewTextRange(-1, -1), props), false))
		catchTags := p.makeEffectCall("catchTags", []*ast.Node{obj}, armPos, armEnd)
		iife := p.makeSingleParamArrow("h", catchTags, armPos, armEnd)
		paren := p.finishSynthesized(p.factory.NewParenthesizedExpression(iife))
		return p.finishNodeWithEnd(p.factory.NewCallExpression(paren, nil, nil, p.newNodeList(core.NewTextRange(armPos, armEnd), []*ast.Node{handler}), ast.NodeFlagsNone), armPos, armEnd), false
	}
}

func (p *Parser) makeSingleParamArrow(paramName string, body *ast.Expression, pos int, end int) *ast.Expression {
	nameIdent := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(paramName)))
	param := p.finishSynthesized(p.factory.NewParameterDeclaration(nil, nil, nameIdent, nil, nil, nil))
	params := p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{param})
	arrowToken := p.finishSynthesized(p.factory.NewToken(ast.KindEqualsGreaterThanToken))
	return p.finishNodeWithEnd(p.factory.NewArrowFunction(nil, nil, params, nil, nil, arrowToken, body), pos, end)
}

// isAtPostfixCatch reports whether a postfix 'catch { ... }' follows the
// just-parsed expression (only inside effect bodies; try statements parse
// their catch clause at statement level and never reach here).
func (p *Parser) isAtPostfixCatch() bool {
	if !p.inEffectBody || p.token != ast.KindCatchKeyword {
		return false
	}
	return p.lookAhead((*Parser).nextTokenIsOpenBrace)
}

// ---- Cycle 7: match expressions ----

type matchPatternKind int

const (
	matchPatternLiteral matchPatternKind = iota
	matchPatternTag
	matchPatternWildcard
	matchPatternBinding
	matchPatternObject
)

type matchPattern struct {
	kind    matchPatternKind
	literal *ast.Expression // matchPatternLiteral
	name    string          // tag / binding name
	fields  []matchField    // matchPatternObject
}

type matchField struct {
	name string
	pat  *matchPattern
}

// match [value|tag] (x) { arms }  ==>  Match.value(x).pipe(..., terminator)
// (inside an effect body the arms become Effect.gen and the whole match is
// bound with yield*; see TRANSPILATION.md §10)
func (p *Parser) parseMatchExpression() *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'match'
	tagMode := false
	if p.token == ast.KindIdentifier {
		switch p.scanner.TokenValue() {
		case "tag":
			tagMode = true
			p.nextToken()
		case "value":
			p.nextToken()
		}
	}
	p.parseExpected(ast.KindOpenParenToken)
	scrutinee := p.parseExpressionAllowIn()
	p.parseExpected(ast.KindCloseParenToken)
	p.parseExpected(ast.KindOpenBraceToken)

	effectful := p.inEffectBody
	value := p.makeHelperCall("Match", "value", []*ast.Node{scrutinee}, pos, p.nodePos())

	var pipeArgs []*ast.Node
	hasCatchAll := false
	for p.token != ast.KindCloseBraceToken && p.token != ast.KindEndOfFile {
		startTok := p.nodePos()
		if hasCatchAll {
			p.parseErrorAt(p.nodePos(), p.nodePos()+1, diagnostics.Unreachable_match_arm_Colon_it_follows_a_catch_all_arm)
		}
		matchArm, isCatchAll := p.parseMatchArm(tagMode, effectful)
		hasCatchAll = hasCatchAll || isCatchAll
		pipeArgs = append(pipeArgs, matchArm)
		if p.nodePos() == startTok {
			p.nextToken() // progress guard
		}
	}
	p.parseExpected(ast.KindCloseBraceToken)
	end := p.nodePos()

	if !hasCatchAll {
		pipeArgs = append(pipeArgs, p.makeHelperAccess("Match", "exhaustive"))
	}

	pipeAccess := p.makePipeAccess(value)
	result := p.finishNodeWithEnd(p.factory.NewCallExpression(pipeAccess, nil, nil, p.newNodeList(core.NewTextRange(pos, end), pipeArgs), ast.NodeFlagsNone), pos, end)
	if effectful {
		yieldExpr := p.makeYieldStar(result, pos, end)
		return p.finishNodeWithEnd(p.factory.NewParenthesizedExpression(yieldExpr), pos, end)
	}
	return result
}

func (p *Parser) parseMatchArm(tagMode bool, effectful bool) (arm *ast.Node, isCatchAll bool) {
	armPos := p.nodePos()

	patternPos := p.nodePos()
	patterns := []*matchPattern{p.parseMatchPattern()}
	for p.token == ast.KindBarToken && !p.isAtPipeOperator() {
		p.nextToken()
		patterns = append(patterns, p.parseMatchPattern())
	}
	first := patterns[0]
	if tagMode && first.kind != matchPatternTag && first.kind != matchPatternWildcard {
		// In tag mode every arm names a tag (or '_'); lowercase identifiers
		// and structural patterns are mistakes.
		p.parseErrorAt(patternPos, p.nodePos(), diagnostics.A_match_arm_pattern_must_be_a_literal_tag_reference_binding_object_pattern_or)
	}

	binding := ""
	if p.token == ast.KindAsKeyword {
		p.nextToken()
		binding = p.parseIdentifier().Text()
	}

	var guard *ast.Expression
	if p.token == ast.KindIfKeyword {
		p.nextToken()
		saveGuard := p.inMatchArmGuard
		p.inMatchArmGuard = true
		guard = p.parseAssignmentExpressionOrHigher()
		p.inMatchArmGuard = saveGuard
	}

	p.reScanGreaterThanToken()
	p.parseExpected(ast.KindGreaterThanGreaterThanToken)
	body := p.parseMatchArmBody(effectful)
	armEnd := p.nodePos()

	handlerParam := p.matchHandlerParam(first, binding)
	handler := p.makeArrowWithParam(handlerParam, body, armPos, armEnd)

	switch {
	case guard != nil && (first.kind == matchPatternBinding || first.kind == matchPatternWildcard):
		// `m if cond >> body` — a guarded binding arm is conditional.
		pred := p.makeMatchGuardPredicate(first, guard, armPos, armEnd)
		return p.makeHelperCall("Match", "when", []*ast.Node{pred, handler}, armPos, armEnd), false
	case first.kind == matchPatternWildcard || first.kind == matchPatternBinding:
		return p.makeHelperCall("Match", "orElse", []*ast.Node{handler}, armPos, armEnd), true
	case first.kind == matchPatternTag || tagMode:
		if len(patterns) == 1 {
			return p.makeHelperCall("Match", "tag", []*ast.Node{p.makeStringLiteral(first.name, armPos), handler}, armPos, armEnd), false
		}
		var props []*ast.Node
		for _, pat := range patterns {
			tagName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(pat.name)))
			href := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("h")))
			props = append(props, p.finishSynthesized(p.factory.NewPropertyAssignment(nil, tagName, nil, nil, href)))
		}
		obj := p.finishSynthesized(p.factory.NewObjectLiteralExpression(p.newNodeList(core.NewTextRange(-1, -1), props), false))
		tags := p.makeHelperCall("Match", "tags", []*ast.Node{obj}, armPos, armEnd)
		iife := p.makeSingleParamArrow("h", tags, armPos, armEnd)
		paren := p.finishSynthesized(p.factory.NewParenthesizedExpression(iife))
		return p.finishNodeWithEnd(p.factory.NewCallExpression(paren, nil, nil, p.newNodeList(core.NewTextRange(armPos, armEnd), []*ast.Node{handler}), ast.NodeFlagsNone), armPos, armEnd), false
	case guard != nil:
		pred := p.makeMatchGuardPredicate(first, guard, armPos, armEnd)
		return p.makeHelperCall("Match", "when", []*ast.Node{pred, handler}, armPos, armEnd), false
	case first.kind == matchPatternLiteral && len(patterns) > 1:
		args := make([]*ast.Node, 0, len(patterns)+1)
		for _, pat := range patterns {
			args = append(args, p.matchTestExpression(pat))
		}
		args = append(args, handler)
		return p.makeHelperCall("Match", "whenOr", args, armPos, armEnd), false
	default:
		return p.makeHelperCall("Match", "when", []*ast.Node{p.matchTestExpression(first), handler}, armPos, armEnd), false
	}
}

// parseMatchArmBody parses the arm RHS: a block (effect body when effectful)
// or an expression; effectful arms lower into Effect.gen.
func (p *Parser) parseMatchArmBody(effectful bool) *ast.Expression {
	bodyPos := p.nodePos()
	if !effectful {
		if p.token == ast.KindOpenBraceToken {
			// pure block arm: (params) => { ... } — keep the block as-is
			block := p.parseFunctionBlock(ParseFlagsNone, nil)
			return block
		}
		return p.parseAssignmentExpressionOrHigher()
	}
	var block *ast.Node
	if p.token == ast.KindOpenBraceToken {
		block = p.parseEffectFunctionBlock()
	} else {
		saveInEffectBody := p.inEffectBody
		p.inEffectBody = true
		value := p.parseAssignmentExpressionOrHigher()
		p.inEffectBody = saveInEffectBody
		ret := p.finishNodeWithEnd(p.factory.NewReturnStatement(value), bodyPos, p.nodePos())
		block = p.finishNodeWithEnd(p.factory.NewBlock(p.newNodeList(core.NewTextRange(bodyPos, p.nodePos()), []*ast.Node{ret}), false), bodyPos, p.nodePos())
	}
	end := p.nodePos()
	emptyParams := p.newNodeList(core.NewTextRange(-1, -1), nil)
	return p.makeEffectCall("gen", []*ast.Node{p.makeGeneratorExpression(emptyParams, block, bodyPos, end)}, bodyPos, end)
}

func (p *Parser) parseMatchPattern() *matchPattern {
	switch p.token {
	case ast.KindNumericLiteral, ast.KindStringLiteral, ast.KindNoSubstitutionTemplateLiteral, ast.KindBigIntLiteral:
		return &matchPattern{kind: matchPatternLiteral, literal: p.parseLiteralExpression(false)}
	case ast.KindTrueKeyword, ast.KindFalseKeyword, ast.KindNullKeyword:
		return &matchPattern{kind: matchPatternLiteral, literal: p.parseKeywordExpression()}
	case ast.KindMinusToken:
		pos := p.nodePos()
		p.nextToken()
		lit := p.parseLiteralExpression(false)
		minus := p.finishNodeWithEnd(p.factory.NewPrefixUnaryExpression(ast.KindMinusToken, lit), pos, p.nodePos())
		return &matchPattern{kind: matchPatternLiteral, literal: minus}
	case ast.KindOpenBraceToken:
		p.nextToken()
		pat := &matchPattern{kind: matchPatternObject}
		for p.token != ast.KindCloseBraceToken && p.token != ast.KindEndOfFile {
			startTok := p.nodePos()
			name := p.parseIdentifier().Text()
			if p.token == ast.KindColonToken {
				p.nextToken()
				field := p.parseMatchPattern()
				// Consume (and ignore for now) trailing or-pattern alternatives
				// in a field value, e.g. `{ status: 301 | 302 }`. The structural
				// test uses the first alternative; full field or-patterns are a
				// later refinement.
				for p.token == ast.KindBarToken && !p.isAtPipeOperator() {
					p.nextToken()
					p.parseMatchPattern()
				}
				pat.fields = append(pat.fields, matchField{name: name, pat: field})
			} else {
				pat.fields = append(pat.fields, matchField{name: name, pat: &matchPattern{kind: matchPatternBinding, name: name}})
			}
			if p.token == ast.KindCommaToken {
				p.nextToken()
			}
			// Progress guard: never spin on an unexpected token.
			if p.nodePos() == startTok {
				p.nextToken()
			}
		}
		p.parseExpected(ast.KindCloseBraceToken)
		return pat
	default:
		name := p.parseIdentifier().Text()
		switch {
		case name == "_":
			return &matchPattern{kind: matchPatternWildcard, name: name}
		case len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z':
			return &matchPattern{kind: matchPatternTag, name: name}
		default:
			return &matchPattern{kind: matchPatternBinding, name: name}
		}
	}
}

// matchTestExpression builds the structural test value passed to Match.when:
// literals stay, object patterns keep only their literal fields.
func (p *Parser) matchTestExpression(pat *matchPattern) *ast.Expression {
	switch pat.kind {
	case matchPatternLiteral:
		return pat.literal
	case matchPatternObject:
		var props []*ast.Node
		for _, f := range pat.fields {
			if f.pat.kind == matchPatternLiteral || f.pat.kind == matchPatternObject {
				name := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(f.name)))
				props = append(props, p.finishSynthesized(p.factory.NewPropertyAssignment(nil, name, nil, nil, p.matchTestExpression(f.pat))))
			}
		}
		return p.finishSynthesized(p.factory.NewObjectLiteralExpression(p.newNodeList(core.NewTextRange(-1, -1), props), false))
	default:
		return p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("undefined")))
	}
}

// matchHandlerParam builds the handler's parameter: the 'as' binding name, an
// object binding pattern of the pattern's bindings, or a throwaway.
func (p *Parser) matchHandlerParam(pat *matchPattern, binding string) *ast.Node {
	if binding != "" {
		return p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(binding)))
	}
	if pat.kind == matchPatternBinding {
		return p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(pat.name)))
	}
	if pat.kind == matchPatternObject {
		if bp := p.matchBindingPattern(pat); bp != nil {
			return bp
		}
	}
	return p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("_")))
}

// matchBindingPattern builds `{ a, status: s }` binding patterns from the
// binding fields of an object pattern (nil when there are none).
func (p *Parser) matchBindingPattern(pat *matchPattern) *ast.Node {
	var elements []*ast.Node
	for _, f := range pat.fields {
		switch f.pat.kind {
		case matchPatternBinding:
			name := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(f.pat.name)))
			var propName *ast.Node
			if f.pat.name != f.name {
				propName = p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(f.name)))
			}
			elements = append(elements, p.finishSynthesized(p.factory.NewBindingElement(nil, propName, name, nil)))
		case matchPatternObject:
			if nested := p.matchBindingPattern(f.pat); nested != nil {
				propName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(f.name)))
				elements = append(elements, p.finishSynthesized(p.factory.NewBindingElement(nil, propName, nested, nil)))
			}
		}
	}
	if len(elements) == 0 {
		return nil
	}
	return p.finishSynthesized(p.factory.NewBindingPattern(ast.KindObjectBindingPattern, p.newNodeList(core.NewTextRange(-1, -1), elements)))
}

// makeMatchGuardPredicate builds `(v) => «structural tests» && ((bindings) => guard)(v)`.
func (p *Parser) makeMatchGuardPredicate(pat *matchPattern, guard *ast.Expression, pos int, end int) *ast.Expression {
	v := func() *ast.Expression {
		return p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("v")))
	}
	var test *ast.Expression
	if pat.kind == matchPatternObject {
		for _, f := range pat.fields {
			if f.pat.kind != matchPatternLiteral {
				continue
			}
			fieldName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(f.name)))
			access := p.finishSynthesized(p.factory.NewPropertyAccessExpression(v(), nil, fieldName, ast.NodeFlagsNone))
			eq := p.finishSynthesized(p.factory.NewBinaryExpression(nil, access, nil, p.finishSynthesized(p.factory.NewToken(ast.KindEqualsEqualsEqualsToken)), f.pat.literal))
			if test == nil {
				test = eq
			} else {
				test = p.finishSynthesized(p.factory.NewBinaryExpression(nil, test, nil, p.finishSynthesized(p.factory.NewToken(ast.KindAmpersandAmpersandToken)), eq))
			}
		}
	}
	guardParam := p.matchHandlerParam(pat, "")
	guardArrow := p.makeArrowWithParam(guardParam, guard, pos, end)
	guardParen := p.finishSynthesized(p.factory.NewParenthesizedExpression(guardArrow))
	guardCall := p.finishNodeWithEnd(p.factory.NewCallExpression(guardParen, nil, nil, p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{v()}), ast.NodeFlagsNone), pos, end)
	cond := guardCall
	if test != nil {
		cond = p.finishSynthesized(p.factory.NewBinaryExpression(nil, test, nil, p.finishSynthesized(p.factory.NewToken(ast.KindAmpersandAmpersandToken)), guardCall))
	}
	return p.makeSingleParamArrowExpr("v", cond, pos, end)
}

func (p *Parser) makeArrowWithParam(param *ast.Node, body *ast.Expression, pos int, end int) *ast.Expression {
	paramDecl := p.finishSynthesized(p.factory.NewParameterDeclaration(nil, nil, param, nil, nil, nil))
	params := p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{paramDecl})
	arrowToken := p.finishSynthesized(p.factory.NewToken(ast.KindEqualsGreaterThanToken))
	return p.finishNodeWithEnd(p.factory.NewArrowFunction(nil, nil, params, nil, nil, arrowToken, body), pos, end)
}

func (p *Parser) makeSingleParamArrowExpr(name string, body *ast.Expression, pos int, end int) *ast.Expression {
	ident := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(name)))
	return p.makeArrowWithParam(ident, body, pos, end)
}

// nextIsMatchExpressionStart commits to a match expression only for the exact
// shape `match [value|tag] ( ... ) {` (otherwise `match(...)` stays a call).
func (p *Parser) nextIsMatchExpressionStart() bool {
	p.nextToken()
	if p.hasPrecedingLineBreak() {
		return false
	}
	if p.token == ast.KindIdentifier {
		switch p.scanner.TokenValue() {
		case "value", "tag":
			p.nextToken()
		}
	}
	if p.token != ast.KindOpenParenToken {
		return false
	}
	depth := 0
	for {
		switch p.token {
		case ast.KindOpenParenToken:
			depth++
		case ast.KindCloseParenToken:
			depth--
			if depth == 0 {
				return p.nextToken() == ast.KindOpenBraceToken
			}
		case ast.KindEndOfFile:
			return false
		}
		p.nextToken()
	}
}

// ---- Cycle 8: type sugar, using-binds, anonymous effect expressions ----

// A raises E requires R  ==>  Effect.Effect<A, E, R>   (omitted clauses: never)
// Called at the tail of parseType; recursion is prevented while parsing the
// E/R operands via inEffectTypeSugar.
func (p *Parser) tryParseEffectTypeSugar(typeNode *ast.TypeNode) *ast.TypeNode {
	if p.inEffectTypeSugar || p.hasPrecedingLineBreak() || p.token != ast.KindIdentifier {
		return typeNode
	}
	word := p.scanner.TokenValue()
	if word != "raises" && word != "requires" {
		return typeNode
	}
	pos := typeNode.Pos()
	saveSugar := p.inEffectTypeSugar
	p.inEffectTypeSugar = true
	var errType, reqType *ast.TypeNode
	if word == "raises" {
		p.nextToken()
		errType = p.parseType()
		if p.token == ast.KindIdentifier && p.scanner.TokenValue() == "requires" && !p.hasPrecedingLineBreak() {
			p.nextToken()
			reqType = p.parseType()
		}
	} else {
		p.nextToken()
		reqType = p.parseType()
	}
	p.inEffectTypeSugar = saveSugar
	end := p.nodePos()

	if errType == nil {
		errType = p.finishSynthesized(p.factory.NewKeywordTypeNode(ast.KindNeverKeyword))
	}
	if reqType == nil {
		reqType = p.finishSynthesized(p.factory.NewKeywordTypeNode(ast.KindNeverKeyword))
	}
	p.useEffectHelper("Effect")
	left := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("Effect")))
	right := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("Effect")))
	qualified := p.finishSynthesized(p.factory.NewQualifiedName(left, right))
	typeArgs := p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{typeNode, errType, reqType})
	return p.finishNodeWithEnd(p.factory.NewTypeReferenceNode(qualified, typeArgs), pos, end)
}

// using x <- acq [release (params) { body }]
//
//	==>  const x = yield* Effect.acquireRelease(acq, (params) => Effect.gen(...));
//	==>  const x = yield* acq;                       // without release clause
func (p *Parser) parseUsingBindStatement() *ast.Statement {
	pos := p.nodePos()
	p.nextToken() // consume 'using'
	name := p.parseIdentifier()
	p.consumeBindArrow()
	acquire := p.parseAssignmentExpressionOrHigher()

	var operand *ast.Expression
	if p.token == ast.KindIdentifier && p.scanner.TokenValue() == "release" && p.lookAhead((*Parser).nextIsReleaseClause) {
		p.nextToken() // consume 'release'
		p.parseExpected(ast.KindOpenParenToken)
		var params []*ast.Node
		for p.token != ast.KindCloseParenToken && p.token != ast.KindEndOfFile {
			paramName := p.parseIdentifier()
			params = append(params, p.finishSynthesized(p.factory.NewParameterDeclaration(nil, nil, paramName, nil, nil, nil)))
			if p.token == ast.KindCommaToken {
				p.nextToken()
			}
		}
		p.parseExpected(ast.KindCloseParenToken)
		body := p.parseEffectFunctionBlock()
		end := p.nodePos()

		emptyParams := p.newNodeList(core.NewTextRange(-1, -1), nil)
		gen := p.makeEffectCall("gen", []*ast.Node{p.makeGeneratorExpression(emptyParams, body, pos, end)}, pos, end)
		arrowToken := p.finishSynthesized(p.factory.NewToken(ast.KindEqualsGreaterThanToken))
		releaseFn := p.finishNodeWithEnd(p.factory.NewArrowFunction(nil, nil, p.newNodeList(core.NewTextRange(-1, -1), params), nil, nil, arrowToken, gen), pos, end)
		operand = p.makeEffectCall("acquireRelease", []*ast.Node{acquire, releaseFn}, pos, end)
	} else {
		operand = acquire
	}
	p.parseSemicolon()
	end := p.nodePos()

	yieldExpr := p.makeYieldStar(operand, pos, end)
	decl := p.finishNodeWithEnd(p.factory.NewVariableDeclaration(name, nil, nil, yieldExpr), pos, end)
	declList := p.finishNodeWithEnd(p.factory.NewVariableDeclarationList(p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{decl}), ast.NodeFlagsConst), pos, end)
	return p.finishNodeWithEnd(p.factory.NewVariableStatement(nil, declList), pos, end)
}

// nextIsReleaseClause: 'release ( ... ) {' — the block disambiguates the
// clause from a call to a user function named release.
func (p *Parser) nextIsReleaseClause() bool {
	if p.nextToken() != ast.KindOpenParenToken {
		return false
	}
	depth := 0
	for {
		switch p.token {
		case ast.KindOpenParenToken:
			depth++
		case ast.KindCloseParenToken:
			depth--
			if depth == 0 {
				return p.nextToken() == ast.KindOpenBraceToken
			}
		case ast.KindEndOfFile:
			return false
		}
		p.nextToken()
	}
}

func (p *Parser) nextIsUsingBind() bool {
	p.nextToken()
	if p.token != ast.KindIdentifier || p.hasPrecedingLineBreak() {
		return false
	}
	p.nextToken()
	return p.isAtBindArrow()
}

// effect (params) [: T] { body }   ==>   Effect.fn(function* (params) { body })
func (p *Parser) parseEffectFunctionExpression() *ast.Expression {
	pos := p.nodePos()
	p.nextToken() // consume 'effect'
	parameters := p.parseParameters(ParseFlagsYield)
	p.parseReturnType(ast.KindColonToken, false /*isType*/)
	body := p.parseEffectFunctionBlock()
	end := p.nodePos()
	funcExpr := p.makeGeneratorExpression(parameters, body, pos, end)
	return p.makeEffectCall("fn", []*ast.Node{funcExpr}, pos, end)
}

// nextIsEffectFunctionExpression: 'effect ( ... ) {' or 'effect ( ... ) :'
func (p *Parser) nextIsEffectFunctionExpression() bool {
	p.nextToken()
	if p.hasPrecedingLineBreak() || p.token != ast.KindOpenParenToken {
		return false
	}
	depth := 0
	for {
		switch p.token {
		case ast.KindOpenParenToken:
			depth++
		case ast.KindCloseParenToken:
			depth--
			if depth == 0 {
				next := p.nextToken()
				return next == ast.KindOpenBraceToken || next == ast.KindColonToken
			}
		case ast.KindEndOfFile:
			return false
		}
		p.nextToken()
	}
}

// effect m(params) { body } (class member)
//
//	==>  m = Effect.fn("C.m")(function* (params) { body });
//
// The method becomes an instance field holding the Effect.fn value
// (class-field semantics; SPEC §3.4). 'static effect m' becomes a static field.
func (p *Parser) parseEffectClassMethod(pos int, modifiers *ast.ModifierList) *ast.Node {
	p.nextToken() // consume 'effect'
	name := p.parseIdentifier()
	spanName := name.Text()
	if p.currentEffectClassName != "" {
		spanName = p.currentEffectClassName + "." + name.Text()
	}
	parameters := p.parseParameters(ParseFlagsYield)
	p.parseReturnType(ast.KindColonToken, false /*isType*/)
	body := p.parseEffectFunctionBlock()
	end := p.nodePos()

	funcExpr := p.makeGeneratorExpression(parameters, body, pos, end)
	fn := p.makeEffectCall("fn", []*ast.Node{p.makeStringLiteral(spanName, pos)}, pos, end)
	value := p.finishNodeWithEnd(p.factory.NewCallExpression(fn, nil, nil, p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{funcExpr}), ast.NodeFlagsNone), pos, end)
	return p.finishNodeWithEnd(p.factory.NewPropertyDeclaration(modifiers, name, nil, nil, value), pos, end)
}
