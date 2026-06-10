package core

import (
	"context"
	"errors"
	"fmt"
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

// ---------------------------------------------------------------------------
// Round-3 fixes: @pos guards, ordinal ranges, merged declarations, overload
// groups, quoted ambient modules.

func TestDecodePositionIDLeadingTriviaErrors(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/m.ts": "// leading comment one\n// leading comment two\nexport const a = 1;\nexport function b(): number { return a; }\n",
	})
	ctx := context.Background()

	// Position inside the file-leading comment must NOT resolve to the
	// SourceFile (a delete would wipe the whole file).
	_, _, err := DecodeSymbolID(ctx, ws, "src/m.ts@8")
	if err == nil {
		t.Fatal("expected error for @pos in leading trivia")
	}
	if !errors.Is(err, ErrNotFound) || !errors.Is(err, ErrBadSymbolAddress) {
		t.Errorf("error should be ErrNotFound+ErrBadSymbolAddress, got %v", err)
	}
	if !strings.Contains(err.Error(), "no declaration at position 8 in src/m.ts") {
		t.Errorf("error = %q, want 'no declaration at position 8 in src/m.ts'", err.Error())
	}
	if !strings.Contains(err.Error(), "src/m.ts#a") {
		t.Errorf("error should suggest top-level declarations, got %q", err.Error())
	}

	// A position inside a real declaration still resolves.
	file, ferr := ws.FileOf("src/m.ts")
	if ferr != nil {
		t.Fatalf("FileOf: %v", ferr)
	}
	pos := strings.Index(file.Text(), "const a") + len("const ")
	symbol, decl, err := DecodeSymbolID(ctx, ws, fmt.Sprintf("src/m.ts@%d", pos))
	if err != nil || decl == nil || symbol == nil || symbol.Name != "a" {
		t.Errorf("@pos inside a declaration = (%v, %v, %v), want const a", symbol, decl, err)
	}

	// EOF errors too.
	_, _, err = DecodeSymbolID(ctx, ws, fmt.Sprintf("src/m.ts@%d", len(file.Text())))
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("@pos at EOF should be a not-found error, got %v", err)
	}
}

func TestDecodeSymbolIDOrdinalOutOfRange(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		// Symbol-table phase: merged interface+namespace x.
		"/project/src/m.ts": "export interface x { a: number }\nexport namespace x { export const b = 1; }\n",
		// Scope-scan phase: two locals named msg in sibling blocks.
		"/project/src/s.ts": "export function f(): string {\n\tif (1) { const msg = \"a\"; return msg; }\n\tconst msg = \"b\";\n\treturn msg;\n}\n",
	})
	ctx := context.Background()

	_, _, err := DecodeSymbolID(ctx, ws, "src/m.ts#x~7")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not-found for out-of-range table ordinal, got %v", err)
	}
	want := "ordinal ~7 out of range for src/m.ts#x (2 declarations: ~0..~1)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	_, _, err = DecodeSymbolID(ctx, ws, "src/s.ts#f.msg~7")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not-found for out-of-range scope ordinal, got %v", err)
	}
	want = "ordinal ~7 out of range for src/s.ts#f.msg (2 declarations: ~0..~1)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	// In-range ordinals still resolve in both phases.
	if _, decl, err := DecodeSymbolID(ctx, ws, "src/m.ts#x~1"); err != nil || decl == nil || decl.Kind != ast.KindModuleDeclaration {
		t.Errorf("decode(#x~1) = (%v, %v), want the namespace declaration", decl, err)
	}
	if _, decl, err := DecodeSymbolID(ctx, ws, "src/s.ts#f.msg~1"); err != nil || decl == nil {
		t.Errorf("decode(#f.msg~1) = (%v, %v), want the second msg", decl, err)
	}
}

func TestMergedDeclarationsOrdinalsSourceOrder(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/m.ts": `export interface Foo { a: number }
export namespace Foo { export const b = 1; }
export function Foo(): void {}

export class C {
	get x(): number { return 1; }
	set x(v: number) {}
}
`,
	})
	ctx := context.Background()
	file, err := ws.FileOf("src/m.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)

	// Encoder: source-order ordinals on every merged declaration.
	iface := findTestNode(t, file.AsNode(), ast.KindInterfaceDeclaration, "Foo")
	nsDecl := findTestNode(t, file.AsNode(), ast.KindModuleDeclaration, "Foo")
	fn := findTestNode(t, file.AsNode(), ast.KindFunctionDeclaration, "Foo")
	for _, test := range []struct {
		node *ast.Node
		want string
	}{
		{iface, "src/m.ts#Foo~0"},
		{nsDecl, "src/m.ts#Foo~1"},
		{fn, "src/m.ts#Foo~2"},
	} {
		if got := EncodeDeclID(ws, test.node); got != test.want {
			t.Errorf("EncodeDeclID = %q, want %q", got, test.want)
		}
		// Round trip.
		_, decl, err := DecodeSymbolID(ctx, ws, test.want)
		if err != nil || decl != test.node {
			t.Errorf("decode(%q) = (%v, %v), want the encoded node", test.want, decl, err)
		}
	}

	// Get/set pair.
	getter := findTestNode(t, file.AsNode(), ast.KindGetAccessor, "x")
	setter := findTestNode(t, file.AsNode(), ast.KindSetAccessor, "x")
	if got := EncodeDeclID(ws, getter); got != "src/m.ts#C.x~0" {
		t.Errorf("EncodeDeclID(getter) = %q, want src/m.ts#C.x~0", got)
	}
	if got := EncodeDeclID(ws, setter); got != "src/m.ts#C.x~1" {
		t.Errorf("EncodeDeclID(setter) = %q, want src/m.ts#C.x~1", got)
	}

	// Decoder: bare ambiguous ID errors listing kinds and lines.
	_, _, err = DecodeSymbolID(ctx, ws, "src/m.ts#Foo")
	if err == nil {
		t.Fatal("expected ambiguity error for bare merged ID")
	}
	if !errors.Is(err, ErrBadSymbolAddress) || !errors.Is(err, ErrNotFound) {
		t.Errorf("ambiguity error class = %v, want ErrBadSymbolAddress+ErrNotFound", err)
	}
	want := "ambiguous: src/m.ts#Foo matches 3 declarations: ~0 interface (line 1), ~1 namespace (line 2), ~2 function (line 3)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if _, _, err := DecodeSymbolID(ctx, ws, "src/m.ts#C.x"); err == nil {
		t.Error("expected ambiguity error for bare get/set ID")
	}

	// EncodeSymbolID carries the primary declaration's ordinal so printed
	// symbol IDs stay decodable.
	if id := EncodeSymbolID(ws, fn.Symbol()); !strings.Contains(id, "~") {
		t.Errorf("EncodeSymbolID(merged Foo) = %q, want an ordinal", id)
	}
}

func TestOverloadGroupBareID(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/ov.ts": `export function ov(x: number): number;
export function ov(x: string): string;
export function ov(x: number | string): number | string {
	return x;
}
`,
	})
	ctx := context.Background()
	file, err := ws.FileOf("src/ov.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)

	// Bare ID resolves to the whole group (no ambiguity error).
	symbol, decl, err := DecodeSymbolID(ctx, ws, "src/ov.ts#ov")
	if err != nil || symbol == nil || decl == nil {
		t.Fatalf("bare overload-group ID must decode, got (%v, %v, %v)", symbol, decl, err)
	}
	group := OverloadGroupDecls(symbol, file)
	if len(group) != 3 {
		t.Fatalf("OverloadGroupDecls = %d decls, want 3", len(group))
	}
	for i := 1; i < len(group); i++ {
		if group[i-1].Pos() >= group[i].Pos() {
			t.Error("overload group must be source-ordered")
		}
	}

	// ~N addresses individual signatures, and the encoder emits them.
	for i, want := range []string{"src/ov.ts#ov~0", "src/ov.ts#ov~1", "src/ov.ts#ov~2"} {
		if got := EncodeDeclID(ws, group[i]); got != want {
			t.Errorf("EncodeDeclID(sig %d) = %q, want %q", i, got, want)
		}
		_, sigDecl, err := DecodeSymbolID(ctx, ws, want)
		if err != nil || sigDecl != group[i] {
			t.Errorf("decode(%q) = (%v, %v), want signature %d", want, sigDecl, err, i)
		}
	}
}

func TestQuotedAmbientModuleIDs(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/types.d.ts": "declare module \"virtual-thing\" {\n\texport const vt: number;\n}\n",
		"/project/src/index.ts":   "export const x = 1;\n",
	})
	ctx := context.Background()
	file, err := ws.FileOf("src/types.d.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)

	mod := file.Statements.Nodes[0]
	if mod.Kind != ast.KindModuleDeclaration {
		t.Fatalf("statement 0 kind = %v, want module declaration", mod.Kind)
	}
	if got := EncodeDeclID(ws, mod); got != `src/types.d.ts#"virtual-thing"` {
		t.Errorf("EncodeDeclID(module) = %q, want src/types.d.ts#\"virtual-thing\"", got)
	}
	_, decl, err := DecodeSymbolID(ctx, ws, `src/types.d.ts#"virtual-thing"`)
	if err != nil || decl != mod {
		t.Errorf("decode(quoted module) = (%v, %v), want the module declaration", decl, err)
	}

	vt := findTestNode(t, file.AsNode(), ast.KindVariableDeclaration, "vt")
	if got := EncodeDeclID(ws, vt); got != `src/types.d.ts#"virtual-thing".vt` {
		t.Errorf("EncodeDeclID(vt) = %q, want src/types.d.ts#\"virtual-thing\".vt", got)
	}
	if _, decl, err := DecodeSymbolID(ctx, ws, `src/types.d.ts#"virtual-thing".vt`); err != nil || decl != vt {
		t.Errorf("decode(quoted member) = (%v, %v), want the vt declarator", decl, err)
	}
}

func TestComputedNameMemberFallback(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/k.ts": `export class K {
	[Symbol.iterator]() {
		return [].values();
	}
}
`,
	})
	ctx := context.Background()
	file, err := ws.FileOf("src/k.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	binder.BindSourceFile(file)
	classNode := findTestNode(t, file.AsNode(), ast.KindClassDeclaration, "K")
	var method *ast.Node
	for _, m := range classNode.Members() {
		if m.Kind == ast.KindMethodDeclaration {
			method = m
		}
	}
	if method == nil {
		t.Fatal("no method found")
	}
	id := EncodeDeclID(ws, method)
	if !strings.Contains(id, "@") || strings.Contains(id, "#") {
		t.Fatalf("computed-name member must use the @pos fallback, got %q", id)
	}
	if _, decl, err := DecodeSymbolID(ctx, ws, id); err != nil || decl == nil {
		t.Errorf("the @pos fallback ID must decode, got (%v, %v)", decl, err)
	}
	// SuggestSymbolIDs must never offer the undecodable @computed form.
	for _, s := range SuggestSymbolIDs(ws, file, "src/k.ts#K.computed", 5) {
		if strings.Contains(s, "@computed") {
			t.Errorf("SuggestSymbolIDs offered undecodable %q", s)
		}
		if _, _, err := DecodeSymbolID(ctx, ws, s); err != nil {
			t.Errorf("suggested ID %q does not decode: %v", s, err)
		}
	}
}
