package cmds

import (
	"context"
	"strings"
	"testing"
)

func brokenProjectFiles() map[string]any {
	return map[string]any{
		"/project/src/ok.ts": `export const fine: number = 1;`,
		"/project/src/bad.ts": `export const broken: string = 42;
export function unusedParam(x: number): void {}
`,
	}
}

func TestCheckFindsPlantedError(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, brokenProjectFiles())
	result, err := runCheck(context.Background(), ws, &checkFlags{}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if result.Total() != 1 {
		for _, d := range result.Diagnostics {
			t.Logf("diag: %+v", d)
		}
		t.Fatalf("expected exactly 1 diagnostic, got %d", result.Total())
	}
	d := result.Diagnostics[0]
	if d.File != "src/bad.ts" {
		t.Errorf("file = %q, want src/bad.ts", d.File)
	}
	if d.Code != "TS2322" {
		t.Errorf("code = %q, want TS2322", d.Code)
	}
	if d.Category != "error" {
		t.Errorf("category = %q, want error", d.Category)
	}
	if d.Range.Line != 1 || d.Range.Col != 14 {
		t.Errorf("range = %+v, want line 1 col 14", d.Range)
	}
	if !strings.HasPrefix(d.DiagRef, "src/bad.ts:") || !strings.HasSuffix(d.DiagRef, ":TS2322") {
		t.Errorf("diagRef = %q, want src/bad.ts:<pos>:TS2322", d.DiagRef)
	}
	if !strings.Contains(d.Message, "not assignable") {
		t.Errorf("message = %q", d.Message)
	}
}

func TestCheckFilters(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, brokenProjectFiles())
	ctx := context.Background()

	// Code filter that does not match drops everything.
	result, err := runCheck(ctx, ws, &checkFlags{code: "TS9999"}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("code filter: expected 0, got %d", result.Total())
	}

	// Matching code filter keeps the error; bare numbers also accepted.
	result, err = runCheck(ctx, ws, &checkFlags{code: "2322"}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if result.Total() != 1 {
		t.Errorf("code filter 2322: expected 1, got %d", result.Total())
	}

	// Severity filter.
	result, err = runCheck(ctx, ws, &checkFlags{severity: "warning"}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("severity=warning: expected 0, got %d", result.Total())
	}

	// Path selection limits to the clean file.
	result, err = runCheck(ctx, ws, &checkFlags{}, []string{"src/ok.ts"})
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("path filter: expected 0, got %d", result.Total())
	}

	// Path glob.
	result, err = runCheck(ctx, ws, &checkFlags{pathGlob: "src/bad*"}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if result.Total() != 1 {
		t.Errorf("path glob: expected 1, got %d", result.Total())
	}

	// Invalid flag values are usage errors.
	if _, err := runCheck(ctx, ws, &checkFlags{severity: "fatal"}, nil); err == nil {
		t.Error("expected usage error for bad severity")
	}
	if _, err := runCheck(ctx, ws, &checkFlags{code: "abc"}, nil); err == nil {
		t.Error("expected usage error for bad code")
	}
}

func TestCheckSuggestions(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `export async function f(): Promise<number> {
	return 1;
}
f();
`,
	})
	base, err := runCheck(context.Background(), ws, &checkFlags{}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	withSuggestions, err := runCheck(context.Background(), ws, &checkFlags{suggestions: true}, nil)
	if err != nil {
		t.Fatalf("runCheck --suggestions: %v", err)
	}
	if withSuggestions.Total() < base.Total() {
		t.Errorf("suggestions should not reduce diagnostics: %d < %d", withSuggestions.Total(), base.Total())
	}
}
