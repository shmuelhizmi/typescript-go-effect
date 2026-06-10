package cmds

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

// ---------------------------------------------------------------------------
// analyze duplicates

const duplicatesFile1 = `export function first(a: number, b: number): number {
	const total = a + b;
	const scaled = total * 2;
	if (scaled > 10) {
		return scaled - 1;
	}
	return scaled + 1;
}

export function second(x: number, y: number): number {
	const sum = x + y;
	const doubled = sum * 2;
	if (doubled > 10) {
		return doubled - 1;
	}
	return doubled + 1;
}

export function localTwinA(s: string): string {
	const upper = s.toUpperCase();
	const trimmed = upper.trim();
	return trimmed + upper;
}

export function localTwinB(t: string): string {
	const big = t.toUpperCase();
	const small = big.trim();
	return small + big;
}

export function different(a: number, b: number): number {
	let acc = 0;
	for (let i = a; i < b; i++) {
		acc += i;
	}
	return acc;
}
`

const duplicatesFile2 = `export function third(p: number, q: number): number {
	const combined = p + q;
	const grown = combined * 2;
	if (grown > 10) {
		return grown - 1;
	}
	return grown + 1;
}
`

func cloneMemberNames(class *CloneClass) []string {
	names := make([]string, 0, len(class.Members))
	for _, m := range class.Members {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return names
}

func TestAnalyzeDuplicates(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/dup1.ts": duplicatesFile1,
		"/project/src/dup2.ts": duplicatesFile2,
	})
	ctx := context.Background()

	result, err := runAnalyzeDuplicates(ctx, ws, &duplicatesFlags{minNodes: 10}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeDuplicates: %v", err)
	}
	if len(result.Classes) != 2 {
		t.Fatalf("classes = %d, want 2: %+v", len(result.Classes), result.Classes)
	}

	// Ranked by members × nodeCount: the 3-member cross-file class first.
	crossFile := result.Classes[0]
	if got := cloneMemberNames(crossFile); !slices.Equal(got, []string{"first", "second", "third"}) {
		t.Errorf("cross-file class members = %v, want [first second third]", got)
	}
	if crossFile.Similarity != "exact-structure" || crossFile.NodeCount < 10 {
		t.Errorf("cross-file class = %+v", crossFile)
	}
	for _, m := range crossFile.Members {
		if m.StartLine <= 0 || m.EndLine < m.StartLine {
			t.Errorf("member range = %+v", m)
		}
	}

	sameFile := result.Classes[1]
	if got := cloneMemberNames(sameFile); !slices.Equal(got, []string{"localTwinA", "localTwinB"}) {
		t.Errorf("same-file class members = %v, want [localTwinA localTwinB]", got)
	}

	// `different` must never appear in a clone class.
	for _, class := range result.Classes {
		if slices.Contains(cloneMemberNames(class), "different") {
			t.Errorf("structurally different function reported as clone: %+v", class)
		}
	}

	// --cross-file-only keeps the cross-file class and drops the local pair.
	result, err = runAnalyzeDuplicates(ctx, ws, &duplicatesFlags{minNodes: 10, crossFileOnly: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeDuplicates(--cross-file-only): %v", err)
	}
	if len(result.Classes) != 1 {
		t.Fatalf("cross-file-only classes = %d, want 1: %+v", len(result.Classes), result.Classes)
	}
	if got := cloneMemberNames(result.Classes[0]); !slices.Equal(got, []string{"first", "second", "third"}) {
		t.Errorf("cross-file-only members = %v", got)
	}

	// A large --min-nodes filters everything out.
	result, err = runAnalyzeDuplicates(ctx, ws, &duplicatesFlags{minNodes: 10000}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeDuplicates(--min-nodes): %v", err)
	}
	if len(result.Classes) != 0 {
		t.Errorf("min-nodes 10000 classes = %+v, want none", result.Classes)
	}
}

// ---------------------------------------------------------------------------
// analyze side-effects

const sideEffectsSource = `let counter = 0;

export function pureHelper(a: number, b: number): number {
	const s = a + b;
	return s * 2;
}

export function bump(): void {
	counter++;
}

export function callsBump(): number {
	bump();
	return 1;
}

export function logs(): void {
	console.log("hi");
}

export function mutatesParam(box: { value: number }): void {
	box.value = 1;
}
`

func purityByName(t *testing.T, result *SideEffectsResult, name string) *FunctionPurity {
	t.Helper()
	for _, f := range result.Functions {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("function %q not in result: %+v", name, result.Functions)
	return nil
}

func TestAnalyzeSideEffectsFunctions(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/fx.ts": sideEffectsSource})
	ctx := context.Background()

	result, err := runAnalyzeSideEffects(ctx, ws, &sideEffectsFlags{depth: 3},
		[]string{"pureHelper", "bump", "callsBump", "logs", "mutatesParam"})
	if err != nil {
		t.Fatalf("runAnalyzeSideEffects: %v", err)
	}

	pure := purityByName(t, result, "pureHelper")
	if pure.Verdict != "pure" || len(pure.Evidence) != 0 {
		t.Errorf("pureHelper = %+v, want pure with no evidence", pure)
	}

	bump := purityByName(t, result, "bump")
	if bump.Verdict != "impure" || len(bump.Evidence) == 0 || bump.Evidence[0].Kind != "mutates-nonlocal" {
		t.Errorf("bump = %+v, want impure via mutates-nonlocal", bump)
	}
	if !strings.HasPrefix(bump.Evidence[0].Loc, "src/fx.ts:") {
		t.Errorf("bump evidence loc = %q", bump.Evidence[0].Loc)
	}

	// Transitive: callsBump is impure through the call chain to bump.
	calls := purityByName(t, result, "callsBump")
	if calls.Verdict != "impure" || len(calls.Evidence) == 0 {
		t.Fatalf("callsBump = %+v, want impure", calls)
	}
	if calls.Evidence[0].Kind != "mutates-nonlocal" || !slices.Equal(calls.Evidence[0].Via, []string{"bump"}) {
		t.Errorf("callsBump evidence = %+v, want mutates-nonlocal via [bump]", calls.Evidence[0])
	}

	logs := purityByName(t, result, "logs")
	if logs.Verdict != "impure" || len(logs.Evidence) == 0 || logs.Evidence[0].Kind != "impure-builtin" {
		t.Errorf("logs = %+v, want impure via impure-builtin", logs)
	}
	if !strings.Contains(logs.Evidence[0].Detail, "console.log") {
		t.Errorf("logs evidence detail = %q", logs.Evidence[0].Detail)
	}

	param := purityByName(t, result, "mutatesParam")
	if param.Verdict != "impure" || len(param.Evidence) == 0 || param.Evidence[0].Kind != "param-mutation" {
		t.Errorf("mutatesParam = %+v, want impure via param-mutation", param)
	}

	// --depth 1 stops before bump's body: verdict degrades to unknown.
	result, err = runAnalyzeSideEffects(ctx, ws, &sideEffectsFlags{depth: 1}, []string{"callsBump"})
	if err != nil {
		t.Fatalf("runAnalyzeSideEffects(--depth 1): %v", err)
	}
	shallow := purityByName(t, result, "callsBump")
	if shallow.Verdict != "unknown" || len(shallow.Evidence) == 0 || shallow.Evidence[0].Kind != "depth-limit" {
		t.Errorf("callsBump at depth 1 = %+v, want unknown via depth-limit", shallow)
	}
}

func TestAnalyzeSideEffectsModule(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/effects.ts": "export function setup(): void {}\nsetup();\n",
		"/project/src/puremod.ts": "export const value = 1;\nexport function helper(): number {\n\treturn value;\n}\n",
		"/project/src/iife.ts":    "export const cache = (() => ({ ready: true }))();\n",
	})
	result, err := runAnalyzeSideEffects(context.Background(), ws, &sideEffectsFlags{module: true, depth: 3}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeSideEffects(--module): %v", err)
	}
	byFile := map[string]*ModulePurity{}
	for _, m := range result.Modules {
		byFile[m.File] = m
	}

	effects := byFile["src/effects.ts"]
	if effects == nil || effects.SafeToTreeShake {
		t.Fatalf("effects.ts = %+v, want not tree-shakeable", effects)
	}
	if effects.Evidence[0].Kind != "top-level-statement" || !strings.Contains(effects.Evidence[0].Detail, "setup()") {
		t.Errorf("effects.ts evidence = %+v", effects.Evidence[0])
	}

	if pure := byFile["src/puremod.ts"]; pure == nil || !pure.SafeToTreeShake || len(pure.Evidence) != 0 {
		t.Errorf("puremod.ts = %+v, want safe to tree-shake", pure)
	}

	iife := byFile["src/iife.ts"]
	if iife == nil || iife.SafeToTreeShake || iife.Evidence[0].Kind != "eager-call" {
		t.Errorf("iife.ts = %+v, want eager-call evidence", iife)
	}
}

// ---------------------------------------------------------------------------
// analyze barrel-cost

func barrelTestFiles() map[string]any {
	return map[string]any{
		"/project/src/lib/a.ts": "export function alpha(n: number): number {\n\treturn n + 1;\n}\n",
		"/project/src/lib/b.ts": "export const beta = 2;\n",
		"/project/src/lib/c.ts": "export function gamma(): string {\n\treturn \"g\";\n}\n",
		"/project/src/lib/index.ts": `export { alpha } from "./a";
export { beta } from "./b";
export * from "./c";
`,
		"/project/src/main.ts":  "import { alpha } from \"./lib/index\";\nexport const out = alpha(1);\n",
		"/project/src/star.ts":  "import { gamma } from \"./lib/index\";\nexport const g = gamma();\n",
		"/project/src/plain.ts": "export const unrelated = true;\n",
	}
}

func TestAnalyzeBarrelCost(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, barrelTestFiles())
	result, err := runAnalyzeBarrelCost(context.Background(), ws, &barrelCostFlags{minReexports: 3}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeBarrelCost: %v", err)
	}
	if len(result.Barrels) != 1 {
		t.Fatalf("barrels = %+v, want exactly src/lib/index.ts", result.Barrels)
	}
	barrel := result.Barrels[0]
	if barrel.File != "src/lib/index.ts" || barrel.Reexports != 3 {
		t.Errorf("barrel = %+v, want src/lib/index.ts with 3 re-exports", barrel)
	}
	if barrel.TransitiveFiles != 3 || barrel.TransitiveLines <= 0 {
		t.Errorf("barrel cost = %d files / %d lines, want 3 files and > 0 lines", barrel.TransitiveFiles, barrel.TransitiveLines)
	}
	if barrel.ExportedNames != 3 {
		t.Errorf("exportedNames = %d, want 3 (alpha, beta, gamma)", barrel.ExportedNames)
	}

	byFile := map[string]*BarrelImporter{}
	for _, imp := range barrel.Importers {
		byFile[imp.File] = imp
	}
	main := byFile["src/main.ts"]
	if main == nil || !slices.Equal(main.UsedNames, []string{"alpha"}) {
		t.Fatalf("main importer = %+v, want usedNames [alpha]", main)
	}
	// The suggestion points at the origin file, not the barrel.
	if !strings.Contains(main.Suggestion, "import { alpha } from \"./lib/a\";") {
		t.Errorf("main suggestion = %q, want direct import from ./lib/a", main.Suggestion)
	}
	// `export *` names resolve through the star to their origin.
	star := byFile["src/star.ts"]
	if star == nil || !strings.Contains(star.Suggestion, "import { gamma } from \"./lib/c\";") {
		t.Errorf("star importer = %+v, want direct import from ./lib/c", star)
	}
}

func TestAnalyzeBarrelCostFixPlan(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, barrelTestFiles())
	result, err := runAnalyzeBarrelCost(context.Background(), ws, &barrelCostFlags{minReexports: 3, fixPlan: true}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeBarrelCost(--fix-plan): %v", err)
	}
	if result.FixPlan == nil {
		t.Fatal("expected a fix plan")
	}
	var mainEdits *DeadCodeFixFile
	for i := range result.FixPlan.Edits {
		if result.FixPlan.Edits[i].File == "src/main.ts" {
			mainEdits = &result.FixPlan.Edits[i]
		}
	}
	if mainEdits == nil {
		t.Fatalf("no fix-plan edits for src/main.ts: %+v", result.FixPlan)
	}

	// Applying the edits must yield a valid direct import and drop the barrel.
	file, err := ws.FileOf("src/main.ts")
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	text := file.Text()
	edits := slices.Clone(mainEdits.Edits)
	slices.SortFunc(edits, func(a, b DeadCodeFixEdit) int { return b.Start - a.Start })
	for _, e := range edits {
		if e.Start < 0 || e.End > len(text) || e.Start > e.End {
			t.Fatalf("edit out of bounds: %+v", e)
		}
		text = text[:e.Start] + e.NewText + text[e.End:]
	}
	if !strings.Contains(text, "import { alpha } from \"./lib/a\";") {
		t.Errorf("patched main.ts missing direct import:\n%s", text)
	}
	if strings.Contains(text, "./lib/index") {
		t.Errorf("patched main.ts still imports the barrel:\n%s", text)
	}
	if !strings.Contains(text, "export const out = alpha(1);") {
		t.Errorf("patched main.ts lost its body:\n%s", text)
	}
}

// ---------------------------------------------------------------------------
// analyze churn-risk

func TestAnalyzeChurnRiskGit(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir) // macOS /var → /private/var
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	write := func(rel string, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	write("tsconfig.json", `{"compilerOptions": {"strict": true, "target": "esnext"}}`)
	write("src/hot.ts", "export const hotThing = 1;\n")
	write("src/main.ts", "import { hotThing } from \"./hot\";\nexport const use = hotThing + 1;\n")
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	write("src/hot.ts", "export const hotThing = 2;\n")
	git("add", ".")
	git("commit", "-q", "-m", "c2")

	ws, err := core.NewWorkspace(core.Options{
		Project:        dir,
		Cwd:            dir,
		FS:             bundled.WrapFS(osvfs.FS()),
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	result, err := runAnalyzeChurnRisk(context.Background(), ws, &churnRiskFlags{since: "10 years ago", top: 25}, nil)
	if err != nil {
		t.Fatalf("runAnalyzeChurnRisk: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatal("expected churn-risk rows")
	}
	top := result.Rows[0]
	if top.Name != "hotThing" || top.File != "src/hot.ts" {
		t.Fatalf("top row = %+v, want hotThing in src/hot.ts", top)
	}
	if top.Churn != 2 {
		t.Errorf("hotThing churn = %d, want 2 (two commits)", top.Churn)
	}
	if top.Refs < 1 {
		t.Errorf("hotThing refs = %d, want >= 1 (used by main.ts)", top.Refs)
	}
	if top.Risk <= 0 {
		t.Errorf("hotThing risk = %f, want > 0", top.Risk)
	}
	// Rows are sorted by risk descending.
	for i := 1; i < len(result.Rows); i++ {
		if result.Rows[i-1].Risk < result.Rows[i].Risk {
			t.Errorf("rows not sorted by risk: %+v", result.Rows)
		}
	}
}

func TestAnalyzeChurnRiskNoGit(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const a = 1;\n"})
	_, err := runAnalyzeChurnRisk(context.Background(), ws, &churnRiskFlags{since: "6 months ago", top: 25}, nil)
	if err == nil {
		t.Fatal("expected an error outside a git work tree")
	}
	if code := cli.ExitCode(err); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d", code, cli.ExitFailed)
	}
	if !strings.Contains(err.Error(), "git") {
		t.Errorf("error should mention git: %v", err)
	}
}
