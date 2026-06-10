package cmds

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

func TestContextTokenEstimator(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("x", 400), 100},
		{strings.Repeat("x", 401), 101},
	}
	for _, c := range cases {
		if got := estimateTokens(c.in); got != c.want {
			t.Errorf("estimateTokens(%d bytes) = %d, want %d", len(c.in), got, c.want)
		}
	}
}

func contextPackTestFiles() map[string]any {
	return map[string]any{
		"/project/src/types.ts": `/** Options doc */
export interface Options {
	verbose: boolean;
	depth: number;
}
`,
		"/project/src/main.ts": `import { Options } from "./types";

/** configure doc */
export function configure(opts: Options): Options {
	return opts;
}
`,
		"/project/src/use.ts": `import { configure } from "./main";

export const result = configure({ verbose: true, depth: 1 });
`,
	}
}

func sliceByKind(slices []*ContextSlice, kind string) []*ContextSlice {
	var out []*ContextSlice
	for _, s := range slices {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

func TestContextPackIncludesTypeDepAndUsage(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, contextPackTestFiles())
	result, err := runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   defaultPackTokenBudget,
		usageExamples: defaultUsageExamples,
	}, []string{"configure"})
	if err != nil {
		t.Fatalf("runContextPack: %v", err)
	}

	targets := sliceByKind(result.Slices, "target")
	if len(targets) != 1 || targets[0].Name != "configure" || targets[0].File != "src/main.ts" {
		t.Fatalf("targets = %+v, want configure in src/main.ts", targets)
	}
	if !strings.Contains(targets[0].Source, "export function configure") || !strings.Contains(targets[0].Source, "/** configure doc */") {
		t.Errorf("target source missing declaration or JSDoc:\n%s", targets[0].Source)
	}

	deps := sliceByKind(result.Slices, "type-dep")
	if len(deps) != 1 || deps[0].Name != "Options" || deps[0].File != "src/types.ts" {
		t.Fatalf("type-deps = %+v, want Options from src/types.ts", deps)
	}
	if !strings.Contains(deps[0].Source, "export interface Options") {
		t.Errorf("type-dep source = %q", deps[0].Source)
	}

	usages := sliceByKind(result.Slices, "usage")
	if len(usages) != 1 || usages[0].File != "src/use.ts" {
		t.Fatalf("usages = %+v, want one from src/use.ts", usages)
	}
	if !strings.Contains(usages[0].Source, "configure({ verbose: true") {
		t.Errorf("usage source = %q", usages[0].Source)
	}

	// Default order: targets, then type-deps, then usages.
	if result.Slices[0].Kind != "target" || result.Slices[1].Kind != "type-dep" || result.Slices[2].Kind != "usage" {
		t.Errorf("default slice order = %v", []string{result.Slices[0].Kind, result.Slices[1].Kind, result.Slices[2].Kind})
	}

	// Manifest mirrors the slices; estimatedTokens is the sum.
	if len(result.Included) != len(result.Slices) {
		t.Errorf("included = %d entries, want %d", len(result.Included), len(result.Slices))
	}
	sum := 0
	for _, s := range result.Slices {
		sum += s.EstTokens
	}
	if result.EstimatedTokens != sum || sum == 0 {
		t.Errorf("estimatedTokens = %d, want %d", result.EstimatedTokens, sum)
	}
	if len(result.Elided) != 0 {
		t.Errorf("elided = %+v, want none", result.Elided)
	}
}

func TestContextPackBudgetElision(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, contextPackTestFiles())
	full, err := runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   defaultPackTokenBudget,
		usageExamples: defaultUsageExamples,
	}, []string{"configure"})
	if err != nil {
		t.Fatalf("runContextPack (full): %v", err)
	}
	targetTokens := sliceByKind(full.Slices, "target")[0].EstTokens
	depTokens := sliceByKind(full.Slices, "type-dep")[0].EstTokens

	// Budget fits the target plus the type-dep but not the usage.
	result, err := runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   targetTokens + depTokens,
		usageExamples: defaultUsageExamples,
	}, []string{"configure"})
	if err != nil {
		t.Fatalf("runContextPack (mid budget): %v", err)
	}
	if len(sliceByKind(result.Slices, "usage")) != 0 {
		t.Errorf("usage should be elided at budget %d: %+v", targetTokens+depTokens, result.Slices)
	}
	if len(sliceByKind(result.Slices, "type-dep")) != 1 {
		t.Errorf("type-dep should fit at budget %d", targetTokens+depTokens)
	}
	if len(result.Elided) != 1 || result.Elided[0].Name != "configure" {
		t.Errorf("elided = %+v, want the usage example (named after the target)", result.Elided)
	}

	// Budget fits only the target: both P2 and P3 are elided, listed in the
	// manifest.
	result, err = runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   targetTokens,
		usageExamples: defaultUsageExamples,
	}, []string{"configure"})
	if err != nil {
		t.Fatalf("runContextPack (target budget): %v", err)
	}
	if len(result.Slices) != 1 || result.Slices[0].Kind != "target" {
		t.Errorf("slices = %+v, want only the target", result.Slices)
	}
	if len(result.Elided) != 2 {
		t.Errorf("elided = %+v, want type-dep + usage", result.Elided)
	}
	if result.Note != "" {
		t.Errorf("note = %q, want empty (target fits)", result.Note)
	}

	// Budget smaller than the target: target still included, with a note.
	result, err = runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   1,
		usageExamples: defaultUsageExamples,
	}, []string{"configure"})
	if err != nil {
		t.Fatalf("runContextPack (tiny budget): %v", err)
	}
	if len(sliceByKind(result.Slices, "target")) != 1 {
		t.Errorf("target must always be included: %+v", result.Slices)
	}
	if result.Note == "" {
		t.Error("note should explain that mandatory slices exceed the budget")
	}
}

func TestContextPackForEditOrdering(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, contextPackTestFiles())
	result, err := runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   defaultPackTokenBudget,
		usageExamples: defaultUsageExamples,
		forMode:       "edit",
	}, []string{"configure"})
	if err != nil {
		t.Fatalf("runContextPack: %v", err)
	}
	if len(result.Slices) != 3 {
		t.Fatalf("slices = %+v, want 3", result.Slices)
	}
	// edit: usages before type-deps.
	if result.Slices[1].Kind != "usage" || result.Slices[2].Kind != "type-dep" {
		t.Errorf("--for edit order = %v, want target, usage, type-dep",
			[]string{result.Slices[0].Kind, result.Slices[1].Kind, result.Slices[2].Kind})
	}
}

func TestContextPackBigTypeDepHeaderOnly(t *testing.T) {
	t.Parallel()
	var big strings.Builder
	big.WriteString("export interface Big {\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&big, "\tfield%d: number;\n", i)
	}
	big.WriteString("}\n")
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/big.ts":  big.String(),
		"/project/src/main.ts": `import { Big } from "./big";` + "\n" + `export function useBig(b: Big): number { return b.field0; }` + "\n",
	})
	result, err := runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   100000,
		usageExamples: 0,
	}, []string{"useBig"})
	if err != nil {
		t.Fatalf("runContextPack: %v", err)
	}
	deps := sliceByKind(result.Slices, "type-dep")
	if len(deps) != 1 || deps[0].Name != "Big" {
		t.Fatalf("type-deps = %+v, want Big", deps)
	}
	if !strings.Contains(deps[0].Source, "export interface Big {") ||
		!strings.Contains(deps[0].Source, "field0: number;") ||
		!strings.Contains(deps[0].Source, "lines elided]") {
		t.Errorf("big type-dep should be header + member signatures + elision marker:\n%s", deps[0].Source)
	}
	if deps[0].Note == "" {
		t.Error("big type-dep slice should carry an elision note")
	}
}

func TestContextPackTextHeaders(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, contextPackTestFiles())
	result, err := runContextPack(context.Background(), ws, &contextPackFlags{
		tokenBudget:   defaultPackTokenBudget,
		usageExamples: defaultUsageExamples,
	}, []string{"configure"})
	if err != nil {
		t.Fatalf("runContextPack: %v", err)
	}
	var buf bytes.Buffer
	if err := result.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	text := buf.String()
	headerRE := regexp.MustCompile(`(?m)^=== src/main\.ts \(lines \d+-\d+\) ===$`)
	if !headerRE.MatchString(text) {
		t.Errorf("text output missing `=== path (lines a-b) ===` header:\n%s", text)
	}
	if !regexp.MustCompile(`(?m)^=== src/types\.ts \(lines \d+-\d+\) ===$`).MatchString(text) {
		t.Errorf("text output missing type-dep header:\n%s", text)
	}
	if strings.Contains(text, "```") {
		t.Error("text output must not contain code fences")
	}
	if !strings.Contains(text, "estimated tokens") {
		t.Errorf("text output missing token footer:\n%s", text)
	}
}

func contextExpandTestFiles() map[string]any {
	return map[string]any{
		"/project/src/exp.ts": `export interface Cfg {
	name: string;
}

export function run(cfg: Cfg): string {
	const label = cfg.name;
	return label;
}
`,
	}
}

func TestContextExpandSemantic(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, contextExpandTestFiles())
	// Position inside the function body (line 6 is `const label = cfg.name;`).
	result, err := runContextExpand(context.Background(), ws, &contextExpandFlags{
		radius:      "semantic",
		tokenBudget: defaultExpandTokenBudget,
	}, []string{"src/exp.ts:6:8"})
	if err != nil {
		t.Fatalf("runContextExpand: %v", err)
	}
	if len(result.Slices) < 1 || result.Slices[0].Kind != "primary" {
		t.Fatalf("slices = %+v, want primary first", result.Slices)
	}
	primary := result.Slices[0]
	if primary.StartLine != 5 || primary.EndLine != 8 {
		t.Errorf("primary lines = %d-%d, want 5-8 (the whole function)", primary.StartLine, primary.EndLine)
	}
	if !strings.Contains(primary.Source, "export function run(cfg: Cfg): string {") ||
		!strings.Contains(primary.Source, "return label;") {
		t.Errorf("primary source = %q", primary.Source)
	}
	deps := sliceByKind(result.Slices, "type-dep")
	if len(deps) != 1 || deps[0].Name != "Cfg" {
		t.Fatalf("type-deps = %+v, want the Cfg interface", deps)
	}
	if !strings.Contains(deps[0].Source, "export interface Cfg") {
		t.Errorf("Cfg source = %q", deps[0].Source)
	}
}

func TestContextExpandNumericRadius(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, contextExpandTestFiles())
	result, err := runContextExpand(context.Background(), ws, &contextExpandFlags{
		radius:      "1",
		tokenBudget: defaultExpandTokenBudget,
	}, []string{"src/exp.ts:6"})
	if err != nil {
		t.Fatalf("runContextExpand: %v", err)
	}
	primary := result.Slices[0]
	if primary.StartLine != 5 || primary.EndLine != 7 {
		t.Errorf("primary lines = %d-%d, want 5-7 (±1 around line 6)", primary.StartLine, primary.EndLine)
	}
	if !strings.Contains(primary.Source, "const label = cfg.name;") {
		t.Errorf("primary source = %q", primary.Source)
	}

	// Clamping: radius larger than the file.
	result, err = runContextExpand(context.Background(), ws, &contextExpandFlags{
		radius:      "100",
		tokenBudget: defaultExpandTokenBudget,
	}, []string{"src/exp.ts:6"})
	if err != nil {
		t.Fatalf("runContextExpand (clamped): %v", err)
	}
	primary = result.Slices[0]
	if primary.StartLine != 1 {
		t.Errorf("clamped startLine = %d, want 1", primary.StartLine)
	}
}

// newGitProjectWorkspace creates an on-disk project inside a fresh git repo
// with one initial commit, returning the directory, a git runner, and a file
// writer. Skips when git is unavailable.
func newGitProjectWorkspace(t *testing.T, files map[string]string) (dir string, git func(args ...string), write func(rel string, content string)) {
	t.Helper()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	dir = t.TempDir()
	dir, err := filepath.EvalSymlinks(dir) // macOS /var → /private/var
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	write = func(rel string, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	git = func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for rel, content := range files {
		write(rel, content)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "v1")
	return dir, git, write
}

func TestContextDeltaGitIntegration(t *testing.T) {
	t.Parallel()
	dir, _, write := newGitProjectWorkspace(t, map[string]string{
		"tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext"}}`,
		"src/util.ts":   "export function helper(n: number): number {\n\treturn n;\n}\n",
		"src/main.ts":   "import { helper } from \"./util\";\n\nexport function run(): number {\n\treturn helper(1);\n}\n",
		"src/extra.ts":  "export const gone = 1;\n",
	})

	// Change helper's body and delete extra.ts.
	write("src/util.ts", "export function helper(n: number): number {\n\treturn n * 2;\n}\n")
	if err := os.Remove(filepath.Join(dir, "src/extra.ts")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	ws, err := core.NewWorkspace(core.Options{
		Project:        dir,
		Cwd:            dir,
		FS:             bundled.WrapFS(osvfs.FS()),
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	result, err := runContextDelta(context.Background(), ws, &contextDeltaFlags{
		since:         "HEAD",
		maxDependents: defaultMaxDependents,
	}, nil)
	if err != nil {
		t.Fatalf("runContextDelta: %v", err)
	}

	if len(result.ChangedSymbols) != 1 {
		t.Fatalf("changedSymbols = %+v, want exactly helper", result.ChangedSymbols)
	}
	cs := result.ChangedSymbols[0]
	if cs.Name != "helper" || cs.File != "src/util.ts" || cs.Kind != "function" {
		t.Errorf("changed symbol = %+v", cs)
	}
	if !strings.Contains(cs.Source, "return n * 2;") {
		t.Errorf("changed symbol source should be the CURRENT source: %q", cs.Source)
	}
	if cs.EstTokens == 0 || result.EstimatedTokens == 0 {
		t.Errorf("estTokens = %d / %d, want > 0", cs.EstTokens, result.EstimatedTokens)
	}

	if len(result.Dependents) != 1 {
		t.Fatalf("dependents = %+v, want main.ts", result.Dependents)
	}
	dep := result.Dependents[0]
	if dep.File != "src/main.ts" || dep.Symbol != "helper" {
		t.Errorf("dependent = %+v", dep)
	}
	found := false
	for _, name := range dep.ReferencesChanged {
		if name == "run" {
			found = true
		}
	}
	if !found {
		t.Errorf("referencesChanged = %v, want to contain run", dep.ReferencesChanged)
	}

	if len(result.RemovedFiles) != 1 || result.RemovedFiles[0] != "src/extra.ts" {
		t.Errorf("removedFiles = %v, want [src/extra.ts]", result.RemovedFiles)
	}

	// Text output: paste-ready review context with headers and dependents.
	var buf bytes.Buffer
	if err := result.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	text := buf.String()
	if !regexp.MustCompile(`(?m)^=== src/util\.ts \(lines \d+-\d+\) ===$`).MatchString(text) {
		t.Errorf("delta text missing file header:\n%s", text)
	}
	if !strings.Contains(text, "Dependents:") || !strings.Contains(text, "src/main.ts: run (uses helper)") {
		t.Errorf("delta text missing dependents:\n%s", text)
	}
	if !strings.Contains(text, "src/extra.ts") {
		t.Errorf("delta text missing removed file:\n%s", text)
	}
}

func TestContextDeltaNotAGitRepo(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{"compilerOptions": {"strict": true}}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ws, err := core.NewWorkspace(core.Options{
		Project:        dir,
		Cwd:            dir,
		FS:             bundled.WrapFS(osvfs.FS()),
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	// Guard against the temp dir unexpectedly living inside a repository.
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
		t.Skipf("temp dir is inside a git work tree (%s); cannot test the no-repo path", strings.TrimSpace(string(out)))
	}

	_, err = runContextDelta(context.Background(), ws, &contextDeltaFlags{since: "HEAD", maxDependents: defaultMaxDependents}, nil)
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

func TestContextDeltaParseDiffRanges(t *testing.T) {
	t.Parallel()
	diff := `diff --git a/src/util.ts b/src/util.ts
index 1111111..2222222 100644
--- a/src/util.ts
+++ b/src/util.ts
@@ -2,1 +2,2 @@ export function helper
-	return n;
+	const m = n * 2;
+	return m;
diff --git a/src/extra.ts b/src/extra.ts
deleted file mode 100644
--- a/src/extra.ts
+++ /dev/null
@@ -1,1 +0,0 @@
-export const gone = 1;
diff --git a/src/new.ts b/src/new.ts
new file mode 100644
--- /dev/null
+++ b/src/new.ts
@@ -0,0 +1,2 @@
+export const fresh = 1;
+export const fresher = 2;
`
	changes := parseDiffNewLineRanges(diff)
	if len(changes) != 3 {
		t.Fatalf("changes = %+v, want 3 files", changes)
	}
	util := changes[0]
	if util.path != "src/util.ts" || util.isNew || util.isDelete {
		t.Errorf("util = %+v", util)
	}
	if len(util.ranges) != 1 || util.ranges[0] != [2]int{2, 3} {
		t.Errorf("util ranges = %v, want [[2,3]]", util.ranges)
	}
	extra := changes[1]
	if !extra.isDelete || extra.oldPath != "src/extra.ts" {
		t.Errorf("extra = %+v, want a deletion of src/extra.ts", extra)
	}
	fresh := changes[2]
	if !fresh.isNew || fresh.path != "src/new.ts" {
		t.Errorf("fresh = %+v, want a new src/new.ts", fresh)
	}
	if len(fresh.ranges) != 1 || fresh.ranges[0] != [2]int{1, 2} {
		t.Errorf("fresh ranges = %v, want [[1,2]]", fresh.ranges)
	}
}

func TestContextDeltaParseDiffContextLinesNotFlagged(t *testing.T) {
	t.Parallel()
	// An addition at the end of a file: the hunk header range (+1,5) covers
	// the unchanged context lines 1-3, but only the added lines 4-5 must be
	// flagged. The deleted line that looks like a `---` header must not be
	// mistaken for a new file section.
	diff := `--- a/src/types.ts
+++ b/src/types.ts
@@ -1,4 +1,5 @@
 export interface Opt {
 	deep: boolean;
 }
--- old trailing comment
+
+export function extra(): number { return 1; }
`
	changes := parseDiffNewLineRanges(diff)
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want 1 file", changes)
	}
	got := changes[0]
	if got.path != "src/types.ts" {
		t.Errorf("path = %q", got.path)
	}
	if len(got.ranges) != 1 || got.ranges[0] != [2]int{4, 5} {
		t.Errorf("ranges = %v, want [[4,5]] (context lines 1-3 unflagged)", got.ranges)
	}
}
