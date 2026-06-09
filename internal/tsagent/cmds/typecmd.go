package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/binder"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

func init() {
	cli.Register(cli.Command{
		Family:       "type",
		Name:         "at",
		Summary:      "Resolve the type at one or more positions or symbols",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &typeAtFlags{}
			fs.IntVar(&f.expandDepth, "expand-depth", 1, "structural expansion depth (beyond it: display string only)")
			fs.StringVar(&f.symbol, "symbol", "", "target symbol ID (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target qualified-name search")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runTypeAt(ctx, ws, flags.(*typeAtFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "type",
		Name:         "assignable",
		Summary:      "Check assignability between two targets with a property drill-down on failure",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &assignableFlags{}
			fs.StringVar(&f.source, "source", "", "source target (file:line:col or symbol ID)")
			fs.StringVar(&f.to, "to", "", "destination target (file:line:col or symbol ID)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runTypeAssignable(ctx, ws, flags.(*assignableFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "type",
		Name:         "coverage",
		Summary:      "Type coverage report: any/unknown expressions, casts, non-null assertions, ts-ignores",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &coverageFlags{}
			fs.Float64Var(&f.threshold, "threshold", -1, "exit 1 when project any%% exceeds this percentage")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			result, err := runTypeCoverage(ctx, ws, flags.(*coverageFlags), args)
			if err != nil {
				return nil, err
			}
			if result.ThresholdExceeded {
				// The CLI layer emits only the error on failure; include the
				// numbers so they are not lost.
				return result, cli.Errorf(cli.ExitFailed, "any coverage %.2f%% exceeds threshold %.2f%% (%d of %d expressions)",
					result.Total.AnyPct, flags.(*coverageFlags).threshold, result.Total.Any, result.Total.Expressions)
			}
			return result, nil
		},
	})
	cli.Register(cli.Command{
		Family:       "type",
		Name:         "complexity",
		Summary:      "Structural complexity metric for exported type aliases, interfaces, and classes",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &complexityFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "compute for a single symbol ID instead of paths")
			fs.IntVar(&f.threshold, "threshold", 0, "only report types with complexity above this value")
			fs.BoolVar(&f.rank, "rank", true, "sort by complexity descending")
			fs.IntVar(&f.top, "top", 50, "keep only the N most complex types (0 = all)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runTypeComplexity(ctx, ws, flags.(*complexityFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "type",
		Name:         "instantiations",
		Summary:      "Best-effort list of concrete instantiations of a generic symbol (via find-references)",
		NeedsProgram: true,
		Flags:        func(fs *flag.FlagSet) any { return &instantiationsFlags{} },
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runTypeInstantiations(ctx, ws, flags.(*instantiationsFlags), args)
		},
	})
}

// parseOperandSpec turns a positional target argument into a TargetSpec:
// file:line:col positions, otherwise symbol IDs (path#qualified.name or
// path@pos).
func parseOperandSpec(s string) core.TargetSpec {
	if _, _, _, err := core.ParsePosition(s); err == nil {
		return core.TargetSpec{At: s}
	}
	return core.TargetSpec{Symbol: s}
}

// typeOfTarget computes the type of a resolved target within checker c. For
// type-alias/interface/class declarations (or their name tokens) it returns
// the declared type; otherwise the location's type.
func typeOfTarget(c *checker.Checker, target *core.Target) *checker.Type {
	if decl := typeDeclarationOf(target.Node); decl != nil {
		binder.BindSourceFile(target.File)
		if symbol := decl.Symbol(); symbol != nil {
			return c.GetDeclaredTypeOfSymbol(symbol)
		}
	}
	return c.GetTypeAtLocation(target.Node)
}

// typeDeclarationOf returns the type-bearing declaration for node: node
// itself when it is a type alias/interface/class declaration, or its parent
// when node is the declaration's name.
func typeDeclarationOf(node *ast.Node) *ast.Node {
	isTypeDecl := func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindTypeAliasDeclaration, ast.KindInterfaceDeclaration, ast.KindClassDeclaration:
			return true
		}
		return false
	}
	if isTypeDecl(node) {
		return node
	}
	if parent := node.Parent; parent != nil && isTypeDecl(parent) && parent.Name() == node {
		return parent
	}
	return nil
}

// declarationSite renders the primary declaration location of a symbol as
// file:line:col.
func declarationSite(ws *core.Workspace, symbol *ast.Symbol) string {
	if symbol == nil || len(symbol.Declarations) == 0 {
		return ""
	}
	decl := symbol.ValueDeclaration
	if decl == nil {
		decl = symbol.Declarations[0]
	}
	file := ast.GetSourceFileOfNode(decl)
	if file == nil {
		return ""
	}
	pos := astnav.GetStartOfNode(decl, file, false /*includeJSDoc*/)
	if name := ast.GetNameOfDeclaration(decl); name != nil {
		pos = astnav.GetStartOfNode(name, file, false /*includeJSDoc*/)
	}
	line, col := ws.PosToLineCol(file, pos)
	return fmt.Sprintf("%s:%d:%d", ws.RelPath(file.FileName()), line, col)
}

// ---------------------------------------------------------------------------
// type at

type typeAtFlags struct {
	expandDepth int
	symbol      string
	name        string
}

// TypeAtEntry is the resolved type of one target.
type TypeAtEntry struct {
	Target      string         `json:"target"`
	Display     string         `json:"display"`
	Type        *core.TypeJSON `json:"type"`
	SymbolID    string         `json:"symbolId,omitempty"`
	Symbol      string         `json:"symbol,omitempty"`
	Declaration string         `json:"declaration,omitempty"`
}

// TypeAtResult is the `type at` result, one entry per target.
type TypeAtResult struct {
	Entries []*TypeAtEntry
}

var _ cli.Lister = (*TypeAtResult)(nil)

func (r *TypeAtResult) Total() int     { return len(r.Entries) }
func (r *TypeAtResult) Item(i int) any { return r.Entries[i] }

func (r *TypeAtResult) WriteItemText(w io.Writer, item any) error {
	e := item.(*TypeAtEntry)
	if _, err := fmt.Fprintf(w, "%s: %s\n", e.Target, e.Display); err != nil {
		return err
	}
	if e.SymbolID != "" {
		if _, err := fmt.Fprintf(w, "%ssymbol %s  %s\n", cli.Indent(1), e.SymbolID, e.Declaration); err != nil {
			return err
		}
	}
	if e.Type != nil {
		if _, err := fmt.Fprintf(w, "%skind %s\n", cli.Indent(1), e.Type.Kind); err != nil {
			return err
		}
	}
	return nil
}

func runTypeAt(ctx context.Context, ws *core.Workspace, flags *typeAtFlags, args []string) (*TypeAtResult, error) {
	type labeledSpec struct {
		label string
		spec  core.TargetSpec
	}
	var specs []labeledSpec
	for _, arg := range args {
		specs = append(specs, labeledSpec{arg, core.TargetSpec{At: arg}})
	}
	if flags.symbol != "" {
		specs = append(specs, labeledSpec{flags.symbol, core.TargetSpec{Symbol: flags.symbol}})
	}
	if flags.name != "" {
		specs = append(specs, labeledSpec{flags.name, core.TargetSpec{Name: flags.name}})
	}
	if len(specs) == 0 {
		return nil, cli.UsageErrorf("type at requires at least one position argument or --symbol/--name")
	}

	result := &TypeAtResult{}
	for _, s := range specs {
		target, err := ws.ResolveTarget(ctx, s.spec)
		if err != nil {
			return nil, err
		}
		// All type operations for this target happen within one checker
		// acquisition; types are never mixed across checkers.
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, target.File)
		t := typeOfTarget(fileChecker, target)
		entry := &TypeAtEntry{
			Target:      s.label,
			SymbolID:    core.EncodeSymbolID(ws, target.Symbol),
			Declaration: declarationSite(ws, target.Symbol),
		}
		if target.Symbol != nil {
			entry.Symbol = fileChecker.SymbolToString(target.Symbol)
		}
		if t != nil {
			displayFlags := core.TypeDisplayFlags
			if decl := typeDeclarationOf(target.Node); decl != nil && decl.Kind == ast.KindTypeAliasDeclaration {
				// Expand the alias's own structure instead of printing its name.
				displayFlags |= checker.TypeFormatFlagsInTypeAlias
			}
			entry.Display = fileChecker.TypeToStringEx(t, target.Node, displayFlags, nil)
			entry.Type = core.EncodeType(fileChecker, t, target.Node, flags.expandDepth)
			entry.Type.Display = entry.Display
		}
		done()
		if t == nil {
			return nil, cli.NotFoundErrorf("no type at %s", s.label)
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// type assignable

type assignableFlags struct {
	source string
	to     string
}

// AssignabilityProblem is one property-wise mismatch found while drilling
// into a failed assignability check.
type AssignabilityProblem struct {
	Property string                  `json:"property"`
	Expected string                  `json:"expected"`
	Actual   string                  `json:"actual"` // "missing" when the source has no such property
	Nested   []*AssignabilityProblem `json:"nested,omitempty"`
}

// AssignableResult is the `type assignable` result.
type AssignableResult struct {
	Assignable bool                    `json:"assignable"`
	Source     string                  `json:"source"`
	Target     string                  `json:"target"`
	Problems   []*AssignabilityProblem `json:"problems,omitempty"`
	Note       string                  `json:"note,omitempty"`
}

var _ cli.Texter = (*AssignableResult)(nil)

func (r *AssignableResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "assignable: %v\n%s\n  -> %s\n", r.Assignable, r.Source, r.Target); err != nil {
		return err
	}
	return writeProblemsText(w, r.Problems, 1)
}

func writeProblemsText(w io.Writer, problems []*AssignabilityProblem, depth int) error {
	for _, p := range problems {
		if _, err := fmt.Fprintf(w, "%s%s: expected %s, got %s\n", cli.Indent(depth), p.Property, p.Expected, p.Actual); err != nil {
			return err
		}
		if err := writeProblemsText(w, p.Nested, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func runTypeAssignable(ctx context.Context, ws *core.Workspace, flags *assignableFlags, args []string) (*AssignableResult, error) {
	if flags.source == "" || flags.to == "" {
		return nil, cli.UsageErrorf("type assignable requires both --source and --to (file:line:col or symbol ID)")
	}
	if len(args) > 0 {
		return nil, cli.UsageErrorf("type assignable takes no positional arguments")
	}
	// Resolve both nodes first (each resolution acquires and releases its own
	// checker), then do every type operation within a single acquisition of
	// the program's generic checker so the two types are comparable.
	sourceTarget, err := ws.ResolveTarget(ctx, parseOperandSpec(flags.source))
	if err != nil {
		return nil, fmt.Errorf("resolving --source: %w", err)
	}
	toTarget, err := ws.ResolveTarget(ctx, parseOperandSpec(flags.to))
	if err != nil {
		return nil, fmt.Errorf("resolving --to: %w", err)
	}

	c, done := ws.Program.GetTypeChecker(ctx)
	defer done()
	sourceType := typeOfTarget(c, sourceTarget)
	targetType := typeOfTarget(c, toTarget)
	if sourceType == nil {
		return nil, cli.NotFoundErrorf("no type for --source %s", flags.source)
	}
	if targetType == nil {
		return nil, cli.NotFoundErrorf("no type for --to %s", flags.to)
	}

	result := &AssignableResult{
		Assignable: c.IsTypeAssignableTo(sourceType, targetType),
		Source:     c.TypeToStringEx(sourceType, sourceTarget.Node, core.TypeDisplayFlags, nil),
		Target:     c.TypeToStringEx(targetType, toTarget.Node, core.TypeDisplayFlags, nil),
	}
	if !result.Assignable {
		result.Problems = drillAssignability(c, sourceType, targetType, 1)
		result.Note = "problems are an approximate property-wise drill-down, not the checker's native elaboration chain"
	}
	return result, nil
}

const (
	maxDrillDepth         = 3
	maxProblemsPerLevel   = 20
	missingPropertyActual = "missing"
)

// drillAssignability reports, for each property of target missing or
// mismatched in source, the expected and actual types, recursing into
// mismatched object properties up to maxDrillDepth.
func drillAssignability(c *checker.Checker, source *checker.Type, target *checker.Type, depth int) []*AssignabilityProblem {
	if depth > maxDrillDepth {
		return nil
	}
	var problems []*AssignabilityProblem
	apparentSource := c.GetApparentType(source)
	for _, targetProp := range c.GetPropertiesOfType(target) {
		if len(problems) >= maxProblemsPerLevel {
			break
		}
		expectedType := c.GetTypeOfSymbol(targetProp)
		sourceProp := c.GetPropertyOfType(apparentSource, targetProp.Name)
		if sourceProp == nil {
			if targetProp.Flags&ast.SymbolFlagsOptional != 0 {
				continue
			}
			problems = append(problems, &AssignabilityProblem{
				Property: targetProp.Name,
				Expected: c.TypeToStringEx(expectedType, nil, core.TypeDisplayFlags, nil),
				Actual:   missingPropertyActual,
			})
			continue
		}
		actualType := c.GetTypeOfSymbol(sourceProp)
		if c.IsTypeAssignableTo(actualType, expectedType) {
			continue
		}
		problems = append(problems, &AssignabilityProblem{
			Property: targetProp.Name,
			Expected: c.TypeToStringEx(expectedType, nil, core.TypeDisplayFlags, nil),
			Actual:   c.TypeToStringEx(actualType, nil, core.TypeDisplayFlags, nil),
			Nested:   drillAssignability(c, actualType, expectedType, depth+1),
		})
	}
	return problems
}

// ---------------------------------------------------------------------------
// type coverage

type coverageFlags struct {
	threshold float64
}

// FileCoverage is the type-coverage report for one file (or the project
// total).
type FileCoverage struct {
	File        string  `json:"file,omitempty"`
	Expressions int     `json:"expressions"`
	Any         int     `json:"any"`
	Unknown     int     `json:"unknown"`
	AnyPct      float64 `json:"anyPct"`
	Casts       int     `json:"casts"`
	NonNull     int     `json:"nonNull"`
	Ignores     int     `json:"ignores"`
}

// CoverageResult is the `type coverage` result.
type CoverageResult struct {
	Files             []*FileCoverage `json:"files"`
	Total             *FileCoverage   `json:"total"`
	Threshold         float64         `json:"threshold,omitempty"`
	ThresholdExceeded bool            `json:"thresholdExceeded,omitempty"`
}

var _ cli.Texter = (*CoverageResult)(nil)

func (r *CoverageResult) WriteText(w io.Writer) error {
	for _, f := range r.Files {
		if err := writeCoverageLine(w, f.File, f); err != nil {
			return err
		}
	}
	return writeCoverageLine(w, "total", r.Total)
}

func writeCoverageLine(w io.Writer, label string, f *FileCoverage) error {
	_, err := fmt.Fprintf(w, "%s  expr:%d any:%d (%.2f%%) unknown:%d casts:%d nonNull:%d ignores:%d\n",
		label, f.Expressions, f.Any, f.AnyPct, f.Unknown, f.Casts, f.NonNull, f.Ignores)
	return err
}

func roundPct(numerator int, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return math.Round(float64(numerator)/float64(denominator)*10000) / 100
}

func runTypeCoverage(ctx context.Context, ws *core.Workspace, flags *coverageFlags, args []string) (*CoverageResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	result := &CoverageResult{Total: &FileCoverage{}}
	for _, file := range files {
		cov := &FileCoverage{File: ws.RelPath(file.FileName())}
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		countCoverage(fileChecker, file, cov)
		done()
		cov.Ignores = len(file.CommentDirectives)
		cov.AnyPct = roundPct(cov.Any, cov.Expressions)
		result.Files = append(result.Files, cov)

		result.Total.Expressions += cov.Expressions
		result.Total.Any += cov.Any
		result.Total.Unknown += cov.Unknown
		result.Total.Casts += cov.Casts
		result.Total.NonNull += cov.NonNull
		result.Total.Ignores += cov.Ignores
	}
	result.Total.AnyPct = roundPct(result.Total.Any, result.Total.Expressions)
	if flags.threshold >= 0 {
		result.Threshold = flags.threshold
		result.ThresholdExceeded = result.Total.AnyPct > flags.threshold
	}
	return result, nil
}

// countCoverage walks the file AST counting expression-position identifiers,
// property accesses, and call expressions plus the syntactic assertion
// inventory.
func countCoverage(c *checker.Checker, file *ast.SourceFile, cov *FileCoverage) {
	var visit ast.Visitor
	visit = func(node *ast.Node) bool {
		switch node.Kind {
		case ast.KindIdentifier, ast.KindPropertyAccessExpression, ast.KindCallExpression:
			if ast.IsExpressionNode(node) {
				cov.Expressions++
				if t := c.GetTypeAtLocation(node); t != nil {
					if t.Flags()&checker.TypeFlagsAny != 0 {
						cov.Any++
					}
					if t.Flags()&checker.TypeFlagsUnknown != 0 {
						cov.Unknown++
					}
				}
			}
		case ast.KindAsExpression, ast.KindSatisfiesExpression, ast.KindTypeAssertionExpression:
			cov.Casts++
		case ast.KindNonNullExpression:
			cov.NonNull++
		}
		node.ForEachChild(visit)
		return false
	}
	file.AsNode().ForEachChild(visit)
}

// ---------------------------------------------------------------------------
// type complexity

type complexityFlags struct {
	symbol    string
	threshold int
	rank      bool
	top       int
}

// ComplexityEntry is the complexity report for one type declaration.
type ComplexityEntry struct {
	SymbolID   string                   `json:"symbolId,omitempty"`
	Name       string                   `json:"name"`
	File       string                   `json:"file"`
	Line       int                      `json:"line"`
	Complexity int                      `json:"complexity"`
	Breakdown  core.ComplexityBreakdown `json:"breakdown"`
}

// ComplexityResult is the `type complexity` result.
type ComplexityResult struct {
	Entries []*ComplexityEntry
}

var _ cli.Lister = (*ComplexityResult)(nil)

func (r *ComplexityResult) Total() int     { return len(r.Entries) }
func (r *ComplexityResult) Item(i int) any { return r.Entries[i] }

func (r *ComplexityResult) WriteItemText(w io.Writer, item any) error {
	e := item.(*ComplexityEntry)
	_, err := fmt.Fprintf(w, "%4d  %s  %s:%d  (union:%d props:%d depth:%d)\n",
		e.Complexity, e.Name, e.File, e.Line, e.Breakdown.UnionWidth, e.Breakdown.PropertyCount, e.Breakdown.Depth)
	return err
}

func runTypeComplexity(ctx context.Context, ws *core.Workspace, flags *complexityFlags, args []string) (*ComplexityResult, error) {
	result := &ComplexityResult{}
	if flags.symbol != "" {
		target, err := ws.ResolveTarget(ctx, core.TargetSpec{Symbol: flags.symbol})
		if err != nil {
			return nil, err
		}
		decl := typeDeclarationOf(target.Node)
		if decl == nil {
			return nil, cli.UsageErrorf("--symbol %s is not a type alias, interface, or class", flags.symbol)
		}
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, target.File)
		entry := complexityEntryFor(ws, fileChecker, target.File, decl)
		done()
		if entry != nil {
			result.Entries = append(result.Entries, entry)
		}
	} else {
		files, err := projectFiles(ws, args)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			binder.BindSourceFile(file)
			decls := exportedTypeDeclarations(file.Statements.Nodes)
			if len(decls) == 0 {
				continue
			}
			fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
			for _, decl := range decls {
				if entry := complexityEntryFor(ws, fileChecker, file, decl); entry != nil {
					result.Entries = append(result.Entries, entry)
				}
			}
			done()
		}
	}

	if flags.threshold > 0 {
		result.Entries = slices.DeleteFunc(result.Entries, func(e *ComplexityEntry) bool {
			return e.Complexity <= flags.threshold
		})
	}
	if flags.rank {
		slices.SortStableFunc(result.Entries, func(a, b *ComplexityEntry) int {
			if a.Complexity != b.Complexity {
				return b.Complexity - a.Complexity
			}
			if c := strings.Compare(a.File, b.File); c != 0 {
				return c
			}
			return a.Line - b.Line
		})
	}
	if flags.top > 0 && len(result.Entries) > flags.top {
		result.Entries = result.Entries[:flags.top]
	}
	return result, nil
}

// exportedTypeDeclarations collects exported type-alias/interface/class
// declarations from a statement list, recursing into namespace bodies.
func exportedTypeDeclarations(statements []*ast.Node) []*ast.Node {
	var decls []*ast.Node
	for _, statement := range statements {
		switch statement.Kind {
		case ast.KindTypeAliasDeclaration, ast.KindInterfaceDeclaration, ast.KindClassDeclaration:
			if core.IsExportedDeclaration(statement) {
				decls = append(decls, statement)
			}
		case ast.KindModuleDeclaration:
			if body := statement.Body(); body != nil && body.Kind == ast.KindModuleBlock {
				decls = append(decls, exportedTypeDeclarations(body.AsModuleBlock().Statements.Nodes)...)
			}
		}
	}
	return decls
}

func complexityEntryFor(ws *core.Workspace, c *checker.Checker, file *ast.SourceFile, decl *ast.Node) *ComplexityEntry {
	symbol := decl.Symbol()
	if symbol == nil {
		return nil
	}
	t := c.GetDeclaredTypeOfSymbol(symbol)
	if t == nil {
		return nil
	}
	cost, breakdown := core.ComplexityWithBreakdown(c, t)
	pos := astnav.GetStartOfNode(decl, file, false /*includeJSDoc*/)
	if name := ast.GetNameOfDeclaration(decl); name != nil {
		pos = astnav.GetStartOfNode(name, file, false /*includeJSDoc*/)
	}
	line, _ := ws.PosToLineCol(file, pos)
	return &ComplexityEntry{
		SymbolID:   core.EncodeSymbolID(ws, symbol),
		Name:       ast.GetDeclarationName(decl),
		File:       ws.RelPath(file.FileName()),
		Line:       line,
		Complexity: cost,
		Breakdown:  breakdown,
	}
}

// ---------------------------------------------------------------------------
// type instantiations

type instantiationsFlags struct{}

// Instantiation is one deduplicated concrete instantiation of a generic
// symbol.
type Instantiation struct {
	Display       string   `json:"display"`
	Kind          string   `json:"kind"` // type | call
	TypeArguments []string `json:"typeArguments,omitempty"`
	Count         int      `json:"count"`
	Locations     []string `json:"locations"`
}

// InstantiationsResult is the `type instantiations` result.
type InstantiationsResult struct {
	Target         string           `json:"target"`
	Method         string           `json:"method"`
	Note           string           `json:"note"`
	Instantiations []*Instantiation `json:"instantiations"`
}

var _ cli.Texter = (*InstantiationsResult)(nil)

func (r *InstantiationsResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s  (%d distinct instantiations, via %s)\n", r.Target, len(r.Instantiations), r.Method); err != nil {
		return err
	}
	for _, inst := range r.Instantiations {
		if _, err := fmt.Fprintf(w, "%s%dx %s  %s\n", cli.Indent(1), inst.Count, inst.Display, strings.Join(inst.Locations, " ")); err != nil {
			return err
		}
	}
	return nil
}

const maxInstantiationLocations = 5

func runTypeInstantiations(ctx context.Context, ws *core.Workspace, flags *instantiationsFlags, args []string) (*InstantiationsResult, error) {
	if len(args) != 1 {
		return nil, cli.UsageErrorf("type instantiations takes exactly one target argument (file:line:col or symbol ID)")
	}
	target, err := ws.ResolveTarget(ctx, parseOperandSpec(args[0]))
	if err != nil {
		return nil, err
	}
	refNode := target.Node
	if name := ast.GetNameOfDeclaration(target.Node); name != nil {
		refNode = name
	}

	entries := ws.LS.GetReferencedSymbolsForNode(ctx, refNode.Pos(), refNode, ws.Program.GetSourceFiles())

	// Skip the declarations themselves; only usages instantiate.
	declNames := make(map[*ast.Node]bool)
	markDecls := func(symbol *ast.Symbol) {
		if symbol == nil {
			return
		}
		for _, decl := range symbol.Declarations {
			if name := ast.GetNameOfDeclaration(decl); name != nil {
				declNames[name] = true
			}
		}
	}
	markDecls(target.Symbol)
	for _, entry := range entries {
		markDecls(entry.DefinitionSymbol())
	}

	// Group reference nodes by file so each file's types are produced and
	// rendered within a single acquisition of that file's checker. Only
	// strings cross checker boundaries.
	byFile := make(map[*ast.SourceFile][]*ast.Node)
	var fileOrder []*ast.SourceFile
	for _, entry := range entries {
		for _, ref := range entry.References() {
			if !ref.IsNodeEntry() || ref.Node() == nil || declNames[ref.Node()] {
				continue
			}
			file := ast.GetSourceFileOfNode(ref.Node())
			if file == nil {
				continue
			}
			if _, seen := byFile[file]; !seen {
				fileOrder = append(fileOrder, file)
			}
			byFile[file] = append(byFile[file], ref.Node())
		}
	}

	collected := make(map[string]*Instantiation)
	var order []string
	for _, file := range fileOrder {
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		for _, ref := range byFile[file] {
			site, kind := instantiationSite(ref)
			if site == nil {
				continue
			}
			display, typeArgs := renderInstantiation(fileChecker, file, site, kind)
			if display == "" {
				continue
			}
			key := kind + "|" + display + "|" + strings.Join(typeArgs, ",")
			inst, ok := collected[key]
			if !ok {
				inst = &Instantiation{Display: display, Kind: kind, TypeArguments: typeArgs}
				collected[key] = inst
				order = append(order, key)
			}
			inst.Count++
			if len(inst.Locations) < maxInstantiationLocations {
				line, col := ws.PosToLineCol(file, astnav.GetStartOfNode(site, file, false /*includeJSDoc*/))
				inst.Locations = append(inst.Locations, fmt.Sprintf("%s:%d:%d", ws.RelPath(file.FileName()), line, col))
			}
		}
		done()
	}

	result := &InstantiationsResult{
		Target: args[0],
		Method: "refs",
		Note: "best-effort: derived from find-all-references; only explicit type-argument references and resolved call/new sites are counted, " +
			"and call instantiations report the resolved signature rather than inferred type arguments",
	}
	for _, key := range order {
		result.Instantiations = append(result.Instantiations, collected[key])
	}
	slices.SortStableFunc(result.Instantiations, func(a, b *Instantiation) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Display, b.Display)
	})
	return result, nil
}

// instantiationSite finds the enclosing node that instantiates the generic
// referenced at ref: a type reference with explicit type arguments or a
// call/new expression with ref as the callee.
func instantiationSite(ref *ast.Node) (site *ast.Node, kind string) {
	// Climb qualified names / property accesses where ref is the rightmost
	// name (Ns.Generic<...> or obj.method(...)).
	expr := ref
	for parent := expr.Parent; parent != nil; parent = expr.Parent {
		if parent.Kind == ast.KindQualifiedName && parent.AsQualifiedName().Right == expr {
			expr = parent
			continue
		}
		if parent.Kind == ast.KindPropertyAccessExpression && parent.Name() == expr {
			expr = parent
			continue
		}
		break
	}
	parent := expr.Parent
	if parent == nil {
		return nil, ""
	}
	switch parent.Kind {
	case ast.KindTypeReference:
		if parent.AsTypeReferenceNode().TypeName == expr && len(parent.TypeArguments()) > 0 {
			return parent, "type"
		}
	case ast.KindExpressionWithTypeArguments:
		if parent.Expression() == expr && len(parent.TypeArguments()) > 0 {
			return parent, "type"
		}
	case ast.KindCallExpression, ast.KindNewExpression:
		if parent.Expression() == expr {
			return parent, "call"
		}
	}
	return nil, ""
}

// renderInstantiation renders the instantiated type (or resolved signature)
// at a site, plus the displays of its explicit type arguments. c must be the
// checker for the site's file; only strings escape.
func renderInstantiation(c *checker.Checker, file *ast.SourceFile, site *ast.Node, kind string) (display string, typeArgs []string) {
	enclosing := file.AsNode()
	if kind == "call" {
		sig := c.GetResolvedSignature(site)
		if sig == nil {
			return "", nil
		}
		return c.SignatureToStringEx(sig, enclosing, core.TypeDisplayFlags|checker.TypeFormatFlagsWriteTypeArgumentsOfSignature, nil), nil
	}
	t := c.GetTypeAtLocation(site)
	if t == nil {
		return "", nil
	}
	for _, argNode := range site.TypeArguments() {
		typeArgs = append(typeArgs, c.TypeToStringEx(c.GetTypeFromTypeNode(argNode), enclosing, core.TypeDisplayFlags, nil))
	}
	return c.TypeToStringEx(t, enclosing, core.TypeDisplayFlags, nil), typeArgs
}
