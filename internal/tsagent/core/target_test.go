package core

import (
	"context"
	"errors"
	"testing"
)

func TestParsePosition(t *testing.T) {
	t.Parallel()
	file, line, col, err := ParsePosition("src/a.ts:12:5")
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	if file != "src/a.ts" || line != 12 || col != 5 {
		t.Errorf("got (%q, %d, %d), want (src/a.ts, 12, 5)", file, line, col)
	}

	for _, bad := range []string{"src/a.ts", "src/a.ts:12", "src/a.ts:x:y", ":1:2"} {
		if _, _, _, err := ParsePosition(bad); err == nil {
			t.Errorf("ParsePosition(%q): expected error", bad)
		}
	}
}

func TestResolveTargetAt(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `export function greet(name: string): string {
	return "hello " + name;
}
`,
	})
	target, err := ws.ResolveTarget(context.Background(), TargetSpec{At: "src/index.ts:1:17"})
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target.Symbol == nil || target.Symbol.Name != "greet" {
		t.Errorf("expected symbol greet, got %+v", target.Symbol)
	}
}

func TestResolveTargetByName(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `export class Widget {}
export function makeWidget(): Widget { return new Widget(); }
`,
	})
	ctx := context.Background()

	target, err := ws.ResolveTarget(ctx, TargetSpec{Name: "makeWidget"})
	if err != nil {
		t.Fatalf("ResolveTarget by name: %v", err)
	}
	if target.Symbol == nil || target.Symbol.Name != "makeWidget" {
		t.Errorf("expected symbol makeWidget, got %+v", target.Symbol)
	}

	// Kind filter.
	if _, err := ws.ResolveTarget(ctx, TargetSpec{Name: "Widget", Kind: "function"}); err == nil {
		t.Error("expected not-found for Widget with kind=function")
	}

	// Unknown name maps to ErrNotFound.
	_, err = ws.ResolveTarget(ctx, TargetSpec{Name: "nope"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveTargetBySymbolID(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `export class Widget { render(): string { return "w"; } }`,
	})
	target, err := ws.ResolveTarget(context.Background(), TargetSpec{Symbol: "src/index.ts#Widget.render"})
	if err != nil {
		t.Fatalf("ResolveTarget by symbol id: %v", err)
	}
	if target.Symbol == nil || target.Symbol.Name != "render" {
		t.Errorf("expected symbol render, got %+v", target.Symbol)
	}
}

func TestResolveTargetSpecValidation(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `export const x = 1;`,
	})
	ctx := context.Background()
	if _, err := ws.ResolveTarget(ctx, TargetSpec{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty spec: expected ErrInvalidArgument, got %v", err)
	}
	if _, err := ws.ResolveTarget(ctx, TargetSpec{At: "a:1:1", Name: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("two addressing modes: expected ErrInvalidArgument, got %v", err)
	}
}

func TestLineColPosRoundTrip(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": "const a = 1;\nconst b = 2;\n",
	})
	file, err := ws.FileOf("src/index.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	pos, err := ws.LineColToPos(file, 2, 7)
	if err != nil {
		t.Fatalf("LineColToPos: %v", err)
	}
	line, col := ws.PosToLineCol(file, pos)
	if line != 2 || col != 7 {
		t.Errorf("round trip: got %d:%d, want 2:7", line, col)
	}
}
