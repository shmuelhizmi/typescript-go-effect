package cmds

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	tsagentcore "github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

// ---------------------------------------------------------------------------
// analyze dead-code

const deadCodeUtilSource = `export function usedExport(): number {
	return helper();
}

function helper(): number {
	return 1;
}

function unusedLocal(): void {}

export function unusedExport(): void {}

function dynamicName(): void {}

const registry = "dynamicName";

export class Service {
	private unusedPrivate(): void {}
	run(): void {}
}
`

const deadCodeMainSource = `import { usedExport } from "./util";

usedExport();
`

func deadCodeNames(result *DeadCodeResult) []string {
	names := make([]string, 0, len(result.Symbols))
	for _, s := range result.Symbols {
		names = append(names, s.Name)
	}
	return names
}

func deadCodeSymbol(t *testing.T, result *DeadCodeResult, name string) *DeadSymbol {
	t.Helper()
	for _, s := range result.Symbols {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("symbol %q not reported; got %v", name, deadCodeNames(result))
	return nil
}

func TestAnalyzeDeadCode(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": deadCodeUtilSource,
		"/project/src/main.ts": deadCodeMainSource,
	})
	ctx := context.Background()

	result, err := runAnalyzeDeadCode(ctx, ws, &deadCodeFlags{}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeDeadCode: %v", err)
	}
	names := deadCodeNames(result)

	unusedLocal := deadCodeSymbol(t, result, "unusedLocal")
	if unusedLocal.Confidence != "certain" {
		t.Errorf("unusedLocal confidence = %q, want certain", unusedLocal.Confidence)
	}
	if unusedLocal.Kind != "function" || unusedLocal.File != "src/util.ts" {
		t.Errorf("unusedLocal = %+v", unusedLocal)
	}
	if unusedLocal.Exported {
		t.Error("unusedLocal should not be marked exported")
	}

	// Mentioned inside a string literal -> dynamic-risk.
	dynamic := deadCodeSymbol(t, result, "dynamicName")
	if dynamic.Confidence != "dynamic-risk" {
		t.Errorf("dynamicName confidence = %q, want dynamic-risk", dynamic.Confidence)
	}

	// Unused private member is in the high-confidence tier.
	if private := deadCodeSymbol(t, result, "unusedPrivate"); private.Kind != "method" {
		t.Errorf("unusedPrivate kind = %q, want method", private.Kind)
	}

	// Live symbols and (by default) exports are not reported.
	for _, name := range []string{"usedExport", "helper", "unusedExport", "Service", "run"} {
		if slices.Contains(names, name) {
			t.Errorf("%s should not be reported (got %v)", name, names)
		}
	}
}

func TestAnalyzeDeadCodeIncludeExports(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": deadCodeUtilSource,
		"/project/src/main.ts": deadCodeMainSource,
	})
	ctx := context.Background()

	result, err := runAnalyzeDeadCode(ctx, ws, &deadCodeFlags{includeExports: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeDeadCode: %v", err)
	}
	unusedExport := deadCodeSymbol(t, result, "unusedExport")
	if !unusedExport.Exported {
		t.Error("unusedExport should be marked exported")
	}
	if names := deadCodeNames(result); slices.Contains(names, "usedExport") {
		t.Errorf("usedExport is imported by main.ts and should not be reported (got %v)", names)
	}

	// --entry treats the file's exports as live roots.
	result, err = runAnalyzeDeadCode(ctx, ws, &deadCodeFlags{includeExports: true, entry: "src/util.ts"}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeDeadCode(--entry): %v", err)
	}
	if names := deadCodeNames(result); slices.Contains(names, "unusedExport") {
		t.Errorf("unusedExport should be protected by --entry (got %v)", names)
	}
}

func TestAnalyzeDeadCodeFixPlan(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": deadCodeUtilSource,
		"/project/src/main.ts": deadCodeMainSource,
	})
	result, err := runAnalyzeDeadCode(context.Background(), ws, &deadCodeFlags{fixPlan: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeDeadCode: %v", err)
	}
	if result.FixPlan == nil {
		t.Fatal("expected a fix plan")
	}
	var utilEdits *DeadCodeFixFile
	for i := range result.FixPlan.Edits {
		if result.FixPlan.Edits[i].File == "src/util.ts" {
			utilEdits = &result.FixPlan.Edits[i]
		}
	}
	if utilEdits == nil {
		t.Fatalf("no fix-plan edits for src/util.ts: %+v", result.FixPlan)
	}
	file, err := ws.FileOf("src/util.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	text := file.Text()
	var deleted strings.Builder
	for _, edit := range utilEdits.Edits {
		if edit.NewText != "" {
			t.Errorf("fix-plan edit should be a pure deletion, got %+v", edit)
		}
		if edit.Start < 0 || edit.End > len(text) || edit.Start >= edit.End {
			t.Fatalf("fix-plan edit out of bounds: %+v", edit)
		}
		deleted.WriteString(text[edit.Start:edit.End])
	}
	for _, want := range []string{"unusedLocal", "unusedPrivate", "registry"} {
		if !strings.Contains(deleted.String(), want) {
			t.Errorf("fix plan deletions missing %s:\n%s", want, deleted.String())
		}
	}
	if strings.Contains(deleted.String(), "usedExport") {
		t.Errorf("fix plan must not delete live code:\n%s", deleted.String())
	}
}

// ---------------------------------------------------------------------------
// analyze assertions

const assertionsSource = `export const raw: unknown = "hi";
export const s = raw as string;
export const c = [1, 2] as const;
export const sat = { x: 1 } satisfies { x: number };
declare const maybe: string | undefined;
export const def = maybe!;
// @ts-ignore
export const ig: number = "str";
// @ts-expect-error
export const ee: number = "str2";
`

func TestAnalyzeAssertions(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/asserts.ts": assertionsSource,
		"/project/src/nocheck.ts": "// @ts-nocheck\nexport const n = 1;\n",
	})
	result, err := runAnalyzeAssertions(context.Background(), ws, &assertionsFlags{}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeAssertions: %v", err)
	}

	wantTotals := map[string]int{
		"as":              1,
		"as-const":        1,
		"satisfies":       1,
		"non-null":        1,
		"ts-ignore":       1,
		"ts-expect-error": 1,
		"ts-nocheck":      1,
	}
	for kind, want := range wantTotals {
		if got := result.Totals[kind]; got != want {
			t.Errorf("totals[%s] = %d, want %d (totals: %v)", kind, got, want, result.Totals)
		}
	}

	var asRow *AssertionRow
	for _, row := range result.Rows {
		if row.Kind == "as" {
			asRow = row
		}
	}
	if asRow == nil {
		t.Fatal("no as row found")
	}
	if asRow.File != "src/asserts.ts" || asRow.Line != 2 {
		t.Errorf("as row at %s:%d, want src/asserts.ts:2", asRow.File, asRow.Line)
	}
	if asRow.FromType != "unknown" || asRow.ToType != "string" {
		t.Errorf("as row types = %q -> %q, want unknown -> string", asRow.FromType, asRow.ToType)
	}

	var fileCounts *AssertionsFileCounts
	for _, f := range result.Files {
		if f.File == "src/asserts.ts" {
			fileCounts = f
		}
	}
	if fileCounts == nil || fileCounts.Counts["non-null"] != 1 {
		t.Errorf("per-file counts for src/asserts.ts = %+v", fileCounts)
	}

	countsOnly, err := runAnalyzeAssertionCounts(ws, nil)
	if err != nil {
		t.Fatalf("runAnalyzeAssertionCounts: %v", err)
	}
	if len(countsOnly.Rows) != 0 {
		t.Fatalf("count-only assertions should not retain detail rows, got %d", len(countsOnly.Rows))
	}
	for kind, want := range wantTotals {
		if got := countsOnly.Totals[kind]; got != want {
			t.Errorf("count-only totals[%s] = %d, want %d (totals: %v)", kind, got, want, countsOnly.Totals)
		}
	}
}

// ---------------------------------------------------------------------------
// analyze unused-deps

func TestAnalyzeUnusedDeps(t *testing.T) {
	t.Parallel()
	files := map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "paths": {"@app/*": ["./src/*"]}}}`,
		"/project/package.json": `{
			"dependencies": {"used-pkg": "1.0.0", "unused-pkg": "1.0.0", "@types/typed-pkg": "1.0.0"},
			"devDependencies": {"dev-unused": "1.0.0"}
		}`,
		"/project/src/lib.ts": "export const lib = 1;\n",
		"/project/src/main.ts": `import { x } from "used-pkg";
import { y } from "phantom-pkg";
import { z } from "typed-pkg/sub";
import { lib } from "@app/lib";
import * as fs from "node:fs";
import { local } from "./lib2";
export const all = [x, y, z, lib, fs, local];
`,
		"/project/src/lib2.ts": "export const local = 2;\n",
	}
	ws := newTestWorkspace(t, files)
	ctx := context.Background()

	result, err := runAnalyzeUnusedDeps(ctx, ws, &unusedDepsFlags{dev: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeUnusedDeps: %v", err)
	}
	if result.PackageJSON != "package.json" {
		t.Errorf("packageJson = %q, want package.json", result.PackageJSON)
	}
	wantUnused := []string{"dev-unused", "unused-pkg"}
	if !slices.Equal(result.UnusedDeps, wantUnused) {
		t.Errorf("unusedDeps = %v, want %v", result.UnusedDeps, wantUnused)
	}
	// phantom-pkg is imported but undeclared; typed-pkg is covered by
	// @types/typed-pkg; node:fs, the path alias, and relative imports are
	// excluded.
	wantPhantom := []string{"phantom-pkg"}
	if !slices.Equal(result.PhantomDeps, wantPhantom) {
		t.Errorf("phantomDeps = %v, want %v", result.PhantomDeps, wantPhantom)
	}

	// --dev=false leaves devDependencies out of the unused analysis.
	result, err = runAnalyzeUnusedDeps(ctx, ws, &unusedDepsFlags{dev: false}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeUnusedDeps(--dev=false): %v", err)
	}
	if !slices.Equal(result.UnusedDeps, []string{"unused-pkg"}) {
		t.Errorf("unusedDeps without dev = %v, want [unused-pkg]", result.UnusedDeps)
	}
}

func TestAnalyzeUnusedDepsBinUsages(t *testing.T) {
	t.Parallel()
	files := map[string]any{
		"/project/package.json": `{
			"scripts": {
				"build": "used-tool --flag && string-tool src/index.ts",
				"check": "myunused-tool"
			},
			"devDependencies": {
				"used-tool": "1.0.0",
				"string-tool": "1.0.0",
				"unused-tool": "1.0.0"
			}
		}`,
		"/project/node_modules/used-tool/package.json":   `{"name":"used-tool","bin":{"used-tool":"bin/cli.js"}}`,
		"/project/node_modules/string-tool/package.json": `{"name":"string-tool","bin":"bin/cli.js"}`,
		"/project/node_modules/unused-tool/package.json": `{"name":"unused-tool","bin":{"unused-tool":"bin/cli.js"}}`,
		"/project/.vscode/tasks.json":                    `{"problemMatcher":{"owner":"unused-tool"}}`,
		"/project/testdata/fixture/tool.json":            `{"command":"unused-tool"}`,
		"/project/src/index.ts":                          "export const value = 1;\n",
	}
	ws := newTestWorkspace(t, files)

	result, err := runAnalyzeUnusedDeps(context.Background(), ws, &unusedDepsFlags{dev: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeUnusedDeps: %v", err)
	}
	if !slices.Equal(result.UnusedDeps, []string{"unused-tool"}) {
		t.Errorf("unusedDeps = %v, want [unused-tool]", result.UnusedDeps)
	}
	wantUsages := []DependencyBinUsage{
		{Dependency: "string-tool", Bin: "string-tool", File: "package.json", Line: 3, Source: "package-script"},
		{Dependency: "used-tool", Bin: "used-tool", File: "package.json", Line: 3, Source: "package-script"},
	}
	if !slices.Equal(result.BinUsages, wantUsages) {
		t.Errorf("binUsages = %#v, want %#v", result.BinUsages, wantUsages)
	}
}

func TestAnalyzeUnusedDepsBinUsagesScanWorkspaceRoot(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	files := map[string]any{
		"/repo/package.json":                           `{"private":true,"workspaces":["packages/*"]}`,
		"/repo/Herebyfile.mjs":                         "await $({ cwd: thisExtensionDir })`vsce package --out extension.vsix`;\n",
		"/repo/node_modules/@vscode/vsce/package.json": `{"name":"@vscode/vsce","bin":{"vsce":"vsce"}}`,
		"/repo/packages/app/tsconfig.json":             `{"compilerOptions":{"strict":true,"target":"esnext"},"include":["src/**/*"]}`,
		"/repo/packages/app/package.json": `{
			"devDependencies": {
				"@vscode/vsce": "1.0.0",
				"unused-tool": "1.0.0"
			}
		}`,
		"/repo/packages/app/node_modules/unused-tool/package.json": `{"name":"unused-tool","bin":{"unused-tool":"bin/cli.js"}}`,
		"/repo/packages/app/src/index.ts":                          "export const value = 1;\n",
	}
	fs := bundled.WrapFS(vfstest.FromMap(files, true /*useCaseSensitiveFileNames*/))
	ws, err := tsagentcore.NewWorkspace(tsagentcore.Options{
		Project:        "/repo/packages/app",
		Cwd:            "/repo/packages/app",
		FS:             fs,
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	result, err := runAnalyzeUnusedDeps(context.Background(), ws, &unusedDepsFlags{dev: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeUnusedDeps: %v", err)
	}
	if !slices.Equal(result.UnusedDeps, []string{"unused-tool"}) {
		t.Errorf("unusedDeps = %v, want [unused-tool]", result.UnusedDeps)
	}
	wantUsages := []DependencyBinUsage{
		{Dependency: "@vscode/vsce", Bin: "vsce", File: "../../Herebyfile.mjs", Line: 1, Source: "task-file"},
	}
	if !slices.Equal(result.BinUsages, wantUsages) {
		t.Errorf("binUsages = %#v, want %#v", result.BinUsages, wantUsages)
	}
}

// ---------------------------------------------------------------------------
// analyze complexity

const analyzeComplexitySource = `export function simple(): number {
	return 1;
}

export function gnarly(a: number, b: number): number {
	if (a > 0) {
		for (let i = 0; i < b; i++) {
			if (i % 2 === 0 && a > 1) {
				return i;
			}
		}
	}
	switch (b) {
		case 1:
			return 2;
		case 2:
			return a > 5 ? 1 : 0;
		default:
			return 0;
	}
}
`

func TestAnalyzeComplexity(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/fns.ts": analyzeComplexitySource})
	ctx := context.Background()

	result, err := runAnalyzeComplexity(ctx, ws, &analyzeComplexityFlags{top: 25}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeComplexity: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	gnarly, simple := result.Entries[0], result.Entries[1]
	if gnarly.Name != "gnarly" || simple.Name != "simple" {
		t.Fatalf("ranking = [%s, %s], want [gnarly, simple]", gnarly.Name, simple.Name)
	}
	if gnarly.Score <= simple.Score {
		t.Errorf("gnarly score %.1f should exceed simple score %.1f", gnarly.Score, simple.Score)
	}
	// if + for + if + && + case + case + conditional = 7 branching points.
	if gnarly.Cyclomatic != 8 {
		t.Errorf("gnarly cyclomatic = %d, want 8", gnarly.Cyclomatic)
	}
	if simple.Cyclomatic != 1 || simple.Cognitive != 0 {
		t.Errorf("simple metrics = cyclomatic:%d cognitive:%d, want 1/0", simple.Cyclomatic, simple.Cognitive)
	}
	if gnarly.Cognitive <= simple.Cognitive {
		t.Errorf("gnarly cognitive %d should exceed simple %d", gnarly.Cognitive, simple.Cognitive)
	}
	if gnarly.TypeComplexity <= 0 {
		t.Errorf("gnarly typeComplexity = %d, want > 0", gnarly.TypeComplexity)
	}

	// --top 1 keeps only the hotspot.
	result, err = runAnalyzeComplexity(ctx, ws, &analyzeComplexityFlags{top: 1}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeComplexity(--top 1): %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Name != "gnarly" {
		t.Errorf("--top 1 entries = %+v, want only gnarly", result.Entries)
	}

	result, err = runAnalyzeComplexity(ctx, ws, &analyzeComplexityFlags{top: 25, typeCandidateLimit: 1}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeComplexity(bounded): %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("bounded entries = %d, want 2", len(result.Entries))
	}
	if result.Entries[0].Name != "gnarly" || result.Entries[0].TypeComplexity <= 0 {
		t.Errorf("bounded top entry = %+v, want gnarly with type complexity", result.Entries[0])
	}
	if result.Entries[1].Name != "simple" || result.Entries[1].TypeComplexity != 0 {
		t.Errorf("bounded second entry = %+v, want simple without type complexity", result.Entries[1])
	}

	// --threshold filters out the simple function.
	result, err = runAnalyzeComplexity(ctx, ws, &analyzeComplexityFlags{top: 25, threshold: 5}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeComplexity(--threshold): %v", err)
	}
	for _, e := range result.Entries {
		if e.Score < 5 {
			t.Errorf("entry %s score %.1f below threshold", e.Name, e.Score)
		}
	}
}

// ---------------------------------------------------------------------------
// analyze exhaustiveness

const exhaustivenessSource = `type Shape =
	| { kind: "circle"; r: number }
	| { kind: "square"; s: number }
	| { kind: "triangle"; b: number };

export function area(sh: Shape): number {
	switch (sh.kind) {
		case "circle":
			return 1;
		case "square":
			return 2;
	}
	return 0;
}

export function areaWithDefault(sh: Shape): number {
	switch (sh.kind) {
		case "circle":
			return 1;
		default:
			return 0;
	}
}

export function areaFull(sh: Shape): number {
	switch (sh.kind) {
		case "circle":
		case "square":
		case "triangle":
			return 1;
	}
	return 0;
}

export enum Color {
	Red,
	Green,
	Blue,
}

export function pick(c: Color): number {
	switch (c) {
		case Color.Red:
			return 0;
		case Color.Green:
			return 1;
	}
	return 2;
}
`

func TestAnalyzeExhaustiveness(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/shapes.ts": exhaustivenessSource})
	ctx := context.Background()

	result, err := runAnalyzeExhaustiveness(ctx, ws, &exhaustivenessFlags{}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeExhaustiveness: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(result.Rows), result.Rows)
	}

	area := result.Rows[0]
	if area.Discriminant != "sh.kind" || !slices.Equal(area.Missing, []string{`"triangle"`}) {
		t.Errorf("area row = %+v, want missing [\"triangle\"]", area)
	}
	if area.HasDefault || area.Status != "missing" {
		t.Errorf("area row = %+v, want status missing without default", area)
	}

	withDefault := result.Rows[1]
	if !withDefault.HasDefault || withDefault.Status != "covered-by-default" {
		t.Errorf("areaWithDefault row = %+v, want covered-by-default", withDefault)
	}
	if !slices.Equal(withDefault.Missing, []string{`"square"`, `"triangle"`}) {
		t.Errorf("areaWithDefault missing = %v", withDefault.Missing)
	}

	enumRow := result.Rows[2]
	if enumRow.Discriminant != "c" || !slices.Equal(enumRow.Missing, []string{"Color.Blue"}) {
		t.Errorf("enum row = %+v, want missing [Color.Blue]", enumRow)
	}

	// areaFull covers everything and must not be reported (only 3 rows above).

	// --strict reports default-covered switches as plain missing.
	result, err = runAnalyzeExhaustiveness(ctx, ws, &exhaustivenessFlags{strict: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeExhaustiveness(--strict): %v", err)
	}
	for _, row := range result.Rows {
		if row.Status != "missing" {
			t.Errorf("--strict row status = %q, want missing (%+v)", row.Status, row)
		}
	}
}
