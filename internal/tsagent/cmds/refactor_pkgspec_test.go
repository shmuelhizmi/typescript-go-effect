package cmds

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Package-specifier preservation for consumer retargets (round-3 FIX):
// consumers that imported the moved symbol through a bare package specifier
// (a package.json exports subpath) are retargeted to the destination's
// package specifier instead of a deep relative path that escapes their own
// workspace package.

func TestRefactorExportsSubpath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		exports string // package.json "exports" value, JSON
		relDest string
		want    string
		ok      bool
	}{
		{"string target is the dot subpath", `"./src/main.ts"`, "src/main.ts", ".", true},
		{"string target mismatch", `"./src/main.ts"`, "src/other.ts", "", false},
		{"subpath with string target", `{"./promise": "./src/promise.utils.ts"}`, "src/promise.utils.ts", "./promise", true},
		{"explicit dot subpath", `{".": "./src/index.ts"}`, "src/index.ts", ".", true},
		{"conditional object target", `{"./sub": {"types": "./src/sub.types.ts", "import": "./src/sub.ts"}}`, "src/sub.ts", "./sub", true},
		{"conditional bun target", `{"./sub": {"bun": "./src/sub.bun.ts"}}`, "src/sub.bun.ts", "./sub", true},
		{"nested conditional target", `{"./sub": {"import": {"default": "./src/sub.ts"}}}`, "src/sub.ts", "./sub", true},
		{"unknown condition keys only", `{"./sub": {"deno": "./src/sub.ts"}}`, "src/sub.ts", "", false},
		{"conditions object for the dot subpath", `{"import": "./src/index.ts", "types": "./src/index.d.ts"}`, "src/index.ts", ".", true},
		{"wildcard pattern", `{"./*": "./src/*.ts"}`, "src/concurrent.utils.ts", "./concurrent.utils", true},
		{"wildcard with subpath prefix and target suffix", `{"./utils/*": "./src/*.utils.ts"}`, "src/promise.utils.ts", "./utils/promise", true},
		{"wildcard mismatch", `{"./*": "./src/*.ts"}`, "lib/x.ts", "", false},
		{"exact subpath wins over wildcard", `{"./*": "./src/*.ts", "./promise": "./src/promise.ts"}`, "src/promise.ts", "./promise", true},
		{"array fallback target", `{"./sub": ["./missing.ts", "./src/sub.ts"]}`, "src/sub.ts", "./sub", true},
		{"starred target under exact subpath is not substitutable", `{"./sub": "./src/*.ts"}`, "src/x.ts", "", false},
		{"null exports", `null`, "src/x.ts", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var exports any
			if err := json.Unmarshal([]byte(tc.exports), &exports); err != nil {
				t.Fatalf("bad exports fixture: %v", err)
			}
			got, ok := refactorExportsSubpath(exports, tc.relDest)
			if got != tc.want || ok != tc.ok {
				t.Errorf("refactorExportsSubpath(%s, %q) = (%q, %v), want (%q, %v)", tc.exports, tc.relDest, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRefactorFindDestPackage(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/dummy.ts":               "export const d = 1;\n",
		"/project/packages/a/package.json":    `{"name": "@x/a", "exports": {"./util": "./src/util.ts"}}`,
		"/project/packages/b/package.json":    `{"name": "@x/b", "main": "./src/index.ts"}`,
		"/project/packages/c/package.json":    `{"name": "@x/c", "exports": {"./other": "./src/other.ts"}}`,
		"/project/packages/d/package.json":    `{"name": "@x/d", "module": "src/index.ts"}`,
		"/project/packages/anon/package.json": `{"private": true}`,
	})
	cases := []struct {
		name     string
		destAbs  string
		found    bool
		wantName string
		wantSpec string
	}{
		{"exports subpath", "/project/packages/a/src/util.ts", true, "@x/a", "@x/a/util"},
		{"main maps the dest (no exports)", "/project/packages/b/src/index.ts", true, "@x/b", "@x/b"},
		{"module maps the dest (no exports)", "/project/packages/d/src/index.ts", true, "@x/d", "@x/d"},
		{"no mapping leaves spec empty", "/project/packages/c/src/util.ts", true, "@x/c", ""},
		{"nameless package.json is skipped", "/project/packages/anon/src/x.ts", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg, found := refactorFindDestPackage(ws, tc.destAbs)
			if found != tc.found || pkg.name != tc.wantName || pkg.spec != tc.wantSpec {
				t.Errorf("refactorFindDestPackage(%q) = (%+v, %v), want name %q spec %q found %v",
					tc.destAbs, pkg, found, tc.wantName, tc.wantSpec, tc.found)
			}
		})
	}
}

// monorepoFiles builds the shared two-package fixture: @play/utils with an
// exports map (overridable) and an app package consuming the moved symbol.
func monorepoFiles(pkgJSON string, extra map[string]any) map[string]any {
	files := map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "module": "preserve", "moduleResolution": "bundler",
			"paths": {"@play/utils/promise": ["./packages/utils/src/promise.utils.ts"], "@play/utils/concurrent": ["./packages/utils/src/concurrent.utils.ts"]}}}`,
		"/project/packages/utils/package.json":            pkgJSON,
		"/project/packages/utils/src/promise.utils.ts":    "export function moveMe(): number {\n\treturn 1;\n}\nexport const keep = 2;\n",
		"/project/packages/utils/src/concurrent.utils.ts": "export const unrelated = 0;\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

func TestRefactorMvSymbolCrossPackageConsumerGetsPackageSpecifier(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, monorepoFiles(
		`{"name": "@play/utils", "exports": {"./promise": "./src/promise.utils.ts", "./concurrent": "./src/concurrent.utils.ts"}}`,
		map[string]any{
			// Outside the destination package, original specifier bare.
			"/project/packages/app/src/main.ts": "import { moveMe } from \"@play/utils/promise\";\nexport const x = moveMe();\n",
			// Inside the destination package: relative original AND bare original both stay relative.
			"/project/packages/utils/src/inside.ts": "import { moveMe } from \"./promise.utils\";\nexport const y = moveMe();\n",
			"/project/packages/utils/src/self.ts":   "import { moveMe } from \"@play/utils/promise\";\nexport const z = moveMe();\n",
		}))
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
		tx:     refactorTxFlags{apply: true},
		to:     "packages/utils/src/concurrent.utils.ts",
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	main := readWorkspaceFile(t, ws, "/project/packages/app/src/main.ts")
	if !strings.Contains(main, "import { moveMe } from \"@play/utils/concurrent\";") {
		t.Errorf("the cross-package consumer should get the exports subpath specifier: %q", main)
	}
	inside := readWorkspaceFile(t, ws, "/project/packages/utils/src/inside.ts")
	if !strings.Contains(inside, "import { moveMe } from \"./concurrent.utils\";") {
		t.Errorf("the same-package relative consumer must stay relative: %q", inside)
	}
	self := readWorkspaceFile(t, ws, "/project/packages/utils/src/self.ts")
	if !strings.Contains(self, "import { moveMe } from \"./concurrent.utils\";") {
		t.Errorf("a consumer inside the destination package keeps a relative specifier: %q", self)
	}
	for _, note := range result.Notes {
		if strings.Contains(note, "package boundary") {
			t.Errorf("no boundary-crossing note expected when the exports subpath maps: %q", note)
		}
	}
}

func TestRefactorMvSymbolCrossPackageReexportGetsPackageSpecifier(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, monorepoFiles(
		`{"name": "@play/utils", "exports": {"./promise": "./src/promise.utils.ts", "./concurrent": "./src/concurrent.utils.ts"}}`,
		map[string]any{
			"/project/packages/app/src/barrel.ts": "export { moveMe } from \"@play/utils/promise\";\n",
			"/project/packages/app/src/main.ts":   "import { moveMe } from \"./barrel\";\nexport const x = moveMe();\n",
		}))
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
		tx:     refactorTxFlags{apply: true},
		to:     "packages/utils/src/concurrent.utils.ts",
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	barrel := readWorkspaceFile(t, ws, "/project/packages/app/src/barrel.ts")
	if barrel != "export { moveMe } from \"@play/utils/concurrent\";\n" {
		t.Errorf("the cross-package re-export should retarget to the exports subpath specifier: %q", barrel)
	}
}

func TestRefactorMvSymbolCrossPackageNoExportsMappingFallsBackWithNote(t *testing.T) {
	t.Parallel()
	// The destination file is NOT reachable through any exports subpath: the
	// consumer gets the relative fallback and the result carries a note.
	ws := newTestWorkspace(t, monorepoFiles(
		`{"name": "@play/utils", "exports": {"./promise": "./src/promise.utils.ts"}}`,
		map[string]any{
			"/project/packages/app/src/main.ts": "import { moveMe } from \"@play/utils/promise\";\nexport const x = moveMe();\n",
		}))
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
		tx:     refactorTxFlags{apply: true},
		to:     "packages/utils/src/concurrent.utils.ts",
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	main := readWorkspaceFile(t, ws, "/project/packages/app/src/main.ts")
	if !strings.Contains(main, "import { moveMe } from \"../../utils/src/concurrent.utils\";") {
		t.Errorf("without an exports mapping the consumer falls back to the relative path: %q", main)
	}
	want := `rewrote "moveMe" consumers with a relative path that crosses the package boundary of @play/utils (no exports subpath maps to packages/utils/src/concurrent.utils.ts)`
	found := false
	for _, note := range result.Notes {
		if note == want {
			found = true
		}
	}
	if !found {
		t.Errorf("result.Notes = %q, want the boundary-crossing note %q", result.Notes, want)
	}
}

func TestRefactorMvSymbolRelativeOriginalAcrossPackagesKeepsRelative(t *testing.T) {
	t.Parallel()
	// The consumer in another package used a RELATIVE specifier originally:
	// the rewrite keeps the relative computation even though an exports
	// subpath maps the destination.
	ws := newTestWorkspace(t, monorepoFiles(
		`{"name": "@play/utils", "exports": {"./promise": "./src/promise.utils.ts", "./concurrent": "./src/concurrent.utils.ts"}}`,
		map[string]any{
			"/project/packages/app/src/main.ts": "import { moveMe } from \"../../utils/src/promise.utils\";\nexport const x = moveMe();\n",
		}))
	f := &refactorMvSymbolFlags{
		target: refactorTargetFlags{name: "moveMe"},
		tx:     refactorTxFlags{apply: true},
		to:     "packages/utils/src/concurrent.utils.ts",
	}
	result, err := runRefactorMvSymbol(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorMvSymbol: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	main := readWorkspaceFile(t, ws, "/project/packages/app/src/main.ts")
	if !strings.Contains(main, "import { moveMe } from \"../../utils/src/concurrent.utils\";") {
		t.Errorf("a relative original keeps the relative computation: %q", main)
	}
	if len(result.Notes) != 0 {
		t.Errorf("no note expected for relative originals: %q", result.Notes)
	}
}
