package core

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/checker"
)

const typeEncodeSource = `export type Status = "on" | "off" | number;

export interface Shape {
	kind: "circle" | "square";
	radius?: number;
}

export type Fn = (a: string, b: number) => boolean;

export interface Box<T> {
	value: T;
}

export const box: Box<string> = { value: "hi" };

export type Simple = string;

export type Deep<T> = T extends string ? { a: T; b: number } : never;

export type Wide = { a: string; b: number; c: boolean } | { d: string } | null;
`

// declaredType resolves a symbol ID and returns its declared type from c.
func declaredType(t *testing.T, ctx context.Context, ws *Workspace, c *checker.Checker, id string) *checker.Type {
	t.Helper()
	symbol, _, err := DecodeSymbolID(ctx, ws, id)
	if err != nil {
		t.Fatalf("DecodeSymbolID(%s): %v", id, err)
	}
	typ := c.GetDeclaredTypeOfSymbol(symbol)
	if typ == nil {
		t.Fatalf("no declared type for %s", id)
	}
	return typ
}

func encodeTestSetup(t *testing.T) (context.Context, *Workspace, *checker.Checker, func()) {
	t.Helper()
	ws := newTestWorkspace(t, map[string]any{"/project/src/types.ts": typeEncodeSource})
	ctx := context.Background()
	c, done := ws.Program.GetTypeChecker(ctx)
	return ctx, ws, c, done
}

func TestTypeEncodeUnion(t *testing.T) {
	t.Parallel()
	ctx, ws, c, done := encodeTestSetup(t)
	defer done()

	status := declaredType(t, ctx, ws, c, "src/types.ts#Status")
	encoded := EncodeType(c, status, nil, 1)
	if encoded.Kind != "union" {
		t.Errorf("Status kind = %q, want union", encoded.Kind)
	}
	if len(encoded.UnionMembers) != 3 {
		t.Fatalf("Status union members = %d, want 3", len(encoded.UnionMembers))
	}
	var displays []string
	for _, m := range encoded.UnionMembers {
		displays = append(displays, m.Display)
	}
	joined := strings.Join(displays, " ")
	for _, want := range []string{`"on"`, `"off"`, "number"} {
		if !strings.Contains(joined, want) {
			t.Errorf("union members %v missing %s", displays, want)
		}
	}
	hasFlag := false
	for _, f := range encoded.Flags {
		if f == "Union" {
			hasFlag = true
		}
	}
	if !hasFlag {
		t.Errorf("Status flags = %v, want Union present", encoded.Flags)
	}
}

func TestTypeEncodeObject(t *testing.T) {
	t.Parallel()
	ctx, ws, c, done := encodeTestSetup(t)
	defer done()

	shape := declaredType(t, ctx, ws, c, "src/types.ts#Shape")
	encoded := EncodeType(c, shape, nil, 1)
	if encoded.Kind != "object" {
		t.Errorf("Shape kind = %q, want object", encoded.Kind)
	}
	if len(encoded.Properties) != 2 {
		t.Fatalf("Shape properties = %d, want 2", len(encoded.Properties))
	}
	byName := map[string]TypePropertyJSON{}
	for _, p := range encoded.Properties {
		byName[p.Name] = p
	}
	if kind, ok := byName["kind"]; !ok || !strings.Contains(kind.Type, "circle") {
		t.Errorf("kind property = %+v", byName["kind"])
	}
	if radius, ok := byName["radius"]; !ok || !radius.Optional || !strings.Contains(radius.Type, "number") {
		t.Errorf("radius property = %+v, want optional number", byName["radius"])
	}

	// Depth 0: display only, no expansion.
	shallow := EncodeType(c, shape, nil, 0)
	if len(shallow.Properties) != 0 || shallow.Display == "" || shallow.Kind != "object" {
		t.Errorf("depth-0 encoding should carry only display/flags/kind, got %+v", shallow)
	}
}

func TestTypeEncodeCallSignatures(t *testing.T) {
	t.Parallel()
	ctx, ws, c, done := encodeTestSetup(t)
	defer done()

	fn := declaredType(t, ctx, ws, c, "src/types.ts#Fn")
	encoded := EncodeType(c, fn, nil, 1)
	if len(encoded.CallSignatures) != 1 {
		t.Fatalf("Fn call signatures = %d, want 1", len(encoded.CallSignatures))
	}
	sig := encoded.CallSignatures[0]
	if sig.ReturnType != "boolean" {
		t.Errorf("Fn return type = %q, want boolean", sig.ReturnType)
	}
	if len(sig.Params) != 2 || sig.Params[0].Name != "a" || sig.Params[0].Type != "string" ||
		sig.Params[1].Name != "b" || sig.Params[1].Type != "number" {
		t.Errorf("Fn params = %+v", sig.Params)
	}
}

func TestTypeEncodeGenericReference(t *testing.T) {
	t.Parallel()
	ctx, ws, c, done := encodeTestSetup(t)
	defer done()

	// Type of the `box` variable: Box<string>, a generic instance reference.
	symbol, _, err := DecodeSymbolID(ctx, ws, "src/types.ts#box")
	if err != nil {
		t.Fatalf("DecodeSymbolID(box): %v", err)
	}
	boxType := c.GetTypeOfSymbol(symbol)
	encoded := EncodeType(c, boxType, nil, 2)
	if encoded.Kind != "reference" {
		t.Errorf("box kind = %q, want reference", encoded.Kind)
	}
	if encoded.Display != "Box<string>" {
		t.Errorf("box display = %q, want Box<string>", encoded.Display)
	}
	if len(encoded.TypeArguments) != 1 || encoded.TypeArguments[0].Display != "string" {
		t.Errorf("box typeArguments = %+v, want [string]", encoded.TypeArguments)
	}
	if len(encoded.Properties) != 1 || encoded.Properties[0].Name != "value" || encoded.Properties[0].Type != "string" {
		t.Errorf("box properties = %+v, want value: string", encoded.Properties)
	}
}

func TestTypeComplexityMetric(t *testing.T) {
	t.Parallel()
	ctx, ws, c, done := encodeTestSetup(t)
	defer done()

	simple := Complexity(c, declaredType(t, ctx, ws, c, "src/types.ts#Simple"))
	if simple != 1 {
		t.Errorf("Simple complexity = %d, want 1", simple)
	}

	deep := Complexity(c, declaredType(t, ctx, ws, c, "src/types.ts#Deep"))
	if deep <= simple {
		t.Errorf("Deep (conditional) complexity = %d, want > %d", deep, simple)
	}
	// Conditional: 3 * (1 + cost(T) + cost(string)) = 9.
	if deep != 9 {
		t.Errorf("Deep complexity = %d, want 9", deep)
	}

	wideType := declaredType(t, ctx, ws, c, "src/types.ts#Wide")
	wide, breakdown := ComplexityWithBreakdown(c, wideType)
	if wide <= deep {
		t.Errorf("Wide complexity = %d, want > %d", wide, deep)
	}
	if breakdown.UnionWidth != 3 {
		t.Errorf("Wide unionWidth = %d, want 3", breakdown.UnionWidth)
	}
	if breakdown.Depth < 1 {
		t.Errorf("Wide depth = %d, want >= 1", breakdown.Depth)
	}

	// Memoized: a second computation returns the same value.
	if again := Complexity(c, wideType); again != wide {
		t.Errorf("Wide complexity not stable: %d then %d", wide, again)
	}
}

func TestTypeKindClassification(t *testing.T) {
	t.Parallel()
	ctx, ws, c, done := encodeTestSetup(t)
	defer done()

	if kind := TypeKind(c.GetStringType()); kind != "primitive" {
		t.Errorf("string kind = %q, want primitive", kind)
	}
	if kind := TypeKind(c.GetBooleanType()); kind != "primitive" {
		t.Errorf("boolean kind = %q, want primitive (boolean is an internal union)", kind)
	}
	if kind := TypeKind(declaredType(t, ctx, ws, c, "src/types.ts#Deep")); kind != "conditional" {
		t.Errorf("Deep kind = %q, want conditional", kind)
	}
}
