package core

import (
	"slices"
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/binder"
)

// findTestNode returns the first node (preorder) of the given kind whose
// declaration name is name.
func findTestNode(t *testing.T, root *ast.Node, kind ast.Kind, name string) *ast.Node {
	t.Helper()
	var found *ast.Node
	var visit func(n *ast.Node) bool
	visit = func(n *ast.Node) bool {
		if found != nil {
			return true
		}
		if n.Kind == kind {
			if nameNode := n.Name(); nameNode != nil && nameNode.Kind == ast.KindIdentifier && nameNode.Text() == name {
				found = n
				return true
			}
		}
		n.ForEachChild(visit)
		return false
	}
	root.ForEachChild(visit)
	if found == nil {
		t.Fatalf("no %v named %q in test source", kind, name)
	}
	return found
}

func scopeTestFile(t *testing.T) (*Workspace, *ast.SourceFile) {
	t.Helper()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/scopes.ts": `
export function outer(flag: boolean): number {
	const a = 1;
	if (flag) { const b = 2; } else { const c = 3; }
	for (let i = 0; i < 3; i++) { const inFor = i; }
	for (const item of [1, 2]) {}
	while (flag) { const inWhile = 4; }
	label: { const inLabel = 5; }
	try {
		const inTry = 6;
	} catch (err) {
		const inCatch = 7;
	} finally {
		const inFinally = 8;
	}
	switch (a) {
		case 1: { const inCase = 9; break; }
		default: { const inDefault = 10; }
	}
	function localFn(): void { const hidden = 11; }
	class LocalCls { m(): void { const inMethod = 12; } }
	type LocalAlias = string;
	const cb = [1].map((n) => { const inCallback = n; return n; });
	return a;
}

const handler = (req: string): number => {
	function parse(body: string): number { return body.length; }
	return parse(req);
};

const justAValue = 42;
const exprArrow = (x: number) => x + 1;

export namespace ns {
	export function fn(): void {}
	const nsLocal = 1;
}
`,
	})
	file, err := ws.FileOf("src/scopes.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)
	return ws, file
}

func declNames(decls []*ast.Node) []string {
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		names = append(names, scopeDeclarationName(d))
	}
	return names
}

func TestScopeDeclarationsFunction(t *testing.T) {
	t.Parallel()
	_, file := scopeTestFile(t)
	outer := findTestNode(t, file.AsNode(), ast.KindFunctionDeclaration, "outer")

	got := declNames(ScopeDeclarations(outer))
	want := []string{
		"a", "b", "c", "i", "inFor", "item", "inWhile", "inLabel",
		"inTry", "inCatch", "inFinally", "inCase", "inDefault",
		"localFn", "LocalCls", "LocalAlias", "cb",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ScopeDeclarations(outer) = %v, want %v", got, want)
	}

	// The catch parameter is not a scope declaration.
	if slices.Contains(got, "err") {
		t.Error("catch parameter must not be collected")
	}
	// Nested function/class/callback bodies are opaque.
	for _, hiddenName := range []string{"hidden", "m", "inMethod", "inCallback", "n"} {
		if slices.Contains(got, hiddenName) {
			t.Errorf("nested scope leaked %q into outer's declarations", hiddenName)
		}
	}
}

func TestScopeDeclarationsArrowConstDeclarator(t *testing.T) {
	t.Parallel()
	_, file := scopeTestFile(t)
	handler := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "handler")
	if got := declNames(ScopeDeclarations(handler)); !slices.Equal(got, []string{"parse"}) {
		t.Errorf("ScopeDeclarations(handler) = %v, want [parse]", got)
	}
}

func TestScopeDeclarationsNamespace(t *testing.T) {
	t.Parallel()
	_, file := scopeTestFile(t)
	ns := findTestNode(t, file.AsNode(), ast.KindModuleDeclaration, "ns")
	if got := declNames(ScopeDeclarations(ns)); !slices.Equal(got, []string{"fn", "nsLocal"}) {
		t.Errorf("ScopeDeclarations(ns) = %v, want [fn nsLocal]", got)
	}
}

func TestScopeDeclarationsNonContainers(t *testing.T) {
	t.Parallel()
	_, file := scopeTestFile(t)
	plain := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "justAValue")
	if got := ScopeDeclarations(plain); got != nil {
		t.Errorf("plain const should have no scope declarations, got %v", declNames(got))
	}
	// Expression-bodied arrows have no block body.
	exprArrow := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "exprArrow")
	if got := ScopeDeclarations(exprArrow); got != nil {
		t.Errorf("expression-bodied arrow should have no scope declarations, got %v", declNames(got))
	}
}

func TestScopeDeclarationsLocalFunctionIsItselfAContainer(t *testing.T) {
	t.Parallel()
	_, file := scopeTestFile(t)
	localFn := findTestNode(t, file.AsNode(), ast.KindFunctionDeclaration, "localFn")
	if got := declNames(ScopeDeclarations(localFn)); !slices.Equal(got, []string{"hidden"}) {
		t.Errorf("ScopeDeclarations(localFn) = %v, want [hidden]", got)
	}
}

func TestScopeDeclarationsNamedClassExpressions(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/factory.ts": `export const exprFactory = () => class ExprNode {
	m(): number { return 1; }
};

export function blockFactory() {
	const Local = class NamedLocal {};
	(class Stmt {});
	return class RetNode {};
}

export const anonExpr = () => class {};
`,
	})
	file, err := ws.FileOf("src/factory.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)

	// Expression-bodied arrow: the named class expression is its scope content.
	exprFactory := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "exprFactory")
	if got := declNames(ScopeDeclarations(exprFactory)); !slices.Equal(got, []string{"ExprNode"}) {
		t.Errorf("ScopeDeclarations(exprFactory) = %v, want [ExprNode]", got)
	}

	// Block body: declarator initializers, expression statements, and return
	// expressions all surface named class expressions.
	blockFactory := findTestNode(t, file.AsNode(), ast.KindFunctionDeclaration, "blockFactory")
	if got := declNames(ScopeDeclarations(blockFactory)); !slices.Equal(got, []string{"Local", "NamedLocal", "Stmt", "RetNode"}) {
		t.Errorf("ScopeDeclarations(blockFactory) = %v, want [Local NamedLocal Stmt RetNode]", got)
	}

	// Anonymous class expressions stay invisible.
	anonExpr := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "anonExpr")
	if got := ScopeDeclarations(anonExpr); got != nil {
		t.Errorf("anonymous class expression should be invisible, got %v", declNames(got))
	}
}
