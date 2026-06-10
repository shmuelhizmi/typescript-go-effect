package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

func readWorkspaceFile(t *testing.T, ws *core.Workspace, fileName string) string {
	t.Helper()
	text, ok := ws.FS.ReadFile(fileName)
	if !ok {
		t.Fatalf("ReadFile(%s) failed", fileName)
	}
	return text
}

func renameFixtureFiles() map[string]any {
	return map[string]any{
		"/project/src/a.ts": "export function greet(name: string): string {\n\treturn name;\n}\n",
		"/project/src/b.ts": "import { greet } from \"./a\";\nexport const message = greet(\"hi\");\n",
	}
}

func TestRefactorRenameDryRun(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, renameFixtureFiles())
	f := &refactorRenameFlags{target: refactorTargetFlags{symbol: "src/a.ts#greet"}}
	result, err := runRefactorRename(context.Background(), ws, f, []string{"welcome"})
	if err != nil {
		t.Fatalf("runRefactorRename: %v", err)
	}
	if result.Applied {
		t.Error("dry-run reported applied")
	}
	if len(result.Diffs) != 2 {
		t.Fatalf("diffs = %+v, want 2 files", result.Diffs)
	}
	all := result.Diffs[0].Diff + result.Diffs[1].Diff
	if !strings.Contains(all, "+export function welcome") || !strings.Contains(all, "+import { welcome }") {
		t.Errorf("unexpected diffs:\n%s", all)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/a.ts"); !strings.Contains(got, "greet") {
		t.Errorf("dry-run modified the FS: %q", got)
	}
}

func TestRefactorRenameApplyAcrossFiles(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, renameFixtureFiles())
	f := &refactorRenameFlags{
		target: refactorTargetFlags{name: "greet"},
		tx:     refactorTxFlags{apply: true},
	}
	result, err := runRefactorRename(context.Background(), ws, f, []string{"welcome"})
	if err != nil {
		t.Fatalf("runRefactorRename --apply: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	a := readWorkspaceFile(t, ws, "/project/src/a.ts")
	b := readWorkspaceFile(t, ws, "/project/src/b.ts")
	if !strings.Contains(a, "export function welcome") || strings.Contains(a, "greet") {
		t.Errorf("a.ts after rename = %q", a)
	}
	if !strings.Contains(b, "import { welcome } from \"./a\";") || !strings.Contains(b, "welcome(\"hi\")") {
		t.Errorf("b.ts after rename = %q", b)
	}
}

func TestRefactorRenameRefusesIneligibleTarget(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const n = 1;\n"})
	// Position of the literal `1` — not a renamable element.
	f := &refactorRenameFlags{target: refactorTargetFlags{at: "src/a.ts:1:18"}}
	_, err := runRefactorRename(context.Background(), ws, f, []string{"two"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if code := cli.ExitCode(err); code != cli.ExitRefused {
		t.Errorf("exit code = %d (%v), want %d", code, err, cli.ExitRefused)
	}
}

func TestRefactorOrganizeImportsSortsAndMerges(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/vals.ts": "export const A = 1;\nexport const B = 2;\n",
		"/project/src/main.ts": "import { B } from \"./vals\";\nimport { A } from \"./vals\";\nexport const v = A + B;\n",
	})
	f := &refactorOrganizeFlags{tx: refactorTxFlags{apply: true}}
	result, err := runRefactorOrganizeImports(context.Background(), ws, f, []string{"src/main.ts"})
	if err != nil {
		t.Fatalf("runRefactorOrganizeImports: %v", err)
	}
	if !result.Applied {
		t.Fatalf("result = %+v, want applied", result)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	// Note: "A,B" (no space) until the Phase 0 lsHost seeds default
	// FormatCodeSettings; accept both spacings here.
	if !strings.Contains(main, "import { A, B } from \"./vals\";") && !strings.Contains(main, "import { A,B } from \"./vals\";") {
		t.Errorf("main.ts after organize = %q", main)
	}
	if strings.Count(main, "import") != 1 {
		t.Errorf("imports were not merged: %q", main)
	}
}

func TestRefactorOrganizeImportsRemovesUnused(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/vals.ts": "export const A = 1;\nexport const B = 2;\n",
		"/project/src/main.ts": "import { A, B } from \"./vals\";\nexport const v = A;\n",
	})
	f := &refactorOrganizeFlags{tx: refactorTxFlags{apply: true}}
	if _, err := runRefactorOrganizeImports(context.Background(), ws, f, []string{"src/main.ts"}); err != nil {
		t.Fatalf("runRefactorOrganizeImports: %v", err)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { A } from \"./vals\";") || strings.Contains(main, "B") {
		t.Errorf("main.ts after organize = %q", main)
	}
}

func TestRefactorSafeDeleteRefusesReferencedSymbol(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function used(): number {\n\treturn 1;\n}\nexport const v = used();\n",
	})
	f := &refactorSafeDeleteFlags{target: refactorTargetFlags{name: "used"}}
	_, err := runRefactorSafeDelete(context.Background(), ws, f, nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if code := cli.ExitCode(err); code != cli.ExitRefused {
		t.Errorf("exit code = %d (%v), want %d", code, err, cli.ExitRefused)
	}
	if !strings.Contains(err.Error(), "src/a.ts:4:") {
		t.Errorf("error should list the blocking reference location: %v", err)
	}
}

func TestRefactorSafeDeleteRemovesUnreferencedDeclaration(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "/** Unused helper (self-recursion is allowed). */\nfunction dead(): void {\n\tdead();\n}\nexport const alive = 1;\n",
	})
	f := &refactorSafeDeleteFlags{
		target: refactorTargetFlags{name: "dead"},
		tx:     refactorTxFlags{apply: true},
	}
	result, err := runRefactorSafeDelete(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorSafeDelete: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/a.ts"); got != "export const alive = 1;\n" {
		t.Errorf("a.ts after safe-delete = %q", got)
	}
	if len(result.Notes) == 0 {
		t.Error("expected a note about unused-import cleanup being out of scope")
	}
}

func TestRefactorSafeDeleteSoleVariableDeclarator(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "const deadValue = 1;\nexport const alive = 2;\n",
	})
	f := &refactorSafeDeleteFlags{
		target: refactorTargetFlags{name: "deadValue"},
		tx:     refactorTxFlags{apply: true},
	}
	if _, err := runRefactorSafeDelete(context.Background(), ws, f, nil); err != nil {
		t.Fatalf("runRefactorSafeDelete: %v", err)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/a.ts"); got != "export const alive = 2;\n" {
		t.Errorf("a.ts after safe-delete = %q", got)
	}
}

func TestRefactorSafeDeleteCascadeDeletesNewlyDeadChain(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "function helperA(): number {\n\treturn helperB();\n}\nfunction helperB(): number {\n\treturn 1;\n}\nexport function entry(): number {\n\treturn helperA();\n}\nexport const keep = 1;\n",
	})
	f := &refactorSafeDeleteFlags{
		target:  refactorTargetFlags{name: "entry"},
		tx:      refactorTxFlags{apply: true},
		cascade: true,
	}
	result, err := runRefactorSafeDelete(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorSafeDelete --cascade: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/a.ts"); got != "export const keep = 1;\n" {
		t.Errorf("a.ts after cascade = %q", got)
	}
	// The cascade tree is reported in the notes.
	all := strings.Join(result.Notes, "\n")
	if !strings.Contains(all, "cascade: helperA") || !strings.Contains(all, "cascade: helperB") {
		t.Errorf("notes should describe the cascade tree, got: %q", all)
	}
	if !strings.Contains(all, "became dead after deleting entry") || !strings.Contains(all, "became dead after deleting helperA") {
		t.Errorf("notes should attribute each cascade deletion, got: %q", all)
	}
}

func TestRefactorSafeDeleteCascadeStopsAtExportedSymbols(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function helper(): number {\n\treturn 1;\n}\nexport function entry(): number {\n\treturn helper();\n}\n",
	})
	f := &refactorSafeDeleteFlags{
		target:  refactorTargetFlags{name: "entry"},
		tx:      refactorTxFlags{apply: true},
		cascade: true,
	}
	result, err := runRefactorSafeDelete(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorSafeDelete --cascade: %v", err)
	}
	if !result.Applied {
		t.Fatalf("result = %+v, want applied", result)
	}
	got := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.Contains(got, "export function helper") || strings.Contains(got, "entry") {
		t.Errorf("exported helper must survive the cascade: %q", got)
	}
}

func TestRefactorSafeDeleteCascadeStillRefusesReferencedRoot(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function used(): number {\n\treturn 1;\n}\nexport const v = used();\n",
	})
	f := &refactorSafeDeleteFlags{target: refactorTargetFlags{name: "used"}, cascade: true}
	_, err := runRefactorSafeDelete(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Errorf("expected a refusal, got %v", err)
	}
}

func mvFixtureFiles() map[string]any {
	return map[string]any{
		"/project/src/util.ts": "export const u = 1;\n",
		"/project/src/main.ts": "import { u } from \"./util\";\nexport const m = u;\n",
	}
}

func TestRefactorMvDryRun(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, mvFixtureFiles())
	f := &refactorMvFlags{}
	result, err := runRefactorMv(context.Background(), ws, f, []string{"src/util.ts", "src/lib/util.ts"})
	if err != nil {
		t.Fatalf("runRefactorMv: %v", err)
	}
	if result.Applied {
		t.Error("dry-run reported applied")
	}
	kinds := map[string]string{}
	var all strings.Builder
	for _, d := range result.Diffs {
		kinds[d.File] = d.Kind
		all.WriteString(d.Diff)
	}
	if kinds["src/main.ts"] != "edit" || kinds["src/util.ts"] != "rename" {
		t.Errorf("diff kinds = %v", kinds)
	}
	if !strings.Contains(all.String(), "+import { u } from \"./lib/util\";") {
		t.Errorf("expected updated import specifier in diff:\n%s", all.String())
	}
	if ws.FS.FileExists("/project/src/lib/util.ts") {
		t.Error("dry-run moved the file")
	}
}

func TestRefactorMvApplyUpdatesImports(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, mvFixtureFiles())
	f := &refactorMvFlags{tx: refactorTxFlags{apply: true}}
	result, err := runRefactorMv(context.Background(), ws, f, []string{"src/util.ts", "src/lib/util.ts"})
	if err != nil {
		t.Fatalf("runRefactorMv --apply: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	if ws.FS.FileExists("/project/src/util.ts") {
		t.Error("source file still exists after mv")
	}
	if got := readWorkspaceFile(t, ws, "/project/src/lib/util.ts"); got != "export const u = 1;\n" {
		t.Errorf("moved file content = %q", got)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/main.ts"); !strings.Contains(got, "import { u } from \"./lib/util\";") {
		t.Errorf("main.ts after mv = %q", got)
	}
}

func TestRefactorMvValidatesArguments(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, mvFixtureFiles())
	f := &refactorMvFlags{}
	ctx := context.Background()
	if _, err := runRefactorMv(ctx, ws, f, []string{"src/util.ts"}); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("single argument: got %v, want usage error", err)
	}
	// Multiple sources require a directory destination.
	if _, err := runRefactorMv(ctx, ws, f, []string{"src/util.ts", "src/main.ts", "src/other.ts"}); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("multi-source to file: got %v, want usage error", err)
	}
	// Destination collision is refused.
	if _, err := runRefactorMv(ctx, ws, f, []string{"src/util.ts", "src/main.ts"}); err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Errorf("existing destination: got %v, want refused", err)
	}
}

func TestRefactorSafeDeleteBlockingRefsSortedByPosition(t *testing.T) {
	t.Parallel()
	// References at lines 9 and 10: a lexicographic sort of "file:line:col"
	// strings would order :10 before :9.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function target(): number {\n\treturn 1;\n}\n",
		"/project/src/b.ts": "import { target } from \"./a\";\n\n\n\n\n\n\n\nexport const x = target();\nexport const y = target();\n",
	})
	f := &refactorSafeDeleteFlags{target: refactorTargetFlags{name: "target"}}
	_, err := runRefactorSafeDelete(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	msg := err.Error()
	i9 := strings.Index(msg, "src/b.ts:9:")
	i10 := strings.Index(msg, "src/b.ts:10:")
	if i9 < 0 || i10 < 0 {
		t.Fatalf("refusal should list both references: %v", err)
	}
	if i10 < i9 {
		t.Errorf("blocking references must sort by file then numeric position (9 before 10): %v", err)
	}
}
