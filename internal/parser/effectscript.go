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
		case "raise":
			if p.inEffectBody && p.lookAhead((*Parser).nextIsRaiseOperandStart) {
				return p.parseRaiseStatement()
			}
		}
		if p.inEffectBody && p.lookAhead((*Parser).nextIsBindArrow) {
			return p.parseBindStatement()
		}
	case ast.KindLessThanToken:
		if p.inEffectBody && p.isAtBindArrow() {
			return p.parseDiscardBindStatement()
		}
	}
	return nil
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

// x <- e;   ==>   const x = yield* e;
func (p *Parser) parseBindStatement() *ast.Statement {
	pos := p.nodePos()
	name := p.parseIdentifier()
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
	p.effectHelperUsed = true
	effectIdent := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("Effect")))
	methodIdent := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier(method)))
	access := p.finishSynthesized(p.factory.NewPropertyAccessExpression(effectIdent, nil, methodIdent, ast.NodeFlagsNone))
	return p.finishNodeWithEnd(p.factory.NewCallExpression(access, nil, nil, p.newNodeList(core.NewTextRange(pos, end), args), ast.NodeFlagsNone), pos, end)
}

func (p *Parser) makeStringLiteral(text string, pos int) *ast.Expression {
	_ = pos
	return p.finishSynthesized(p.factory.NewStringLiteral(text, ast.TokenFlagsNone))
}

func (p *Parser) makeGeneratorExpression(parameters *ast.NodeList, body *ast.Node, pos int, end int) *ast.Expression {
	asterisk := p.finishNodeWithEnd(p.factory.NewToken(ast.KindAsteriskToken), pos, pos)
	return p.finishNodeWithEnd(p.factory.NewFunctionExpression(nil, asterisk, nil, nil, parameters, nil, nil, body), pos, end)
}

// injectEffectScriptImports prepends `import { Effect } from "effect";` when
// any lowered construct referenced the helper and the user has no top-level
// binding for it already.
func (p *Parser) injectEffectScriptImports(statements []*ast.Node) []*ast.Node {
	if !p.effectHelperUsed || p.hasTopLevelEffectImport(statements) {
		return statements
	}
	specName := p.finishSynthesized(p.factory.NewIdentifier(p.internIdentifier("Effect")))
	spec := p.finishSynthesized(p.factory.NewImportSpecifier(false /*isTypeOnly*/, nil, specName))
	named := p.finishSynthesized(p.factory.NewNamedImports(p.newNodeList(core.NewTextRange(-1, -1), []*ast.Node{spec})))
	clause := p.finishSynthesized(p.factory.NewImportClause(ast.KindUnknown, nil, named))
	module := p.finishSynthesized(p.factory.NewStringLiteral("effect", ast.TokenFlagsNone))
	imp := p.finishNodeWithEnd(p.factory.NewImportDeclaration(nil, clause, module, nil), 0, 0)
	return append([]*ast.Node{imp}, statements...)
}

func (p *Parser) hasTopLevelEffectImport(statements []*ast.Node) bool {
	for _, s := range statements {
		if s.Kind != ast.KindImportDeclaration {
			continue
		}
		clause := s.AsImportDeclaration().ImportClause
		if clause == nil {
			continue
		}
		if name := clause.Name(); name != nil && name.Text() == "Effect" {
			return true
		}
		if bindings := clause.AsImportClause().NamedBindings; bindings != nil {
			if bindings.Kind == ast.KindNamespaceImport && bindings.Name() != nil && bindings.Name().Text() == "Effect" {
				return true
			}
			if bindings.Kind == ast.KindNamedImports {
				for _, el := range bindings.AsNamedImports().Elements.Nodes {
					if el.Name() != nil && el.Name().Text() == "Effect" {
						return true
					}
				}
			}
		}
	}
	return false
}
