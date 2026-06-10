package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
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
		Name:         "infer",
		Summary:      "Suggest types for unannotated parameters and returns (usage- and inference-based)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &inferFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "infer for a single function target (symbol ID or file:line:col) instead of paths")
			fs.BoolVar(&f.fixPlan, "fix-plan", false, "include an annotation edit set (for refactor apply-edits / check --with-edits)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runTypeInfer(ctx, ws, flags.(*inferFlags), args)
		},
	})
}

type inferFlags struct {
	symbol  string
	fixPlan bool
}

// InferSuggestion is one suggested annotation.
type InferSuggestion struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Col        int    `json:"col"`
	Kind       string `json:"kind"`    // param | return
	Target     string `json:"target"`  // parameter name, or the function name for returns
	Current    string `json:"current"` // implicit-any | none
	Suggested  string `json:"suggested"`
	Confidence string `json:"confidence"` // high | medium | low

	file *ast.SourceFile // fix-plan bookkeeping, not serialized
	pos  int             // insertion offset for `: T`
}

// InferFixEdit is one insertion in the fix plan (byte offsets; pos == end).
type InferFixEdit struct {
	Pos     int    `json:"pos"`
	End     int    `json:"end"`
	NewText string `json:"newText"`
}

// InferFixFile groups the insertions of one file.
type InferFixFile struct {
	File  string         `json:"file"`
	Edits []InferFixEdit `json:"edits"`
}

// InferFixPlan is the EditSet-shaped annotation plan (--fix-plan),
// consumable by `refactor apply-edits` and `check --with-edits`.
type InferFixPlan struct {
	Edits []InferFixFile `json:"edits"`
}

// InferResult is the `type infer` result.
type InferResult struct {
	Suggestions []*InferSuggestion `json:"suggestions"`
	FixPlan     *InferFixPlan      `json:"fixPlan,omitempty"`
}

var _ cli.Texter = (*InferResult)(nil)

func (r *InferResult) WriteText(w io.Writer) error {
	for _, s := range r.Suggestions {
		if _, err := fmt.Fprintf(w, "%s:%d:%d %s %s: %s -> %s  [%s]\n",
			s.File, s.Line, s.Col, s.Kind, s.Target, s.Current, s.Suggested, s.Confidence); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "total: %d suggestion(s)\n", len(r.Suggestions)); err != nil {
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

const maxSuggestedDisplayLen = 200

func runTypeInfer(ctx context.Context, ws *core.Workspace, flags *inferFlags, args []string) (*InferResult, error) {
	var functions []*ast.Node
	if flags.symbol != "" {
		if len(args) > 0 {
			return nil, cli.UsageErrorf("--symbol and path arguments are mutually exclusive")
		}
		target, err := ws.ResolveTarget(ctx, parseOperandSpec(flags.symbol))
		if err != nil {
			return nil, err
		}
		fn := functionDeclarationOf(target.Node)
		if fn == nil && target.Symbol != nil && target.Symbol.ValueDeclaration != nil {
			fn = functionDeclarationOf(target.Symbol.ValueDeclaration)
		}
		if fn == nil {
			return nil, cli.UsageErrorf("--symbol %s does not resolve to a function-like declaration", flags.symbol)
		}
		functions = append(functions, fn)
	} else {
		files, err := projectFiles(ws, args)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			functions = append(functions, collectFunctionLikes(file)...)
		}
	}

	result := &InferResult{Suggestions: []*InferSuggestion{}}
	for _, fn := range functions {
		suggestions, err := inferForFunction(ctx, ws, fn)
		if err != nil {
			return nil, err
		}
		result.Suggestions = append(result.Suggestions, suggestions...)
	}

	slices.SortStableFunc(result.Suggestions, func(a, b *InferSuggestion) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Col - b.Col
	})

	if flags.fixPlan {
		result.FixPlan = buildInferFixPlan(ws, result.Suggestions)
	}
	return result, nil
}

// functionDeclarationOf returns the function-like declaration for node: the
// node itself, its parent when node is the function's name, or the
// initializer when node is a variable declaration holding an arrow/function
// expression.
func functionDeclarationOf(node *ast.Node) *ast.Node {
	if ast.IsFunctionLikeDeclaration(node) {
		return node
	}
	if parent := node.Parent; parent != nil && ast.IsFunctionLikeDeclaration(parent) && parent.Name() == node {
		return parent
	}
	decl := node
	if parent := node.Parent; parent != nil && parent.Kind == ast.KindVariableDeclaration && parent.Name() == node {
		decl = parent
	}
	if decl.Kind == ast.KindVariableDeclaration {
		if initializer := decl.Initializer(); initializer != nil && ast.IsFunctionLikeDeclaration(initializer) {
			return initializer
		}
	}
	return nil
}

// collectFunctionLikes gathers every function-like declaration with a body
// in the file.
func collectFunctionLikes(file *ast.SourceFile) []*ast.Node {
	var functions []*ast.Node
	var visit ast.Visitor
	visit = func(node *ast.Node) bool {
		if ast.IsFunctionLikeDeclaration(node) && node.Body() != nil {
			functions = append(functions, node)
		}
		node.ForEachChild(visit)
		return false
	}
	file.AsNode().ForEachChild(visit)
	return functions
}

// inferForFunction produces return and parameter suggestions for one
// function-like declaration.
func inferForFunction(ctx context.Context, ws *core.Workspace, fn *ast.Node) ([]*InferSuggestion, error) {
	file := ast.GetSourceFileOfNode(fn)
	if file == nil {
		return nil, nil
	}
	binder.BindSourceFile(file)
	fnName := "(anonymous)"
	if name := fn.Name(); name != nil {
		fnName = name.Text()
	}

	var suggestions []*InferSuggestion

	// Unannotated parameters needing usage-based inference (collected first;
	// call-site types are computed afterwards, per call-site file).
	type pendingParam struct {
		param *ast.Node
		index int // value-parameter index (this excluded)
	}
	var pending []pendingParam

	c, done := ws.Program.GetTypeCheckerForFile(ctx, file)

	// (a) Unannotated return type.
	if fn.Type() == nil && isSignatureSupportingReturnAnnotationNode(fn) {
		if sig := c.GetSignatureFromDeclaration(fn); sig != nil {
			if ret := c.GetReturnTypeOfSignature(sig); ret != nil {
				display := c.TypeToStringEx(ret, fn, core.TypeDisplayFlags, nil)
				if ret.Flags()&(checker.TypeFlagsAny) == 0 && len(display) <= maxSuggestedDisplayLen {
					pos := returnAnnotationPosition(fn, file)
					line, col := ws.PosToLineCol(file, pos)
					suggestions = append(suggestions, &InferSuggestion{
						File: ws.RelPath(file.FileName()), Line: line, Col: col,
						Kind: "return", Target: fnName, Current: "none",
						Suggested: display, Confidence: "high",
						file: file, pos: pos,
					})
				}
			}
		}
	}

	// (b) Unannotated parameters.
	index := 0
	for _, param := range fn.Parameters() {
		if ast.IsThisParameter(param) {
			continue
		}
		i := index
		index++
		if param.Type() != nil || param.Name() == nil || param.Name().Kind != ast.KindIdentifier ||
			param.AsParameterDeclaration().DotDotDotToken != nil || param.Initializer() != nil {
			continue
		}
		// Contextual inference first (the inlay-hints technique): when the
		// parameter is context-sensitive its symbol's type at the
		// declaration is the contextually inferred type, not `any`.
		paramSymbol := param.Symbol()
		if paramSymbol != nil {
			t := c.GetTypeOfSymbolAtLocation(paramSymbol, param)
			if t != nil && t.Flags()&checker.TypeFlagsAny == 0 {
				display := c.TypeToStringEx(t, param, core.TypeDisplayFlags, nil)
				if len(display) <= maxSuggestedDisplayLen {
					pos := paramAnnotationPosition(param)
					line, col := ws.PosToLineCol(file, pos)
					suggestions = append(suggestions, &InferSuggestion{
						File: ws.RelPath(file.FileName()), Line: line, Col: col,
						Kind: "param", Target: param.Name().Text(), Current: "implicit-any",
						Suggested: display, Confidence: "high",
						file: file, pos: pos,
					})
				}
				continue
			}
		}
		pending = append(pending, pendingParam{param: param, index: i})
	}
	done()

	if len(pending) == 0 {
		return suggestions, nil
	}

	// Usage-based inference: union the (widened) types of the corresponding
	// argument at each call site.
	refNode := fn.Name()
	if refNode == nil {
		// Anonymous functions assigned to a variable are referenced through
		// the variable's name.
		if parent := fn.Parent; parent != nil && parent.Kind == ast.KindVariableDeclaration && parent.Name() != nil {
			refNode = parent.Name()
		}
	}
	if refNode == nil {
		return suggestions, nil
	}

	entries := ws.LS.GetReferencedSymbolsForNode(ctx, refNode.Pos(), refNode, ws.Program.GetSourceFiles())
	byFile := make(map[*ast.SourceFile][]*ast.Node) // call/new expressions per file
	var fileOrder []*ast.SourceFile
	for _, entry := range entries {
		for _, ref := range entry.References() {
			if !ref.IsNodeEntry() || ref.Node() == nil || ref.Node() == refNode {
				continue
			}
			site, kind := instantiationSite(ref.Node())
			if site == nil || kind != "call" {
				continue
			}
			siteFile := ast.GetSourceFileOfNode(site)
			if siteFile == nil {
				continue
			}
			if _, seen := byFile[siteFile]; !seen {
				fileOrder = append(fileOrder, siteFile)
			}
			byFile[siteFile] = append(byFile[siteFile], site)
		}
	}

	// argTypes[paramIndex] is the ordered set of distinct argument type
	// displays observed across call sites.
	argTypes := make(map[int][]string, len(pending))
	for _, siteFile := range fileOrder {
		siteChecker, siteDone := ws.Program.GetTypeCheckerForFile(ctx, siteFile)
		for _, call := range byFile[siteFile] {
			callArgs := call.Arguments()
			for _, p := range pending {
				if p.index >= len(callArgs) {
					continue
				}
				argType := siteChecker.GetTypeAtLocation(callArgs[p.index])
				if argType == nil {
					continue
				}
				display := siteChecker.TypeToStringEx(siteChecker.GetBaseTypeOfLiteralType(argType), callArgs[p.index], core.TypeDisplayFlags, nil)
				if !slices.Contains(argTypes[p.index], display) {
					argTypes[p.index] = append(argTypes[p.index], display)
				}
			}
		}
		siteDone()
	}

	for _, p := range pending {
		distinct := argTypes[p.index]
		if len(distinct) == 0 {
			continue
		}
		suggested, confidence := unionSuggestion(distinct)
		if len(suggested) > maxSuggestedDisplayLen {
			continue
		}
		pos := paramAnnotationPosition(p.param)
		line, col := ws.PosToLineCol(file, pos)
		suggestions = append(suggestions, &InferSuggestion{
			File: ws.RelPath(file.FileName()), Line: line, Col: col,
			Kind: "param", Target: p.param.Name().Text(), Current: "implicit-any",
			Suggested: suggested, Confidence: confidence,
			file: file, pos: pos,
		})
	}
	return suggestions, nil
}

// unionSuggestion turns the distinct call-site type displays into a
// suggested annotation: one consistent type is high confidence, a small
// union is medium, too many distinct types degrades to unknown/low.
func unionSuggestion(distinct []string) (suggested string, confidence string) {
	switch {
	case len(distinct) == 1:
		return distinct[0], "high"
	case len(distinct) <= 4:
		return strings.Join(distinct, " | "), "medium"
	default:
		return "unknown", "low"
	}
}

// isSignatureSupportingReturnAnnotationNode mirrors the LS inlay-hints
// predicate for declarations that accept a return type annotation.
func isSignatureSupportingReturnAnnotationNode(node *ast.Node) bool {
	switch node.Kind {
	case ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindArrowFunction,
		ast.KindMethodDeclaration, ast.KindGetAccessor:
		return true
	}
	return false
}

// paramAnnotationPosition is the byte offset where `: T` is inserted for a
// parameter: after the question token when present, otherwise after the
// name.
func paramAnnotationPosition(param *ast.Node) int {
	if q := param.QuestionToken(); q != nil {
		return q.End()
	}
	return param.Name().End()
}

// returnAnnotationPosition is the byte offset where `: T` is inserted for a
// return type: after the parameter list's close paren.
func returnAnnotationPosition(fn *ast.Node, file *ast.SourceFile) int {
	if closeParen := astnav.FindChildOfKind(fn, ast.KindCloseParenToken, file); closeParen != nil {
		return closeParen.End()
	}
	return fn.ParameterList().End()
}

// buildInferFixPlan converts the suggestions into a `{"edits": …}` EditSet
// (insertions only; pos == end).
func buildInferFixPlan(ws *core.Workspace, suggestions []*InferSuggestion) *InferFixPlan {
	perFile := make(map[string][]InferFixEdit)
	for _, s := range suggestions {
		if s.file == nil {
			continue
		}
		perFile[s.File] = append(perFile[s.File], InferFixEdit{Pos: s.pos, End: s.pos, NewText: ": " + s.Suggested})
	}
	plan := &InferFixPlan{Edits: []InferFixFile{}}
	for _, file := range slices.Sorted(maps.Keys(perFile)) {
		edits := perFile[file]
		slices.SortFunc(edits, func(a, b InferFixEdit) int { return a.Pos - b.Pos })
		plan.Edits = append(plan.Edits, InferFixFile{File: file, Edits: edits})
	}
	return plan
}
