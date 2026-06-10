package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// catchArm is one recognized handler of a catch pipe chain.
type catchArm struct {
	tags    []string // nil → catch-all ('_') arm
	binding string   // "" → no 'as' binding
	body    *ast.Node
}

// tryCatchChain matches pattern #16 (inside effect bodies):
//
//	expr.pipe(
//	  Effect.catchTag("NotFound", (e) => Effect.gen(function* () { … })),
//	  ((h) => Effect.catchTags({ DbError: h, NetError: h }))((e) => …),
//	  Effect.catchAll((e) => …),
//	)
//	→ expr' catch {
//	    NotFound as e >> …
//	    DbError | NetError as e >> …
//	    _ as e >> …
//	  }
//
// Every pipe argument must be a catch form (a mixed combinator chain is left
// for the pipe rule), and a catch-all must be last.
func (r *rewriter) tryCatchChain(node *ast.Node) (string, bool) {
	call, ok := pipeMethodCall(node)
	if !ok || len(call.Arguments.Nodes) == 0 {
		return "", false
	}
	receiver := call.Expression.AsPropertyAccessExpression().Expression

	arms := make([]catchArm, 0, len(call.Arguments.Nodes))
	for i, arg := range call.Arguments.Nodes {
		arm, ok := r.matchCatchArm(arg)
		if !ok {
			return "", false
		}
		if arm.tags == nil && i != len(call.Arguments.Nodes)-1 {
			return "", false
		}
		arms = append(arms, arm)
	}

	// `expr catch { … }` attaches at postfix level: the receiver must be a
	// left-hand-side expression for the re-parse to bind it identically.
	switch receiver.Kind {
	case ast.KindIdentifier, ast.KindCallExpression, ast.KindPropertyAccessExpression,
		ast.KindElementAccessExpression, ast.KindNewExpression, ast.KindParenthesizedExpression,
		ast.KindNonNullExpression:
	default:
		return "", false
	}

	indent := r.lineIndent(r.start(node))
	var b strings.Builder
	b.WriteString(r.emit(receiver))
	b.WriteString(" catch {\n")
	for _, arm := range arms {
		b.WriteString(indent)
		b.WriteString("  ")
		if arm.tags == nil {
			b.WriteString("_")
		} else {
			b.WriteString(strings.Join(arm.tags, " | "))
		}
		if arm.binding != "" {
			b.WriteString(" as ")
			b.WriteString(arm.binding)
		}
		b.WriteString(" >> ")
		b.WriteString(r.armBodyText(arm.body, indent+"  "))
		b.WriteString("\n")
	}
	b.WriteString(indent)
	b.WriteString("}")
	r.stats.count("catch")
	return b.String(), true
}

// matchCatchArm recognizes the three handler forms the forward emitter
// produces.
func (r *rewriter) matchCatchArm(node *ast.Node) (catchArm, bool) {
	// Effect.catchTag("Tag", handler) / Effect.catchAll(handler)
	if helper, method, args, ok := r.helperCall(node); ok && helper == "Effect" {
		switch method {
		case "catchTag":
			if len(args) != 2 || args[0].Kind != ast.KindStringLiteral {
				return catchArm{}, false
			}
			tag := args[0].Text()
			if !isValidIdentifier(tag) || tag == "_" {
				return catchArm{}, false
			}
			binding, body, ok := r.catchHandler(args[1])
			if !ok {
				return catchArm{}, false
			}
			return catchArm{tags: []string{tag}, binding: binding, body: body}, true
		case "catchAll":
			if len(args) != 1 {
				return catchArm{}, false
			}
			binding, body, ok := r.catchHandler(args[0])
			if !ok {
				return catchArm{}, false
			}
			return catchArm{binding: binding, body: body}, true
		}
		return catchArm{}, false
	}

	// ((h) => Effect.catchTags({ T1: h, T2: h }))(handler)
	tags, handler, ok := r.sharedHandlerIIFE(node, "Effect", "catchTags")
	if !ok {
		return catchArm{}, false
	}
	binding, body, ok := r.catchHandler(handler)
	if !ok {
		return catchArm{}, false
	}
	return catchArm{tags: tags, binding: binding, body: body}, true
}

// catchHandler matches `(e) => Effect.gen(function* () { body })` and returns
// the binding name ("" for the throwaway '_') and the body block.
func (r *rewriter) catchHandler(node *ast.Node) (string, *ast.Node, bool) {
	if node.Kind != ast.KindArrowFunction {
		return "", nil, false
	}
	arrow := node.AsArrowFunction()
	if arrow.Modifiers() != nil || arrow.TypeParameters != nil || arrow.Type != nil || len(arrow.Parameters.Nodes) != 1 {
		return "", nil, false
	}
	p := arrow.Parameters.Nodes[0].AsParameterDeclaration()
	name := arrow.Parameters.Nodes[0].Name()
	if name == nil || name.Kind != ast.KindIdentifier || p.Type != nil || p.Initializer != nil || p.DotDotDotToken != nil || p.QuestionToken != nil {
		return "", nil, false
	}
	helper, method, args, ok := r.helperCall(arrow.Body)
	if !ok || helper != "Effect" || method != "gen" || len(args) != 1 {
		return "", nil, false
	}
	gen, ok := r.genArg(args[0], true)
	if !ok {
		return "", nil, false
	}
	binding := name.Text()
	if binding == "_" {
		binding = ""
	} else if !isValidIdentifier(binding) {
		return "", nil, false
	}
	return binding, gen.Body, true
}

// armBodyText renders an arm body: a single `return expr;` block becomes an
// expression arm, anything else a block arm. Both render under effect-body
// rules.
func (r *rewriter) armBodyText(block *ast.Node, indent string) string {
	stmts := block.AsBlock().Statements.Nodes
	if len(stmts) == 1 && stmts[0].Kind == ast.KindReturnStatement {
		if expr := stmts[0].AsReturnStatement().Expression; expr != nil {
			save := r.inEffectBody
			r.inEffectBody = true
			out := r.emit(expr)
			r.inEffectBody = save
			return out
		}
	}
	_ = indent
	return r.emitEffectBody(block)
}

// lineIndent returns the leading whitespace of the line containing pos.
func (r *rewriter) lineIndent(pos int) string {
	ls := strings.LastIndexByte(r.src[:pos], '\n') + 1
	end := ls
	for end < len(r.src) && (r.src[end] == ' ' || r.src[end] == '\t') {
		end++
	}
	return r.src[ls:end]
}
