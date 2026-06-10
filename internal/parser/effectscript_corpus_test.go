package parser_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/parser"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/repo"
	"github.com/microsoft/typescript-go/internal/tspath"
	"gotest.tools/v3/assert"
)

// TestEffectScriptCorpus runs the golden corpus in
// testdata/tests/cases/effectscript/emit: each <case>.ets[x] must lower to
// the same AST as its <case>.expected.ts[x] (comparison is print-normalized,
// so formatting differences don't matter, structure does).
//
// Set UPDATE_EFFECT_BASELINES=1 to rewrite the expected files from actual
// output.
func TestEffectScriptCorpus(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repo.TestDataPath(), "tests", "cases", "effectscript", "emit")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("corpus not found: %v", err)
	}
	update := os.Getenv("UPDATE_EFFECT_BASELINES") != ""

	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, ".expected.") || (!strings.HasSuffix(name, ".ets") && !strings.HasSuffix(name, ".etsx")) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srcPath := filepath.Join(dir, name)
			src, err := os.ReadFile(srcPath)
			assert.NilError(t, err)

			scriptKind := core.ScriptKindETS
			expectedExt := ".expected.ts"
			base := strings.TrimSuffix(name, ".ets")
			if strings.HasSuffix(name, ".etsx") {
				scriptKind = core.ScriptKindETSX
				expectedExt = ".expected.tsx"
				base = strings.TrimSuffix(name, ".etsx")
			}

			actual := printNormalized(t, "/"+name, string(src), scriptKind)

			expectedPath := filepath.Join(dir, base+expectedExt)
			if update {
				assert.NilError(t, os.WriteFile(expectedPath, []byte(actual), 0o644))
				return
			}
			expectedSrc, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Skipf("no expected file yet: %v", err)
			}
			tsKind := core.ScriptKindTS
			if scriptKind == core.ScriptKindETSX {
				tsKind = core.ScriptKindTSX
			}
			expected := printNormalized(t, "/expected.ts", string(expectedSrc), tsKind)
			assert.Equal(t, actual, expected)
		})
	}
}

func printNormalized(t *testing.T, fileName string, sourceText string, scriptKind core.ScriptKind) string {
	t.Helper()
	opts := ast.SourceFileParseOptions{FileName: fileName, Path: tspath.Path(fileName)}
	file := parser.ParseSourceFile(opts, sourceText, scriptKind)
	for _, d := range file.Diagnostics() {
		t.Errorf("parse diagnostic in %s at %d: TS%d", fileName, d.Pos(), d.Code())
	}
	ec := printer.NewEmitContext()
	pr := printer.NewPrinter(printer.PrinterOptions{}, printer.PrintHandlers{}, ec)
	return pr.EmitSourceFile(file)
}
