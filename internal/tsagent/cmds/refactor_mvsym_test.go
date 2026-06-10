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

func TestRefactorMvSymbolCarriesDefaultAndNamespaceImportDeps(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/sj.ts":    "const sj = { stringify: (v: unknown): string => String(v) };\nexport default sj;\n",
		"/project/src/nsmod.ts": "export const x = 1;\n",
		"/project/src/util.ts":  "import superjson from \"./sj\";\nimport * as ns from \"./nsmod\";\nexport function moveMe(): string {\n\treturn superjson.stringify(ns.x);\n}\nexport const keep = 1;\n",
		"/project/src/dest.ts":  "export const unrelated = 0;\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
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
	if !strings.Contains(dest, "import superjson from \"./sj\";") {
		t.Errorf("destination should re-import the default binding: %q", dest)
	}
	if !strings.Contains(dest, "import * as ns from \"./nsmod\";") {
		t.Errorf("destination should re-import the namespace binding: %q", dest)
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
	if !strings.Contains(err.Error(), "--with-deps") || !strings.Contains(err.Error(), "with-deps") {
		t.Errorf("refusal should suggest --with-deps / with-deps: %v", err)
	}
}

func TestRefactorMvSymbolRewritesReexportConsumer(t *testing.T) {
	t.Parallel()
	// barrel.ts re-exports the moved symbol; main.ts consumes it THROUGH the
	// barrel. The re-export retargets to the new file, the consumer stays
	// untouched, and the emptied source file is deleted.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts":   "export function moveMe(): number {\n\treturn 1;\n}\n",
		"/project/src/barrel.ts": "export { moveMe } from \"./util\";\n",
		"/project/src/dest.ts":   "export const unrelated = 0;\n",
		"/project/src/main.ts":   "import { moveMe } from \"./barrel\";\nexport const x = moveMe();\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
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
	barrel := readWorkspaceFile(t, ws, "/project/src/barrel.ts")
	if barrel != "export { moveMe } from \"./dest\";\n" {
		t.Errorf("barrel.ts = %q, want the re-export retargeted to ./dest", barrel)
	}
	if ws.FS.FileExists("/project/src/util.ts") {
		t.Error("util.ts became empty and should have been deleted")
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if main != "import { moveMe } from \"./barrel\";\nexport const x = moveMe();\n" {
		t.Errorf("the through-the-barrel consumer must stay untouched: %q", main)
	}
}

func TestRefactorMvSymbolRewritesMultiSpecifierReexport(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts":   "export function moveMe(): number {\n\treturn 1;\n}\nexport function stay(): number {\n\treturn 2;\n}\n",
		"/project/src/barrel.ts": "export { moveMe, stay } from \"./util\";\n",
		"/project/src/dest.ts":   "export const unrelated = 0;\n",
		"/project/src/main.ts":   "import { moveMe, stay } from \"./barrel\";\nexport const x = moveMe() + stay();\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
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
	barrel := readWorkspaceFile(t, ws, "/project/src/barrel.ts")
	want := "export { stay } from \"./util\";\nexport { moveMe } from \"./dest\";\n"
	if barrel != want {
		t.Errorf("barrel.ts = %q, want %q", barrel, want)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if !strings.Contains(util, "export function stay") || strings.Contains(util, "moveMe") {
		t.Errorf("util.ts should keep stay and lose moveMe: %q", util)
	}
}

func TestRefactorMvSymbolPreservesReexportAlias(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts":   "export function moveMe(): number {\n\treturn 1;\n}\n",
		"/project/src/barrel.ts": "export { moveMe as renamed } from \"./util\";\n",
		"/project/src/dest.ts":   "export const unrelated = 0;\n",
		"/project/src/main.ts":   "import { renamed } from \"./barrel\";\nexport const x = renamed();\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
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
	barrel := readWorkspaceFile(t, ws, "/project/src/barrel.ts")
	if barrel != "export { moveMe as renamed } from \"./dest\";\n" {
		t.Errorf("barrel.ts = %q, want the alias preserved and the specifier retargeted", barrel)
	}
}

func TestRefactorMvSymbolPreservesTypeOnlyReexports(t *testing.T) {
	t.Parallel()
	// Statement-level `export type { … } from` (sole specifier, retargeted in
	// place) and a per-specifier `type` inside a multi-name clause (split into
	// a fresh statement).
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts":    "export interface Shape {\n\tarea: number;\n}\nexport function stay(): number {\n\treturn 2;\n}\n",
		"/project/src/barrel.ts":  "export type { Shape } from \"./util\";\n",
		"/project/src/barrel2.ts": "export { type Shape, stay } from \"./util\";\n",
		"/project/src/dest.ts":    "export const unrelated = 0;\n",
		"/project/src/main.ts":    "import type { Shape } from \"./barrel\";\nimport { stay } from \"./barrel2\";\nexport const s: Shape = { area: stay() };\n",
	})
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "Shape"},
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
	barrel := readWorkspaceFile(t, ws, "/project/src/barrel.ts")
	if barrel != "export type { Shape } from \"./dest\";\n" {
		t.Errorf("barrel.ts = %q, want export type retargeted in place", barrel)
	}
	barrel2 := readWorkspaceFile(t, ws, "/project/src/barrel2.ts")
	want := "export { stay } from \"./util\";\nexport { type Shape } from \"./dest\";\n"
	if barrel2 != want {
		t.Errorf("barrel2.ts = %q, want %q", barrel2, want)
	}
}

func TestRefactorMvSymbolRefusesStarReexportWithLocation(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts":   "export function moveMe(): number {\n\treturn 1;\n}\n",
		"/project/src/barrel.ts": "export const padding = 1;\nexport * from \"./util\";\n",
		"/project/src/dest.ts":   "export const unrelated = 0;\n",
		"/project/src/main.ts":   "import { moveMe } from \"./barrel\";\nexport const x = moveMe();\n",
	})
	f := &refactorMvSymbolFlags{target: refactorTargetFlags{name: "moveMe"}, to: "src/dest.ts"}
	_, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "export * from") || !strings.Contains(err.Error(), "src/barrel.ts:2") {
		t.Errorf("refusal should name the star re-export file and line: %v", err)
	}
}

func TestRefactorMvSymbolWithDepsMovesLocalHelper(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "function helper(): number {\n\treturn 7;\n}\nexport function moveMe(): number {\n\treturn helper();\n}\nexport const keep = 1;\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
		"/project/src/main.ts": "import { moveMe } from \"./util\";\nexport const x = moveMe();\n",
	})
	f := &refactorMvSymbolFlags{
		target:   refactorTargetFlags{name: "moveMe"},
		tx:       refactorTxFlags{apply: true},
		to:       "src/dest.ts",
		withDeps: true,
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol --with-deps: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	dest := readWorkspaceFile(t, ws, "/project/src/dest.ts")
	indexOrder(t, dest, "function helper", "export function moveMe")
	if strings.Contains(dest, "export function helper") {
		t.Errorf("the moved dependency must stay unexported: %q", dest)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if strings.Contains(util, "helper") || strings.Contains(util, "moveMe") || !strings.Contains(util, "export const keep = 1;") {
		t.Errorf("util.ts should keep only the remaining declaration: %q", util)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { moveMe } from \"./dest\";") {
		t.Errorf("importer should point at the new file: %q", main)
	}
}

func TestRefactorMvSymbolWithDepsTransitiveChain(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "function c(): number {\n\treturn 3;\n}\nfunction b(): number {\n\treturn c();\n}\nfunction a(): number {\n\treturn b();\n}\nexport function moveMe(): number {\n\treturn a();\n}\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
	})
	f := &refactorMvSymbolFlags{
		target:   refactorTargetFlags{name: "moveMe"},
		tx:       refactorTxFlags{apply: true},
		to:       "src/dest.ts",
		withDeps: true,
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol --with-deps: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	dest := readWorkspaceFile(t, ws, "/project/src/dest.ts")
	indexOrder(t, dest, "function c", "function b", "function a", "export function moveMe")
	if ws.FS.FileExists("/project/src/util.ts") {
		t.Error("util.ts became empty (decl + whole dep chain moved) and should have been deleted")
	}
}

func TestRefactorMvSymbolWithDepsRefusesSharedDep(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "function helper(): number {\n\treturn 7;\n}\nexport function moveMe(): number {\n\treturn helper();\n}\nexport function alsoUses(): number {\n\treturn helper();\n}\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
	})
	f := &refactorMvSymbolFlags{target: refactorTargetFlags{name: "moveMe"}, to: "src/dest.ts", withDeps: true}
	_, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "helper") || !strings.Contains(err.Error(), "alsoUses") {
		t.Errorf("refusal should name the shared dep and the remaining declaration using it: %v", err)
	}
}

func TestRefactorMvSymbolWithDepsKeepsExportedDep(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "export function helper(): number {\n\treturn 7;\n}\nexport function moveMe(): number {\n\treturn helper();\n}\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
	})
	f := &refactorMvSymbolFlags{
		target:   refactorTargetFlags{name: "moveMe"},
		tx:       refactorTxFlags{apply: true},
		to:       "src/dest.ts",
		withDeps: true,
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol --with-deps: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if !strings.Contains(util, "export function helper") {
		t.Errorf("the exported dep must stay in the source file: %q", util)
	}
	dest := readWorkspaceFile(t, ws, "/project/src/dest.ts")
	if !strings.Contains(dest, "import { helper } from \"./util\";") {
		t.Errorf("the destination should import the exported dep back: %q", dest)
	}
	if strings.Contains(dest, "function helper(): number") {
		t.Errorf("the exported dep must NOT be moved: %q", dest)
	}
}

func TestRefactorMvSymbolWithDepsCarriesDepExternalImports(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/base.ts": "export const BASE = 10;\n",
		"/project/src/util.ts": "import { BASE } from \"./base\";\nfunction helper(): number {\n\treturn BASE;\n}\nexport function moveMe(): number {\n\treturn helper();\n}\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
	})
	f := &refactorMvSymbolFlags{
		target:   refactorTargetFlags{name: "moveMe"},
		tx:       refactorTxFlags{apply: true},
		to:       "src/dest.ts",
		withDeps: true,
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol --with-deps: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	dest := readWorkspaceFile(t, ws, "/project/src/dest.ts")
	if !strings.Contains(dest, "import { BASE } from \"./base\";") {
		t.Errorf("the destination should carry the dep's external import: %q", dest)
	}
	indexOrder(t, dest, "function helper", "export function moveMe")
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
