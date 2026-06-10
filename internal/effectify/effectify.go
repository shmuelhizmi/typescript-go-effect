// Package effectify rewrites classic effect-library TypeScript into
// EffectScript (.ets) syntax — the reverse of the desugaring rules in
// docs/effectscript/TRANSPILATION.md.
//
// The rewrite is conservative: EffectScript is a strict superset of
// TypeScript, so any code shape not confidently recognized is left verbatim.
// Every conversion is verified by re-parsing the output as EffectScript and
// comparing its desugared form against the original program; a file whose
// round trip diverges is rejected wholesale.
package effectify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/parser"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/tspath"
)

type Options struct {
	// ImportSource is the module specifier of the effect library
	// (compilerOptions.effectImportSource); defaults to "effect".
	ImportSource string
	// ConvertPipes rewrites pipe(a, f) / a.pipe(f) chains into |> pipelines.
	// Off by default: |> desugars to nested calls (g(f(a))), which loses the
	// left-to-right type inference that effect's pipe() overloads provide —
	// lambda stages like Arr.findFirst((x) => …) infer their parameter from
	// the piped-in value only in the pipe() form, so the rewrite can turn a
	// cleanly-checking file into one full of implicit-unknown errors.
	ConvertPipes bool
}

// Skip reasons reported in Result.SkipReason.
const (
	SkipNoEffectImport    = "no-effect-import"
	SkipNamespaceImport   = "namespace-import"
	SkipAliasedImport     = "aliased-helper-import"
	SkipHelperShadowed    = "helper-shadowed"
	SkipTSParseError      = "ts-parse-error"
	SkipETSParseError     = "ets-parse-error"
	SkipRoundTripMismatch = "round-trip-mismatch"
)

type FileStats struct {
	Patterns map[string]int // pattern family -> conversion count
}

func (s *FileStats) count(pattern string) {
	if s.Patterns == nil {
		s.Patterns = make(map[string]int)
	}
	s.Patterns[pattern]++
}

func (s *FileStats) Total() int {
	n := 0
	for _, c := range s.Patterns {
		n += c
	}
	return n
}

func (s *FileStats) String() string {
	if len(s.Patterns) == 0 {
		return "no patterns"
	}
	keys := make([]string, 0, len(s.Patterns))
	for k := range s.Patterns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, s.Patterns[k]))
	}
	return strings.Join(parts, ", ")
}

type Result struct {
	// Output is the EffectScript text; equal to the input when nothing
	// converted or the file was skipped.
	Output string
	// Converted reports whether Output differs from the input and passed
	// verification.
	Converted bool
	// SkipReason is non-empty when the file was not converted; one of the
	// Skip* constants.
	SkipReason string
	// Detail optionally elaborates on SkipReason (e.g. the first parse
	// diagnostic of a failed verification).
	Detail string
	Stats  FileStats
}

// Effectify rewrites a single .ts source text to EffectScript.
func Effectify(fileName string, src string, opts Options) Result {
	if opts.ImportSource == "" {
		opts.ImportSource = "effect"
	}

	file := parseFile(fileName, src, core.ScriptKindTS)
	if len(file.Diagnostics()) > 0 {
		d := file.Diagnostics()[0]
		return Result{Output: src, SkipReason: SkipTSParseError, Detail: fmt.Sprintf("TS%d at %d", d.Code(), d.Pos())}
	}

	bindings := scanBindings(file, opts.ImportSource)
	if reason := bindings.skipReason(); reason != "" {
		return Result{Output: src, SkipReason: reason}
	}
	if shadowed := findHelperShadowing(file, bindings); shadowed != "" {
		return Result{Output: src, SkipReason: SkipHelperShadowed, Detail: shadowed}
	}

	r := &rewriter{src: src, file: file, helpers: bindings.helpers, convertPipes: opts.ConvertPipes}
	out := r.emit(file.AsNode())
	if out == src || r.stats.Total() == 0 {
		return Result{Output: src, Stats: r.stats}
	}

	// Verification 1: the output must be diagnostic-free EffectScript.
	etsFile := parseFile(toEtsFileName(fileName), out, core.ScriptKindETS)
	if len(etsFile.Diagnostics()) > 0 {
		d := etsFile.Diagnostics()[0]
		return Result{Output: src, SkipReason: SkipETSParseError, Detail: fmt.Sprintf("TS%d at %d", d.Code(), d.Pos()), Stats: r.stats}
	}

	// Verification 2: desugaring the output must yield the original program.
	// Parsing .ets runs the forward desugarer; both sides are then printed,
	// pipe-flattened (|> desugars to nested calls, so pipe(a, f) and a |> f
	// must canonicalize identically) and compared modulo
	// semantically-transparent parentheses.
	before := parseFile("/before.ts", flattenPipes("/before.ts", printNormalized(file)), core.ScriptKindTS)
	after := parseFile("/after.ts", flattenPipes("/after.ts", printNormalized(etsFile)), core.ScriptKindTS)
	if !equalModuloParens(before.AsNode(), after.AsNode()) {
		return Result{Output: src, SkipReason: SkipRoundTripMismatch, Stats: r.stats}
	}

	return Result{Output: out, Converted: true, Stats: r.stats}
}

func toEtsFileName(fileName string) string {
	return strings.TrimSuffix(fileName, ".ts") + ".ets"
}

func parseFile(fileName string, src string, kind core.ScriptKind) *ast.SourceFile {
	opts := ast.SourceFileParseOptions{FileName: fileName, Path: tspath.Path(fileName)}
	return parser.ParseSourceFile(opts, src, kind)
}

func printNormalized(file *ast.SourceFile) string {
	ec := printer.NewEmitContext()
	pr := printer.NewPrinter(printer.PrinterOptions{}, printer.PrintHandlers{}, ec)
	return pr.EmitSourceFile(file)
}
