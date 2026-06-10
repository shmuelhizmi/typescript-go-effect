package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

func TestRefactorExportsToNamedNamedDeclaration(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/f.ts":    "export default function greet(): string {\n\treturn \"hi\";\n}\n",
		"/project/src/main.ts": "import greet from \"./f\";\nexport const s = greet();\n",
	})
	f := &refactorExportsFlags{to: "named", tx: refactorTxFlags{apply: true}}
	result, err := runRefactorExports(context.Background(), ws, f, []string{"src/f.ts"})
	if err != nil {
		t.Fatalf("runRefactorExports: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	fts := readWorkspaceFile(t, ws, "/project/src/f.ts")
	if !strings.Contains(fts, "export function greet") || strings.Contains(fts, "default") {
		t.Errorf("f.ts after exports --to named = %q", fts)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { greet } from \"./f\";") {
		t.Errorf("main.ts after exports --to named = %q", main)
	}
}

func TestRefactorExportsToNamedAnonymousAndAliasedImport(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/math-helper.ts": "export default function (): number {\n\treturn 1;\n}\n",
		"/project/src/main.ts":        "import calc from \"./math-helper\";\nexport const n = calc();\n",
	})
	f := &refactorExportsFlags{to: "named", tx: refactorTxFlags{apply: true}}
	result, err := runRefactorExports(context.Background(), ws, f, []string{"src/math-helper.ts"})
	if err != nil {
		t.Fatalf("runRefactorExports: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	helper := readWorkspaceFile(t, ws, "/project/src/math-helper.ts")
	if !strings.Contains(helper, "export function mathHelper()") {
		t.Errorf("anonymous default export should be named from the file name: %q", helper)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { mathHelper as calc } from \"./math-helper\";") {
		t.Errorf("importer should keep its local alias: %q", main)
	}
}

func TestRefactorExportsToNamedExpressionAndMixedImport(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/config.ts": "export const version = 2;\nexport default { retries: 3 };\n",
		"/project/src/main.ts":   "import config, { version } from \"./config\";\nexport const r = config.retries + version;\n",
	})
	f := &refactorExportsFlags{to: "named", tx: refactorTxFlags{apply: true}}
	result, err := runRefactorExports(context.Background(), ws, f, []string{"src/config.ts"})
	if err != nil {
		t.Fatalf("runRefactorExports: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	cfg := readWorkspaceFile(t, ws, "/project/src/config.ts")
	if !strings.Contains(cfg, "export const config = { retries: 3 };") {
		t.Errorf("config.ts after exports --to named = %q", cfg)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { config, version } from \"./config\";") {
		t.Errorf("default + named import should merge into one named clause: %q", main)
	}
}

func TestRefactorExportsToDefault(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/f.ts":    "export function greet(): string {\n\treturn \"hi\";\n}\n",
		"/project/src/main.ts": "import { greet as hello } from \"./f\";\nexport const s = hello();\n",
	})
	f := &refactorExportsFlags{to: "default", tx: refactorTxFlags{apply: true}}
	result, err := runRefactorExports(context.Background(), ws, f, []string{"src/f.ts"})
	if err != nil {
		t.Fatalf("runRefactorExports --to default: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	fts := readWorkspaceFile(t, ws, "/project/src/f.ts")
	if !strings.Contains(fts, "export default function greet") {
		t.Errorf("f.ts after exports --to default = %q", fts)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import hello from \"./f\";") {
		t.Errorf("main.ts after exports --to default = %q", main)
	}
}

func TestRefactorExportsToDefaultConstUsesTrailingStatement(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/f.ts":    "export const limit = 10;\n",
		"/project/src/main.ts": "import { limit } from \"./f\";\nexport const l = limit;\n",
	})
	f := &refactorExportsFlags{to: "default", tx: refactorTxFlags{apply: true}}
	result, err := runRefactorExports(context.Background(), ws, f, []string{"src/f.ts"})
	if err != nil {
		t.Fatalf("runRefactorExports --to default: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	fts := readWorkspaceFile(t, ws, "/project/src/f.ts")
	if !strings.Contains(fts, "const limit = 10;") || !strings.Contains(fts, "export default limit;") {
		t.Errorf("f.ts after exports --to default = %q", fts)
	}
	if got := readWorkspaceFile(t, ws, "/project/src/main.ts"); !strings.Contains(got, "import limit from \"./f\";") {
		t.Errorf("main.ts after exports --to default = %q", got)
	}
}

func TestRefactorExportsToDefaultRefusesMultipleExports(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/f.ts": "export const a = 1;\nexport const b = 2;\n",
	})
	f := &refactorExportsFlags{to: "default"}
	_, err := runRefactorExports(context.Background(), ws, f, []string{"src/f.ts"})
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "a, b") {
		t.Errorf("refusal should list the export names: %v", err)
	}
}

func TestRefactorExportsToNamedRefusesDefaultReExport(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/inner.ts": "export default function inner(): number {\n\treturn 1;\n}\n",
		"/project/src/f.ts":     "export { default } from \"./inner\";\n",
	})
	f := &refactorExportsFlags{to: "named"}
	_, err := runRefactorExports(context.Background(), ws, f, []string{"src/f.ts"})
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Errorf("expected a refusal for default re-exports, got %v", err)
	}
}

func TestRefactorExportsValidatesFlags(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/f.ts": "export const a = 1;\n"})
	ctx := context.Background()
	if _, err := runRefactorExports(ctx, ws, &refactorExportsFlags{to: "both"}, []string{"src/f.ts"}); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("bad --to: got %v, want usage error", err)
	}
	if _, err := runRefactorExports(ctx, ws, &refactorExportsFlags{to: "named"}, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("no paths: got %v, want usage error", err)
	}
	if _, err := runRefactorExports(ctx, ws, &refactorExportsFlags{to: "named"}, []string{"src/f.ts"}); err == nil || cli.ExitCode(err) != cli.ExitNotFound {
		t.Errorf("no default export: got %v, want not-found", err)
	}
}
