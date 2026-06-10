package cmds

import (
	"context"
	"testing"
)

const navShapeLibSource = `export function greet(name: string): string {
	return name;
}

export function add(a: number, b: number): number {
	return a + b;
}

export const fetcher = (url: string): Promise<number> => Promise.resolve(url.length);

function local(s: string): string {
	return s;
}

export class Box {
	wrap(s: string): string {
		return s;
	}
	combine(s: string, n: number, b: boolean): string {
		return s + n + b;
	}
}

void local;
`

func navShapeProjectFiles() map[string]any {
	return map[string]any{
		"/project/src/shapelib.ts": navShapeLibSource,
	}
}

func shapeMatchNames(result *ShapeResult) map[string]*ShapeMatch {
	byName := make(map[string]*ShapeMatch)
	for _, m := range result.Matches {
		byName[m.Name] = m
	}
	return byName
}

func TestNavShapeExactMatch(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navShapeProjectFiles())
	result, err := runNavShape(context.Background(), ws, &shapeFlags{}, []string{"(string) => string"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	byName := shapeMatchNames(result)
	for _, want := range []string{"greet", "local", "wrap"} {
		if byName[want] == nil {
			t.Errorf("missing match %s in %+v", want, result.Matches)
		}
	}
	for _, reject := range []string{"add", "fetcher", "combine"} {
		if byName[reject] != nil {
			t.Errorf("unexpected match %s", reject)
		}
	}
	if len(result.Matches) != 3 {
		t.Errorf("matches = %d, want 3", len(result.Matches))
	}
	if greet := byName["greet"]; greet != nil {
		if greet.Kind != "function" || greet.File != "src/shapelib.ts" || greet.Line != 1 {
			t.Errorf("greet = %+v", greet)
		}
		if greet.SymbolID != "src/shapelib.ts#greet" {
			t.Errorf("greet symbolId = %q", greet.SymbolID)
		}
	}
	if wrap := byName["wrap"]; wrap != nil && wrap.Kind != "method" {
		t.Errorf("wrap kind = %q, want method", wrap.Kind)
	}
}

func TestNavShapeKindAndExportedFilters(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navShapeProjectFiles())
	ctx := context.Background()

	result, err := runNavShape(ctx, ws, &shapeFlags{kind: "function"}, []string{"(string) => string"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	byName := shapeMatchNames(result)
	if len(result.Matches) != 2 || byName["greet"] == nil || byName["local"] == nil {
		t.Errorf("kind=function matches = %+v, want greet+local", result.Matches)
	}

	result, err = runNavShape(ctx, ws, &shapeFlags{exportedOnly: true}, []string{"(string) => string"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	byName = shapeMatchNames(result)
	if byName["local"] != nil {
		t.Error("exported-only should drop the non-exported function")
	}
	if byName["greet"] == nil || byName["wrap"] == nil {
		t.Errorf("exported-only matches = %+v, want greet and wrap kept", result.Matches)
	}

	if _, err := runNavShape(ctx, ws, &shapeFlags{kind: "bogus"}, []string{"(string) => string"}); err == nil {
		t.Error("expected error for invalid --kind")
	}
}

func TestNavShapeWildcard(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navShapeProjectFiles())
	ctx := context.Background()

	// Embedded wildcard, plus the documented `name: type` param form.
	result, err := runNavShape(ctx, ws, &shapeFlags{}, []string{"(url: string) => Promise<*>"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Name != "fetcher" || result.Matches[0].Kind != "arrow" {
		t.Fatalf("matches = %+v, want fetcher (arrow)", result.Matches)
	}

	// Bare wildcards in both positions.
	result, err = runNavShape(ctx, ws, &shapeFlags{}, []string{"(*, *) => *"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Name != "add" {
		t.Errorf("(*, *) => * matches = %+v, want add", result.Matches)
	}
}

func TestNavShapeParamCountMismatch(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navShapeProjectFiles())
	ctx := context.Background()

	// add has two number params: a one-param pattern must not match it.
	result, err := runNavShape(ctx, ws, &shapeFlags{}, []string{"(number) => number"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	if len(result.Matches) != 0 {
		t.Errorf("matches = %+v, want none (param count must match)", result.Matches)
	}

	result, err = runNavShape(ctx, ws, &shapeFlags{}, []string{"(number, number) => number"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Name != "add" {
		t.Errorf("matches = %+v, want add", result.Matches)
	}
}

func TestNavShapeVariadicSuffix(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navShapeProjectFiles())
	result, err := runNavShape(context.Background(), ws, &shapeFlags{}, []string{"(string, ...) => string"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	byName := shapeMatchNames(result)
	// First param string + string return, any number of extra params.
	for _, want := range []string{"greet", "local", "wrap", "combine"} {
		if byName[want] == nil {
			t.Errorf("missing match %s in %+v", want, result.Matches)
		}
	}
	if byName["add"] != nil || byName["fetcher"] != nil {
		t.Errorf("unexpected matches: %+v", result.Matches)
	}
}

func TestNavShapePatternErrors(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navShapeProjectFiles())
	ctx := context.Background()
	for _, bad := range []string{
		"string => string",        // missing parens
		"(string) string",         // missing arrow
		"(string",                 // unbalanced
		"(..., string) => string", // ... not last
		"(string) =>",             // empty return
	} {
		if _, err := runNavShape(ctx, ws, &shapeFlags{}, []string{bad}); err == nil {
			t.Errorf("pattern %q should be rejected", bad)
		}
	}
	if _, err := runNavShape(ctx, ws, &shapeFlags{}, nil); err == nil {
		t.Error("missing pattern should be rejected")
	}
}

func TestNavShapePathGlob(t *testing.T) {
	t.Parallel()
	files := navShapeProjectFiles()
	files["/project/src/extra/more.ts"] = "export function shout(name: string): string { return name; }\n"
	ws := newTestWorkspace(t, files)
	ctx := context.Background()

	result, err := runNavShape(ctx, ws, &shapeFlags{pathGlob: "src/extra/*.ts"}, []string{"(string) => string"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Name != "shout" {
		t.Errorf("glob matches = %+v, want only shout", result.Matches)
	}

	result, err = runNavShape(ctx, ws, &shapeFlags{pathGlob: "src/**"}, []string{"(string) => string"})
	if err != nil {
		t.Fatalf("runNavShape: %v", err)
	}
	if len(result.Matches) != 4 {
		t.Errorf("src/** matches = %d (%+v), want 4", len(result.Matches), result.Matches)
	}
}
