package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/scanner"
)

// rewriter walks the parsed TypeScript AST and produces EffectScript text by
// recursive span splicing: a recognized pattern emits its EffectScript form
// (recursing into the children it keeps), everything else is the original
// source text with child rewrites spliced in. Comments and formatting in
// untouched regions survive byte-for-byte.
type rewriter struct {
	src  string
	file *ast.SourceFile
	// localToCanonical maps each bound local name to the canonical helper
	// namespace (Effect, Layer, …, pipe); handles aliases and subpath imports.
	localToCanonical map[string]string
	// barrelRoots are local names bound to `import * as X from "effect"`,
	// used at depth 2 (X.Effect.gen).
	barrelRoots map[string]bool
	// bound is the set of canonical helpers reachable in this file (under any
	// local name); consulted by the pipe/type-sugar gates.
	bound map[string]bool
	// inEffectBody is true while emitting statements of a generator being
	// converted into an effect body; the statement-level patterns (binds,
	// raise, defer, …) only fire there, mirroring the forward parser.
	inEffectBody bool
	// convertPipes enables the pipe(a, f) / a.pipe(f) → |> rewrite
	// (Options.ConvertPipes).
	convertPipes bool
	stats        FileStats
}

// start is the position of node's first token (skipping leading trivia).
func (r *rewriter) start(node *ast.Node) int {
	if node.Pos() >= node.End() {
		return node.Pos()
	}
	s := scanner.SkipTrivia(r.src, node.Pos())
	if s > node.End() {
		return node.Pos()
	}
	return s
}

// text is the original source slice of node, without leading trivia.
func (r *rewriter) text(node *ast.Node) string {
	return r.src[r.start(node):node.End()]
}

// emit returns the EffectScript text for node.
func (r *rewriter) emit(node *ast.Node) string {
	if out, ok := r.tryRewrite(node); ok {
		return out
	}
	return r.emitChildren(node)
}

// emitChildren returns node's original text with each child span replaced by
// its emitted form. Trivia between siblings is preserved verbatim. If child
// visitation ever turns out not to be in source order the original text is
// returned unchanged (conservative fallback).
func (r *rewriter) emitChildren(node *ast.Node) string {
	return r.emitChildrenWith(node, r.emit)
}

// emitEffectBody emits a function block as an effect body: the statement
// forms (binds, raise, …) are active inside it.
func (r *rewriter) emitEffectBody(block *ast.Node) string {
	save := r.inEffectBody
	r.inEffectBody = true
	out := r.emitChildren(block)
	r.inEffectBody = save
	return out
}

// emitOrdinaryFunctionBody emits the body of a non-effect function nested
// inside an effect body: statement forms deactivate, expression patterns
// (Effect.gen → effect { }) still apply.
func (r *rewriter) emitOrdinaryFunction(node *ast.Node) string {
	save := r.inEffectBody
	r.inEffectBody = false
	out := r.emitChildren(node)
	r.inEffectBody = save
	return out
}

// tryRewrite dispatches to the pattern handlers. It returns ok=false when no
// pattern applies and the node should be emitted verbatim (with child
// splices).
func (r *rewriter) tryRewrite(node *ast.Node) (string, bool) {
	switch node.Kind {
	case ast.KindVariableStatement:
		if out, ok := r.tryEffectDeclaration(node); ok {
			return out, true
		}
		if out, ok := r.tryLayerDeclaration(node); ok {
			return out, true
		}
		if r.inEffectBody {
			if out, ok := r.tryUsingBind(node); ok {
				return out, true
			}
			if out, ok := r.tryBindStatement(node); ok {
				return out, true
			}
		}
	case ast.KindExpressionStatement:
		if r.inEffectBody {
			if out, ok := r.tryDeferStatement(node); ok {
				return out, true
			}
			if out, ok := r.tryDiscardBind(node); ok {
				return out, true
			}
		}
	case ast.KindClassDeclaration:
		if out, ok := r.tryServiceDeclaration(node); ok {
			return out, true
		}
	case ast.KindReturnStatement:
		if r.inEffectBody {
			if out, ok := r.tryRaiseStatement(node); ok {
				return out, true
			}
		}
	case ast.KindPropertyDeclaration:
		if out, ok := r.tryEffectClassField(node); ok {
			return out, true
		}
	case ast.KindExportAssignment:
		if out, ok := r.tryExportDefaultEffectFn(node); ok {
			return out, true
		}
	case ast.KindCallExpression:
		if r.inEffectBody {
			if out, ok := r.tryCatchChain(node); ok {
				return out, true
			}
		}
		if out, ok := r.tryMatchExpression(node); ok {
			return out, true
		}
		if out, ok := r.tryEffectGenBlock(node); ok {
			return out, true
		}
		if out, ok := r.tryEffectFnExpression(node); ok {
			return out, true
		}
		if r.inEffectBody {
			if out, ok := r.tryConcurrency(node); ok {
				return out, true
			}
		}
		if out, ok := r.tryPipeChain(node); ok {
			return out, true
		}
	case ast.KindParenthesizedExpression:
		if r.inEffectBody {
			if out, ok := r.tryRaiseExpression(node); ok {
				return out, true
			}
			if out, ok := r.tryEffectfulMatch(node); ok {
				return out, true
			}
			if out, ok := r.tryBindExpression(node); ok {
				return out, true
			}
		}
	case ast.KindTypeReference:
		if out, ok := r.tryEffectTypeSugar(node); ok {
			return out, true
		}
	case ast.KindYieldExpression:
		if r.inEffectBody {
			if out, ok := r.tryBareYieldStarExpression(node); ok {
				return out, true
			}
		}
	case ast.KindFunctionExpression, ast.KindArrowFunction, ast.KindFunctionDeclaration,
		ast.KindMethodDeclaration, ast.KindGetAccessor, ast.KindSetAccessor, ast.KindConstructor:
		// Ordinary nested functions are not effect bodies.
		if r.inEffectBody {
			return r.emitOrdinaryFunction(node), true
		}
	}
	return "", false
}

// ---- shared shape helpers ----

// helperMethod matches `Helper.method` where Helper resolves to a tracked
// canonical namespace — either a direct local binding (Effect.gen, E.gen for an
// aliased/subpath import) or a depth-2 barrel access (Eff.Effect.gen). The
// returned helper is always the canonical name, so the `helper == "Effect"`
// checks throughout the package keep working regardless of the local spelling.
func (r *rewriter) helperMethod(node *ast.Node) (helper string, method string, ok bool) {
	if node.Kind != ast.KindPropertyAccessExpression {
		return "", "", false
	}
	pa := node.AsPropertyAccessExpression()
	if pa.QuestionDotToken != nil {
		return "", "", false
	}
	name := pa.Name()
	if name == nil || name.Kind != ast.KindIdentifier {
		return "", "", false
	}
	switch pa.Expression.Kind {
	case ast.KindIdentifier:
		canon, bound := r.localToCanonical[pa.Expression.Text()]
		if !bound {
			return "", "", false
		}
		return canon, name.Text(), true
	case ast.KindPropertyAccessExpression:
		// depth-2 barrel access: Root.Canonical.method
		mid := pa.Expression.AsPropertyAccessExpression()
		if mid.QuestionDotToken != nil || mid.Expression.Kind != ast.KindIdentifier {
			return "", "", false
		}
		if !r.barrelRoots[mid.Expression.Text()] {
			return "", "", false
		}
		midName := mid.Name()
		if midName == nil || midName.Kind != ast.KindIdentifier || !namespaceHelpers[midName.Text()] {
			return "", "", false
		}
		return midName.Text(), name.Text(), true
	}
	return "", "", false
}

// helperCall matches `Helper.method(args…)` (no type arguments, no optional
// chaining) on a tracked helper.
func (r *rewriter) helperCall(node *ast.Node) (helper string, method string, args []*ast.Node, ok bool) {
	if node.Kind != ast.KindCallExpression {
		return "", "", nil, false
	}
	call := node.AsCallExpression()
	if call.QuestionDotToken != nil || call.TypeArguments != nil {
		return "", "", nil, false
	}
	helper, method, ok = r.helperMethod(call.Expression)
	if !ok {
		return "", "", nil, false
	}
	return helper, method, call.Arguments.Nodes, true
}

// genArg matches a `function* () { … }` literal usable as an effect body:
// a generator function expression with no name, no type parameters and no
// return type annotation, whose body avoids the contextual-keyword hazards.
func (r *rewriter) genArg(node *ast.Node, wantZeroParams bool) (*ast.FunctionExpression, bool) {
	if node.Kind != ast.KindFunctionExpression {
		return nil, false
	}
	fn := node.AsFunctionExpression()
	if fn.AsteriskToken == nil || fn.Name() != nil || fn.TypeParameters != nil || fn.Type != nil || fn.Body == nil {
		return nil, false
	}
	if fn.Modifiers() != nil {
		return nil, false
	}
	if wantZeroParams && len(fn.Parameters.Nodes) > 0 {
		return nil, false
	}
	if !r.bodyConvertible(fn.Body) {
		return nil, false
	}
	return fn, true
}

// effectKeywordIdents are identifiers that take on contextual-keyword meaning
// somewhere inside an effect body (or, for match/service/layer, anywhere a
// statement parses). A generator whose body references one of these as a
// plain identifier is left unconverted.
var effectKeywordIdents = map[string]bool{
	"effect": true, "raise": true, "service": true, "layer": true, "scoped": true,
	"fork": true, "par": true, "race": true, "join": true, "defer": true,
	"match": true, "using": true, "release": true,
}

// bodyConvertible scans a candidate effect body for identifier references that
// would collide with EffectScript contextual keywords when the body text is
// re-parsed as .ets. A contextual keyword (effect, raise, match, join, …) only
// changes meaning when it leads a statement or primary expression *and* the
// token that follows it matches that keyword's grammar — e.g. `join(x)`
// re-parses as the fork/join operator, but `args.join`, `const join = …`,
// `return service` and `{ match: x }` are all harmless. Only the hazardous
// follow-token positions are rejected; everything else converts (and the
// whole-file round-trip verifier is the backstop for anything missed).
func (r *rewriter) bodyConvertible(body *ast.Node) bool {
	ok := true
	var visit func(node *ast.Node) bool
	visit = func(node *ast.Node) bool {
		if !ok {
			return true
		}
		switch node.Kind {
		case ast.KindPropertyAccessExpression:
			// Only the receiver can collide; `s.match` is always safe.
			return visit(node.AsPropertyAccessExpression().Expression)
		case ast.KindPropertyAssignment, ast.KindPropertyDeclaration, ast.KindMethodDeclaration,
			ast.KindPropertySignature, ast.KindMethodSignature:
			// Visit everything except the property name.
			name := node.Name()
			return node.ForEachChild(func(child *ast.Node) bool {
				if child == name {
					return false
				}
				return visit(child)
			})
		case ast.KindIdentifier:
			if effectKeywordIdents[node.Text()] {
				c, sameLine := r.nextSignificant(node.End())
				if keywordRefHazard(node.Text(), c, sameLine) {
					ok = false
					return true
				}
			}
		}
		return node.ForEachChild(visit)
	}
	visit(body)
	return ok
}

// nextSignificant returns the first non-trivia byte at or after pos and whether
// it sits on the same source line as pos (no intervening line break). Line and
// block comments are skipped. The returned byte is 0 at end of input.
func (r *rewriter) nextSignificant(pos int) (byte, bool) {
	sameLine := true
	for i := pos; i < len(r.src); {
		c := r.src[i]
		switch {
		case c == '\n':
			sameLine = false
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '/' && i+1 < len(r.src) && r.src[i+1] == '/':
			i += 2
			for i < len(r.src) && r.src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(r.src) && r.src[i+1] == '*':
			i += 2
			for i < len(r.src) && !(r.src[i] == '*' && i+1 < len(r.src) && r.src[i+1] == '/') {
				if r.src[i] == '\n' {
					sameLine = false
				}
				i++
			}
			i += 2
		default:
			return c, sameLine
		}
	}
	return 0, sameLine
}

func isIdentStartByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// exprStartByte reports whether c can begin a primary expression (a raise
// operand). Over-approximates with the common value-start bytes.
func exprStartByte(c byte) bool {
	if isIdentStartByte(c) || (c >= '0' && c <= '9') {
		return true
	}
	switch c {
	case '(', '[', '{', '"', '\'', '`', '+', '-', '!', '~', '@':
		return true
	}
	return false
}

// keywordRefHazard reports whether a contextual-keyword identifier followed by
// byte c (sameLine: no intervening line break) would be reinterpreted by the
// EffectScript parser as that keyword's construct rather than a plain
// identifier. Mirrors the lookahead predicates in internal/parser/effectscript.go.
func keywordRefHazard(name string, c byte, sameLine bool) bool {
	switch name {
	case "effect":
		// effect { … } block, or effect name(…)/effect (…) fn forms.
		return (sameLine && c == '{') || c == '(' || isIdentStartByte(c)
	case "atomic":
		return sameLine && c == '{'
	case "raise":
		return sameLine && exprStartByte(c)
	case "match":
		return c == '('
	case "layer":
		return isIdentStartByte(c) || c == '{'
	case "scoped", "service", "release":
		return isIdentStartByte(c)
	case "fork", "join":
		return sameLine && (c == '(' || isIdentStartByte(c))
	case "par":
		return sameLine && (c == '(' || c == '[' || c == '{')
	case "race":
		return sameLine && c == '['
	case "defer":
		return sameLine && c == '{'
	case "using":
		return isIdentStartByte(c) || c == '[' || c == '{'
	}
	return false
}

// paramsText returns the original text between the parentheses of a function
// expression's parameter list, parens included.
func (r *rewriter) paramsText(fn *ast.FunctionExpression) string {
	nodes := fn.Parameters.Nodes
	if len(nodes) == 0 {
		return "()"
	}
	from := r.start(nodes[0])
	to := nodes[len(nodes)-1].End()
	return "(" + r.src[from:to] + ")"
}

// unarySafe reports whether an expression can stand as the operand of the
// unary keyword forms (raise-expression, fork, join) without changing how it
// re-parses; anything else needs wrapping parentheses.
func unarySafe(node *ast.Node) bool {
	switch node.Kind {
	case ast.KindIdentifier, ast.KindCallExpression, ast.KindNewExpression,
		ast.KindPropertyAccessExpression, ast.KindElementAccessExpression,
		ast.KindParenthesizedExpression, ast.KindNonNullExpression,
		ast.KindStringLiteral, ast.KindNumericLiteral, ast.KindBigIntLiteral,
		ast.KindTrueKeyword, ast.KindFalseKeyword, ast.KindNullKeyword,
		ast.KindNoSubstitutionTemplateLiteral, ast.KindTemplateExpression,
		ast.KindThisKeyword:
		return true
	}
	return false
}

// prevStatementTerminated reports whether it is safe to start this statement's
// replacement with a token (`[`, `(`, `<`) that could fuse with an
// unterminated previous statement under ASI. True when the statement is first
// in its list or the previous sibling's text ends with ';', '}' or '{'.
func (r *rewriter) prevStatementTerminated(stmt *ast.Node) bool {
	last, ok := r.prevStatementLastChar(stmt)
	return ok && (last == 0 || last == ';' || last == '}' || last == '{')
}

// prevStatementEndsWithSemicolon is the stricter variant for statements whose
// replacement starts with `<-`: the forward parser re-scans a balanced block
// at statement position followed by `<-` as a destructuring bind pattern, so
// a previous sibling ending in '}' is NOT safe — only ';' (or being first in
// the list) is.
func (r *rewriter) prevStatementEndsWithSemicolon(stmt *ast.Node) bool {
	last, ok := r.prevStatementLastChar(stmt)
	return ok && (last == 0 || last == ';')
}

// prevStatementLastChar returns the last non-whitespace character of the
// previous sibling statement's text, 0 when the statement is first in its
// list, and ok=false when the parent has no statement list.
func (r *rewriter) prevStatementLastChar(stmt *ast.Node) (byte, bool) {
	stmts := statementListOf(stmt.Parent)
	if stmts == nil {
		return 0, false
	}
	var prev *ast.Node
	for _, s := range stmts {
		if s == stmt {
			break
		}
		prev = s
	}
	if prev == nil {
		return 0, true
	}
	t := strings.TrimRight(r.text(prev), " \t\r\n")
	if t == "" {
		return 0, false
	}
	return t[len(t)-1], true
}

func statementListOf(parent *ast.Node) []*ast.Node {
	if parent == nil {
		return nil
	}
	switch parent.Kind {
	case ast.KindBlock:
		return parent.AsBlock().Statements.Nodes
	case ast.KindSourceFile:
		return parent.AsSourceFile().Statements.Nodes
	case ast.KindModuleBlock:
		return parent.AsModuleBlock().Statements.Nodes
	case ast.KindCaseClause, ast.KindDefaultClause:
		return parent.AsCaseOrDefaultClause().Statements.Nodes
	}
	return nil
}

// modifiersText returns the original text of a modifier list followed by one
// space, or "" when there are none. Decorators are excluded (callers handle
// them separately).
func (r *rewriter) modifiersText(modifiers *ast.ModifierList) string {
	if modifiers == nil || len(modifiers.Nodes) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range modifiers.Nodes {
		if ast.IsDecorator(m) {
			continue
		}
		b.WriteString(r.text(m))
		b.WriteString(" ")
	}
	return b.String()
}
