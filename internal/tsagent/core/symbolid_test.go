package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/binder"
)

func TestSymbolIDRoundTrip(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `
export class Greeter {
	greeting: string = "hi";
	constructor(greeting: string) {
		this.greeting = greeting;
	}
	greet(name: string): string {
		return this.greeting + name;
	}
}

export function topLevel(x: number): number {
	return x * 2;
}

function privateHelper(): void {}

export const answer = 42;
`,
	})
	ctx := context.Background()

	for _, test := range []struct {
		name string
		id   string
	}{
		{"exported class", "src/index.ts#Greeter"},
		{"class method", "src/index.ts#Greeter.greet"},
		{"constructor (internal name)", "src/index.ts#Greeter.@constructor"},
		{"exported function", "src/index.ts#topLevel"},
		{"non-exported function", "src/index.ts#privateHelper"},
		{"exported const", "src/index.ts#answer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			symbol, decl, err := DecodeSymbolID(ctx, ws, test.id)
			if err != nil {
				t.Fatalf("DecodeSymbolID(%q): %v", test.id, err)
			}
			if decl == nil {
				t.Fatalf("DecodeSymbolID(%q): nil declaration", test.id)
			}
			encoded := EncodeSymbolID(ws, symbol)
			if encoded != test.id {
				t.Errorf("round trip: got %q, want %q", encoded, test.id)
			}
		})
	}
}

func TestSymbolIDLocalFallback(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `
export function outer(): number {
	const local = 1;
	return local;
}
`,
	})
	ctx := context.Background()

	// Resolve the local variable through a position target, then encode.
	target, err := ws.ResolveTarget(ctx, TargetSpec{At: "src/index.ts:3:8"})
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target.Symbol == nil {
		t.Fatal("expected a symbol at local declaration")
	}
	id := EncodeSymbolID(ws, target.Symbol)
	if !strings.Contains(id, "@") {
		t.Fatalf("expected position-encoded fallback id for a local, got %q", id)
	}
	symbol, _, err := DecodeSymbolID(ctx, ws, id)
	if err != nil {
		t.Fatalf("DecodeSymbolID(%q): %v", id, err)
	}
	if symbol.Name != "local" {
		t.Errorf("decoded symbol name = %q, want %q", symbol.Name, "local")
	}
}

func TestDecodeSymbolIDNotFound(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `export const x = 1;`,
	})
	if _, _, err := DecodeSymbolID(context.Background(), ws, "src/index.ts#doesNotExist"); err == nil {
		t.Fatal("expected error for unknown symbol id")
	}
}

// ---------------------------------------------------------------------------
// Scope-qualified IDs (EncodeDeclID / decode phase 2)

func nestedIDTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	return newTestWorkspace(t, map[string]any{
		"/project/src/nested.ts": `
export function outer(flag: boolean): number {
	const inner = 1;
	if (flag) { const tmp = 2; }
	function localFn(): number { const deep = 3; return deep; }
	return inner + localFn();
}

export const handler = (req: string): number => {
	function helper(body: string): number { return body.length; }
	return helper(req);
};

export namespace ns {
	export function fn(): number {
		const local = 4;
		return local;
	}
}

export class C {
	m(): number {
		const x = 5;
		return x;
	}
}
`,
	})
}

func TestEncodeDeclIDNestedRoundTrip(t *testing.T) {
	t.Parallel()
	ws := nestedIDTestWorkspace(t)
	ctx := context.Background()
	for _, test := range []struct {
		name string
		id   string
	}{
		{"local const", "src/nested.ts#outer.inner"},
		{"const inside transparent if-block", "src/nested.ts#outer.tmp"},
		{"local function", "src/nested.ts#outer.localFn"},
		{"local of a local function", "src/nested.ts#outer.localFn.deep"},
		{"function inside named arrow const", "src/nested.ts#handler.helper"},
		{"local of a namespace function", "src/nested.ts#ns.fn.local"},
		{"local of a class method", "src/nested.ts#C.m.x"},
	} {
		t.Run(test.name, func(t *testing.T) {
			symbol, decl, err := DecodeSymbolID(ctx, ws, test.id)
			if err != nil {
				t.Fatalf("DecodeSymbolID(%q): %v", test.id, err)
			}
			if decl == nil {
				t.Fatalf("DecodeSymbolID(%q): nil declaration", test.id)
			}
			if symbol == nil {
				t.Fatalf("DecodeSymbolID(%q): nil symbol", test.id)
			}
			wantName := test.id[strings.LastIndexByte(test.id, '.')+1:]
			if symbol.Name != wantName {
				t.Errorf("decoded symbol name = %q, want %q", symbol.Name, wantName)
			}
			if encoded := EncodeDeclID(ws, decl); encoded != test.id {
				t.Errorf("round trip: got %q, want %q", encoded, test.id)
			}
		})
	}
}

func TestEncodeDeclIDMatchesEncodeSymbolIDForQualifiedSymbols(t *testing.T) {
	t.Parallel()
	ws := nestedIDTestWorkspace(t)
	ctx := context.Background()
	// Everything encodable through the symbol-parent walk must stay
	// byte-identical between EncodeSymbolID and EncodeDeclID.
	for _, id := range []string{
		"src/nested.ts#outer",
		"src/nested.ts#handler",
		"src/nested.ts#ns",
		"src/nested.ts#ns.fn",
		"src/nested.ts#C",
		"src/nested.ts#C.m",
	} {
		symbol, decl, err := DecodeSymbolID(ctx, ws, id)
		if err != nil {
			t.Fatalf("DecodeSymbolID(%q): %v", id, err)
		}
		if got := EncodeSymbolID(ws, symbol); got != id {
			t.Errorf("EncodeSymbolID round trip: got %q, want %q", got, id)
		}
		if got := EncodeDeclID(ws, decl); got != id {
			t.Errorf("EncodeDeclID: got %q, want %q", got, id)
		}
	}
}

func TestEncodeDeclIDShadowingOrdinals(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/shadow.ts": `
export function f(): string {
	try {
		return "a";
	} catch (e) {
		const msg = "first";
		return msg;
	} finally {
	}
	try {
		return "b";
	} catch (e) {
		const msg = "second";
		return msg;
	}
}
`,
	})
	ctx := context.Background()

	first, firstDecl, err := DecodeSymbolID(ctx, ws, "src/shadow.ts#f.msg~0")
	if err != nil {
		t.Fatalf("DecodeSymbolID(~0): %v", err)
	}
	second, secondDecl, err := DecodeSymbolID(ctx, ws, "src/shadow.ts#f.msg~1")
	if err != nil {
		t.Fatalf("DecodeSymbolID(~1): %v", err)
	}
	if first == second || firstDecl == secondDecl {
		t.Fatal("ordinals must select distinct declarations")
	}
	if firstDecl.Pos() >= secondDecl.Pos() {
		t.Error("ordinals must be in source order")
	}
	if got := EncodeDeclID(ws, firstDecl); got != "src/shadow.ts#f.msg~0" {
		t.Errorf("encode first = %q, want src/shadow.ts#f.msg~0", got)
	}
	if got := EncodeDeclID(ws, secondDecl); got != "src/shadow.ts#f.msg~1" {
		t.Errorf("encode second = %q, want src/shadow.ts#f.msg~1", got)
	}

	// A bare ambiguous name errors, listing the ordinal candidates.
	_, _, err = DecodeSymbolID(ctx, ws, "src/shadow.ts#f.msg")
	if err == nil {
		t.Fatal("expected ambiguity error for bare shadowed name")
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("ambiguity error should be ErrInvalidArgument, got %v", err)
	}
	if !strings.Contains(err.Error(), "msg~0") || !strings.Contains(err.Error(), "msg~1") {
		t.Errorf("ambiguity error should list candidates msg~0/msg~1, got %q", err.Error())
	}

	// The catch parameter itself is not scope-addressable: it keeps the
	// position fallback.
	file, err := ws.FileOf("src/shadow.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)
	catchParam := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "e")
	if id := EncodeDeclID(ws, catchParam); !strings.Contains(id, "@") {
		t.Errorf("catch parameter should keep the position fallback, got %q", id)
	}
}

func TestEncodeDeclIDAnonymousCallbackFallback(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/cb.ts": `
export function g(): number[] {
	return [1, 2].map((n) => {
		const inCallback = n + 1;
		return inCallback;
	});
}
`,
	})
	ctx := context.Background()
	file, err := ws.FileOf("src/cb.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)
	decl := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "inCallback")
	id := EncodeDeclID(ws, decl)
	if !strings.Contains(id, "@") {
		t.Fatalf("expected position fallback for callback local, got %q", id)
	}
	symbol, _, err := DecodeSymbolID(ctx, ws, id)
	if err != nil {
		t.Fatalf("DecodeSymbolID(%q): %v", id, err)
	}
	if symbol.Name != "inCallback" {
		t.Errorf("decoded symbol name = %q, want inCallback", symbol.Name)
	}
}

func TestDecodeSymbolIDTablePrecedence(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/prec.ts": `
export namespace ns {
	export type x = number;
	const x = 1;
	const y = 2;
}
`,
	})
	ctx := context.Background()

	// The exported (table) declaration wins for #ns.x.
	symbol, decl, err := DecodeSymbolID(ctx, ws, "src/prec.ts#ns.x")
	if err != nil {
		t.Fatalf("DecodeSymbolID(#ns.x): %v", err)
	}
	if decl == nil || decl.Kind != ast.KindTypeAliasDeclaration {
		t.Fatalf("table-resolved #ns.x should be the exported type alias, got %v", decl.Kind)
	}
	if symbol.Name != "x" {
		t.Errorf("symbol name = %q, want x", symbol.Name)
	}

	// The shadowed local const x is therefore NOT scope-addressable: it must
	// keep the position fallback.
	file, err := ws.FileOf("src/prec.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)
	constX := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "x")
	if id := EncodeDeclID(ws, constX); !strings.Contains(id, "@") {
		t.Errorf("local const x shadowed by an export must use position fallback, got %q", id)
	}

	// An uncontested namespace-local resolves through the scope walk.
	constY := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "y")
	if id := EncodeDeclID(ws, constY); id != "src/prec.ts#ns.y" {
		t.Errorf("namespace-local const y = %q, want src/prec.ts#ns.y", id)
	}
	if _, yDecl, err := DecodeSymbolID(ctx, ws, "src/prec.ts#ns.y"); err != nil || yDecl != constY {
		t.Errorf("DecodeSymbolID(#ns.y) = (%v, %v), want the const y declarator", yDecl, err)
	}
}

func TestSuggestSymbolIDs(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/server.ts": `
export function startServer(port: number): number {
	function logRequest(req: string): void {}
	return port;
}
export function stopServer(): void {}
export const handler = (req: string): number => {
	function parse(body: string): number { return body.length; }
	return parse(req);
};
`,
	})
	file, err := ws.FileOf("src/server.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}

	got := SuggestSymbolIDs(ws, file, "src/server.ts#startSrver", 3)
	if len(got) == 0 || got[0] != "src/server.ts#startServer" {
		t.Errorf("SuggestSymbolIDs(startSrver) = %v, want src/server.ts#startServer first", got)
	}

	got = SuggestSymbolIDs(ws, file, "src/server.ts#handler.prse", 3)
	if len(got) == 0 || got[0] != "src/server.ts#handler.parse" {
		t.Errorf("SuggestSymbolIDs(handler.prse) = %v, want src/server.ts#handler.parse first", got)
	}

	// max caps the result count.
	if got = SuggestSymbolIDs(ws, file, "src/server.ts#s", 2); len(got) > 2 {
		t.Errorf("SuggestSymbolIDs max=2 returned %d results", len(got))
	}
}

func TestSymbolIDClassExpressionFactory(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/factory.ts": `export interface Cfg { tag: string; }

export const createTagNodeClass = (cfg: Cfg) => class TagNode {
	tag(): string { return cfg.tag; }
};

export function blockFactory(cfg: Cfg) {
	return class BlockNode {
		label(): string { return cfg.tag; }
	};
}

export const anonFactory = (cfg: Cfg) => class {
	x(): number { return 1; }
};
`,
	})
	file, err := ws.FileOf("src/factory.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}

	// Expression-bodied arrow factory: the named class expression and its
	// members round-trip through qualified IDs.
	tagNode := findTestNode(t, file.AsNode(), ast.KindClassExpression, "TagNode")
	if id := EncodeDeclID(ws, tagNode); id != "src/factory.ts#createTagNodeClass.TagNode" {
		t.Errorf("EncodeDeclID(TagNode) = %q, want src/factory.ts#createTagNodeClass.TagNode", id)
	}
	_, decl, err := DecodeSymbolID(context.Background(), ws, "src/factory.ts#createTagNodeClass.TagNode")
	if err != nil {
		t.Fatalf("DecodeSymbolID(#createTagNodeClass.TagNode): %v", err)
	}
	if decl != tagNode {
		t.Errorf("decode(#createTagNodeClass.TagNode) = %v at %d, want the class expression", decl.Kind, decl.Pos())
	}

	tagMethod := findTestNode(t, file.AsNode(), ast.KindMethodDeclaration, "tag")
	if id := EncodeDeclID(ws, tagMethod); id != "src/factory.ts#createTagNodeClass.TagNode.tag" {
		t.Errorf("EncodeDeclID(tag) = %q, want src/factory.ts#createTagNodeClass.TagNode.tag", id)
	}
	symbol, decl, err := DecodeSymbolID(context.Background(), ws, "src/factory.ts#createTagNodeClass.TagNode.tag")
	if err != nil {
		t.Fatalf("DecodeSymbolID(#createTagNodeClass.TagNode.tag): %v", err)
	}
	if symbol == nil || decl != tagMethod {
		t.Errorf("decode of the class-expression method did not hand back the member (decl=%v)", decl)
	}

	// `return class BlockNode ...` inside a block body.
	blockNode := findTestNode(t, file.AsNode(), ast.KindClassExpression, "BlockNode")
	if id := EncodeDeclID(ws, blockNode); id != "src/factory.ts#blockFactory.BlockNode" {
		t.Errorf("EncodeDeclID(BlockNode) = %q, want src/factory.ts#blockFactory.BlockNode", id)
	}
	if _, decl, err := DecodeSymbolID(context.Background(), ws, "src/factory.ts#blockFactory.BlockNode.label"); err != nil || decl == nil || decl.Kind != ast.KindMethodDeclaration {
		t.Errorf("decode(#blockFactory.BlockNode.label) = %v, %v; want the method", decl, err)
	}

	// Anonymous class expressions keep the position fallback.
	xMethod := findTestNode(t, file.AsNode(), ast.KindMethodDeclaration, "x")
	if id := EncodeDeclID(ws, xMethod); !strings.Contains(id, "@") {
		t.Errorf("EncodeDeclID(method of anonymous class) = %q, want a position fallback", id)
	}
}
