package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

func TestRefactorApplyEditsJSON(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts":   "export const a: number = 1;\nexport const b = a;\n",
		"/project/edits.json": `{"edits":[{"file":"src/a.ts","edits":[{"pos":25,"end":26,"newText":"2"}]}]}`,
	})
	f := &refactorApplyEditsFlags{tx: refactorTxFlags{apply: true}}
	result, err := runRefactorApplyEdits(context.Background(), ws, f, []string{"edits.json"})
	if err != nil {
		t.Fatalf("runRefactorApplyEdits: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/a.ts"); !strings.Contains(got, "= 2;") {
		t.Errorf("a.ts after apply-edits = %q", got)
	}
}

func TestRefactorApplyEditsJSONFromStdinWithOps(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const a = 1;\n",
	})
	input := `{"ops":[{"kind":"create","path":"src/b.ts","content":"export const b = 2;\n"}]}`
	f := &refactorApplyEditsFlags{tx: refactorTxFlags{apply: true}, stdin: strings.NewReader(input)}
	result, err := runRefactorApplyEdits(context.Background(), ws, f, []string{"-"})
	if err != nil {
		t.Fatalf("runRefactorApplyEdits: %v", err)
	}
	if !result.Applied {
		t.Fatalf("result = %+v, want applied", result)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/b.ts"); got != "export const b = 2;\n" {
		t.Errorf("created file content = %q", got)
	}
}

func TestRefactorApplyEditsRefusesTypeRegression(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts":   "export const a: number = 1;\n",
		"/project/edits.json": `{"edits":[{"file":"src/a.ts","edits":[{"pos":25,"end":26,"newText":"\"oops\""}]}]}`,
	})
	f := &refactorApplyEditsFlags{tx: refactorTxFlags{apply: true}}
	_, err := runRefactorApplyEdits(context.Background(), ws, f, []string{"edits.json"})
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected exit %d (refused), got %v", cli.ExitRefused, err)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/a.ts"); !strings.Contains(got, "= 1;") {
		t.Errorf("refused transaction must not write: %q", got)
	}
	// Dry-run of the same bad edit is fine (diff only, no gate).
	f2 := &refactorApplyEditsFlags{}
	result, err := runRefactorApplyEdits(context.Background(), ws, f2, []string{"edits.json"})
	if err != nil || result.Applied {
		t.Errorf("dry-run should succeed without applying, got result=%+v err=%v", result, err)
	}
}

func TestRefactorApplyEditsDiffMode(t *testing.T) {
	t.Parallel()
	diff := "--- a/src/a.ts\n" +
		"+++ b/src/a.ts\n" +
		"@@ -1,2 +1,2 @@\n" +
		"-export const a: number = 1;\n" +
		"+export const a: number = 42;\n" +
		" export const b = a;\n"
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const a: number = 1;\nexport const b = a;\n",
	})
	f := &refactorApplyEditsFlags{tx: refactorTxFlags{apply: true}, diff: true, stdin: strings.NewReader(diff)}
	result, err := runRefactorApplyEdits(context.Background(), ws, f, []string{"-"})
	if err != nil {
		t.Fatalf("runRefactorApplyEdits --diff: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/a.ts"); !strings.Contains(got, "= 42;") {
		t.Errorf("a.ts after diff apply = %q", got)
	}
}

func TestRefactorApplyEditsValidatesInput(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const a = 1;\n"})
	ctx := context.Background()
	if _, err := runRefactorApplyEdits(ctx, ws, &refactorApplyEditsFlags{}, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("missing argument: got %v, want usage error", err)
	}
	f := &refactorApplyEditsFlags{stdin: strings.NewReader("not json")}
	if _, err := runRefactorApplyEdits(ctx, ws, f, []string{"-"}); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("bad JSON: got %v, want usage error", err)
	}
	f = &refactorApplyEditsFlags{stdin: strings.NewReader(`{"edits":[]}`)}
	if _, err := runRefactorApplyEdits(ctx, ws, f, []string{"-"}); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("empty edit set: got %v, want usage error", err)
	}
}
