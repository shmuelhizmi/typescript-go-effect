package cmds

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

func apiTestFiles() map[string]any {
	return map[string]any{
		"/project/src/index.ts": `export const count: number = 1;
export function greet(name: string): string {
	return name;
}
export interface Shape {
	area: number;
}
export { helper } from "./util";
`,
		"/project/src/util.ts": `export function helper(n: number): number {
	return n * 2;
}
`,
	}
}

func TestApiSurfaceContent(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, apiTestFiles())
	result, err := runApiSurface(context.Background(), ws, nil)
	if err != nil {
		t.Fatalf("runApiSurface: %v", err)
	}
	if !strings.HasPrefix(result.Digest, "sha256:") {
		t.Errorf("digest = %q, want sha256: prefix", result.Digest)
	}
	byName := map[string]*ApiExport{}
	for _, e := range result.Exports {
		if e.Entry != "src/index.ts" {
			t.Errorf("entry = %q, want src/index.ts", e.Entry)
		}
		byName[e.Name] = e
	}
	if len(byName) != 4 {
		t.Fatalf("exports = %+v, want count/greet/Shape/helper", result.Exports)
	}
	if e := byName["count"]; e == nil || e.Kind != "const" || e.Type != "number" {
		t.Errorf("count = %+v, want const: number", e)
	}
	if e := byName["greet"]; e == nil || e.Kind != "function" || !strings.Contains(e.Type, "(name: string) => string") {
		t.Errorf("greet = %+v", e)
	}
	if e := byName["Shape"]; e == nil || e.Kind != "interface" {
		t.Errorf("Shape = %+v, want interface", e)
	}
	// Re-export resolves through the alias for declaredAt but keeps the name.
	if e := byName["helper"]; e == nil || !strings.HasPrefix(e.DeclaredAt, "src/util.ts:") || e.Kind != "function" {
		t.Errorf("helper = %+v, want declaredAt in src/util.ts", e)
	}
	// Sorted by (entry, name).
	for i := 1; i < len(result.Exports); i++ {
		if result.Exports[i-1].Name > result.Exports[i].Name {
			t.Errorf("exports not sorted: %q > %q", result.Exports[i-1].Name, result.Exports[i].Name)
		}
	}
}

func TestApiSurfaceDeterministic(t *testing.T) {
	t.Parallel()
	run := func() []byte {
		ws := newTestWorkspace(t, apiTestFiles())
		result, err := runApiSurface(context.Background(), ws, nil)
		if err != nil {
			t.Fatalf("runApiSurface: %v", err)
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			t.Fatalf("encode: %v", err)
		}
		return buf.Bytes()
	}
	first := run()
	second := run()
	if !bytes.Equal(first, second) {
		t.Errorf("api surface output is not deterministic:\n--- first\n%s\n--- second\n%s", first, second)
	}
}

func TestApiSurfaceNoEntriesError(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/main.ts": "export const x = 1;\n",
	})
	_, err := runApiSurface(context.Background(), ws, nil)
	if err == nil {
		t.Fatal("expected an error when no index.ts/index.tsx exists")
	}
	if code := cli.ExitCode(err); code != cli.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	// Explicit entries still work.
	result, err := runApiSurface(context.Background(), ws, []string{"src/main.ts"})
	if err != nil {
		t.Fatalf("runApiSurface with explicit entry: %v", err)
	}
	if len(result.Exports) != 1 || result.Exports[0].Name != "x" {
		t.Errorf("exports = %+v, want x", result.Exports)
	}
}

func TestApiDiffClassification(t *testing.T) {
	t.Parallel()
	base := &ApiSurfaceResult{
		Digest: "sha256:base",
		Exports: []*ApiExport{
			{Entry: "src/index.ts", Name: "gone", Kind: "function", Type: "() => void"},
			{Entry: "src/index.ts", Name: "changed", Kind: "const", Type: "number"},
			{Entry: "src/index.ts", Name: "stable", Kind: "const", Type: "string"},
			{Entry: "src/index.ts", Name: "reshaped", Kind: "const", Type: "string"},
		},
	}
	head := &ApiSurfaceResult{
		Digest: "sha256:head",
		Exports: []*ApiExport{
			{Entry: "src/index.ts", Name: "changed", Kind: "const", Type: "string"},
			{Entry: "src/index.ts", Name: "stable", Kind: "const", Type: "string"},
			{Entry: "src/index.ts", Name: "reshaped", Kind: "let", Type: "string"},
			{Entry: "src/index.ts", Name: "brandNew", Kind: "function", Type: "() => number"},
		},
	}
	diff := compareSurfaces(base, head)
	if len(diff.Breaking) != 1 || diff.Breaking[0].Name != "gone" || diff.Breaking[0].Rule != "removed export" {
		t.Errorf("Breaking = %+v", diff.Breaking)
	}
	if len(diff.PossiblyBreaking) != 2 {
		t.Errorf("PossiblyBreaking = %+v, want type change + kind change", diff.PossiblyBreaking)
	} else {
		if diff.PossiblyBreaking[0].Name != "changed" || diff.PossiblyBreaking[0].BaseType != "number" || diff.PossiblyBreaking[0].HeadType != "string" {
			t.Errorf("type change = %+v", diff.PossiblyBreaking[0])
		}
		if diff.PossiblyBreaking[1].Name != "reshaped" || !strings.Contains(diff.PossiblyBreaking[1].Rule, "kind changed") {
			t.Errorf("kind change = %+v", diff.PossiblyBreaking[1])
		}
	}
	if len(diff.Additive) != 1 || diff.Additive[0].Name != "brandNew" {
		t.Errorf("Additive = %+v", diff.Additive)
	}
	if diff.DigestBase != "sha256:base" || diff.DigestHead != "sha256:head" {
		t.Errorf("digests = %q / %q", diff.DigestBase, diff.DigestHead)
	}
}

func TestApiDiffGitIntegration(t *testing.T) {
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
	write("src/index.ts", "export const removedLater: number = 1;\nexport function kept(x: string): string { return x; }\n")
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "v1")

	// Head: remove an export, change a signature, add an export.
	write("src/index.ts", "export function kept(x: number): string { return String(x); }\nexport const added = true;\n")

	ws, err := core.NewWorkspace(core.Options{
		Project:        dir,
		Cwd:            dir,
		FS:             bundled.WrapFS(osvfs.FS()),
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	diff, err := runApiDiff(context.Background(), ws, &apiDiffFlags{base: "HEAD"}, nil)
	if err != nil {
		t.Fatalf("runApiDiff: %v", err)
	}
	if len(diff.Breaking) != 1 || diff.Breaking[0].Name != "removedLater" {
		t.Errorf("Breaking = %+v, want removedLater", diff.Breaking)
	}
	if len(diff.PossiblyBreaking) != 1 || diff.PossiblyBreaking[0].Name != "kept" {
		t.Errorf("PossiblyBreaking = %+v, want kept", diff.PossiblyBreaking)
	}
	if len(diff.Additive) != 1 || diff.Additive[0].Name != "added" {
		t.Errorf("Additive = %+v, want added", diff.Additive)
	}
	if diff.DigestBase == diff.DigestHead {
		t.Errorf("digests should differ: %q", diff.DigestBase)
	}

	// --fail-on breaking returns the result plus an exit-1 error.
	result, err := runApiDiff(context.Background(), ws, &apiDiffFlags{base: "HEAD", failOn: "breaking"}, nil)
	if err == nil {
		t.Fatal("expected --fail-on breaking to error")
	}
	if code := cli.ExitCode(err); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d", code, cli.ExitFailed)
	}
	if result == nil || len(result.Breaking) != 1 {
		t.Errorf("result = %+v, want the diff alongside the error", result)
	}
}
