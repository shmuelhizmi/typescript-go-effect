package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/binder"
	"github.com/microsoft/typescript-go/internal/checker"
	tscore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// The analyze family (§4.5): read-only quality and analysis reports.

func init() {
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "dead-code",
		Summary:      "Find unreferenced symbols with confidence classification",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &deadCodeFlags{}
			fs.BoolVar(&f.includeExports, "include-exports", false, "also report unused exported symbols")
			fs.StringVar(&f.entry, "entry", "", "comma-separated entry files whose exports are live roots")
			fs.StringVar(&f.kind, "kind", "", "comma-separated kind filter (function,class,interface,...)")
			fs.BoolVar(&f.fixPlan, "fix-plan", false, "include a deletion edit set (for refactor apply-edits)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeDeadCode(ctx, ws, flags.(*deadCodeFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "assertions",
		Summary:      "Inventory of as-casts, non-null assertions, satisfies, and ts-ignore directives",
		NeedsProgram: true,
		Flags:        func(fs *flag.FlagSet) any { return &assertionsFlags{} },
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeAssertions(ctx, ws, flags.(*assertionsFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "unused-deps",
		Summary:      "Cross-reference package.json dependencies against actual imports",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &unusedDepsFlags{}
			fs.BoolVar(&f.dev, "dev", true, "include devDependencies in the unused analysis")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeUnusedDeps(ctx, ws, flags.(*unusedDepsFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "complexity",
		Summary:      "Per-function cyclomatic + cognitive + type complexity hotspots",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &analyzeComplexityFlags{}
			fs.IntVar(&f.top, "top", 25, "keep only the N highest-scoring functions (0 = all)")
			fs.Float64Var(&f.threshold, "threshold", 0, "only report functions with score >= this value")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeComplexity(ctx, ws, flags.(*analyzeComplexityFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "exhaustiveness",
		Summary:      "Switches over literal unions / enums that miss variants",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &exhaustivenessFlags{}
			fs.BoolVar(&f.strict, "strict", false, "report missing variants even when a default clause exists")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeExhaustiveness(ctx, ws, flags.(*exhaustivenessFlags), args)
		},
	})
}

// ---------------------------------------------------------------------------
// analyze dead-code

type deadCodeFlags struct {
	includeExports bool
	entry          string
	kind           string
	fixPlan        bool
}

// DeadSymbol is one symbol with no references outside its own declaration.
type DeadSymbol struct {
	SymbolID   string `json:"symbolId,omitempty"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Exported   bool   `json:"exported,omitempty"`
	Confidence string `json:"confidence"` // certain | dynamic-risk
}

// DeadCodeFixEdit is one deletion range (byte offsets, NewText always "").
type DeadCodeFixEdit struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	NewText string `json:"newText"`
}

// DeadCodeFixFile groups the deletion edits of one file.
type DeadCodeFixFile struct {
	File  string            `json:"file"`
	Edits []DeadCodeFixEdit `json:"edits"`
}

// DeadCodeFixPlan is the EditSet-shaped deletion plan (--fix-plan).
type DeadCodeFixPlan struct {
	Edits []DeadCodeFixFile `json:"edits"`
	Ops   []any             `json:"ops"`
}

// DeadCodeResult is the `analyze dead-code` result.
type DeadCodeResult struct {
	Symbols []*DeadSymbol    `json:"symbols"`
	FixPlan *DeadCodeFixPlan `json:"fixPlan,omitempty"`
}

var _ cli.Texter = (*DeadCodeResult)(nil)

func (r *DeadCodeResult) WriteText(w io.Writer) error {
	for _, s := range r.Symbols {
		exported := ""
		if s.Exported {
			exported = "  [export]"
		}
		if _, err := fmt.Fprintf(w, "%s %s  %s:%d  [%s]%s\n", s.Kind, s.Name, s.File, s.Line, s.Confidence, exported); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "total: %d dead symbol(s)\n", len(r.Symbols)); err != nil {
		return err
	}
	if r.FixPlan != nil {
		encoded, err := json.MarshalIndent(r.FixPlan, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "fix plan:\n%s\n", encoded); err != nil {
			return err
		}
	}
	return nil
}

// identOccurrence is one identifier token in the project-wide name index.
type identOccurrence struct {
	file *ast.SourceFile
	pos  int
	end  int
}

// identifierIndex is a single-pass project-wide index used to prefilter
// dead-code candidates without running find-all-references: every identifier
// (and private identifier) token by name, plus the set of identifier-like
// words appearing inside string literals (for dynamic-risk classification).
type identifierIndex struct {
	occurrences map[string][]identOccurrence
	stringWords map[string]bool
}

func buildIdentifierIndex(files []*ast.SourceFile) *identifierIndex {
	idx := &identifierIndex{
		occurrences: make(map[string][]identOccurrence),
		stringWords: make(map[string]bool),
	}
	for _, file := range files {
		var visit ast.Visitor
		visit = func(node *ast.Node) bool {
			switch node.Kind {
			case ast.KindIdentifier, ast.KindPrivateIdentifier:
				idx.occurrences[node.Text()] = append(idx.occurrences[node.Text()], identOccurrence{file: file, pos: node.Pos(), end: node.End()})
			case ast.KindStringLiteral, ast.KindNoSubstitutionTemplateLiteral, ast.KindTemplateHead,
				ast.KindTemplateMiddle, ast.KindTemplateTail:
				addStringWords(idx.stringWords, node.Text())
			}
			node.ForEachChild(visit)
			return false
		}
		file.AsNode().ForEachChild(visit)
	}
	return idx
}

// addStringWords splits a string literal's text into identifier-like words.
func addStringWords(words map[string]bool, text string) {
	start := -1
	for i := 0; i <= len(text); i++ {
		isWord := i < len(text) && (text[i] == '_' || text[i] == '$' || text[i] == '#' ||
			text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z' ||
			text[i] >= '0' && text[i] <= '9' || text[i] >= 0x80)
		if isWord {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			words[text[start:i]] = true
			start = -1
		}
	}
}

// deadCodeCandidate is one declaration considered by the dead-code scan.
type deadCodeCandidate struct {
	file     *ast.SourceFile
	decl     *ast.Node
	nameNode *ast.Node
	name     string
	kind     string
	exported bool
}

func runAnalyzeDeadCode(ctx context.Context, ws *core.Workspace, flags *deadCodeFlags, args []string) (*DeadCodeResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	kindFilter := cli.CommaSet(flags.kind)

	entryFiles := make(map[*ast.SourceFile]bool)
	if flags.entry != "" {
		for part := range strings.SplitSeq(flags.entry, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			file, err := ws.FileOf(part)
			if err != nil {
				return nil, fmt.Errorf("--entry: %w", err)
			}
			entryFiles[file] = true
		}
	}

	// References to a candidate may live anywhere in the program, so the
	// cheap identifier index always covers every project file, regardless of
	// which paths the candidates come from.
	allFiles, err := projectFiles(ws, nil)
	if err != nil {
		return nil, err
	}
	index := buildIdentifierIndex(allFiles)

	result := &DeadCodeResult{Symbols: []*DeadSymbol{}}
	var planEdits map[string][]DeadCodeFixEdit
	if flags.fixPlan {
		planEdits = make(map[string][]DeadCodeFixEdit)
	}

	for _, file := range files {
		binder.BindSourceFile(file)
		for _, cand := range deadCodeCandidates(file) {
			if cand.exported && (!flags.includeExports || entryFiles[file]) {
				continue
			}
			if !core.KindMatchesFilter(cand.kind, kindFilter) {
				continue
			}
			declarations := candidateDeclarations(cand)
			if !deadCodeIsDead(ctx, ws, index, cand, declarations) {
				continue
			}
			confidence := "certain"
			if index.stringWords[cand.name] {
				confidence = "dynamic-risk"
			}
			line, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(cand.nameNode, file, false /*includeJSDoc*/))
			result.Symbols = append(result.Symbols, &DeadSymbol{
				SymbolID:   core.EncodeSymbolID(ws, cand.decl.Symbol()),
				Name:       cand.name,
				Kind:       cand.kind,
				File:       ws.RelPath(file.FileName()),
				Line:       line,
				Exported:   cand.exported,
				Confidence: confidence,
			})
			if flags.fixPlan {
				rel := ws.RelPath(file.FileName())
				for _, decl := range declarations {
					rng := refactorDeletionRange(file, refactorDeletionNode(decl))
					planEdits[rel] = append(planEdits[rel], DeadCodeFixEdit{Start: rng.Pos(), End: rng.End()})
				}
			}
		}
	}

	slices.SortFunc(result.Symbols, func(a, b *DeadSymbol) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Name, b.Name)
	})

	if flags.fixPlan {
		plan := &DeadCodeFixPlan{Edits: []DeadCodeFixFile{}, Ops: []any{}}
		for _, file := range slices.Sorted(maps.Keys(planEdits)) {
			edits := planEdits[file]
			slices.SortFunc(edits, func(a, b DeadCodeFixEdit) int { return a.Start - b.Start })
			edits = slices.Compact(edits)
			plan.Edits = append(plan.Edits, DeadCodeFixFile{File: file, Edits: edits})
		}
		result.FixPlan = plan
	}
	return result, nil
}

// deadCodeCandidates collects the v1 candidate declarations of a file:
// module-scope functions, classes, interfaces, type aliases, enums, and
// variable declarations, plus private-only class members.
func deadCodeCandidates(file *ast.SourceFile) []*deadCodeCandidate {
	var candidates []*deadCodeCandidate
	add := func(decl *ast.Node) {
		nameNode := ast.GetNameOfDeclaration(decl)
		if nameNode == nil || (nameNode.Kind != ast.KindIdentifier && nameNode.Kind != ast.KindPrivateIdentifier) {
			return
		}
		candidates = append(candidates, &deadCodeCandidate{
			file:     file,
			decl:     decl,
			nameNode: nameNode,
			name:     nameNode.Text(),
			kind:     core.DeclarationKind(decl),
			exported: core.IsExportedDeclaration(decl),
		})
	}
	for _, statement := range file.Statements.Nodes {
		switch statement.Kind {
		case ast.KindVariableStatement:
			for _, decl := range statement.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
				add(decl)
			}
		case ast.KindFunctionDeclaration, ast.KindTypeAliasDeclaration, ast.KindInterfaceDeclaration, ast.KindEnumDeclaration:
			add(statement)
		case ast.KindClassDeclaration:
			add(statement)
			for _, member := range statement.Members() {
				switch member.Kind {
				case ast.KindMethodDeclaration, ast.KindPropertyDeclaration, ast.KindGetAccessor, ast.KindSetAccessor:
					if isPrivateMember(member) {
						add(member)
					}
				}
			}
		}
	}
	return candidates
}

func isPrivateMember(member *ast.Node) bool {
	if ast.GetCombinedModifierFlags(member)&ast.ModifierFlagsPrivate != 0 {
		return true
	}
	name := ast.GetNameOfDeclaration(member)
	return name != nil && name.Kind == ast.KindPrivateIdentifier
}

// candidateDeclarations returns all declaration nodes of the candidate's
// symbol (covering merged declarations and overloads), falling back to the
// candidate node itself.
func candidateDeclarations(cand *deadCodeCandidate) []*ast.Node {
	if symbol := cand.decl.Symbol(); symbol != nil && len(symbol.Declarations) > 0 {
		return symbol.Declarations
	}
	return []*ast.Node{cand.decl}
}

// deadCodeIsDead decides liveness in two tiers: the identifier index proves
// deadness for free when the name never occurs outside the candidate's own
// declaration ranges; otherwise find-all-references resolves whether the
// remaining occurrences actually bind to this symbol.
func deadCodeIsDead(ctx context.Context, ws *core.Workspace, index *identifierIndex, cand *deadCodeCandidate, declarations []*ast.Node) bool {
	insideOwnDeclarations := func(file *ast.SourceFile, pos int, end int) bool {
		for _, decl := range declarations {
			if ast.GetSourceFileOfNode(decl) == file && pos >= decl.Pos() && end <= decl.End() {
				return true
			}
		}
		return false
	}

	outside := 0
	for _, occ := range index.occurrences[cand.name] {
		if !insideOwnDeclarations(occ.file, occ.pos, occ.end) {
			outside++
		}
	}
	if outside == 0 {
		return true
	}

	// Inconclusive: the name occurs elsewhere, but those tokens may bind to
	// different symbols (shadowing, unrelated properties). Resolve precisely.
	refPos := astnav.GetStartOfNode(cand.nameNode, cand.file, false /*includeJSDoc*/)
	entries := ws.LS.GetReferencedSymbolsForNode(ctx, refPos, cand.nameNode, ws.Program.GetSourceFiles())
	for _, entry := range entries {
		for _, ref := range entry.References() {
			node := ref.Node()
			if node == nil {
				return false // unresolvable reference: assume live
			}
			file := ast.GetSourceFileOfNode(node)
			if file == nil || !insideOwnDeclarations(file, node.Pos(), node.End()) {
				return false
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// analyze assertions

type assertionsFlags struct{}

// AssertionRow is one trust-boundary site.
type AssertionRow struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Kind     string `json:"kind"` // as | as-const | type-assertion | satisfies | non-null | ts-ignore | ts-expect-error | ts-nocheck
	Detail   string `json:"detail,omitempty"`
	FromType string `json:"fromType,omitempty"`
	ToType   string `json:"toType,omitempty"`
}

// AssertionsFileCounts is the per-kind tally of one file.
type AssertionsFileCounts struct {
	File   string         `json:"file"`
	Counts map[string]int `json:"counts"`
}

// AssertionsResult is the `analyze assertions` result.
type AssertionsResult struct {
	Rows   []*AssertionRow         `json:"rows"`
	Files  []*AssertionsFileCounts `json:"files"`
	Totals map[string]int          `json:"totals"`
}

var _ cli.Texter = (*AssertionsResult)(nil)

func (r *AssertionsResult) WriteText(w io.Writer) error {
	for _, row := range r.Rows {
		types := ""
		if row.FromType != "" || row.ToType != "" {
			types = fmt.Sprintf("  %s -> %s", row.FromType, row.ToType)
		}
		if _, err := fmt.Fprintf(w, "%s:%d:%d  %s  %s%s\n", row.File, row.Line, row.Col, row.Kind, row.Detail, types); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "total: %s\n", formatKindCounts(r.Totals))
	return err
}

func formatKindCounts(counts map[string]int) string {
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s:%d", kind, counts[kind]))
	}
	return strings.Join(parts, " ")
}

const maxAssertionDetailLen = 80

func runAnalyzeAssertions(ctx context.Context, ws *core.Workspace, flags *assertionsFlags, args []string) (*AssertionsResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	result := &AssertionsResult{Rows: []*AssertionRow{}, Totals: make(map[string]int)}
	for _, file := range files {
		counts := make(map[string]int)
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		rows := collectAssertions(ws, fileChecker, file)
		done()
		for _, row := range rows {
			counts[row.Kind]++
			result.Totals[row.Kind]++
		}
		result.Rows = append(result.Rows, rows...)
		if len(rows) > 0 {
			result.Files = append(result.Files, &AssertionsFileCounts{File: ws.RelPath(file.FileName()), Counts: counts})
		}
	}
	return result, nil
}

// collectAssertions walks one file (with its checker held) collecting every
// assertion site plus the file's comment directives. Only strings escape the
// checker acquisition.
func collectAssertions(ws *core.Workspace, c *checker.Checker, file *ast.SourceFile) []*AssertionRow {
	rel := ws.RelPath(file.FileName())
	var rows []*AssertionRow

	newRow := func(pos int, kind string, detail string) *AssertionRow {
		line, col := ws.PosToLineCol(file, pos)
		return &AssertionRow{File: rel, Line: line, Col: col, Kind: kind, Detail: detail}
	}
	nodeStart := func(node *ast.Node) int {
		return astnav.GetStartOfNode(node, file, false /*includeJSDoc*/)
	}
	nodeText := func(node *ast.Node) string {
		start := scanner.SkipTrivia(file.Text(), node.Pos())
		return cli.Truncate(strings.Join(strings.Fields(file.Text()[start:node.End()]), " "), maxAssertionDetailLen)
	}
	typeDisplay := func(node *ast.Node) string {
		if t := c.GetTypeAtLocation(node); t != nil {
			return c.TypeToStringEx(t, file.AsNode(), core.TypeDisplayFlags, nil)
		}
		return ""
	}

	var visit ast.Visitor
	visit = func(node *ast.Node) bool {
		switch node.Kind {
		case ast.KindAsExpression, ast.KindTypeAssertionExpression:
			kind := "as"
			if node.Kind == ast.KindTypeAssertionExpression {
				kind = "type-assertion"
			}
			if ast.IsConstAssertion(node) {
				kind = "as-const"
			}
			row := newRow(nodeStart(node), kind, nodeText(node))
			if kind != "as-const" {
				row.FromType = typeDisplay(node.Expression())
				row.ToType = typeDisplay(node)
			}
			rows = append(rows, row)
		case ast.KindSatisfiesExpression:
			row := newRow(nodeStart(node), "satisfies", nodeText(node))
			row.FromType = typeDisplay(node.Expression())
			if t := c.GetTypeFromTypeNode(node.AsSatisfiesExpression().Type); t != nil {
				row.ToType = c.TypeToStringEx(t, file.AsNode(), core.TypeDisplayFlags, nil)
			}
			rows = append(rows, row)
		case ast.KindNonNullExpression:
			row := newRow(nodeStart(node), "non-null", nodeText(node))
			row.FromType = typeDisplay(node.Expression())
			row.ToType = typeDisplay(node)
			rows = append(rows, row)
		}
		node.ForEachChild(visit)
		return false
	}
	file.AsNode().ForEachChild(visit)

	for _, directive := range file.CommentDirectives {
		kind := "ts-ignore"
		if directive.Kind == ast.CommentDirectiveKindExpectError {
			kind = "ts-expect-error"
		}
		text := strings.TrimSpace(file.Text()[directive.Loc.Pos():directive.Loc.End()])
		rows = append(rows, newRow(directive.Loc.Pos(), kind, cli.Truncate(text, maxAssertionDetailLen)))
	}
	if file.CheckJsDirective != nil && !file.CheckJsDirective.Enabled {
		rows = append(rows, newRow(file.CheckJsDirective.Range.Pos(), "ts-nocheck", "@ts-nocheck"))
	}

	slices.SortFunc(rows, func(a, b *AssertionRow) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Col - b.Col
	})
	return rows
}

// ---------------------------------------------------------------------------
// analyze unused-deps

type unusedDepsFlags struct {
	dev bool
}

// UnusedDepsResult is the `analyze unused-deps` result.
type UnusedDepsResult struct {
	PackageJSON string               `json:"packageJson"`
	UnusedDeps  []string             `json:"unusedDeps"`
	PhantomDeps []string             `json:"phantomDeps"`
	BinUsages   []DependencyBinUsage `json:"binUsages,omitempty"`
	Note        string               `json:"note,omitempty"`
}

// DependencyBinUsage records non-import evidence that a dependency is used as
// a CLI tool. It is emitted in JSON output only; text output stays concise.
type DependencyBinUsage struct {
	Dependency string `json:"dependency"`
	Bin        string `json:"bin"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Source     string `json:"source"`
}

var _ cli.Texter = (*UnusedDepsResult)(nil)

func (r *UnusedDepsResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "package.json: %s\n", r.PackageJSON); err != nil {
		return err
	}
	for _, dep := range r.UnusedDeps {
		if _, err := fmt.Fprintf(w, "unused: %s\n", dep); err != nil {
			return err
		}
	}
	for _, dep := range r.PhantomDeps {
		if _, err := fmt.Fprintf(w, "phantom: %s\n", dep); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "total: %d unused, %d phantom\n", len(r.UnusedDeps), len(r.PhantomDeps))
	return err
}

type packageJSONDeps struct {
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
}

func runAnalyzeUnusedDeps(ctx context.Context, ws *core.Workspace, flags *unusedDepsFlags, args []string) (*UnusedDepsResult, error) {
	if len(args) > 0 {
		return nil, cli.UsageErrorf("analyze unused-deps takes no positional arguments")
	}
	packageJSONPath, content, err := findPackageJSON(ws)
	if err != nil {
		return nil, err
	}
	var pkg packageJSONDeps
	if err := json.Unmarshal([]byte(content), &pkg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", ws.RelPath(packageJSONPath), err)
	}

	declared := make(map[string]bool)
	analyzed := make(map[string]bool) // deps subject to the unused check
	for name := range pkg.Dependencies {
		declared[name] = true
		analyzed[name] = true
	}
	for name := range pkg.DevDependencies {
		declared[name] = true
		if flags.dev {
			analyzed[name] = true
		}
	}
	for name := range pkg.PeerDependencies {
		declared[name] = true
		analyzed[name] = true
	}

	imported := collectImportedPackages(ws)
	binUsages := collectDependencyBinUsages(ws, packageJSONPath, analyzed)
	usedBins := make(map[string]bool, len(binUsages))
	for _, usage := range binUsages {
		usedBins[usage.Dependency] = true
	}

	used := func(dep string) bool {
		if imported[dep] {
			return true
		}
		if usedBins[dep] {
			return true
		}
		if base, ok := strings.CutPrefix(dep, "@types/"); ok {
			// @types/foo covers foo; @types/scope__name covers @scope/name.
			if strings.Contains(base, "__") {
				base = "@" + strings.Replace(base, "__", "/", 1)
			}
			return imported[base]
		}
		// foo is also used when only its types package is imported.
		typesPkg := typesPackageNameFor(dep)
		return imported[typesPkg] || usedBins[typesPkg]
	}

	result := &UnusedDepsResult{
		PackageJSON: ws.RelPath(packageJSONPath),
		UnusedDeps:  []string{},
		PhantomDeps: []string{},
		BinUsages:   binUsages,
		Note:        "type-only dependency analysis (deps used only in type positions) is not implemented in v1",
	}
	for dep := range analyzed {
		if !used(dep) {
			result.UnusedDeps = append(result.UnusedDeps, dep)
		}
	}
	for pkgName := range imported {
		if !declared[pkgName] && !declared[typesPackageNameFor(pkgName)] {
			result.PhantomDeps = append(result.PhantomDeps, pkgName)
		}
	}
	slices.Sort(result.UnusedDeps)
	slices.Sort(result.PhantomDeps)
	return result, nil
}

type packageJSONToolMetadata struct {
	Bin        json.RawMessage   `json:"bin"`
	Scripts    map[string]string `json:"scripts"`
	Workspaces json.RawMessage   `json:"workspaces"`
}

func collectDependencyBinUsages(ws *core.Workspace, packageJSONPath string, analyzed map[string]bool) []DependencyBinUsage {
	packageDir := tspath.GetDirectoryPath(packageJSONPath)
	binToDeps := make(map[string][]string)
	for dep := range analyzed {
		for _, bin := range dependencyBinNames(ws, packageDir, dep) {
			binToDeps[bin] = append(binToDeps[bin], dep)
		}
	}
	if len(binToDeps) == 0 {
		return nil
	}
	for bin := range binToDeps {
		slices.Sort(binToDeps[bin])
	}

	scanRoot := dependencyToolScanRoot(ws, packageDir)
	var usages []DependencyBinUsage
	seen := make(map[string]bool)
	_ = ws.FS.WalkDir(scanRoot, func(path string, d iofs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != scanRoot && skipToolUsageDir(name) {
				return iofs.SkipDir
			}
			return nil
		}
		source := toolUsageSource(name)
		if source == "" {
			return nil
		}
		content, ok := ws.FS.ReadFile(path)
		if !ok {
			return nil
		}
		if source == "package-script" {
			collectPackageScriptBinUsages(ws, path, content, binToDeps, &usages, seen)
			return nil
		}
		collectTextBinUsages(ws, path, content, source, binToDeps, &usages, seen)
		return nil
	})
	slices.SortFunc(usages, func(a, b DependencyBinUsage) int {
		if c := strings.Compare(a.Dependency, b.Dependency); c != 0 {
			return c
		}
		if c := strings.Compare(a.Bin, b.Bin); c != 0 {
			return c
		}
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if c := a.Line - b.Line; c != 0 {
			return c
		}
		return strings.Compare(a.Source, b.Source)
	})
	return usages
}

func dependencyBinNames(ws *core.Workspace, packageDir string, dep string) []string {
	content, ok := findInstalledPackageJSON(ws, packageDir, dep)
	if !ok {
		return nil
	}
	var meta packageJSONToolMetadata
	if err := json.Unmarshal([]byte(content), &meta); err != nil || len(meta.Bin) == 0 {
		return nil
	}
	var binPath string
	if err := json.Unmarshal(meta.Bin, &binPath); err == nil {
		if strings.TrimSpace(binPath) == "" {
			return nil
		}
		return []string{defaultBinName(dep)}
	}
	var binMap map[string]json.RawMessage
	if err := json.Unmarshal(meta.Bin, &binMap); err != nil {
		return nil
	}
	bins := make([]string, 0, len(binMap))
	for bin, raw := range binMap {
		if strings.TrimSpace(bin) == "" || string(raw) == "null" {
			continue
		}
		bins = append(bins, bin)
	}
	slices.Sort(bins)
	return bins
}

func findInstalledPackageJSON(ws *core.Workspace, startDir string, dep string) (string, bool) {
	for dir := startDir; ; {
		candidate := tspath.CombinePaths(dir, "node_modules", dep, "package.json")
		if content, ok := ws.FS.ReadFile(candidate); ok {
			return content, true
		}
		parent := tspath.GetDirectoryPath(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func defaultBinName(dep string) string {
	if strings.HasPrefix(dep, "@") {
		_, name, ok := strings.Cut(dep, "/")
		if ok {
			return name
		}
	}
	return dep
}

func dependencyToolScanRoot(ws *core.Workspace, packageDir string) string {
	gitRoot := ""
	for dir := packageDir; ; {
		if content, ok := ws.FS.ReadFile(tspath.CombinePaths(dir, "package.json")); ok && packageJSONHasWorkspaces(content) {
			return dir
		}
		gitPath := tspath.CombinePaths(dir, ".git")
		if gitRoot == "" && (ws.FS.DirectoryExists(gitPath) || ws.FS.FileExists(gitPath)) {
			gitRoot = dir
		}
		parent := tspath.GetDirectoryPath(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if gitRoot != "" {
		return gitRoot
	}
	return packageDir
}

func packageJSONHasWorkspaces(content string) bool {
	var meta packageJSONToolMetadata
	if err := json.Unmarshal([]byte(content), &meta); err != nil {
		return false
	}
	raw := strings.TrimSpace(string(meta.Workspaces))
	return raw != "" && raw != "null"
}

func skipToolUsageDir(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "build", "coverage", "testdata":
		return true
	default:
		return false
	}
}

func toolUsageSource(name string) string {
	switch name {
	case "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock":
		return ""
	case "package.json":
		return "package-script"
	case "Makefile", "makefile", "GNUmakefile", "justfile":
		return "task-file"
	}
	switch tspath.TryGetExtensionFromPath(name) {
	case ".js", ".mjs", ".cjs", ".ts", ".mts", ".cts", ".json", ".jsonc", ".yaml", ".yml", ".sh":
		return "task-file"
	default:
		return ""
	}
}

func collectPackageScriptBinUsages(ws *core.Workspace, path string, content string, binToDeps map[string][]string, usages *[]DependencyBinUsage, seen map[string]bool) {
	var meta packageJSONToolMetadata
	if err := json.Unmarshal([]byte(content), &meta); err != nil {
		return
	}
	names := slices.Collect(maps.Keys(meta.Scripts))
	slices.Sort(names)
	for _, name := range names {
		line := jsonPropertyLine(content, name)
		collectLineBinUsages(ws, path, meta.Scripts[name], line, "package-script", binToDeps, usages, seen)
	}
}

func collectTextBinUsages(ws *core.Workspace, path string, content string, source string, binToDeps map[string][]string, usages *[]DependencyBinUsage, seen map[string]bool) {
	configFile := isToolUsageConfigFile(tspath.GetBaseFileName(path))
	for i, line := range strings.Split(content, "\n") {
		if configFile && !isToolUsageConfigLine(line) {
			continue
		}
		collectLineBinUsages(ws, path, line, i+1, source, binToDeps, usages, seen)
	}
}

func isToolUsageConfigFile(name string) bool {
	switch tspath.TryGetExtensionFromPath(name) {
	case ".json", ".jsonc", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func isToolUsageConfigLine(line string) bool {
	line = strings.ToLower(line)
	for _, key := range []string{"command", "cmd", "script", "scripts", "args"} {
		if strings.Contains(line, `"`+key+`"`) || strings.Contains(line, key+":") {
			return true
		}
	}
	return false
}

func collectLineBinUsages(ws *core.Workspace, path string, line string, lineNumber int, source string, binToDeps map[string][]string, usages *[]DependencyBinUsage, seen map[string]bool) {
	for _, token := range toolUsageTokens(line) {
		deps := binToDeps[token]
		if len(deps) == 0 {
			continue
		}
		for _, dep := range deps {
			usage := DependencyBinUsage{
				Dependency: dep,
				Bin:        token,
				File:       ws.RelPath(path),
				Line:       lineNumber,
				Source:     source,
			}
			key := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", usage.Dependency, usage.Bin, usage.File, usage.Line, usage.Source)
			if seen[key] {
				continue
			}
			seen[key] = true
			*usages = append(*usages, usage)
		}
	}
}

func toolUsageTokens(line string) []string {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', '"', '\'', '`', '(', ')', '{', '}', '[', ']', ';', '|', '&', '<', '>', ',':
			return true
		default:
			return false
		}
	})
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		token := normalizeToolUsageToken(field)
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func normalizeToolUsageToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "./")
	token = strings.TrimPrefix(token, ".\\")
	if i := strings.LastIndexAny(token, `/\`); i >= 0 {
		token = token[i+1:]
	}
	token = strings.Trim(token, ":")
	if strings.HasPrefix(token, "-") || strings.Contains(token, "=") {
		return ""
	}
	return token
}

func jsonPropertyLine(content string, key string) int {
	needle := `"` + key + `"`
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	return 0
}

// findPackageJSON walks up from the project root looking for package.json.
func findPackageJSON(ws *core.Workspace) (string, string, error) {
	for dir := ws.RootDir; ; {
		candidate := tspath.CombinePaths(dir, "package.json")
		if content, ok := ws.FS.ReadFile(candidate); ok {
			return candidate, content, nil
		}
		parent := tspath.GetDirectoryPath(dir)
		if parent == dir {
			return "", "", cli.NotFoundErrorf("no package.json found walking up from %s", ws.RootDir)
		}
		dir = parent
	}
}

// collectImportedPackages gathers the package names of every module specifier
// in the project's own files, skipping relative paths, node builtins, and
// tsconfig path aliases.
func collectImportedPackages(ws *core.Workspace) map[string]bool {
	options := ws.Program.Options()
	imported := make(map[string]bool)
	for _, file := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(file) || isInNodeModules(file.FileName()) {
			continue
		}
		for _, specifier := range file.Imports() {
			text := specifier.Text()
			if text == "" || strings.HasPrefix(text, ".") || strings.HasPrefix(text, "/") {
				continue
			}
			if tscore.NodeCoreModules()[text] {
				continue
			}
			if matchesPathAlias(options, text) {
				continue
			}
			if name := packageNameOfSpecifier(text); name != "" {
				imported[name] = true
			}
		}
	}
	return imported
}

// packageNameOfSpecifier extracts the package name from a bare specifier:
// `pkg/sub` -> pkg, `@scope/name/sub` -> @scope/name.
func packageNameOfSpecifier(specifier string) string {
	parts := strings.Split(specifier, "/")
	if strings.HasPrefix(specifier, "@") {
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// typesPackageNameFor renders the DefinitelyTyped package name of a package:
// foo -> @types/foo, @scope/name -> @types/scope__name.
func typesPackageNameFor(pkg string) string {
	if rest, ok := strings.CutPrefix(pkg, "@"); ok {
		return "@types/" + strings.Replace(rest, "/", "__", 1)
	}
	return "@types/" + pkg
}

// matchesPathAlias reports whether a specifier matches a tsconfig `paths`
// pattern (exact or single-`*` wildcard).
func matchesPathAlias(options *tscore.CompilerOptions, specifier string) bool {
	if options == nil || options.Paths.Size() == 0 {
		return false
	}
	for key := range options.Paths.Keys() {
		star := strings.IndexByte(key, '*')
		if star < 0 {
			if key == specifier {
				return true
			}
			continue
		}
		prefix, suffix := key[:star], key[star+1:]
		if len(specifier) >= len(prefix)+len(suffix) &&
			strings.HasPrefix(specifier, prefix) && strings.HasSuffix(specifier, suffix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// analyze complexity

type analyzeComplexityFlags struct {
	top       int
	threshold float64
}

// FunctionComplexity is the complexity report of one function.
type FunctionComplexity struct {
	SymbolID       string  `json:"symbolId,omitempty"`
	Name           string  `json:"name"`
	File           string  `json:"file"`
	Line           int     `json:"line"`
	Cyclomatic     int     `json:"cyclomatic"`
	Cognitive      int     `json:"cognitive"`
	TypeComplexity int     `json:"typeComplexity"`
	Score          float64 `json:"score"`
}

// AnalyzeComplexityResult is the `analyze complexity` result.
type AnalyzeComplexityResult struct {
	Entries []*FunctionComplexity
}

var _ cli.Lister = (*AnalyzeComplexityResult)(nil)

func (r *AnalyzeComplexityResult) Total() int     { return len(r.Entries) }
func (r *AnalyzeComplexityResult) Item(i int) any { return r.Entries[i] }

func (r *AnalyzeComplexityResult) WriteItemText(w io.Writer, item any) error {
	e := item.(*FunctionComplexity)
	_, err := fmt.Fprintf(w, "%7.1f  %s  %s:%d  (cyclomatic:%d cognitive:%d type:%d)\n",
		e.Score, e.Name, e.File, e.Line, e.Cyclomatic, e.Cognitive, e.TypeComplexity)
	return err
}

func runAnalyzeComplexity(ctx context.Context, ws *core.Workspace, flags *analyzeComplexityFlags, args []string) (*AnalyzeComplexityResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	result := &AnalyzeComplexityResult{}
	for _, file := range files {
		binder.BindSourceFile(file)
		functions := functionLikeNodesWithBody(file)
		if len(functions) == 0 {
			continue
		}
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		for _, fn := range functions {
			cyclomatic, cognitive := branchMetrics(fn.Body())
			typeComplexity := 0
			if t := fileChecker.GetTypeAtLocation(fn); t != nil {
				typeComplexity = core.Complexity(fileChecker, t)
			}
			pos := astnav.GetStartOfNode(fn, file, false /*includeJSDoc*/)
			if name := ast.GetNameOfDeclaration(fn); name != nil {
				pos = astnav.GetStartOfNode(name, file, false /*includeJSDoc*/)
			}
			line, _ := ws.PosToLineCol(file, pos)
			result.Entries = append(result.Entries, &FunctionComplexity{
				SymbolID:       core.EncodeSymbolID(ws, fn.Symbol()),
				Name:           functionDisplayName(fn),
				File:           ws.RelPath(file.FileName()),
				Line:           line,
				Cyclomatic:     cyclomatic,
				Cognitive:      cognitive,
				TypeComplexity: typeComplexity,
				Score:          float64(cyclomatic+cognitive) + float64(typeComplexity)/10,
			})
		}
		done()
	}

	if flags.threshold > 0 {
		result.Entries = slices.DeleteFunc(result.Entries, func(e *FunctionComplexity) bool {
			return e.Score < flags.threshold
		})
	}
	slices.SortStableFunc(result.Entries, func(a, b *FunctionComplexity) int {
		if a.Score != b.Score {
			if a.Score < b.Score {
				return 1
			}
			return -1
		}
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
	if flags.top > 0 && len(result.Entries) > flags.top {
		result.Entries = result.Entries[:flags.top]
	}
	return result, nil
}

// functionLikeNodesWithBody collects every function-like node of a file that
// has a body (declarations, methods, accessors, constructors, function
// expressions, arrow functions).
func functionLikeNodesWithBody(file *ast.SourceFile) []*ast.Node {
	var functions []*ast.Node
	var visit ast.Visitor
	visit = func(node *ast.Node) bool {
		if isAnalyzableFunction(node) && node.Body() != nil {
			functions = append(functions, node)
		}
		node.ForEachChild(visit)
		return false
	}
	file.AsNode().ForEachChild(visit)
	return functions
}

func isAnalyzableFunction(node *ast.Node) bool {
	switch node.Kind {
	case ast.KindFunctionDeclaration, ast.KindMethodDeclaration, ast.KindConstructor,
		ast.KindGetAccessor, ast.KindSetAccessor, ast.KindFunctionExpression, ast.KindArrowFunction:
		return true
	}
	return false
}

// functionDisplayName renders a stable name for a function-like node, looking
// through variable assignments and property assignments for anonymous forms.
func functionDisplayName(fn *ast.Node) string {
	if name := ast.GetDeclarationName(fn); name != "" {
		return name
	}
	if fn.Kind == ast.KindConstructor {
		return "constructor"
	}
	if parent := fn.Parent; parent != nil {
		switch parent.Kind {
		case ast.KindVariableDeclaration, ast.KindPropertyAssignment, ast.KindPropertyDeclaration:
			if name := ast.GetDeclarationName(parent); name != "" {
				return name
			}
		}
	}
	return "(anonymous)"
}

// branchMetrics computes cyclomatic complexity (1 + branching nodes) and a
// nesting-weighted cognitive-lite metric (+depth+1 at each branching node;
// +1 per short-circuit operator) over a function body, without descending
// into nested functions.
func branchMetrics(body *ast.Node) (cyclomatic int, cognitive int) {
	cyclomatic = 1
	var visit func(node *ast.Node, depth int)
	visit = func(node *ast.Node, depth int) {
		node.ForEachChild(func(child *ast.Node) bool {
			if isAnalyzableFunction(child) {
				return false // nested functions are measured separately
			}
			childDepth := depth
			switch child.Kind {
			case ast.KindIfStatement, ast.KindConditionalExpression, ast.KindCaseClause, ast.KindCatchClause,
				ast.KindForStatement, ast.KindForInStatement, ast.KindForOfStatement,
				ast.KindWhileStatement, ast.KindDoStatement:
				cyclomatic++
				cognitive += depth + 1
				childDepth = depth + 1
			case ast.KindBinaryExpression:
				switch child.AsBinaryExpression().OperatorToken.Kind {
				case ast.KindAmpersandAmpersandToken, ast.KindBarBarToken, ast.KindQuestionQuestionToken,
					ast.KindAmpersandAmpersandEqualsToken, ast.KindBarBarEqualsToken, ast.KindQuestionQuestionEqualsToken:
					cyclomatic++
					cognitive++
				}
			}
			visit(child, childDepth)
			return false
		})
	}
	visit(body, 0)
	return cyclomatic, cognitive
}

// ---------------------------------------------------------------------------
// analyze exhaustiveness

type exhaustivenessFlags struct {
	strict bool
}

// ExhaustivenessRow is one switch with uncovered union variants.
type ExhaustivenessRow struct {
	File         string   `json:"file"`
	Line         int      `json:"line"`
	Discriminant string   `json:"discriminant"`
	Missing      []string `json:"missing"`
	HasDefault   bool     `json:"hasDefault"`
	Status       string   `json:"status"` // missing | covered-by-default
}

// ExhaustivenessResult is the `analyze exhaustiveness` result.
type ExhaustivenessResult struct {
	Rows []*ExhaustivenessRow
}

var _ cli.Lister = (*ExhaustivenessResult)(nil)

func (r *ExhaustivenessResult) Total() int     { return len(r.Rows) }
func (r *ExhaustivenessResult) Item(i int) any { return r.Rows[i] }

func (r *ExhaustivenessResult) WriteItemText(w io.Writer, item any) error {
	row := item.(*ExhaustivenessRow)
	defaultNote := ""
	if row.HasDefault {
		defaultNote = " (has default)"
	}
	_, err := fmt.Fprintf(w, "%s:%d  switch (%s)  %s: %s%s\n",
		row.File, row.Line, row.Discriminant, row.Status, strings.Join(row.Missing, ", "), defaultNote)
	return err
}

func runAnalyzeExhaustiveness(ctx context.Context, ws *core.Workspace, flags *exhaustivenessFlags, args []string) (*ExhaustivenessResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	result := &ExhaustivenessResult{}
	for _, file := range files {
		switches := switchStatementsOf(file)
		if len(switches) == 0 {
			continue
		}
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		for _, sw := range switches {
			if row := exhaustivenessOf(ws, fileChecker, file, sw, flags.strict); row != nil {
				result.Rows = append(result.Rows, row)
			}
		}
		done()
	}
	return result, nil
}

func switchStatementsOf(file *ast.SourceFile) []*ast.Node {
	var switches []*ast.Node
	var visit ast.Visitor
	visit = func(node *ast.Node) bool {
		if node.Kind == ast.KindSwitchStatement {
			switches = append(switches, node)
		}
		node.ForEachChild(visit)
		return false
	}
	file.AsNode().ForEachChild(visit)
	return switches
}

// exhaustivenessOf checks one switch: when the discriminant is a union of
// string/number/boolean/enum literals, it diffs the union members against the
// covered case values and reports the missing ones. c must be the file's
// checker; only strings escape.
func exhaustivenessOf(ws *core.Workspace, c *checker.Checker, file *ast.SourceFile, sw *ast.Node, strict bool) *ExhaustivenessRow {
	switchStatement := sw.AsSwitchStatement()
	discriminantType := c.GetTypeAtLocation(switchStatement.Expression)
	if discriminantType == nil || discriminantType.Flags()&checker.TypeFlagsUnion == 0 {
		return nil
	}
	members := discriminantType.Types()
	memberDisplays := make([]string, 0, len(members))
	for _, member := range members {
		if member.Flags()&checker.TypeFlagsLiteral == 0 {
			return nil // not a pure literal/enum-member union
		}
		memberDisplays = append(memberDisplays, c.TypeToString(member))
	}

	covered := make(map[string]bool)
	hasDefault := false
	for _, clause := range switchStatement.CaseBlock.AsCaseBlock().Clauses.Nodes {
		if clause.Kind == ast.KindDefaultClause {
			hasDefault = true
			continue
		}
		expr := clause.AsCaseOrDefaultClause().Expression
		caseType := c.GetTypeAtLocation(expr)
		switch {
		case caseType == nil:
			start := scanner.SkipTrivia(file.Text(), expr.Pos())
			covered[strings.TrimSpace(file.Text()[start:expr.End()])] = true
		case caseType.Flags()&checker.TypeFlagsUnion != 0:
			for _, member := range caseType.Types() {
				covered[c.TypeToString(member)] = true
			}
		default:
			covered[c.TypeToString(caseType)] = true
		}
	}

	var missing []string
	for _, display := range memberDisplays {
		if !covered[display] {
			missing = append(missing, display)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	status := "missing"
	if hasDefault && !strict {
		status = "covered-by-default"
	}
	exprStart := scanner.SkipTrivia(file.Text(), switchStatement.Expression.Pos())
	line, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(sw, file, false /*includeJSDoc*/))
	return &ExhaustivenessRow{
		File:         ws.RelPath(file.FileName()),
		Line:         line,
		Discriminant: strings.TrimSpace(file.Text()[exprStart:switchStatement.Expression.End()]),
		Missing:      missing,
		HasDefault:   hasDefault,
		Status:       status,
	}
}
