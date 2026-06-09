package core

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
)

func specDiag(file string, code string, pos int, end int, message string) SpecDiag {
	return SpecDiag{File: file, Code: code, Message: message, pos: pos, end: end}
}

func TestDeltaClassification(t *testing.T) {
	t.Parallel()
	base := []SpecDiag{
		specDiag("a.ts", "TS2322", 10, 14, "not assignable"), // unchanged (exact)
		specDiag("a.ts", "TS2322", 50, 54, "not assignable"), // moved in spec
		specDiag("b.ts", "TS2304", 5, 8, "cannot find name"), // fixed
	}
	spec := []SpecDiag{
		specDiag("a.ts", "TS2322", 10, 14, "not assignable"),  // exact match
		specDiag("a.ts", "TS2322", 90, 94, "not assignable"),  // moved (same file/code/message, new pos)
		specDiag("c.ts", "TS2345", 1, 4, "argument mismatch"), // new
	}
	delta := DiagnosticsDelta(base, spec)
	if delta.UnchangedCount != 1 {
		t.Errorf("UnchangedCount = %d, want 1", delta.UnchangedCount)
	}
	if len(delta.Moved) != 1 || delta.Moved[0].pos != 90 {
		t.Errorf("Moved = %+v, want one entry at pos 90", delta.Moved)
	}
	if len(delta.New) != 1 || delta.New[0].File != "c.ts" {
		t.Errorf("New = %+v, want one entry in c.ts", delta.New)
	}
	if len(delta.Fixed) != 1 || delta.Fixed[0].File != "b.ts" {
		t.Errorf("Fixed = %+v, want one entry in b.ts", delta.Fixed)
	}
}

func TestDeltaMultisetCounts(t *testing.T) {
	t.Parallel()
	// Two identical-message diagnostics in base, one in spec: one is matched
	// (exact), the other is fixed — multiset semantics, not set semantics.
	base := []SpecDiag{
		specDiag("a.ts", "TS2322", 10, 14, "boom"),
		specDiag("a.ts", "TS2322", 20, 24, "boom"),
	}
	spec := []SpecDiag{
		specDiag("a.ts", "TS2322", 10, 14, "boom"),
	}
	delta := DiagnosticsDelta(base, spec)
	if delta.UnchangedCount != 1 || len(delta.Fixed) != 1 || len(delta.New) != 0 || len(delta.Moved) != 0 {
		t.Errorf("delta = %+v, want 1 unchanged + 1 fixed", delta)
	}
}

func TestDeltaEmptySnapshots(t *testing.T) {
	t.Parallel()
	delta := DiagnosticsDelta(nil, nil)
	if delta.UnchangedCount != 0 || len(delta.New) != 0 || len(delta.Fixed) != 0 || len(delta.Moved) != 0 {
		t.Errorf("empty delta = %+v", delta)
	}
}

func TestSpecDeltaSpeculativeWorkspace(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/bad.ts":   "export const broken: string = 42;\n",
		"/project/src/other.ts": "export const ok: number = 1;\n",
	})
	ctx := context.Background()

	baseDiags := CollectDiagnostics(ctx, ws)
	if len(baseDiags) != 1 || baseDiags[0].Code != "TS2322" || baseDiags[0].File != "src/bad.ts" {
		t.Fatalf("base diags = %+v, want one TS2322 in src/bad.ts", baseDiags)
	}
	if baseDiags[0].Category != "error" {
		t.Errorf("category = %q, want error", baseDiags[0].Category)
	}
	if !strings.HasPrefix(baseDiags[0].DiagRef, "src/bad.ts:") {
		t.Errorf("diagRef = %q", baseDiags[0].DiagRef)
	}

	// Speculative state: insert lines above the bad declaration (the error
	// moves), and break other.ts (a new error appears).
	specWs, err := SpeculativeWorkspace(ws, map[string]string{
		"/project/src/bad.ts":   "// pad\n// pad\nexport const broken: string = 42;\n",
		"/project/src/other.ts": "export const ok: number = \"nope\";\n",
	}, nil, true)
	if err != nil {
		t.Fatalf("SpeculativeWorkspace: %v", err)
	}
	delta := DiagnosticsDelta(baseDiags, CollectDiagnostics(ctx, specWs))
	if len(delta.Moved) != 1 || delta.Moved[0].File != "src/bad.ts" {
		t.Errorf("Moved = %+v, want the shifted src/bad.ts error", delta.Moved)
	}
	if delta.Moved != nil && len(delta.Moved) == 1 && delta.Moved[0].Range.Line != 3 {
		t.Errorf("moved line = %d, want 3 (the new position)", delta.Moved[0].Range.Line)
	}
	if len(delta.New) != 1 || delta.New[0].File != "src/other.ts" {
		t.Errorf("New = %+v, want one error in src/other.ts", delta.New)
	}
	if len(delta.Fixed) != 0 || delta.UnchangedCount != 0 {
		t.Errorf("delta = %+v, want no fixed/unchanged", delta)
	}

	// Fixing the bad file yields a fixed entry.
	fixedWs, err := SpeculativeWorkspace(ws, map[string]string{
		"/project/src/bad.ts": "export const broken: string = \"42\";\n",
	}, nil, true)
	if err != nil {
		t.Fatalf("SpeculativeWorkspace: %v", err)
	}
	delta = DiagnosticsDelta(baseDiags, CollectDiagnostics(ctx, fixedWs))
	if len(delta.Fixed) != 1 || len(delta.New) != 0 || len(delta.Moved) != 0 {
		t.Errorf("delta = %+v, want exactly one fixed", delta)
	}

	// Deleting the bad file also fixes its diagnostic.
	deletedWs, err := SpeculativeWorkspace(ws, nil, []string{"/project/src/bad.ts"}, true)
	if err != nil {
		t.Fatalf("SpeculativeWorkspace: %v", err)
	}
	delta = DiagnosticsDelta(baseDiags, CollectDiagnostics(ctx, deletedWs))
	if len(delta.Fixed) != 1 || len(delta.New) != 0 {
		t.Errorf("delete delta = %+v, want one fixed and no new", delta)
	}
}

func TestSpecDeltaOverlayFromEditSet(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const a: number = 1;\n",
	})
	es := EditSet{
		Edits: []FileEdit{{
			FileName: "/project/src/a.ts",
			Edits: []core.TextChange{{
				TextRange: core.NewTextRange(len("export const a: number = "), len("export const a: number = 1")),
				NewText:   "\"x\"",
			}},
		}},
		Ops: []FileOp{{Kind: FileOpCreate, Path: "/project/src/b.ts", Content: "export const b = 2;\n"}},
	}
	contents, deleted, err := OverlayFromEditSet(ws, es)
	if err != nil {
		t.Fatalf("OverlayFromEditSet: %v", err)
	}
	if got := contents["/project/src/a.ts"]; got != "export const a: number = \"x\";\n" {
		t.Errorf("edited content = %q", got)
	}
	if got := contents["/project/src/b.ts"]; got != "export const b = 2;\n" {
		t.Errorf("created content = %q", got)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want empty", deleted)
	}
}
