package core

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
)

// editReplacing builds a FileEdit replacing the first occurrence of old with
// new in the given file.
func editReplacing(t *testing.T, ws *Workspace, fileName string, old string, new string) FileEdit {
	t.Helper()
	text, err := readFileText(ws, fileName)
	if err != nil {
		t.Fatalf("readFileText(%s): %v", fileName, err)
	}
	pos := strings.Index(text, old)
	if pos < 0 {
		t.Fatalf("%s does not contain %q", fileName, old)
	}
	return FileEdit{
		FileName: fileName,
		Edits:    []core.TextChange{{TextRange: core.NewTextRange(pos, pos+len(old)), NewText: new}},
	}
}

func mustReadFile(t *testing.T, ws *Workspace, fileName string) string {
	t.Helper()
	text, ok := ws.FS.ReadFile(fileName)
	if !ok {
		t.Fatalf("ReadFile(%s) failed", fileName)
	}
	return text
}

func TestTxDryRunReturnsDiffWithoutWriting(t *testing.T) {
	t.Parallel()
	const original = "export const answer: number = 41;\n"
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": original})

	es := EditSet{Edits: []FileEdit{editReplacing(t, ws, "/project/src/a.ts", "41", "42")}}
	result, err := Execute(context.Background(), ws, es, TxOpts{SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Applied {
		t.Error("dry-run reported applied = true")
	}
	if len(result.Diffs) != 1 || result.Diffs[0].Kind != "edit" || result.Diffs[0].File != "src/a.ts" {
		t.Fatalf("diffs = %+v", result.Diffs)
	}
	diff := result.Diffs[0].Diff
	if !strings.Contains(diff, "-export const answer: number = 41;") || !strings.Contains(diff, "+export const answer: number = 42;") {
		t.Errorf("unexpected diff:\n%s", diff)
	}
	if got := mustReadFile(t, ws, "/project/src/a.ts"); got != original {
		t.Errorf("dry-run wrote to the FS: %q", got)
	}
}

func TestTxApplyWritesThroughWorkspaceFS(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const answer: number = 41;\n"})

	es := EditSet{Edits: []FileEdit{editReplacing(t, ws, "/project/src/a.ts", "41", "42")}}
	result, err := Execute(context.Background(), ws, es, TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Applied || result.Refused {
		t.Fatalf("result = %+v, want applied", result)
	}
	if len(result.NewErrors) != 0 || result.FixedErrors != 0 {
		t.Errorf("diagnostics delta = %+v / %d, want clean", result.NewErrors, result.FixedErrors)
	}
	if got := mustReadFile(t, ws, "/project/src/a.ts"); got != "export const answer: number = 42;\n" {
		t.Errorf("file after apply = %q", got)
	}
}

func TestTxGateRefusesErrorIntroducingEdit(t *testing.T) {
	t.Parallel()
	const original = "export const answer: number = 41;\n"
	files := map[string]any{"/project/src/a.ts": original}
	ws := newTestWorkspace(t, files)

	// Breaking edit: assign a string to a number.
	es := EditSet{Edits: []FileEdit{editReplacing(t, ws, "/project/src/a.ts", "41", `"oops"`)}}
	result, err := Execute(context.Background(), ws, es, TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Refused || result.Applied {
		t.Fatalf("result = %+v, want refused", result)
	}
	if len(result.NewErrors) != 1 {
		t.Fatalf("newErrors = %+v, want exactly 1", result.NewErrors)
	}
	if d := result.NewErrors[0]; d.Code != "TS2322" || d.File != "src/a.ts" {
		t.Errorf("new error = %+v, want TS2322 in src/a.ts", d)
	}
	if got := mustReadFile(t, ws, "/project/src/a.ts"); got != original {
		t.Errorf("refused transaction wrote to the FS: %q", got)
	}

	// --allow-errors applies anyway and still reports the delta.
	ws2 := newTestWorkspace(t, map[string]any{"/project/src/a.ts": original})
	es2 := EditSet{Edits: []FileEdit{editReplacing(t, ws2, "/project/src/a.ts", "41", `"oops"`)}}
	result2, err := Execute(context.Background(), ws2, es2, TxOpts{Apply: true, AllowErrors: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute --allow-errors: %v", err)
	}
	if !result2.Applied || result2.Refused {
		t.Fatalf("result = %+v, want applied", result2)
	}
	if len(result2.NewErrors) != 1 {
		t.Errorf("newErrors = %+v, want 1", result2.NewErrors)
	}
	if got := mustReadFile(t, ws2, "/project/src/a.ts"); !strings.Contains(got, `"oops"`) {
		t.Errorf("file after allow-errors apply = %q", got)
	}
}

func TestTxGateUnusedDiagnosticsDoNotGateByDefault(t *testing.T) {
	t.Parallel()
	files := map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "noUnusedLocals": true}}`,
		"/project/src/a.ts":      "export const answer: number = 41;\n",
	}
	ws := newTestWorkspace(t, files)

	// Appending an unused helper introduces TS6133 under noUnusedLocals; the
	// gate must let it through, report it as an UnusedWarning, and note it.
	appendHelper := func(ws *Workspace) EditSet {
		text := mustReadFile(t, ws, "/project/src/a.ts")
		return EditSet{Edits: []FileEdit{{
			FileName: "/project/src/a.ts",
			Edits: []core.TextChange{{
				TextRange: core.NewTextRange(len(text), len(text)),
				NewText:   "function helper(): number {\n\treturn 2;\n}\n",
			}},
		}}}
	}
	result, err := Execute(context.Background(), ws, appendHelper(ws), TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Applied || result.Refused || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want applied with no gating errors", result)
	}
	if len(result.UnusedWarnings) != 1 || result.UnusedWarnings[0].Code != "TS6133" {
		t.Fatalf("UnusedWarnings = %+v, want one TS6133", result.UnusedWarnings)
	}
	if len(result.Notes) != 1 || !strings.Contains(result.Notes[0], "not gating; --strict-gate") {
		t.Errorf("Notes = %v, want the unused-symbol note", result.Notes)
	}

	// StrictGate restores the refusal.
	ws2 := newTestWorkspace(t, map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "noUnusedLocals": true}}`,
		"/project/src/a.ts":      "export const answer: number = 41;\n",
	})
	result2, err := Execute(context.Background(), ws2, appendHelper(ws2), TxOpts{Apply: true, StrictGate: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute --strict-gate: %v", err)
	}
	if !result2.Refused || result2.Applied {
		t.Fatalf("result = %+v, want refused under StrictGate", result2)
	}
	if len(result2.NewErrors) != 1 || result2.NewErrors[0].Code != "TS6133" {
		t.Errorf("NewErrors = %+v, want the TS6133 gating", result2.NewErrors)
	}
}

func TestTxGateCountsFixedErrors(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const broken: number = \"bad\";\n"})
	es := EditSet{Edits: []FileEdit{editReplacing(t, ws, "/project/src/a.ts", `"bad"`, "7")}}
	result, err := Execute(context.Background(), ws, es, TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Applied || result.FixedErrors != 1 || len(result.NewErrors) != 0 {
		t.Errorf("result = %+v, want applied with 1 fixed error", result)
	}
}

func TestTxFileOps(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/keep.ts": "export const keep = 1;\n",
		"/project/src/move.ts": "export const moved = 2;\n",
		"/project/src/drop.ts": "export const dropped = 3;\n",
	})
	es := EditSet{Ops: []FileOp{
		{Kind: FileOpCreate, Path: "/project/src/fresh.ts", Content: "export const fresh = 4;\n"},
		{Kind: FileOpRename, Path: "/project/src/move.ts", NewPath: "/project/src/lib/move.ts"},
		{Kind: FileOpDelete, Path: "/project/src/drop.ts"},
	}}
	result, err := Execute(context.Background(), ws, es, TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Applied {
		t.Fatalf("result = %+v, want applied", result)
	}
	kinds := map[string]string{}
	for _, d := range result.Diffs {
		kinds[d.File] = d.Kind
	}
	if kinds["src/fresh.ts"] != "create" || kinds["src/move.ts"] != "rename" || kinds["src/drop.ts"] != "delete" {
		t.Errorf("diff kinds = %v", kinds)
	}
	if got := mustReadFile(t, ws, "/project/src/fresh.ts"); got != "export const fresh = 4;\n" {
		t.Errorf("fresh.ts = %q", got)
	}
	if got := mustReadFile(t, ws, "/project/src/lib/move.ts"); got != "export const moved = 2;\n" {
		t.Errorf("lib/move.ts = %q", got)
	}
	if ws.FS.FileExists("/project/src/move.ts") {
		t.Error("rename left the source file behind")
	}
	if ws.FS.FileExists("/project/src/drop.ts") {
		t.Error("delete left the file behind")
	}
}

func TestTxOverlappingEditsRejected(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const answer = 41;\n"})
	es := EditSet{Edits: []FileEdit{{
		FileName: "/project/src/a.ts",
		Edits: []core.TextChange{
			{TextRange: core.NewTextRange(0, 10), NewText: "x"},
			{TextRange: core.NewTextRange(5, 15), NewText: "y"},
		},
	}}}
	if _, err := Execute(context.Background(), ws, es, TxOpts{SingleThreaded: true}); err == nil {
		t.Error("expected an overlap error")
	}
}

func TestEditSetFromWorkspaceEdit(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "const first = 1;\nconst second = 2;\n"})

	uri := ws.URI("/project/src/a.ts")
	changes := map[lsproto.DocumentUri][]*lsproto.TextEdit{
		uri: {
			{
				// "second" on line 2 (0-based line 1, chars 6-12).
				Range:   lsproto.Range{Start: lsproto.Position{Line: 1, Character: 6}, End: lsproto.Position{Line: 1, Character: 12}},
				NewText: "renamed",
			},
		},
	}
	var es EditSet
	if err := es.AddWorkspaceEdit(ws, &lsproto.WorkspaceEdit{Changes: &changes}); err != nil {
		t.Fatalf("AddWorkspaceEdit: %v", err)
	}
	if len(es.Edits) != 1 || len(es.Edits[0].Edits) != 1 {
		t.Fatalf("es = %+v", es)
	}
	edit := es.Edits[0].Edits[0]
	wantPos := strings.Index("const first = 1;\nconst second = 2;\n", "second")
	if edit.Pos() != wantPos || edit.End() != wantPos+len("second") || edit.NewText != "renamed" {
		t.Errorf("edit = [%d,%d) %q, want [%d,%d) \"renamed\"", edit.Pos(), edit.End(), edit.NewText, wantPos, wantPos+len("second"))
	}
}
