package cmds

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
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

func TestCheckZeroDiagnosticsText(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/ok.ts": `export const fine: number = 1;`,
	})
	result, err := runCheck(context.Background(), ws, &checkFlags{}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var buf strings.Builder
	out := &cli.Output{W: &buf, Format: cli.FormatText}
	if err := out.Write(result); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.String() != "0 diagnostics\n" {
		t.Errorf("clean check text = %q, want \"0 diagnostics\\n\"", buf.String())
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

func TestCheckWithDiffFixesAndIntroduces(t *testing.T) {
	t.Parallel()
	// bad.ts has a planted TS2322; the patch fixes it and breaks ok.ts.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/ok.ts":  "export const fine: number = 1;\n",
		"/project/src/bad.ts": "export const broken: string = 42;\n",
		"/project/fix.diff": `--- a/src/bad.ts
+++ b/src/bad.ts
@@ -1,1 +1,1 @@
-export const broken: string = 42;
+export const broken: string = "42";
--- a/src/ok.ts
+++ b/src/ok.ts
@@ -1,1 +1,1 @@
-export const fine: number = 1;
+export const fine: number = "oops";
`,
	})
	result, err := runCheckSpeculative(context.Background(), ws, &checkFlags{withDiff: "fix.diff"}, nil)
	if err != nil {
		t.Fatalf("runCheckSpeculative: %v", err)
	}
	if len(result.FixedErrors) != 1 || result.FixedErrors[0].File != "src/bad.ts" || result.FixedErrors[0].Code != "TS2322" {
		t.Errorf("FixedErrors = %+v, want the src/bad.ts TS2322", result.FixedErrors)
	}
	if len(result.NewErrors) != 1 || result.NewErrors[0].File != "src/ok.ts" {
		t.Errorf("NewErrors = %+v, want one error in src/ok.ts", result.NewErrors)
	}
	if result.MovedCount != 0 {
		t.Errorf("MovedCount = %d, want 0", result.MovedCount)
	}
	if !strings.Contains(result.Summary, "1 new, 1 fixed") {
		t.Errorf("Summary = %q", result.Summary)
	}
}

func TestCheckWithDiffMovedNotNew(t *testing.T) {
	t.Parallel()
	// The patch inserts lines above the existing error: it must be reported
	// as moved, not as a new+fixed pair.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/bad.ts": "export const broken: string = 42;\n",
		"/project/pad.diff": `--- a/src/bad.ts
+++ b/src/bad.ts
@@ -1,1 +1,3 @@
+// padding
+// padding
 export const broken: string = 42;
`,
	})
	result, err := runCheckSpeculative(context.Background(), ws, &checkFlags{withDiff: "pad.diff"}, nil)
	if err != nil {
		t.Fatalf("runCheckSpeculative: %v", err)
	}
	if len(result.NewErrors) != 0 || len(result.FixedErrors) != 0 {
		t.Errorf("new=%+v fixed=%+v, want none (only moved)", result.NewErrors, result.FixedErrors)
	}
	if result.MovedCount != 1 {
		t.Errorf("MovedCount = %d, want 1", result.MovedCount)
	}
}

func TestCheckWithDiffStdinCreateAndDelete(t *testing.T) {
	t.Parallel()
	// Patch from stdin: delete the broken file, create a new broken one.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/bad.ts": "export const broken: string = 42;\n",
	})
	patch := `--- a/src/bad.ts
+++ /dev/null
@@ -1,1 +0,0 @@
-export const broken: string = 42;
--- /dev/null
+++ b/src/fresh.ts
@@ -0,0 +1,1 @@
+export const fresh: number = "nope";
`
	flags := &checkFlags{withDiff: "-", stdin: strings.NewReader(patch)}
	result, err := runCheckSpeculative(context.Background(), ws, flags, nil)
	if err != nil {
		t.Fatalf("runCheckSpeculative: %v", err)
	}
	if len(result.FixedErrors) != 1 || result.FixedErrors[0].File != "src/bad.ts" {
		t.Errorf("FixedErrors = %+v, want the deleted file's error", result.FixedErrors)
	}
	if len(result.NewErrors) != 1 || result.NewErrors[0].File != "src/fresh.ts" {
		t.Errorf("NewErrors = %+v, want one error in the created file", result.NewErrors)
	}
}

func TestCheckWithEdits(t *testing.T) {
	t.Parallel()
	// Byte-offset edit that breaks ok.ts: replace the trailing `1` with a string.
	pos := len("export const fine: number = ")
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/ok.ts": "export const fine: number = 1;\n",
		"/project/edits.json": `{
  "edits": [
    {"file": "src/ok.ts", "edits": [{"pos": ` + strconv.Itoa(pos) + `, "end": ` + strconv.Itoa(pos+1) + `, "newText": "\"oops\""}]}
  ]
}`,
	})
	result, err := runCheckSpeculative(context.Background(), ws, &checkFlags{withEdits: "edits.json"}, nil)
	if err != nil {
		t.Fatalf("runCheckSpeculative: %v", err)
	}
	if len(result.NewErrors) != 1 || result.NewErrors[0].File != "src/ok.ts" || result.NewErrors[0].Code != "TS2322" {
		t.Errorf("NewErrors = %+v, want one TS2322 in src/ok.ts", result.NewErrors)
	}
	if len(result.FixedErrors) != 0 {
		t.Errorf("FixedErrors = %+v, want none", result.FixedErrors)
	}
}

func TestCheckWithDiffFailOnRegression(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/ok.ts": "export const fine: number = 1;\n",
		"/project/break.diff": `--- a/src/ok.ts
+++ b/src/ok.ts
@@ -1,1 +1,1 @@
-export const fine: number = 1;
+export const fine: number = "oops";
`,
	})
	flags := &checkFlags{withDiff: "break.diff", failOnRegression: true}
	result, err := runCheckSpeculative(context.Background(), ws, flags, nil)
	if err == nil {
		t.Fatal("expected an error with --fail-on-regression")
	}
	if code := cli.ExitCode(err); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d", code, cli.ExitFailed)
	}
	// The result must still be returned so the CLI can print it before
	// exiting nonzero (cmd/tsagent main prints result + error).
	if result == nil || len(result.NewErrors) != 1 {
		t.Errorf("result = %+v, want the delta alongside the error", result)
	}
}

func TestCheckWithDiffAndEditsMutuallyExclusive(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/ok.ts": "export const fine: number = 1;\n",
	})
	flags := &checkFlags{withDiff: "x.diff", withEdits: "y.json"}
	if _, err := runCheckSpeculative(context.Background(), ws, flags, nil); err == nil {
		t.Fatal("expected usage error for --with-diff + --with-edits")
	} else if code := cli.ExitCode(err); code != cli.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUsage)
	}
}
