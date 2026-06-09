package parser

import (
	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
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
			if p.inEffectBody && p.lookAhead((*Parser).nextIsRaiseOperandStart) {
				return p.parseRaiseStatement()
			}
		case "defer":
			if p.inEffectBody && p.lookAhead((*Parser).nextIsDeferBodyStart) {
				return p.parseDeferStatement()
			}
		}
		if p.inEffectBody && p.lookAhead((*Parser).nextIsBindArrow) {
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
		if p.inEffectBody && p.isAtBindArrow() {
			return p.parseDiscardBindStatement()
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
	p.consumeBindArrow()
	operandPos := p.nodePos()
	operand := p.parseAssignmentExpressionOrHigher()
	p.parseSemicolon()
	end := p.nodePos()

	yieldExpr := p.makeYieldStar(operand, operandPos, end)
	decl := p.finishNodeWithEnd(p.factory.NewVariableDeclaration(name, nil, nil, yieldExpr), pos, end)
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

	funcExpr := p.makeGeneratorExpression(parameters, body, pos, end)
	fn := p.makeEffectCall("fn", []*ast.Node{p.makeStringLiteral(nameText, pos)}, pos, end)
	outer := p.finishNodeWithEnd(p.factory.NewCallExpression(fn, nil, nil, p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{funcExpr}), ast.NodeFlagsNone), pos, end)
	decl := p.finishNodeWithEnd(p.factory.NewVariableDeclaration(name, nil, nil, outer), pos, end)
	declList := p.finishNodeWithEnd(p.factory.NewVariableDeclarationList(p.newNodeList(core.NewTextRange(pos, end), []*ast.Node{decl}), ast.NodeFlagsConst), pos, end)
	return p.finishNodeWithEnd(p.factory.NewVariableStatement(modifiers, declList), pos, end)
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
	node.Flags |= p.contextFlags
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
func (p *Parser) makeHelperCall(helper string, method string, args []*ast.Node, pos int, end int) *ast.Expression {
	access := p.makeHelperAccess(helper, method)
	return p.finishNodeWithEnd(p.factory.NewCallExpression(access, nil, nil, p.newNodeList(core.NewTextRange(pos, end), args), ast.NodeFlagsNone), pos, end)
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
var effectHelperImportOrder = []string{"Effect", "Layer", "Context", "Fiber"}

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
		pipeName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("pipe")))
		pipeAccess := p.finishSynthesized(p.factory.NewPropertyAccessExpression(layerExpr, nil, pipeName, ast.NodeFlagsNone))
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
	case "raise":
		if p.inEffectBody && p.lookAhead((*Parser).nextIsRaiseOperandStart) {
			return p.parseRaiseExpression()
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
	for p.token != ast.KindCloseBraceToken && p.token != ast.KindEndOfFile {
		handlers = append(handlers, p.parseCatchArm())
	}
	p.parseExpected(ast.KindCloseBraceToken)
	end := p.nodePos()

	pipeName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("pipe")))
	pipeAccess := p.finishSynthesized(p.factory.NewPropertyAccessExpression(expr, nil, pipeName, ast.NodeFlagsNone))
	return p.finishNodeWithEnd(p.factory.NewCallExpression(pipeAccess, nil, nil, p.newNodeList(core.NewTextRange(pos, end), handlers), ast.NodeFlagsNone), pos, end)
}

func (p *Parser) parseCatchArm() *ast.Node {
	armPos := p.nodePos()

	// Pattern: '_' or Tag { '|' Tag }
	var tags []string
	isCatchAll := false
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
		return p.makeEffectCall("catchAll", []*ast.Node{handler}, armPos, armEnd)
	case len(tags) == 1:
		return p.makeEffectCall("catchTag", []*ast.Node{p.makeStringLiteral(tags[0], armPos), handler}, armPos, armEnd)
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
		return p.finishNodeWithEnd(p.factory.NewCallExpression(paren, nil, nil, p.newNodeList(core.NewTextRange(armPos, armEnd), []*ast.Node{handler}), ast.NodeFlagsNone), armPos, armEnd)
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
