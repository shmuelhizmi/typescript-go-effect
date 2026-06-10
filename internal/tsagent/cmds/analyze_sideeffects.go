package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// analyze side-effects (§4.5): purity verdicts. Function mode walks a
// function body transitively (--depth call levels) collecting impure
// evidence; module mode (--module) classifies whether a file's top level is
// safe to tree-shake.

func init() {
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "side-effects",
		Summary:      "Purity verdict per function (transitive) or per module (--module)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &sideEffectsFlags{}
			fs.BoolVar(&f.module, "module", false, "module mode: classify top-level side effects of files")
			fs.IntVar(&f.depth, "depth", 3, "call levels to follow in function mode")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeSideEffects(ctx, ws, flags.(*sideEffectsFlags), args)
		},
	})
}

type sideEffectsFlags struct {
	module bool
	depth  int
}

// PurityEvidence is one impure (or unknown) operation reached from a function
// or module top level.
type PurityEvidence struct {
	Kind   string   `json:"kind"` // mutates-nonlocal | param-mutation | impure-builtin | io-import | unknown-call | depth-limit | throws | top-level-statement | eager-call
	Detail string   `json:"detail"`
	Loc    string   `json:"loc"` // file:line
	Via    []string `json:"via,omitempty"`
}

// FunctionPurity is the verdict for one function target.
type FunctionPurity struct {
	SymbolID string            `json:"symbolId,omitempty"`
	Name     string            `json:"name"`
	File     string            `json:"file"`
	Line     int               `json:"line"`
	Verdict  string            `json:"verdict"` // pure | impure | unknown
	Throws   bool              `json:"throws,omitempty"`
	Evidence []*PurityEvidence `json:"evidence"`
}

// ModulePurity is the verdict for one file's top level.
type ModulePurity struct {
	File            string            `json:"file"`
	SafeToTreeShake bool              `json:"safeToTreeShake"`
	Evidence        []*PurityEvidence `json:"evidence"`
}

// SideEffectsResult is the `analyze side-effects` result.
type SideEffectsResult struct {
	Functions []*FunctionPurity `json:"functions,omitempty"`
	Modules   []*ModulePurity   `json:"modules,omitempty"`
}

var _ cli.Texter = (*SideEffectsResult)(nil)

func (r *SideEffectsResult) WriteText(w io.Writer) error {
	writeEvidence := func(evidence []*PurityEvidence) error {
		for _, e := range evidence {
			via := ""
			if len(e.Via) > 0 {
				via = "  via " + strings.Join(e.Via, " -> ")
			}
			if _, err := fmt.Fprintf(w, "  %s  %s  %s%s\n", e.Kind, e.Detail, e.Loc, via); err != nil {
				return err
			}
		}
		return nil
	}
	for _, f := range r.Functions {
		throws := ""
		if f.Throws {
			throws = " [throws]"
		}
		if _, err := fmt.Fprintf(w, "%s  %s:%d  %s%s\n", f.Name, f.File, f.Line, f.Verdict, throws); err != nil {
			return err
		}
		if err := writeEvidence(f.Evidence); err != nil {
			return err
		}
	}
	for _, m := range r.Modules {
		verdict := "safe-to-tree-shake"
		if !m.SafeToTreeShake {
			verdict = "has-top-level-side-effects"
		}
		if _, err := fmt.Fprintf(w, "%s  %s\n", m.File, verdict); err != nil {
			return err
		}
		if err := writeEvidence(m.Evidence); err != nil {
			return err
		}
	}
	return nil
}

func runAnalyzeSideEffects(ctx context.Context, ws *core.Workspace, flags *sideEffectsFlags, args []string) (*SideEffectsResult, error) {
	if flags.module {
		return runModuleSideEffects(ws, args)
	}
	if len(args) == 0 {
		return nil, cli.UsageErrorf("analyze side-effects requires function targets (file:line:col, symbol id, or name), or --module with file paths")
	}
	result := &SideEffectsResult{Functions: []*FunctionPurity{}}
	for _, arg := range args {
		target, err := ws.ResolveTarget(ctx, analyzeTargetSpec(arg))
		if err != nil {
			return nil, err
		}
		fn, err := sideEffectsFunctionOf(target)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", arg, err)
		}
		result.Functions = append(result.Functions, analyzeFunctionPurity(ctx, ws, fn, flags.depth))
	}
	return result, nil
}

// analyzeTargetSpec classifies a positional target: symbol id (contains '#'
// or 'path@pos'), file:line:col position, or bare declaration name.
func analyzeTargetSpec(arg string) core.TargetSpec {
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

// sideEffectsFunctionOf finds the function-like node (with a body) behind a
// resolved target, looking through variable declarations holding function
// expressions and through enclosing declarations of a position.
func sideEffectsFunctionOf(target *core.Target) (*ast.Node, error) {
	candidates := []*ast.Node{}
	if target.Symbol != nil {
		if target.Symbol.ValueDeclaration != nil {
			candidates = append(candidates, target.Symbol.ValueDeclaration)
		}
		candidates = append(candidates, target.Symbol.Declarations...)
	}
	if target.Node != nil {
		candidates = append(candidates, target.Node)
	}
	for _, cand := range candidates {
		if fn := functionLikeOfDeclaration(cand); fn != nil {
			return fn, nil
		}
	}
	// Position targets land on tokens: walk up to the enclosing function.
	if target.Node != nil {
		for node := target.Node; node != nil; node = node.Parent {
			if isAnalyzableFunction(node) && node.Body() != nil {
				return node, nil
			}
		}
	}
	return nil, fmt.Errorf("target is not a function with a body: %w", core.ErrInvalidArgument)
}

func functionLikeOfDeclaration(decl *ast.Node) *ast.Node {
	if decl == nil {
		return nil
	}
	if isAnalyzableFunction(decl) && decl.Body() != nil {
		return decl
	}
	if decl.Kind == ast.KindVariableDeclaration || decl.Kind == ast.KindPropertyDeclaration || decl.Kind == ast.KindPropertyAssignment {
		if init := decl.Initializer(); init != nil && isAnalyzableFunction(init) && init.Body() != nil {
			return init
		}
	}
	return nil
}

// impureGlobalRoots are global objects whose member calls and member writes
// are I/O or nondeterminism by definition.
var impureGlobalRoots = map[string]bool{
	"console": true,
	"process": true,
}

// impureGlobalCalls are exact member calls on otherwise-pure globals.
var impureGlobalCalls = map[string]bool{
	"Math.random": true,
	"Date.now":    true,
}

// impureGlobalFunctions are bare global functions that perform I/O.
var impureGlobalFunctions = map[string]bool{
	"fetch": true,
}

// impureImportModules are node builtins whose imported functions are treated
// as I/O without further analysis.
var impureImportModules = map[string]bool{
	"fs": true, "node:fs": true,
	"fs/promises": true, "node:fs/promises": true,
	"child_process": true, "node:child_process": true,
	"http": true, "node:http": true,
	"https": true, "node:https": true,
	"net": true, "node:net": true,
}

// purityWalker carries the state of one transitive purity analysis.
type purityWalker struct {
	ctx      context.Context
	ws       *core.Workspace
	visited  map[*ast.Node]bool
	evidence []*PurityEvidence
	throws   bool
	unknown  bool
}

func analyzeFunctionPurity(ctx context.Context, ws *core.Workspace, fn *ast.Node, depth int) *FunctionPurity {
	walker := &purityWalker{ctx: ctx, ws: ws, visited: map[*ast.Node]bool{fn: true}}
	walker.walkFunction(fn, depth, nil)

	file := ast.GetSourceFileOfNode(fn)
	pos := astnav.GetStartOfNode(fn, file, false /*includeJSDoc*/)
	if name := ast.GetNameOfDeclaration(fn); name != nil {
		pos = astnav.GetStartOfNode(name, file, false /*includeJSDoc*/)
	}
	line, _ := ws.PosToLineCol(file, pos)

	verdict := "pure"
	switch {
	case len(walker.evidence) > 0 && !onlyUnknownEvidence(walker.evidence):
		verdict = "impure"
	case walker.unknown:
		verdict = "unknown"
	}
	return &FunctionPurity{
		SymbolID: core.EncodeSymbolID(ws, fn.Symbol()),
		Name:     functionDisplayName(fn),
		File:     ws.RelPath(file.FileName()),
		Line:     line,
		Verdict:  verdict,
		Throws:   walker.throws,
		Evidence: append([]*PurityEvidence{}, walker.evidence...),
	}
}

func onlyUnknownEvidence(evidence []*PurityEvidence) bool {
	for _, e := range evidence {
		if e.Kind != "unknown-call" && e.Kind != "depth-limit" {
			return false
		}
	}
	return true
}

// walkFunction scans one function body with its file's checker held, records
// direct impure evidence, then recurses into resolved project callees.
func (pw *purityWalker) walkFunction(fn *ast.Node, depth int, via []string) {
	file := ast.GetSourceFileOfNode(fn)
	c, done := pw.ws.Program.GetTypeCheckerForFile(pw.ctx, file)
	type callee struct {
		fn   *ast.Node
		name string
	}
	var callees []callee

	loc := func(node *ast.Node) string {
		line, _ := pw.ws.PosToLineCol(file, astnav.GetStartOfNode(node, file, false /*includeJSDoc*/))
		return fmt.Sprintf("%s:%d", pw.ws.RelPath(file.FileName()), line)
	}
	snippet := func(node *ast.Node) string {
		start := scanner.SkipTrivia(file.Text(), node.Pos())
		return cli.Truncate(strings.Join(strings.Fields(file.Text()[start:node.End()]), " "), 60)
	}
	add := func(kind string, node *ast.Node, detail string) {
		pw.evidence = append(pw.evidence, &PurityEvidence{Kind: kind, Detail: detail, Loc: loc(node), Via: via})
	}

	var visit ast.Visitor
	visit = func(node *ast.Node) bool {
		switch node.Kind {
		case ast.KindThrowStatement:
			pw.throws = true
		case ast.KindBinaryExpression:
			bin := node.AsBinaryExpression()
			if ast.IsAssignmentOperator(bin.OperatorToken.Kind) {
				pw.classifyWrite(c, fn, bin.Left, add, snippet)
			}
		case ast.KindPrefixUnaryExpression:
			unary := node.AsPrefixUnaryExpression()
			if unary.Operator == ast.KindPlusPlusToken || unary.Operator == ast.KindMinusMinusToken {
				pw.classifyWrite(c, fn, unary.Operand, add, snippet)
			}
		case ast.KindPostfixUnaryExpression:
			unary := node.AsPostfixUnaryExpression()
			if unary.Operator == ast.KindPlusPlusToken || unary.Operator == ast.KindMinusMinusToken {
				pw.classifyWrite(c, fn, unary.Operand, add, snippet)
			}
		case ast.KindDeleteExpression:
			pw.classifyWrite(c, fn, node.Expression(), add, snippet)
		case ast.KindNewExpression:
			if expr := node.Expression(); expr.Kind == ast.KindIdentifier && expr.Text() == "Date" {
				add("impure-builtin", node, "new Date()")
			}
		case ast.KindCallExpression:
			expr := node.Expression()
			switch pw.classifyCallee(c, file, expr) {
			case "impure":
				add("impure-builtin", node, snippet(expr)+"()")
			case "io-import":
				add("io-import", node, snippet(expr)+"()")
			case "project":
				calleeFn, calleeName := pw.resolveProjectCallee(c, expr)
				if calleeFn != nil {
					callees = append(callees, callee{fn: calleeFn, name: calleeName})
				} else {
					pw.unknown = true
					add("unknown-call", node, snippet(expr)+"()")
				}
			}
		}
		node.ForEachChild(visit)
		return false
	}
	fn.Body().ForEachChild(visit)
	done()

	for _, target := range callees {
		if pw.visited[target.fn] {
			continue
		}
		pw.visited[target.fn] = true
		if depth <= 1 {
			pw.unknown = true
			pw.evidence = append(pw.evidence, &PurityEvidence{
				Kind:   "depth-limit",
				Detail: target.name + " not analyzed (--depth exhausted)",
				Loc:    nodeFileLine(pw.ws, target.fn),
				Via:    via,
			})
			continue
		}
		pw.walkFunction(target.fn, depth-1, append(append([]string{}, via...), target.name))
	}
}

// classifyWrite records evidence when the written-to root binding is declared
// outside fn (mutates-nonlocal) or is a property write through a parameter of
// fn (param-mutation). Local writes are pure.
func (pw *purityWalker) classifyWrite(c *checker.Checker, fn *ast.Node, lhs *ast.Node, add func(kind string, node *ast.Node, detail string), snippet func(*ast.Node) string) {
	root, isPropertyWrite := assignmentRoot(lhs)
	if root == nil || root.Kind != ast.KindIdentifier {
		return
	}
	if impureGlobalRoots[root.Text()] {
		add("impure-builtin", lhs, snippet(lhs))
		return
	}
	symbol := c.GetSymbolAtLocation(root)
	if symbol == nil || symbol.ValueDeclaration == nil {
		return
	}
	decl := symbol.ValueDeclaration
	declaredInside := ast.GetSourceFileOfNode(decl) == ast.GetSourceFileOfNode(fn) &&
		decl.Pos() >= fn.Pos() && decl.End() <= fn.End()
	if !declaredInside {
		add("mutates-nonlocal", lhs, snippet(lhs))
		return
	}
	if isPropertyWrite && decl.Kind == ast.KindParameter {
		add("param-mutation", lhs, snippet(lhs))
	}
}

// assignmentRoot unwraps property/element accesses (and parens/non-null) to
// the root expression of an assignment target, reporting whether the write
// goes through at least one member access.
func assignmentRoot(lhs *ast.Node) (root *ast.Node, isPropertyWrite bool) {
	node := lhs
	for {
		switch node.Kind {
		case ast.KindPropertyAccessExpression, ast.KindElementAccessExpression:
			isPropertyWrite = true
			node = node.Expression()
		case ast.KindParenthesizedExpression, ast.KindNonNullExpression:
			node = node.Expression()
		default:
			return node, isPropertyWrite
		}
	}
}

// classifyCallee buckets a call's callee expression: "impure" (builtin
// denylist), "io-import" (imported from a node I/O builtin), "pure-builtin"
// (ignorable), or "project" (resolve and recurse).
func (pw *purityWalker) classifyCallee(c *checker.Checker, file *ast.SourceFile, expr *ast.Node) string {
	switch expr.Kind {
	case ast.KindIdentifier:
		if impureGlobalFunctions[expr.Text()] {
			return "impure"
		}
		if pw.isImportedFromImpureModule(c, expr) {
			return "io-import"
		}
		return "project"
	case ast.KindPropertyAccessExpression:
		access := expr.AsPropertyAccessExpression()
		base := access.Expression
		if base.Kind == ast.KindIdentifier {
			if impureGlobalRoots[base.Text()] {
				return "impure"
			}
			if impureGlobalCalls[base.Text()+"."+access.Name().Text()] {
				return "impure"
			}
			if pw.isImportedFromImpureModule(c, base) {
				return "io-import"
			}
		}
		return "project"
	}
	return "project"
}

// isImportedFromImpureModule reports whether an identifier binds to an import
// from one of the denylisted node I/O modules.
func (pw *purityWalker) isImportedFromImpureModule(c *checker.Checker, ident *ast.Node) bool {
	symbol := c.GetSymbolAtLocation(ident)
	if symbol == nil {
		return false
	}
	for _, decl := range symbol.Declarations {
		switch decl.Kind {
		case ast.KindImportSpecifier, ast.KindNamespaceImport, ast.KindImportClause:
			importDecl := ast.FindAncestor(decl, func(n *ast.Node) bool { return n.Kind == ast.KindImportDeclaration })
			if importDecl == nil {
				continue
			}
			spec := importDecl.AsImportDeclaration().ModuleSpecifier
			if spec != nil && ast.IsStringLiteral(spec) && impureImportModules[spec.Text()] {
				return true
			}
		}
	}
	return false
}

// resolveProjectCallee resolves a callee expression to a project function
// with a body (through aliases), or nil when unknown/external/bodyless.
func (pw *purityWalker) resolveProjectCallee(c *checker.Checker, expr *ast.Node) (*ast.Node, string) {
	symbol := c.GetSymbolAtLocation(expr)
	if symbol == nil {
		return nil, ""
	}
	if symbol.Flags&ast.SymbolFlagsAlias != 0 {
		if resolved := c.GetAliasedSymbol(symbol); resolved != nil {
			symbol = resolved
		}
	}
	candidates := symbol.Declarations
	if symbol.ValueDeclaration != nil {
		candidates = append([]*ast.Node{symbol.ValueDeclaration}, candidates...)
	}
	for _, decl := range candidates {
		fn := functionLikeOfDeclaration(decl)
		if fn == nil {
			continue
		}
		if !refactorIsProjectSourceNode(pw.ws, fn) {
			return nil, ""
		}
		return fn, symbol.Name
	}
	return nil, ""
}

func nodeFileLine(ws *core.Workspace, node *ast.Node) string {
	file := ast.GetSourceFileOfNode(node)
	line, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(node, file, false /*includeJSDoc*/))
	return fmt.Sprintf("%s:%d", ws.RelPath(file.FileName()), line)
}

// ---------------------------------------------------------------------------
// module mode

func runModuleSideEffects(ws *core.Workspace, args []string) (*SideEffectsResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	result := &SideEffectsResult{Modules: []*ModulePurity{}}
	for _, file := range files {
		result.Modules = append(result.Modules, moduleSideEffectsOf(ws, file))
	}
	return result, nil
}

// moduleSideEffectsOf classifies one file's top level: any statement that is
// not a declaration / import / export / type is a side effect, and variable
// initializers that eagerly invoke calls (IIFEs and friends) are flagged too.
func moduleSideEffectsOf(ws *core.Workspace, file *ast.SourceFile) *ModulePurity {
	m := &ModulePurity{File: ws.RelPath(file.FileName()), Evidence: []*PurityEvidence{}}
	loc := func(node *ast.Node) string {
		line, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(node, file, false /*includeJSDoc*/))
		return fmt.Sprintf("%s:%d", m.File, line)
	}
	snippet := func(node *ast.Node) string {
		start := scanner.SkipTrivia(file.Text(), node.Pos())
		return cli.Truncate(strings.Join(strings.Fields(file.Text()[start:node.End()]), " "), 60)
	}
	for _, statement := range file.Statements.Nodes {
		switch statement.Kind {
		case ast.KindImportDeclaration, ast.KindImportEqualsDeclaration, ast.KindExportDeclaration,
			ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindInterfaceDeclaration,
			ast.KindTypeAliasDeclaration, ast.KindEnumDeclaration, ast.KindModuleDeclaration,
			ast.KindEmptyStatement:
			// declaration-shaped: no top-level effect
		case ast.KindVariableStatement:
			for _, decl := range statement.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
				if init := decl.Initializer(); init != nil {
					if call := eagerCallIn(init); call != nil {
						m.Evidence = append(m.Evidence, &PurityEvidence{
							Kind: "eager-call", Detail: snippet(call), Loc: loc(call),
						})
					}
				}
			}
		default:
			m.Evidence = append(m.Evidence, &PurityEvidence{
				Kind: "top-level-statement", Detail: snippet(statement), Loc: loc(statement),
			})
		}
	}
	m.SafeToTreeShake = len(m.Evidence) == 0
	return m
}

// eagerCallIn finds the first call/new expression evaluated eagerly inside an
// initializer (not nested inside a function-like).
func eagerCallIn(init *ast.Node) *ast.Node {
	if isAnalyzableFunction(init) {
		return nil
	}
	if init.Kind == ast.KindCallExpression || init.Kind == ast.KindNewExpression {
		return init
	}
	var found *ast.Node
	var visit ast.Visitor
	visit = func(node *ast.Node) bool {
		if found != nil || isAnalyzableFunction(node) {
			return false
		}
		if node.Kind == ast.KindCallExpression || node.Kind == ast.KindNewExpression {
			found = node
			return false
		}
		node.ForEachChild(visit)
		return false
	}
	init.ForEachChild(visit)
	return found
}
