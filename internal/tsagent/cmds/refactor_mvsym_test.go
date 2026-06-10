package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

func TestRefactorMvSymbolToExistingFile(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "/** Doubles a number. */\nexport function double(n: number): number {\n\treturn n * 2;\n}\nexport const other = 1;\n",
		"/project/src/math.ts": "export const PI = 3.14;\n",
		"/project/src/main.ts": "import { double, other } from \"./util\";\nexport const d = double(other);\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "double"},
		tx:     refactorTxFlags{apply: true},
		to:     "src/math.ts",
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if strings.Contains(util, "double") {
		t.Errorf("util.ts still contains the moved symbol: %q", util)
	}
	math := readWorkspaceFile(t, ws, "/project/src/math.ts")
	if !strings.Contains(math, "export function double") || !strings.Contains(math, "/** Doubles a number. */") {
		t.Errorf("math.ts should hold the moved declaration with its JSDoc: %q", math)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { double } from \"./math\";") || !strings.Contains(main, "import { other } from \"./util\";") {
		t.Errorf("main.ts imports were not rewritten: %q", main)
	}
}

func TestRefactorMvSymbolCreatesDestinationAndExportsWhenReferenced(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		// `helper` is unexported but used by main via... it cannot be; use the
		// same file: helper is used by util itself after the move.
		"/project/src/util.ts": "function helper(): number {\n\treturn 7;\n}\nexport const seven = helper();\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "helper"},
		tx:     refactorTxFlags{apply: true},
		to:     "src/lib/helper.ts",
		create: true,
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol --create: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	created := readWorkspaceFile(t, ws, "/project/src/lib/helper.ts")
	if !strings.Contains(created, "export function helper") {
		t.Errorf("moved declaration should be exported in the destination (it is referenced): %q", created)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if !strings.Contains(util, "import { helper } from \"./lib/helper\";") {
		t.Errorf("source file should import the symbol back: %q", util)
	}
	if !strings.Contains(util, "export const seven = helper();") {
		t.Errorf("source usage must survive: %q", util)
	}
}

func TestRefactorMvSymbolCarriesImportedDependencies(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/base.ts": "export const BASE = 10;\n",
		"/project/src/util.ts": "import { BASE } from \"./base\";\nexport function scaled(n: number): number {\n\treturn n * BASE;\n}\nexport const ten = BASE;\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
		"/project/src/main.ts": "import { scaled } from \"./util\";\nexport const s = scaled(2);\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "scaled"},
		tx:     refactorTxFlags{apply: true},
		to:     "src/dest.ts",
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	dest := readWorkspaceFile(t, ws, "/project/src/dest.ts")
	if !strings.Contains(dest, "import { BASE } from \"./base\";") {
		t.Errorf("destination should re-import the moved declaration's dependency: %q", dest)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { scaled } from \"./dest\";") {
		t.Errorf("importer should point at the new file: %q", main)
	}
}

func TestRefactorMvSymbolRefusesLocalUnexportedDeps(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "const secret = 42;\nexport function reveal(): number {\n\treturn secret;\n}\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
	})
	f := &refactorMvSymbolFlags{target: refactorTargetFlags{name: "reveal"}, to: "src/dest.ts"}
	_, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("refusal should list the blocking local dependency: %v", err)
	}
}

func TestRefactorMvSymbolValidatesArguments(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "export const u = 1;\n",
	})
	ctx := context.Background()
	f := &refactorMvSymbolFlags{target: refactorTargetFlags{name: "u"}}
	if _, err := runRefactorMvSymbol(ctx, ws, f, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("missing --to: got %v, want usage error", err)
	}
	f = &refactorMvSymbolFlags{target: refactorTargetFlags{name: "u"}, to: "src/new.ts"}
	if _, err := runRefactorMvSymbol(ctx, ws, f, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("missing --create for a new destination: got %v, want usage error", err)
	}
}
