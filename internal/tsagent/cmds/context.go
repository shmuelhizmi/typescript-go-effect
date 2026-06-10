package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/binder"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// The context family produces paste-ready LLM context: prioritized source
// slices under a token budget, with a manifest of what was included and what
// was elided. Text output is the primary product — file-path headers, raw
// source (no code fences), and explicit elision markers.

const (
	defaultPackTokenBudget   = 4000
	defaultExpandTokenBudget = 2000
	defaultUsageExamples     = 3
	defaultMaxDependents     = 10
	// Type-dep declarations longer than this many lines are reduced to their
	// header plus first-level member signatures.
	maxFullTypeDepLines = 40
	// Cap on a single reconstructed member-signature line.
	maxMemberSigLen = 160
)

func init() {
	cli.Register(cli.Command{
		Family:       "context",
		Name:         "pack",
		Summary:      "Budgeted, prioritized source slices for symbols: declaration + type deps + usage examples",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &contextPackFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			fs.IntVar(&f.tokenBudget, "token-budget", defaultPackTokenBudget, "approximate token budget (tokens ≈ bytes/4)")
			fs.IntVar(&f.usageExamples, "usage-examples", defaultUsageExamples, "max usage examples per target (from other files)")
			fs.StringVar(&f.forMode, "for", "", "tune slice ordering: edit, review, or explain")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runContextPack(ctx, ws, flags.(*contextPackFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "context",
		Name:         "expand",
		Summary:      "Expand a position to its semantic enclosure (or ±N lines) plus the types used inside",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &contextExpandFlags{}
			fs.StringVar(&f.radius, "radius", "semantic", "expansion radius: semantic, or a line count N")
			fs.IntVar(&f.tokenBudget, "token-budget", defaultExpandTokenBudget, "approximate token budget (tokens ≈ bytes/4)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runContextExpand(ctx, ws, flags.(*contextExpandFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "context",
		Name:         "delta",
		Summary:      "Changed symbols since a git ref plus their blast radius, as ready-to-paste review context",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &contextDeltaFlags{}
			fs.StringVar(&f.since, "since", "", "git ref to diff the working tree against (required)")
			fs.IntVar(&f.maxDependents, "max-dependents", defaultMaxDependents, "max dependent files reported per changed symbol")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runContextDelta(ctx, ws, flags.(*contextDeltaFlags), args)
		},
	})
}

// ---------------------------------------------------------------------------
// shared infrastructure

// estimateTokens is the single token estimator used across the context
// family: tokens ≈ ceil(bytes/4).
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// ContextSlice is one source slice in a context result.
type ContextSlice struct {
	Kind      string `json:"kind"` // target | type-dep | usage | primary
	Name      string `json:"name"`
	File      string `json:"file"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	EstTokens int    `json:"estTokens"`
	Source    string `json:"source"`
	Note      string `json:"note,omitempty"`
}

// ManifestEntry describes one included slice in the manifest.
type ManifestEntry struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	File      string `json:"file"`
	Lines     string `json:"lines"`
	EstTokens int    `json:"estTokens"`
}

// ElidedEntry describes one slice dropped to fit the budget.
type ElidedEntry struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// ctxSourceLines returns the raw text of 1-based lines [startLine, endLine]
// (clamped), without the trailing newline.
func ctxSourceLines(file *ast.SourceFile, startLine int, endLine int) string {
	starts := file.ECMALineMap()
	startLine = max(startLine, 1)
	endLine = min(endLine, len(starts))
	if endLine < startLine {
		return ""
	}
	start := int(starts[startLine-1])
	end := len(file.Text())
	if endLine < len(starts) {
		end = int(starts[endLine])
	}
	return strings.TrimRight(file.Text()[start:end], "\r\n")
}

// sliceableNode widens a declaration node to the span worth emitting:
// variable declarations become their whole variable statement, and
// arrow/function expressions used as initializers become the enclosing
// statement/property.
func sliceableNode(node *ast.Node) *ast.Node {
	n := node
	for n.Parent != nil {
		switch {
		case n.Kind == ast.KindVariableDeclaration && n.Parent.Kind == ast.KindVariableDeclarationList:
			n = n.Parent
		case n.Kind == ast.KindVariableDeclarationList && n.Parent.Kind == ast.KindVariableStatement:
			n = n.Parent
		case (n.Kind == ast.KindArrowFunction || n.Kind == ast.KindFunctionExpression) &&
			(n.Parent.Kind == ast.KindVariableDeclaration || n.Parent.Kind == ast.KindPropertyAssignment):
			n = n.Parent
		default:
			return n
		}
	}
	return n
}

// declDisplayName names a declaration for manifests, looking through variable
// statements to the first declarator.
func declDisplayName(decl *ast.Node) string {
	if name := ast.GetDeclarationName(decl); name != "" {
		return name
	}
	if decl.Kind == ast.KindVariableStatement {
		declarations := decl.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes
		if len(declarations) > 0 {
			if name := ast.GetDeclarationName(declarations[0]); name != "" {
				return name
			}
		}
	}
	if decl.Kind == ast.KindConstructor {
		return "constructor"
	}
	return "(anonymous)"
}

// buildDeclSlice renders a declaration (JSDoc included, expanded to full
// lines). Type-dep slices of big classes/interfaces/enums are reduced to the
// header line plus first-level member signatures with an elision marker.
func buildDeclSlice(ws *core.Workspace, decl *ast.Node, kind string) *ContextSlice {
	node := sliceableNode(decl)
	file := ast.GetSourceFileOfNode(node)
	start := astnav.GetStartOfNode(node, file, true /*includeJSDoc*/)
	startLine, _ := ws.PosToLineCol(file, start)
	endLine, _ := ws.PosToLineCol(file, node.End())

	source := ""
	note := ""
	totalLines := endLine - startLine + 1
	if kind == "type-dep" && totalLines > maxFullTypeDepLines &&
		(decl.Kind == ast.KindClassDeclaration || decl.Kind == ast.KindInterfaceDeclaration || decl.Kind == ast.KindEnumDeclaration) {
		var elided int
		source, elided = headerOnlyDeclSource(file, decl, startLine, endLine)
		note = fmt.Sprintf("member bodies elided (%d lines)", elided)
	} else {
		source = ctxSourceLines(file, startLine, endLine)
	}

	return &ContextSlice{
		Kind:      kind,
		Name:      declDisplayName(decl),
		File:      ws.RelPath(file.FileName()),
		StartLine: startLine,
		EndLine:   endLine,
		EstTokens: estimateTokens(source),
		Source:    source,
		Note:      note,
	}
}

// headerOnlyDeclSource reconstructs a big class/interface/enum as its header
// line, one line per first-level member signature (bodies stripped), an
// elision marker, and a closing brace.
func headerOnlyDeclSource(file *ast.SourceFile, decl *ast.Node, startLine int, endLine int) (string, int) {
	var members []*ast.Node
	switch decl.Kind {
	case ast.KindClassDeclaration, ast.KindInterfaceDeclaration:
		members = decl.Members()
	case ast.KindEnumDeclaration:
		members = decl.AsEnumDeclaration().Members.Nodes
	}
	lines := []string{ctxSourceLines(file, startLine, startLine)}
	for _, member := range members {
		if sig := memberSignatureText(file, member); sig != "" {
			lines = append(lines, "  "+sig)
		}
	}
	elided := max((endLine-startLine+1)-len(lines)-1, 0)
	lines = append(lines, fmt.Sprintf("  … [%d lines elided]", elided), "}")
	return strings.Join(lines, "\n"), elided
}

// memberSignatureText renders one member as a single signature line: the
// member source up to (excluding) its body, whitespace-collapsed.
func memberSignatureText(file *ast.SourceFile, member *ast.Node) string {
	start := astnav.GetStartOfNode(member, file, false /*includeJSDoc*/)
	end := member.End()
	if ast.IsFunctionLike(member) {
		if body := member.Body(); body != nil {
			end = body.Pos()
		}
	}
	text := strings.Join(strings.Fields(file.Text()[start:end]), " ")
	if text == "" {
		return ""
	}
	if !strings.HasSuffix(text, ";") && !strings.HasSuffix(text, ",") {
		text += ";"
	}
	return cli.Truncate(text, maxMemberSigLen)
}

// collectTypeDeps walks root's AST, resolves every identifier through the
// checker, and returns the project-internal named type declarations
// (interface/type alias/class/enum) it references, deduped, in first-use
// order. exclude filters out the targets themselves and anything declared
// inside the walked span.
func collectTypeDeps(ctx context.Context, ws *core.Workspace, root *ast.Node, exclude func(*ast.Node) bool) []*ast.Node {
	file := ast.GetSourceFileOfNode(root)
	if file == nil {
		return nil
	}
	fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()

	var deps []*ast.Node
	seen := make(map[*ast.Node]bool)
	var visit func(node *ast.Node)
	visit = func(node *ast.Node) {
		node.ForEachChild(func(child *ast.Node) bool {
			if child.Kind == ast.KindIdentifier {
				if decl := resolveTypeDepDecl(ws, fileChecker, child); decl != nil && !seen[decl] && !exclude(decl) {
					seen[decl] = true
					deps = append(deps, decl)
				}
			}
			visit(child)
			return false
		})
	}
	visit(root)
	return deps
}

// resolveTypeDepDecl resolves an identifier to a named type declaration in a
// project (non-lib, non-node_modules) file, or nil.
func resolveTypeDepDecl(ws *core.Workspace, fileChecker *checker.Checker, ident *ast.Node) *ast.Node {
	symbol := fileChecker.GetSymbolAtLocation(ident)
	if symbol == nil {
		return nil
	}
	if symbol.Flags&ast.SymbolFlagsAlias != 0 {
		if resolved := fileChecker.GetAliasedSymbol(symbol); resolved != nil {
			symbol = resolved
		}
	}
	decl := symbol.ValueDeclaration
	if decl == nil && len(symbol.Declarations) > 0 {
		decl = symbol.Declarations[0]
	}
	if decl == nil {
		return nil
	}
	switch decl.Kind {
	case ast.KindInterfaceDeclaration, ast.KindTypeAliasDeclaration, ast.KindClassDeclaration, ast.KindEnumDeclaration:
	default:
		return nil
	}
	file := ast.GetSourceFileOfNode(decl)
	if file == nil || ws.Program.IsLibFile(file) || isInNodeModules(file.FileName()) {
		return nil
	}
	return decl
}

// enclosingStatementNode finds the nearest enclosing statement (a node whose
// parent is a source file, block, module block, or case/default clause).
func enclosingStatementNode(node *ast.Node) *ast.Node {
	for n := node; n != nil; n = n.Parent {
		parent := n.Parent
		if parent == nil {
			return n
		}
		switch parent.Kind {
		case ast.KindSourceFile, ast.KindBlock, ast.KindModuleBlock, ast.KindCaseClause, ast.KindDefaultClause:
			return n
		}
	}
	return node
}

// ctxIsImportRef reports whether a reference node sits inside an
// import/export declaration (not a useful usage example).
func ctxIsImportRef(node *ast.Node) bool {
	return ast.FindAncestor(node, func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindImportDeclaration, ast.KindImportEqualsDeclaration, ast.KindExportDeclaration, ast.KindJSDocImportTag:
			return true
		}
		return false
	}) != nil
}

// assembleContextBudget always includes the mandatory slices, then fills the
// remaining budget greedily group by group (skipping slices that do not fit
// but still trying later, smaller ones). Returns the final slice order, the
// manifest, the elisions, the running token total, and a note when the
// mandatory slices alone exceed the budget.
func assembleContextBudget(mandatory []*ContextSlice, groups [][]*ContextSlice, budget int) (out []*ContextSlice, included []*ManifestEntry, elided []*ElidedEntry, total int, note string) {
	elided = []*ElidedEntry{}
	for _, s := range mandatory {
		total += s.EstTokens
		out = append(out, s)
	}
	if total > budget {
		note = fmt.Sprintf("mandatory slices alone are ~%d tokens (budget %d); they are included anyway", total, budget)
	}
	for _, group := range groups {
		for _, s := range group {
			if total+s.EstTokens > budget {
				elided = append(elided, &ElidedEntry{
					Name:   s.Name,
					Reason: fmt.Sprintf("over token budget (%s, ~%d tokens)", s.Kind, s.EstTokens),
				})
				continue
			}
			total += s.EstTokens
			out = append(out, s)
		}
	}
	included = make([]*ManifestEntry, 0, len(out))
	for _, s := range out {
		included = append(included, &ManifestEntry{
			Kind:      s.Kind,
			Name:      s.Name,
			File:      s.File,
			Lines:     fmt.Sprintf("%d-%d", s.StartLine, s.EndLine),
			EstTokens: s.EstTokens,
		})
	}
	return out, included, elided, total, note
}

func writeContextSlicesText(w io.Writer, slices []*ContextSlice) error {
	for i, s := range slices {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "=== %s (lines %d-%d) ===\n%s\n", s.File, s.StartLine, s.EndLine, s.Source); err != nil {
			return err
		}
	}
	return nil
}

func writeContextFooterText(w io.Writer, estimated int, budget int, elided []*ElidedEntry, note string) error {
	if _, err := fmt.Fprintf(w, "\n~%d estimated tokens (budget %d)\n", estimated, budget); err != nil {
		return err
	}
	for _, e := range elided {
		if _, err := fmt.Fprintf(w, "elided: %s (%s)\n", e.Name, e.Reason); err != nil {
			return err
		}
	}
	if note != "" {
		if _, err := fmt.Fprintf(w, "note: %s\n", note); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// context pack

type contextPackFlags struct {
	symbol        string
	name          string
	tokenBudget   int
	usageExamples int
	forMode       string
}

// PackResult is the `context pack` result.
type PackResult struct {
	For             string           `json:"for,omitempty"`
	TokenBudget     int              `json:"tokenBudget"`
	EstimatedTokens int              `json:"estimatedTokens"`
	Slices          []*ContextSlice  `json:"slices"`
	Included        []*ManifestEntry `json:"included"`
	Elided          []*ElidedEntry   `json:"elided"`
	Note            string           `json:"note,omitempty"`
}

var _ cli.Texter = (*PackResult)(nil)

func (r *PackResult) WriteText(w io.Writer) error {
	if err := writeContextSlicesText(w, r.Slices); err != nil {
		return err
	}
	return writeContextFooterText(w, r.EstimatedTokens, r.TokenBudget, r.Elided, r.Note)
}

func runContextPack(ctx context.Context, ws *core.Workspace, flags *contextPackFlags, args []string) (*PackResult, error) {
	switch flags.forMode {
	case "", "edit", "review", "explain":
	default:
		return nil, cli.UsageErrorf("invalid --for %q (want edit, review, or explain)", flags.forMode)
	}
	if flags.tokenBudget < 1 {
		return nil, cli.UsageErrorf("--token-budget must be >= 1")
	}
	if flags.usageExamples < 0 {
		return nil, cli.UsageErrorf("--usage-examples must be >= 0")
	}

	targetDecls, err := resolvePackTargets(ctx, ws, flags, args)
	if err != nil {
		return nil, err
	}

	// Exclusion set: a dep never duplicates a target (or its widened span).
	targetSet := make(map[*ast.Node]bool, len(targetDecls)*2)
	for _, decl := range targetDecls {
		targetSet[decl] = true
		targetSet[sliceableNode(decl)] = true
	}

	// P1: full declaration source of each target.
	targetSlices := make([]*ContextSlice, 0, len(targetDecls))
	for _, decl := range targetDecls {
		targetSlices = append(targetSlices, buildDeclSlice(ws, decl, "target"))
	}

	// P2: project-internal named type declarations referenced inside the
	// targets, in first-use order, deduped across targets.
	var depSlices []*ContextSlice
	seenDeps := make(map[*ast.Node]bool)
	for _, decl := range targetDecls {
		root := sliceableNode(decl)
		deps := collectTypeDeps(ctx, ws, root, func(d *ast.Node) bool {
			if targetSet[d] || seenDeps[d] {
				return true
			}
			// Declared inside the target span: already part of P1.
			return ast.GetSourceFileOfNode(d) == ast.GetSourceFileOfNode(root) && d.Pos() >= root.Pos() && d.End() <= root.End()
		})
		for _, d := range deps {
			seenDeps[d] = true
			depSlices = append(depSlices, buildDeclSlice(ws, d, "type-dep"))
		}
	}

	// P3: usage examples from other files.
	var usageSlices []*ContextSlice
	for _, decl := range targetDecls {
		usageSlices = append(usageSlices, collectUsageSlices(ctx, ws, decl, flags.usageExamples)...)
	}

	// --for tweaks the P2/P3 fill order. Targets always come first:
	//   edit, review     → usages before type-deps
	//   explain, default → type-deps before usages (spec order P1→P2→P3)
	groups := [][]*ContextSlice{depSlices, usageSlices}
	if flags.forMode == "edit" || flags.forMode == "review" {
		groups = [][]*ContextSlice{usageSlices, depSlices}
	}

	slicesOut, included, elided, total, note := assembleContextBudget(targetSlices, groups, flags.tokenBudget)
	return &PackResult{
		For:             flags.forMode,
		TokenBudget:     flags.tokenBudget,
		EstimatedTokens: total,
		Slices:          slicesOut,
		Included:        included,
		Elided:          elided,
		Note:            note,
	}, nil
}

// resolvePackTargets resolves --symbol/--name plus positional targets. Each
// positional argument may be a symbol id (contains '#', or 'path@pos'), a
// file:line:col position, or a bare declaration name.
func resolvePackTargets(ctx context.Context, ws *core.Workspace, flags *contextPackFlags, args []string) ([]*ast.Node, error) {
	var specs []core.TargetSpec
	if flags.symbol != "" {
		specs = append(specs, core.TargetSpec{Symbol: flags.symbol})
	}
	if flags.name != "" {
		specs = append(specs, core.TargetSpec{Name: flags.name})
	}
	for _, arg := range args {
		specs = append(specs, packTargetSpec(arg))
	}
	if len(specs) == 0 {
		return nil, cli.UsageErrorf("context pack requires at least one target (file:line:col, symbol id, name, or --symbol/--name)")
	}

	var decls []*ast.Node
	seen := make(map[*ast.Node]bool)
	for _, spec := range specs {
		target, err := ws.ResolveTarget(ctx, spec)
		if err != nil {
			return nil, err
		}
		decl, err := contextTargetDecl(ws, target)
		if err != nil {
			return nil, err
		}
		if !seen[decl] {
			seen[decl] = true
			decls = append(decls, decl)
		}
	}
	return decls, nil
}

func packTargetSpec(arg string) core.TargetSpec {
	if strings.Contains(arg, "#") {
		return core.TargetSpec{Symbol: arg}
	}
	if _, _, _, err := core.ParsePosition(arg); err == nil {
		return core.TargetSpec{At: arg}
	}
	if at := strings.LastIndexByte(arg, '@'); at >= 0 {
		if _, err := strconv.Atoi(arg[at+1:]); err == nil {
			return core.TargetSpec{Symbol: arg}
		}
	}
	return core.TargetSpec{Name: arg}
}

// contextTargetDecl picks the declaration node for a resolved target: the
// symbol's primary declaration when available, otherwise the nearest
// enclosing declaration of the position.
func contextTargetDecl(ws *core.Workspace, target *core.Target) (*ast.Node, error) {
	if target.Symbol != nil {
		decl := target.Symbol.ValueDeclaration
		if decl == nil && len(target.Symbol.Declarations) > 0 {
			decl = target.Symbol.Declarations[0]
		}
		if decl != nil {
			return decl, nil
		}
	}
	if target.Node != nil {
		if decl := enclosingDeclarationNode(target.Node); decl != nil {
			return decl, nil
		}
	}
	line, col := ws.PosToLineCol(target.File, target.Pos)
	return nil, cli.NotFoundErrorf("no declaration at %s:%d:%d", ws.RelPath(target.File.FileName()), line, col)
}

func enclosingDeclarationNode(node *ast.Node) *ast.Node {
	for n := node; n != nil && n.Kind != ast.KindSourceFile; n = n.Parent {
		switch n.Kind {
		case ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindInterfaceDeclaration,
			ast.KindTypeAliasDeclaration, ast.KindEnumDeclaration, ast.KindModuleDeclaration,
			ast.KindMethodDeclaration, ast.KindConstructor, ast.KindGetAccessor, ast.KindSetAccessor,
			ast.KindVariableStatement:
			return n
		}
	}
	return nil
}

// collectUsageSlices finds up to maxExamples reference sites of decl in OTHER
// files, each rendered as the enclosing statement ±1 line. Import sites are
// skipped. Results are sorted by (file, line) for determinism.
func collectUsageSlices(ctx context.Context, ws *core.Workspace, decl *ast.Node, maxExamples int) []*ContextSlice {
	if maxExamples <= 0 {
		return nil
	}
	declFile := ast.GetSourceFileOfNode(decl)
	nameNode := ast.GetNameOfDeclaration(decl)
	if declFile == nil || nameNode == nil {
		return nil
	}
	name := declDisplayName(decl)
	pos := astnav.GetStartOfNode(nameNode, declFile, false /*includeJSDoc*/)
	entries := ws.LS.GetReferencedSymbolsForNode(ctx, pos, nameNode, ws.Program.GetSourceFiles())

	var candidates []*ContextSlice
	seen := make(map[string]bool)
	for _, entry := range entries {
		for _, ref := range entry.References() {
			if !ref.IsNodeEntry() {
				continue
			}
			node := ref.Node()
			refFile := ast.GetSourceFileOfNode(node)
			if refFile == nil || refFile == declFile || ctxIsImportRef(node) {
				continue
			}
			s := buildUsageSlice(ws, node, name)
			key := s.File + ":" + strconv.Itoa(s.StartLine)
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, s)
		}
	}
	slices.SortFunc(candidates, func(a, b *ContextSlice) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return a.StartLine - b.StartLine
	})
	if len(candidates) > maxExamples {
		candidates = candidates[:maxExamples]
	}
	return candidates
}

func buildUsageSlice(ws *core.Workspace, refNode *ast.Node, name string) *ContextSlice {
	file := ast.GetSourceFileOfNode(refNode)
	stmt := enclosingStatementNode(refNode)
	start := astnav.GetStartOfNode(stmt, file, false /*includeJSDoc*/)
	startLine, _ := ws.PosToLineCol(file, start)
	endLine, _ := ws.PosToLineCol(file, stmt.End())
	startLine = max(1, startLine-1)
	endLine = min(len(file.ECMALineMap()), endLine+1)
	source := ctxSourceLines(file, startLine, endLine)
	return &ContextSlice{
		Kind:      "usage",
		Name:      name,
		File:      ws.RelPath(file.FileName()),
		StartLine: startLine,
		EndLine:   endLine,
		EstTokens: estimateTokens(source),
		Source:    source,
	}
}

// ---------------------------------------------------------------------------
// context expand

type contextExpandFlags struct {
	radius      string
	tokenBudget int
}

// ExpandResult is the `context expand` result: the primary slice followed by
// the type-dep slices that fit the budget.
type ExpandResult struct {
	Radius          string           `json:"radius"`
	TokenBudget     int              `json:"tokenBudget"`
	EstimatedTokens int              `json:"estimatedTokens"`
	Slices          []*ContextSlice  `json:"slices"`
	Included        []*ManifestEntry `json:"included"`
	Elided          []*ElidedEntry   `json:"elided"`
	Note            string           `json:"note,omitempty"`
}

var _ cli.Texter = (*ExpandResult)(nil)

func (r *ExpandResult) WriteText(w io.Writer) error {
	if err := writeContextSlicesText(w, r.Slices); err != nil {
		return err
	}
	return writeContextFooterText(w, r.EstimatedTokens, r.TokenBudget, r.Elided, r.Note)
}

func runContextExpand(ctx context.Context, ws *core.Workspace, flags *contextExpandFlags, args []string) (*ExpandResult, error) {
	if flags.tokenBudget < 1 {
		return nil, cli.UsageErrorf("--token-budget must be >= 1")
	}
	if len(args) != 1 {
		return nil, cli.UsageErrorf("context expand takes exactly one file:line[:col] target")
	}
	numericRadius := -1
	if flags.radius != "semantic" {
		n, err := strconv.Atoi(flags.radius)
		if err != nil || n < 0 {
			return nil, cli.UsageErrorf("invalid --radius %q (want semantic or a line count)", flags.radius)
		}
		numericRadius = n
	}

	fileName, line, col, err := parseExpandTarget(args[0])
	if err != nil {
		return nil, err
	}
	file, err := ws.FileOf(fileName)
	if err != nil {
		return nil, err
	}
	totalLines := len(file.ECMALineMap())
	if line < 1 || line > totalLines {
		return nil, cli.UsageErrorf("line %d out of range for %s (1-%d)", line, ws.RelPath(file.FileName()), totalLines)
	}
	binder.BindSourceFile(file)

	var primary *ContextSlice
	var depRoots []*ast.Node
	if numericRadius < 0 {
		pos, posErr := ws.LineColToPos(file, line, col)
		if posErr != nil {
			return nil, posErr
		}
		token := astnav.GetTouchingToken(file, pos)
		if token == nil {
			return nil, cli.NotFoundErrorf("no token at %s:%d:%d", ws.RelPath(file.FileName()), line, col)
		}
		span := semanticSpanNode(token)
		if span == nil {
			return nil, cli.NotFoundErrorf("no enclosing declaration or statement at %s:%d:%d", ws.RelPath(file.FileName()), line, col)
		}
		primary = buildDeclSlice(ws, span, "primary")
		depRoots = []*ast.Node{sliceableNode(span)}
	} else {
		startLine := max(1, line-numericRadius)
		endLine := min(totalLines, line+numericRadius)
		source := ctxSourceLines(file, startLine, endLine)
		primary = &ContextSlice{
			Kind:      "primary",
			Name:      fmt.Sprintf("%s:%d", ws.RelPath(file.FileName()), line),
			File:      ws.RelPath(file.FileName()),
			StartLine: startLine,
			EndLine:   endLine,
			EstTokens: estimateTokens(source),
			Source:    source,
		}
		depRoots = statementsInLineRange(ws, file, startLine, endLine)
	}

	// Type deps used inside the span, budget permitting.
	insideRoots := func(d *ast.Node) bool {
		for _, root := range depRoots {
			if d == root || (ast.GetSourceFileOfNode(d) == ast.GetSourceFileOfNode(root) && d.Pos() >= root.Pos() && d.End() <= root.End()) {
				return true
			}
		}
		return false
	}
	var depSlices []*ContextSlice
	seenDeps := make(map[*ast.Node]bool)
	for _, root := range depRoots {
		for _, d := range collectTypeDeps(ctx, ws, root, func(d *ast.Node) bool { return seenDeps[d] || insideRoots(d) }) {
			seenDeps[d] = true
			depSlices = append(depSlices, buildDeclSlice(ws, d, "type-dep"))
		}
	}

	slicesOut, included, elided, total, note := assembleContextBudget([]*ContextSlice{primary}, [][]*ContextSlice{depSlices}, flags.tokenBudget)
	return &ExpandResult{
		Radius:          flags.radius,
		TokenBudget:     flags.tokenBudget,
		EstimatedTokens: total,
		Slices:          slicesOut,
		Included:        included,
		Elided:          elided,
		Note:            note,
	}, nil
}

func parseExpandTarget(arg string) (fileName string, line int, col int, err error) {
	if f, l, c, perr := core.ParsePosition(arg); perr == nil {
		return f, l, c, nil
	}
	if i := strings.LastIndexByte(arg, ':'); i > 0 {
		if l, aerr := strconv.Atoi(arg[i+1:]); aerr == nil && l >= 1 {
			return arg[:i], l, 1, nil
		}
	}
	return "", 0, 0, cli.UsageErrorf("malformed target %q (want file:line or file:line:col)", arg)
}

// semanticSpanNode walks up from a token and returns the outermost enclosing
// function-like or class-like node, falling back to the statement at module
// scope.
func semanticSpanNode(token *ast.Node) *ast.Node {
	var fnOrClass *ast.Node
	var topStatement *ast.Node
	for n := token; n != nil && n.Kind != ast.KindSourceFile; n = n.Parent {
		if ast.IsFunctionLike(n) || ast.IsClassLike(n) {
			fnOrClass = n
		}
		if n.Parent != nil && n.Parent.Kind == ast.KindSourceFile {
			topStatement = n
		}
	}
	if fnOrClass != nil {
		return fnOrClass
	}
	return topStatement
}

// statementsInLineRange returns the top-level statements whose line span
// intersects [startLine, endLine].
func statementsInLineRange(ws *core.Workspace, file *ast.SourceFile, startLine int, endLine int) []*ast.Node {
	var roots []*ast.Node
	for _, st := range file.Statements.Nodes {
		stStart, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(st, file, false /*includeJSDoc*/))
		stEnd, _ := ws.PosToLineCol(file, st.End())
		if stStart <= endLine && stEnd >= startLine {
			roots = append(roots, st)
		}
	}
	return roots
}

// ---------------------------------------------------------------------------
// context delta

type contextDeltaFlags struct {
	since         string
	maxDependents int
}

// ChangedSymbol is one top-level declaration intersecting the diff.
type ChangedSymbol struct {
	SymbolID  string `json:"symbolId,omitempty"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	EstTokens int    `json:"estTokens"`
	Source    string `json:"source"`
}

// DeltaDependent is one file referencing a changed exported symbol.
type DeltaDependent struct {
	File              string   `json:"file"`
	Symbol            string   `json:"symbol"`
	ReferencesChanged []string `json:"referencesChanged"`
}

// DeltaResult is the `context delta` result.
type DeltaResult struct {
	Since           string            `json:"since"`
	ChangedSymbols  []*ChangedSymbol  `json:"changedSymbols"`
	Dependents      []*DeltaDependent `json:"dependents"`
	RemovedFiles    []string          `json:"removedFiles,omitempty"`
	EstimatedTokens int               `json:"estimatedTokens"`
}

var _ cli.Texter = (*DeltaResult)(nil)

func (r *DeltaResult) WriteText(w io.Writer) error {
	if len(r.ChangedSymbols) == 0 && len(r.RemovedFiles) == 0 {
		_, err := fmt.Fprintf(w, "no TypeScript changes since %s\n", r.Since)
		return err
	}
	depsBySymbol := make(map[string][]*DeltaDependent)
	for _, d := range r.Dependents {
		depsBySymbol[d.Symbol] = append(depsBySymbol[d.Symbol], d)
	}
	seenStatement := make(map[string]bool)
	for _, cs := range r.ChangedSymbols {
		key := fmt.Sprintf("%s:%d", cs.File, cs.StartLine)
		if !seenStatement[key] {
			seenStatement[key] = true
			if _, err := fmt.Fprintf(w, "=== %s (lines %d-%d) ===\n%s\n", cs.File, cs.StartLine, cs.EndLine, cs.Source); err != nil {
				return err
			}
		}
		if deps := depsBySymbol[cs.Name]; len(deps) > 0 {
			if _, err := fmt.Fprintf(w, "Dependents:\n"); err != nil {
				return err
			}
			for _, d := range deps {
				if _, err := fmt.Fprintf(w, "  %s: %s (uses %s)\n", d.File, strings.Join(d.ReferencesChanged, ", "), d.Symbol); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	if len(r.RemovedFiles) > 0 {
		if _, err := fmt.Fprintf(w, "Removed files:\n"); err != nil {
			return err
		}
		for _, f := range r.RemovedFiles {
			if _, err := fmt.Fprintf(w, "  %s\n", f); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "~%d estimated tokens\n", r.EstimatedTokens)
	return err
}

func runContextDelta(ctx context.Context, ws *core.Workspace, flags *contextDeltaFlags, args []string) (*DeltaResult, error) {
	if flags.since == "" {
		return nil, cli.UsageErrorf("context delta requires --since <git-ref>")
	}
	if flags.maxDependents < 1 {
		return nil, cli.UsageErrorf("--max-dependents must be >= 1")
	}
	if len(args) > 0 {
		return nil, cli.UsageErrorf("context delta takes no positional arguments")
	}

	// Resolve symlinks (e.g. /tmp → /private/tmp on macOS) so the project
	// root compares correctly against git's toplevel (same technique as
	// `api diff`).
	realRoot := tspath.NormalizePath(ws.FS.Realpath(ws.RootDir))
	gitTopRaw, err := gitOutput(ctx, realRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, cli.Errorf(cli.ExitFailed, "context delta requires a git work tree: %s is not inside one (%v)", ws.RootDir, err)
	}
	gitTop := tspath.NormalizePath(strings.TrimSpace(gitTopRaw))
	relDir := tspath.ConvertToRelativePath(realRoot, tspath.ComparePathsOptions{
		CurrentDirectory:          gitTop,
		UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
	})
	if strings.HasPrefix(relDir, "..") || tspath.IsRootedDiskPath(relDir) {
		return nil, cli.Errorf(cli.ExitFailed, "project root %s is not inside the git work tree %s", ws.RootDir, gitTop)
	}
	pathspec := relDir
	prefix := ""
	if relDir == "" || relDir == "." {
		pathspec = "."
	} else {
		prefix = relDir + "/"
	}

	diffText, err := gitOutput(ctx, gitTop, "diff", flags.since, "--", pathspec)
	if err != nil {
		return nil, cli.Errorf(cli.ExitFailed, "context delta: %v", err)
	}

	result := &DeltaResult{
		Since:          flags.since,
		ChangedSymbols: []*ChangedSymbol{},
		Dependents:     []*DeltaDependent{},
	}
	for _, change := range parseDiffNewLineRanges(diffText) {
		repoPath := change.path
		if change.isDelete {
			repoPath = change.oldPath
		}
		if !hasTSSourceExtension(repoPath) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(repoPath, prefix) {
			continue
		}
		rel := strings.TrimPrefix(repoPath, prefix)
		if change.isDelete {
			result.RemovedFiles = append(result.RemovedFiles, rel)
			continue
		}
		file := ws.Program.GetSourceFile(tspath.CombinePaths(ws.RootDir, rel))
		if file == nil {
			continue // changed on disk but not part of the program
		}
		binder.BindSourceFile(file)
		collectDeltaForFile(ctx, ws, file, change.ranges, flags.maxDependents, result)
	}

	slices.SortFunc(result.ChangedSymbols, func(a, b *ChangedSymbol) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.StartLine != b.StartLine {
			return a.StartLine - b.StartLine
		}
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(result.Dependents, func(a, b *DeltaDependent) int {
		if c := strings.Compare(a.Symbol, b.Symbol); c != 0 {
			return c
		}
		return strings.Compare(a.File, b.File)
	})
	slices.Sort(result.RemovedFiles)
	return result, nil
}

func hasTSSourceExtension(path string) bool {
	for _, ext := range []string{".ts", ".tsx", ".mts", ".cts"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// collectDeltaForFile maps changed line ranges onto the file's top-level
// declarations, appending changed symbols (with source slices) and, for
// exported ones, their cross-file dependents.
func collectDeltaForFile(ctx context.Context, ws *core.Workspace, file *ast.SourceFile, ranges [][2]int, maxDependents int, result *DeltaResult) {
	rel := ws.RelPath(file.FileName())
	for _, st := range file.Statements.Nodes {
		var declNodes []*ast.Node
		switch st.Kind {
		case ast.KindVariableStatement:
			declNodes = st.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes
		case ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindInterfaceDeclaration,
			ast.KindTypeAliasDeclaration, ast.KindEnumDeclaration, ast.KindModuleDeclaration:
			declNodes = []*ast.Node{st}
		default:
			continue
		}
		startLine, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(st, file, true /*includeJSDoc*/))
		endLine, _ := ws.PosToLineCol(file, st.End())
		if !lineRangesIntersect(ranges, startLine, endLine) {
			continue
		}
		source := ctxSourceLines(file, startLine, endLine)
		result.EstimatedTokens += estimateTokens(source)
		for _, decl := range declNodes {
			result.ChangedSymbols = append(result.ChangedSymbols, &ChangedSymbol{
				SymbolID:  core.EncodeSymbolID(ws, decl.Symbol()),
				Name:      declDisplayName(decl),
				Kind:      core.DeclarationKind(decl),
				File:      rel,
				StartLine: startLine,
				EndLine:   endLine,
				EstTokens: estimateTokens(source),
				Source:    source,
			})
			if core.IsExportedDeclaration(decl) {
				result.Dependents = append(result.Dependents, deltaDependentsFor(ctx, ws, decl, declDisplayName(decl), maxDependents)...)
			}
		}
	}
}

func lineRangesIntersect(ranges [][2]int, startLine int, endLine int) bool {
	for _, r := range ranges {
		if r[0] <= endLine && r[1] >= startLine {
			return true
		}
	}
	return false
}

// deltaDependentsFor finds references to a changed exported declaration in
// OTHER files (imports excluded) and groups the referencing declaration names
// per file, capped at maxDependents files.
func deltaDependentsFor(ctx context.Context, ws *core.Workspace, decl *ast.Node, symbolName string, maxDependents int) []*DeltaDependent {
	declFile := ast.GetSourceFileOfNode(decl)
	nameNode := ast.GetNameOfDeclaration(decl)
	if declFile == nil || nameNode == nil {
		return nil
	}
	pos := astnav.GetStartOfNode(nameNode, declFile, false /*includeJSDoc*/)
	entries := ws.LS.GetReferencedSymbolsForNode(ctx, pos, nameNode, ws.Program.GetSourceFiles())

	type fileAgg struct {
		names []string
		seen  map[string]bool
	}
	perFile := make(map[string]*fileAgg)
	var order []string
	for _, entry := range entries {
		for _, ref := range entry.References() {
			if !ref.IsNodeEntry() {
				continue
			}
			node := ref.Node()
			refFile := ast.GetSourceFileOfNode(node)
			if refFile == nil || refFile == declFile || ctxIsImportRef(node) {
				continue
			}
			fileRel := ws.RelPath(refFile.FileName())
			agg := perFile[fileRel]
			if agg == nil {
				if len(order) >= maxDependents {
					continue
				}
				agg = &fileAgg{seen: make(map[string]bool)}
				perFile[fileRel] = agg
				order = append(order, fileRel)
			}
			refName := enclosingNamedDeclName(node)
			if !agg.seen[refName] {
				agg.seen[refName] = true
				agg.names = append(agg.names, refName)
			}
		}
	}
	slices.Sort(order)
	dependents := make([]*DeltaDependent, 0, len(order))
	for _, fileRel := range order {
		dependents = append(dependents, &DeltaDependent{
			File:              fileRel,
			Symbol:            symbolName,
			ReferencesChanged: perFile[fileRel].names,
		})
	}
	return dependents
}

// enclosingNamedDeclName names the nearest enclosing named declaration of a
// reference node.
func enclosingNamedDeclName(node *ast.Node) string {
	for n := node.Parent; n != nil && n.Kind != ast.KindSourceFile; n = n.Parent {
		switch n.Kind {
		case ast.KindFunctionDeclaration, ast.KindMethodDeclaration, ast.KindClassDeclaration,
			ast.KindInterfaceDeclaration, ast.KindTypeAliasDeclaration, ast.KindEnumDeclaration,
			ast.KindVariableDeclaration, ast.KindPropertyDeclaration, ast.KindGetAccessor, ast.KindSetAccessor:
			if name := ast.GetDeclarationName(n); name != "" {
				return name
			}
		case ast.KindConstructor:
			return "constructor"
		}
	}
	return "(module scope)"
}

// ---------------------------------------------------------------------------
// unified-diff line-range extraction (new-file side)

type deltaFileChange struct {
	path     string // repo-relative new path ("/dev/null" for deletions)
	oldPath  string
	isNew    bool
	isDelete bool
	ranges   [][2]int // changed 1-based line ranges in the NEW file
}

// parseDiffNewLineRanges extracts per-file changed line ranges (new-file
// side) from `git diff` output by walking hunk bodies: only `+` lines (and
// the new-file position adjacent to deletions) are marked, so unchanged
// context lines never flag neighboring declarations. Hunk bodies are tracked
// by the remaining old/new line counts from the header, so content lines that
// look like headers (e.g. a deleted line starting with "--") cannot confuse
// the parser.
func parseDiffNewLineRanges(diffText string) []*deltaFileChange {
	var changes []*deltaFileChange
	var current *deltaFileChange
	lastOld := ""
	oldRemaining, newRemaining := 0, 0
	newLine := 0

	markChanged := func(line int) {
		line = max(line, 1)
		if n := len(current.ranges); n > 0 && line <= current.ranges[n-1][1]+1 {
			current.ranges[n-1][1] = max(current.ranges[n-1][1], line)
			return
		}
		current.ranges = append(current.ranges, [2]int{line, line})
	}

	for line := range strings.SplitSeq(diffText, "\n") {
		if current != nil && (oldRemaining > 0 || newRemaining > 0) {
			// Inside a hunk body.
			switch {
			case strings.HasPrefix(line, "+") && newRemaining > 0:
				markChanged(newLine)
				newLine++
				newRemaining--
			case strings.HasPrefix(line, "-") && oldRemaining > 0:
				// Deletion: mark the adjacent position in the new file.
				markChanged(newLine)
				oldRemaining--
			case strings.HasPrefix(line, "\\"):
				// "\ No newline at end of file"
			default: // context line (possibly trimmed to "")
				newLine++
				oldRemaining--
				newRemaining--
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "--- "):
			lastOld = stripDiffPathPrefix(line[len("--- "):])
		case strings.HasPrefix(line, "+++ "):
			newPath := stripDiffPathPrefix(line[len("+++ "):])
			current = &deltaFileChange{
				path:     newPath,
				oldPath:  lastOld,
				isNew:    lastOld == "/dev/null",
				isDelete: newPath == "/dev/null",
			}
			changes = append(changes, current)
		case strings.HasPrefix(line, "@@ ") && current != nil:
			oldCount, newStart, newCount, ok := parseHunkHeaderCounts(line)
			if !ok {
				continue
			}
			oldRemaining, newRemaining = oldCount, newCount
			newLine = newStart
			if newCount == 0 {
				// Pure deletion: newStart is the line BEFORE the removal.
				newLine = newStart + 1
			}
		}
	}
	return changes
}

func stripDiffPathPrefix(s string) string {
	if tab := strings.IndexByte(s, '\t'); tab >= 0 {
		s = s[:tab]
	}
	s = strings.TrimSpace(s)
	if s == "/dev/null" {
		return s
	}
	if rest, ok := strings.CutPrefix(s, "a/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(s, "b/"); ok {
		return rest
	}
	return s
}

// parseHunkHeaderCounts parses `@@ -oldStart[,oldCount] +newStart[,newCount] @@`.
func parseHunkHeaderCounts(line string) (oldCount int, newStart int, newCount int, ok bool) {
	rest, ok := strings.CutPrefix(line, "@@ ")
	if !ok {
		return 0, 0, 0, false
	}
	end := strings.Index(rest, " @@")
	if end < 0 {
		return 0, 0, 0, false
	}
	fields := strings.Fields(rest[:end])
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "-") || !strings.HasPrefix(fields[1], "+") {
		return 0, 0, 0, false
	}
	parseRange := func(spec string) (start int, count int, ok bool) {
		count = 1
		startText, countText, hasCount := strings.Cut(spec, ",")
		s, err := strconv.Atoi(startText)
		if err != nil {
			return 0, 0, false
		}
		if hasCount {
			c, cerr := strconv.Atoi(countText)
			if cerr != nil {
				return 0, 0, false
			}
			count = c
		}
		return s, count, true
	}
	_, oldCount, okOld := parseRange(fields[0][1:])
	newStart, newCount, okNew := parseRange(fields[1][1:])
	return oldCount, newStart, newCount, okOld && okNew
}
