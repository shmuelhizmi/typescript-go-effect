package effectify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/repo"
	"gotest.tools/v3/assert"
)

// TestEffectifyCorpus runs the golden corpus in
// testdata/tests/cases/effectscript/effectify: each <case>.ts must convert to
// its <case>.expected.ets, byte for byte.
//
// Set UPDATE_EFFECT_BASELINES=1 to rewrite the expected files from actual
// output.
func TestEffectifyCorpus(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repo.TestDataPath(), "tests", "cases", "effectscript", "effectify")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("corpus not found: %v", err)
	}
	update := os.Getenv("UPDATE_EFFECT_BASELINES") != ""

	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, ".expected.") || !strings.HasSuffix(name, ".ts") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(filepath.Join(dir, name))
			assert.NilError(t, err)

			result := Effectify("/"+name, string(src), Options{})
			assert.Equal(t, result.SkipReason, "", "skip detail: %s", result.Detail)
			assert.Assert(t, result.Converted, "expected at least one conversion")

			expectedPath := filepath.Join(dir, strings.TrimSuffix(name, ".ts")+".expected.ets")
			if update {
				assert.NilError(t, os.WriteFile(expectedPath, []byte(result.Output), 0o644))
				return
			}
			expected, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("no expected file yet (run with UPDATE_EFFECT_BASELINES=1): %v", err)
			}
			assert.Equal(t, result.Output, string(expected))
		})
	}
}

// TestEffectifyEmitCorpusRoundTrip feeds the forward corpus's desugared
// outputs (emit/<case>.expected.ts) back through Effectify and checks that
// the result is desugar-equivalent to the original <case>.ets source. This
// holds regardless of which reverse patterns are implemented: unconverted
// regions are already in desugared form.
func TestEffectifyEmitCorpusRoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repo.TestDataPath(), "tests", "cases", "effectscript", "emit")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("emit corpus not found: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".expected.ts") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(filepath.Join(dir, name))
			assert.NilError(t, err)
			etsSrc, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(name, ".expected.ts")+".ets"))
			assert.NilError(t, err)

			result := Effectify("/"+name, string(src), Options{})
			assert.Equal(t, result.SkipReason, "", "skip detail: %s; stats: %s", result.Detail, result.Stats.String())

			converted := parseFile("/converted.ets", result.Output, core.ScriptKindETS)
			assert.Equal(t, len(converted.Diagnostics()), 0, "converted output has parse diagnostics")
			original := parseFile("/original.ets", string(etsSrc), core.ScriptKindETS)
			assert.Equal(t, len(original.Diagnostics()), 0, "original .ets has parse diagnostics")
			// Same canonicalization as the in-package verifier: |> desugars
			// to nested calls while hand-written chains use .pipe — flatten
			// both before comparing.
			convertedFlat := parseFile("/c.ts", flattenPipes("/c.ts", printNormalized(converted)), core.ScriptKindTS)
			originalFlat := parseFile("/o.ts", flattenPipes("/o.ts", printNormalized(original)), core.ScriptKindTS)
			assert.Assert(t, equalModuloParens(convertedFlat.AsNode(), originalFlat.AsNode()),
				"effectified output is not desugar-equivalent to the original .ets\n--- effectified ---\n%s\n--- desugared(effectified) ---\n%s\n--- desugared(original) ---\n%s",
				result.Output, printNormalized(converted), printNormalized(original))
		})
	}
}

// TestEffectifySkips checks the negative paths: ineligible or hazardous
// inputs come back untouched with the right reason.
func TestEffectifySkips(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		src    string
		reason string
	}{
		{
			name:   "no effect import",
			src:    "const x = Effect.gen(function* () { return 1; });\n",
			reason: SkipNoEffectImport,
		},
		{
			name:   "namespace import only",
			src:    "import * as Eff from \"effect\";\nconst x = Eff.Effect.gen(function* () { return 1; });\n",
			reason: SkipNamespaceImport,
		},
		{
			name:   "aliased helper import",
			src:    "import { Effect as E } from \"effect\";\nconst x = E.gen(function* () { return 1; });\n",
			reason: SkipAliasedImport,
		},
		{
			name:   "helper shadowed",
			src:    "import { Effect } from \"effect\";\nfunction f(Effect: unknown) { return Effect; }\nconst x = Effect.gen(function* () { return 1; });\n",
			reason: SkipHelperShadowed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := Effectify("/case.ts", tc.src, Options{})
			assert.Equal(t, result.SkipReason, tc.reason)
			assert.Equal(t, result.Output, tc.src)
			assert.Assert(t, !result.Converted)
		})
	}
}

// TestEffectifyLeavesHazardsVerbatim checks per-generator bails: a generator
// whose body collides with contextual keywords stays in classic form while
// the rest of the file still converts.
func TestEffectifyLeavesHazardsVerbatim(t *testing.T) {
	t.Parallel()
	src := `import { Effect } from "effect";
declare const ea: Effect.Effect<number>;
declare function fork(n: number): number;
const hazard = Effect.gen(function* () {
  const x = yield* ea;
  return fork(x);
});
const clean = Effect.gen(function* () {
  const x = yield* ea;
  return x;
});
`
	result := Effectify("/case.ts", src, Options{})
	assert.Equal(t, result.SkipReason, "", "detail: %s", result.Detail)
	assert.Assert(t, result.Converted)
	assert.Assert(t, strings.Contains(result.Output, "const hazard = Effect.gen(function* () {"),
		"hazardous generator should stay verbatim:\n%s", result.Output)
	assert.Assert(t, strings.Contains(result.Output, "const clean = effect {"),
		"clean generator should convert:\n%s", result.Output)
}
