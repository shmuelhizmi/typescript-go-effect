package core

import (
	"github.com/microsoft/typescript-go/internal/ast"
)

// This file is the single source of truth for "named scope" navigation used
// by the extended symbol ID scheme (encode, decode, and the outline walker):
// which declarations live directly in a scope body, and how a declaration's
// chain of enclosing named scopes maps to dotted ID segments.

// scopeBody returns the body node within which a scope container's local
// declarations live: a function-like's block body, the initializer's body for
// `const f = () => {}`-style declarators and properties, or a namespace's
// module block. Returns nil when the container has no such body (overload
// signatures, expression-bodied arrows, plain variables, ...).
func scopeBody(container *ast.Node) *ast.Node {
	if container == nil {
		return nil
	}
	if ast.IsFunctionLike(container) {
		if body := container.Body(); body != nil && body.Kind == ast.KindBlock {
			return body
		}
		return nil
	}
	switch container.Kind {
	case ast.KindVariableDeclaration, ast.KindPropertyDeclaration, ast.KindPropertyAssignment:
		if init := container.Initializer(); init != nil && ast.IsFunctionLike(init) {
			if body := init.Body(); body != nil && body.Kind == ast.KindBlock {
				return body
			}
		}
	case ast.KindModuleDeclaration:
		if body := container.Body(); body != nil && body.Kind == ast.KindModuleBlock {
			return body
		}
	}
	return nil
}

// ScopeDeclarations returns the named declarations directly within a scope
// container's body, in source order. Transparent blocks (if/for/while/do/
// try/catch/switch/labeled/bare blocks) are descended; nested function-like
// and class-like scopes are not. Collected nodes are function, class,
// interface, enum, type-alias, and namespace declarations plus variable
// declarators with identifier names (including `const f = () => {}`
// declarators, which are scope containers themselves) and NAMED class/
// function expressions reachable through return statements, expression
// statements, declarator initializers, and expression-bodied arrows (the
// `const f = (cfg) => class Name extends Base {}` factory pattern). The
// container may be any function-like node, a variable declarator or property
// whose initializer is function-like, or a namespace declaration; anything
// else yields nil.
func ScopeDeclarations(container *ast.Node) []*ast.Node {
	var decls []*ast.Node
	if body := scopeBody(container); body != nil {
		collectScopeDecls(body, &decls)
	} else if expr := scopeExpressionBody(container); expr != nil {
		collectScopeExprDecls(expr, &decls)
	}
	return decls
}

// scopeExpressionBody returns the expression body of an expression-bodied
// arrow container (`const f = () => expr`), directly or through a declarator
// or property initializer. Returns nil for everything else.
func scopeExpressionBody(container *ast.Node) *ast.Node {
	if container == nil {
		return nil
	}
	fn := container
	if !ast.IsFunctionLike(fn) {
		switch container.Kind {
		case ast.KindVariableDeclaration, ast.KindPropertyDeclaration, ast.KindPropertyAssignment:
			init := container.Initializer()
			if init == nil || !ast.IsFunctionLike(init) {
				return nil
			}
			fn = init
		default:
			return nil
		}
	}
	if body := fn.Body(); body != nil && body.Kind != ast.KindBlock {
		return body
	}
	return nil
}

// collectScopeExprDecls collects the NAMED class and function expressions an
// expression directly evaluates to (parentheses unwrapped). Anonymous ones
// stay invisible — they keep the position-fallback addressing.
func collectScopeExprDecls(expr *ast.Node, decls *[]*ast.Node) {
	if expr == nil {
		return
	}
	switch expr.Kind {
	case ast.KindParenthesizedExpression:
		collectScopeExprDecls(expr.Expression(), decls)
	case ast.KindClassExpression, ast.KindFunctionExpression:
		if scopeDeclarationName(expr) != "" {
			*decls = append(*decls, expr)
		}
	}
}

// collectScopeDecls appends the named declarations found in a statement (or
// block-like node) to decls, descending transparent blocks and stopping at
// nested scopes.
func collectScopeDecls(n *ast.Node, decls *[]*ast.Node) {
	if n == nil {
		return
	}
	switch n.Kind {
	case ast.KindBlock:
		for _, s := range n.AsBlock().Statements.Nodes {
			collectScopeDecls(s, decls)
		}
	case ast.KindModuleBlock:
		for _, s := range n.AsModuleBlock().Statements.Nodes {
			collectScopeDecls(s, decls)
		}
	case ast.KindVariableStatement:
		collectDeclarationList(n.AsVariableStatement().DeclarationList, decls)
	case ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindInterfaceDeclaration,
		ast.KindEnumDeclaration, ast.KindTypeAliasDeclaration, ast.KindModuleDeclaration:
		if scopeDeclarationName(n) != "" {
			*decls = append(*decls, n)
		}
	case ast.KindReturnStatement:
		collectScopeExprDecls(n.Expression(), decls)
	case ast.KindExpressionStatement:
		collectScopeExprDecls(n.Expression(), decls)
	case ast.KindIfStatement:
		stmt := n.AsIfStatement()
		collectScopeDecls(stmt.ThenStatement, decls)
		collectScopeDecls(stmt.ElseStatement, decls)
	case ast.KindForStatement:
		stmt := n.AsForStatement()
		collectDeclarationList(stmt.Initializer, decls)
		collectScopeDecls(stmt.Statement, decls)
	case ast.KindForInStatement, ast.KindForOfStatement:
		stmt := n.AsForInOrOfStatement()
		collectDeclarationList(stmt.Initializer, decls)
		collectScopeDecls(stmt.Statement, decls)
	case ast.KindWhileStatement:
		collectScopeDecls(n.AsWhileStatement().Statement, decls)
	case ast.KindDoStatement:
		collectScopeDecls(n.AsDoStatement().Statement, decls)
	case ast.KindLabeledStatement:
		collectScopeDecls(n.AsLabeledStatement().Statement, decls)
	case ast.KindTryStatement:
		stmt := n.AsTryStatement()
		collectScopeDecls(stmt.TryBlock, decls)
		if stmt.CatchClause != nil {
			// The catch parameter is not a scope declaration; only the
			// declarations inside the catch block are collected.
			collectScopeDecls(stmt.CatchClause.AsCatchClause().Block, decls)
		}
		collectScopeDecls(stmt.FinallyBlock, decls)
	case ast.KindSwitchStatement:
		if caseBlock := n.AsSwitchStatement().CaseBlock; caseBlock != nil {
			for _, clause := range caseBlock.AsCaseBlock().Clauses.Nodes {
				for _, s := range clause.AsCaseOrDefaultClause().Statements.Nodes {
					collectScopeDecls(s, decls)
				}
			}
		}
	}
}

// collectDeclarationList appends a variable declaration list's
// identifier-named declarators (binding patterns are skipped), plus any named
// class/function expression a declarator is initialized with.
func collectDeclarationList(list *ast.Node, decls *[]*ast.Node) {
	if list == nil || list.Kind != ast.KindVariableDeclarationList {
		return
	}
	for _, d := range list.AsVariableDeclarationList().Declarations.Nodes {
		if name := d.Name(); name != nil && name.Kind == ast.KindIdentifier {
			*decls = append(*decls, d)
		}
		collectScopeExprDecls(d.Initializer(), decls)
	}
}

// scopeDeclarationName returns a declaration's identifier name, or "" when
// it has no plain identifier name (anonymous default exports, binding
// patterns, computed/string names).
func scopeDeclarationName(n *ast.Node) string {
	if name := n.Name(); name != nil && name.Kind == ast.KindIdentifier {
		return name.Text()
	}
	return ""
}

// scopeSegmentName returns the ID segment for a declaration node itself
// (constructors use the readable internal-name encoding).
func scopeSegmentName(n *ast.Node) string {
	if n.Kind == ast.KindConstructor {
		return "@constructor"
	}
	return scopeDeclarationName(n)
}

// scopeChainSegments returns the qualified-ID segments (without the file
// part) for a declaration node by walking up through its enclosing named
// scope containers. ok is false when the node or any enclosing scope cannot
// be named (anonymous callbacks, IIFEs, anonymous class/function expressions,
// static blocks): those keep the `@pos` fallback.
func scopeChainSegments(node *ast.Node) (segments []string, ok bool) {
	seg := scopeSegmentName(node)
	if seg == "" {
		return nil, false
	}
	segments = []string{seg}
	cur := node.Parent
	for cur != nil && cur.Kind != ast.KindSourceFile {
		name, container, isScope := namedScopeOf(cur)
		if !isScope {
			cur = cur.Parent
			continue
		}
		if name == "" {
			return nil, false
		}
		segments = append([]string{name}, segments...)
		cur = container.Parent
	}
	return segments, true
}

// namedScopeOf classifies a node on the parent walk: isScope reports whether
// it is a scope boundary for the ID scheme, and name is its segment name
// ("" for unnameable scopes, which force the position fallback). For arrows
// and function expressions that initialize a named declarator or property,
// the declarator names the scope and is returned as the container.
func namedScopeOf(n *ast.Node) (name string, container *ast.Node, isScope bool) {
	switch n.Kind {
	case ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindInterfaceDeclaration,
		ast.KindEnumDeclaration, ast.KindModuleDeclaration,
		ast.KindMethodDeclaration, ast.KindGetAccessor, ast.KindSetAccessor:
		return scopeDeclarationName(n), n, true
	case ast.KindConstructor:
		return "@constructor", n, true
	case ast.KindFunctionExpression, ast.KindArrowFunction:
		if parent := n.Parent; parent != nil {
			switch parent.Kind {
			case ast.KindVariableDeclaration, ast.KindPropertyDeclaration, ast.KindPropertyAssignment:
				if parent.Initializer() == n {
					if declName := scopeDeclarationName(parent); declName != "" {
						return declName, parent, true
					}
				}
			}
		}
		// A named function expression names its own scope.
		return scopeDeclarationName(n), n, true
	case ast.KindClassExpression:
		// Named class expressions (`const f = () => class Name {}`) are
		// addressable scopes; anonymous ones force the position fallback.
		return scopeDeclarationName(n), n, true
	case ast.KindClassStaticBlockDeclaration:
		return "", n, true
	}
	return "", nil, false
}

// allFileDeclarations enumerates every declaration node in a file that could
// carry a qualified symbol ID: top-level declarations (descending transparent
// blocks), class/interface/enum members, namespace bodies, and scope-local
// declarations, recursively.
func allFileDeclarations(file *ast.SourceFile) []*ast.Node {
	var queue []*ast.Node
	for _, s := range file.Statements.Nodes {
		collectScopeDecls(s, &queue)
	}
	for i := 0; i < len(queue); i++ {
		decl := queue[i]
		switch decl.Kind {
		case ast.KindClassDeclaration, ast.KindClassExpression, ast.KindInterfaceDeclaration:
			for _, m := range decl.Members() {
				switch m.Kind {
				case ast.KindMethodDeclaration, ast.KindMethodSignature, ast.KindPropertyDeclaration,
					ast.KindPropertySignature, ast.KindConstructor, ast.KindGetAccessor, ast.KindSetAccessor:
					queue = append(queue, m)
				}
			}
		case ast.KindEnumDeclaration:
			queue = append(queue, decl.AsEnumDeclaration().Members.Nodes...)
		default:
			queue = append(queue, ScopeDeclarations(decl)...)
		}
	}
	return queue
}
