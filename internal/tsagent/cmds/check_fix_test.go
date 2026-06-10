package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

// The reliably triggerable one-shot code-fix provider is
// IsolatedDeclarationsFixProvider (annotation insertion needs no auto-import
// index). The import fix and class-implements providers require the
// auto-import registry, which only an LSP session prepares — `check fix`
// reports those as unfixable (covered below).

func checkFixFiles() map[string]any {
	return map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "declaration": true, "isolatedDeclarations": true}}`,
		// TS9013: function must have an explicit return type annotation.
		"/project/src/lib.ts": `export function double(x: number) {
	return x * 2;
}
`,
	}
}

func TestCheckFixIsolatedDeclarationsDryRun(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, checkFixFiles())
	flags := &checkFixFlags{code: "TS9013"}
	result, err := runCheckFix(context.Background(), ws, flags, nil)
	if err != nil {
		t.Fatalf("runCheckFix: %v", err)
	}
	if len(result.Outcomes) != 1 || !result.Outcomes[0].Fixed {
		t.Fatalf("outcomes = %+v, want one fixed", result.Outcomes)
	}
	o := result.Outcomes[0]
	if o.File != "src/lib.ts" || o.Code != "TS9013" || o.Action == "" {
		t.Errorf("outcome = %+v", o)
	}
	if result.Tx == nil || result.Tx.Applied {
		t.Fatalf("dry-run must not apply, tx = %+v", result.Tx)
	}
	if len(result.Tx.Diffs) != 1 || !strings.Contains(result.Tx.Diffs[0].Diff, ": number") {
		t.Errorf("diff should add the return annotation, got %+v", result.Tx.Diffs)
	}
	// Disk untouched.
	content, _ := ws.FS.ReadFile("/project/src/lib.ts")
	if strings.Contains(content, "): number") {
		t.Error("dry-run wrote to disk")
	}
}

func TestCheckFixIsolatedDeclarationsApply(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, checkFixFiles())
	flags := &checkFixFlags{code: "9013", tx: refactorTxFlags{apply: true}}
	result, err := runCheckFix(context.Background(), ws, flags, nil)
	if err != nil {
		t.Fatalf("runCheckFix --apply: %v", err)
	}
	if result.Tx == nil || !result.Tx.Applied {
		t.Fatalf("expected applied tx, got %+v", result.Tx)
	}
	content, _ := ws.FS.ReadFile("/project/src/lib.ts")
	if !strings.Contains(content, "export function double(x: number): number {") {
		t.Errorf("annotation not written, content:\n%s", content)
	}
}

func TestCheckFixByDiagRef(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, checkFixFiles())
	ctx := context.Background()

	// Resolve the planted diagnostic's ref via the fix collector itself
	// (declaration diagnostics are not part of plain `check` output).
	diags, err := checkFixTargetDiagnostics(ctx, ws, &checkFixFlags{code: "TS9013"}, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	ref := checkFixOutcomeFor(ws, diags[0]).DiagRef

	result, err := runCheckFix(ctx, ws, &checkFixFlags{}, []string{ref})
	if err != nil {
		t.Fatalf("runCheckFix(%s): %v", ref, err)
	}
	if len(result.Outcomes) != 1 || !result.Outcomes[0].Fixed {
		t.Fatalf("outcomes = %+v", result.Outcomes)
	}

	// Unknown diagRef is a not-found error.
	if _, err := runCheckFix(ctx, ws, &checkFixFlags{}, []string{"src/lib.ts:0:TS9999"}); cli.ExitCode(err) != cli.ExitNotFound {
		t.Errorf("unknown diagRef: err = %v, want exit %d", err, cli.ExitNotFound)
	}
}

func TestCheckFixImportFixNeedsRegistry(t *testing.T) {
	t.Parallel()
	// TS2304 Cannot find name: the import-fix provider needs the auto-import
	// registry, which is unavailable in one-shot mode; check fix must report
	// the diagnostic as unfixable instead of erroring out.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "export function helper(): number { return 1; }\n",
		"/project/src/main.ts": "export const x = helper();\n",
	})
	result, err := runCheckFix(context.Background(), ws, &checkFixFlags{code: "TS2304"}, nil)
	if cli.ExitCode(err) != cli.ExitFailed {
		t.Fatalf("err = %v, want exit %d (nothing fixable)", err, cli.ExitFailed)
	}
	if result == nil || len(result.Outcomes) != 1 || result.Outcomes[0].Fixed {
		t.Fatalf("outcomes = %+v, want one unfixable", result)
	}
	if !strings.Contains(result.Outcomes[0].Reason, "auto-import") {
		t.Errorf("reason = %q, want auto-import explanation", result.Outcomes[0].Reason)
	}
}

func TestCheckFixUsage(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, checkFixFiles())
	ctx := context.Background()
	if _, err := runCheckFix(ctx, ws, &checkFixFlags{}, nil); cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("no target: err = %v, want usage error", err)
	}
	if _, err := runCheckFix(ctx, ws, &checkFixFlags{code: "TS9013"}, []string{"x:1:TS9013"}); cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("both targets: err = %v, want usage error", err)
	}
	if _, err := runCheckFix(ctx, ws, &checkFixFlags{code: "TS9999"}, nil); cli.ExitCode(err) != cli.ExitNotFound {
		t.Errorf("unmatched code: err = %v, want not-found", err)
	}
}
