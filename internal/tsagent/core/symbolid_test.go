package core

import (
	"context"
	"strings"
	"testing"
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
