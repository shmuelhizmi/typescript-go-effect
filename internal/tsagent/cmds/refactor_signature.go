package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// refactor signature (§4.4), v1: change a function's parameter list with the
// ops add / remove / reorder, updating every call site atomically. New
// required parameters need --fill-with (or a "default" literal in the op);
// otherwise the command refuses and lists the call sites.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "signature",
		Summary:      "Change a function signature (add/remove/reorder params), updating all call sites",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorSignatureFlags{}
			registerRefactorTargetFlags(fs, &f.target)
			registerRefactorTxFlags(fs, &f.tx)
			fs.StringVar(&f.ops, "ops", "", `ops JSON: [{"op":"add","name":"x","type":"number","default":"1","index":0},{"op":"remove","name":"x"},{"op":"reorder","order":[1,0]}]`)
			fs.StringVar(&f.fillWith, "fill-with", "", "expression inserted at call sites for new parameters without a default")
			fs.BoolVar(&f.force, "force", false, "remove parameters even when they are used in the body")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorSignature(ctx, ws, flags.(*refactorSignatureFlags), args)
		},
	})
}

type refactorSignatureFlags struct {
	target   refactorTargetFlags
	tx       refactorTxFlags
	ops      string
	fillWith string
	force    bool
}

type refactorSignatureOp struct {
	Op      string `json:"op"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Default string `json:"default,omitempty"`
	Index   *int   `json:"index,omitempty"`
	Order   []int  `json:"order,omitempty"`
}

// refactorSigParam is the working model of one parameter during op
// application.
type refactorSigParam struct {
	name string
	text string    // full parameter text in the declaration
	node *ast.Node // nil for newly added parameters
}

func runRefactorSignature(ctx context.Context, ws *core.Workspace, f *refactorSignatureFlags, args []string) (*core.TxResult, error) {
	if f.ops == "" {
		return nil, cli.UsageErrorf("--ops is required")
	}
	var ops []refactorSignatureOp
	if err := json.Unmarshal([]byte(f.ops), &ops); err != nil {
		return nil, cli.UsageErrorf("invalid --ops JSON: %v", err)
	}
	if len(ops) == 0 {
		return nil, cli.UsageErrorf("--ops contains no operations")
	}
	target, rest, err := resolveRefactorTarget(ctx, ws, &f.target, args)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, cli.UsageErrorf("unexpected extra arguments: %s", strings.Join(rest, " "))
	}
	symbol := target.Symbol
	if symbol == nil || len(symbol.Declarations) == 0 {
		return nil, cli.NotFoundErrorf("target does not resolve to a symbol with declarations")
	}
	decl, fn := refactorSignatureFunction(symbol.Declarations[0])
	if fn == nil {
		return nil, cli.RefusedErrorf("target is not a function/method declaration with a parameter list")
	}
	if len(symbol.Declarations) > 1 {
		return nil, cli.RefusedErrorf("symbol has %d declarations (overloads are not supported yet)", len(symbol.Declarations))
	}
	file := ast.GetSourceFileOfNode(fn)
	nameNode := ast.GetNameOfDeclaration(decl)
	if nameNode == nil {
		nameNode = ast.GetNameOfDeclaration(fn)
	}
	if nameNode == nil {
		return nil, cli.RefusedErrorf("the target function has no name to find call sites for")
	}

	// Parameter model.
	params := make([]refactorSigParam, 0, len(fn.Parameters()))
	for _, p := range fn.Parameters() {
		name := ""
		if p.Name() != nil && p.Name().Kind == ast.KindIdentifier {
			name = p.Name().Text()
		}
		params = append(params, refactorSigParam{name: name, text: refactorNodeText(file, p), node: p})
	}

	// Call sites.
	type callSite struct {
		call *ast.Node
		file *ast.SourceFile
		args []string // argument texts (mutated by ops)
	}
	var sites []callSite
	var notes []string
	otherUses := 0
	for _, ref := range refactorReferenceNodes(ctx, ws, nameNode) {
		if ref == nameNode {
			continue
		}
		parent := ref.Parent
		if parent != nil && (parent.Kind == ast.KindImportSpecifier || parent.Kind == ast.KindExportSpecifier) {
			continue
		}
		refFile := ast.GetSourceFileOfNode(ref)
		if refFile == file && ref.Pos() >= decl.Pos() && ref.End() <= decl.End() {
			continue
		}
		if parent == nil || (parent.Kind != ast.KindCallExpression && parent.Kind != ast.KindNewExpression) || parent.Expression() != ref {
			otherUses++
			continue
		}
		texts := make([]string, 0, len(parent.Arguments()))
		for _, arg := range parent.Arguments() {
			texts = append(texts, refactorNodeText(refFile, arg))
		}
		sites = append(sites, callSite{call: parent, file: refFile, args: texts})
	}
	if otherUses > 0 {
		notes = append(notes, fmt.Sprintf("%d non-call reference(s) to the function were left unchanged (callbacks, aliases); the diagnostics gate will catch breakage", otherUses))
	}

	siteLoc := func(s callSite) string { return refactorNodeLineCol(ws, s.call) }

	// Apply ops to the declaration model and every call site.
	for opIndex, op := range ops {
		switch op.Op {
		case "add":
			if op.Name == "" || op.Type == "" {
				return nil, cli.UsageErrorf("ops[%d]: add needs name and type", opIndex)
			}
			index := len(params)
			if op.Index != nil {
				if *op.Index < 0 || *op.Index > len(params) {
					return nil, cli.UsageErrorf("ops[%d]: index %d out of range (0-%d)", opIndex, *op.Index, len(params))
				}
				index = *op.Index
			}
			text := op.Name + ": " + op.Type
			if op.Default != "" {
				text += " = " + op.Default
			}
			params = slices.Insert(params, index, refactorSigParam{name: op.Name, text: text})

			fill := op.Default
			if f.fillWith != "" {
				fill = f.fillWith
			}
			for i := range sites {
				if index >= len(sites[i].args) && op.Default != "" {
					continue // appended optional-with-default: sites stay valid
				}
				if fill == "" {
					locs := make([]string, 0, len(sites))
					for _, s := range sites {
						locs = append(locs, siteLoc(s))
					}
					slices.Sort(locs)
					return nil, cli.RefusedErrorf("ops[%d]: new required parameter %q has no default and no --fill-with; %d call site(s) need a value:\n  %s",
						opIndex, op.Name, len(sites), strings.Join(slices.Compact(locs), "\n  "))
				}
				at := min(index, len(sites[i].args))
				sites[i].args = slices.Insert(sites[i].args, at, fill)
			}
		case "remove":
			index := -1
			if op.Name != "" {
				index = slices.IndexFunc(params, func(p refactorSigParam) bool { return p.name == op.Name })
			} else if op.Index != nil {
				index = *op.Index
			}
			if index < 0 || index >= len(params) {
				return nil, cli.UsageErrorf("ops[%d]: remove: no such parameter (name %q, index %v)", opIndex, op.Name, op.Index)
			}
			if params[index].node != nil && !f.force {
				if uses := refactorParamBodyUses(ctx, ws, file, fn, params[index].node); uses > 0 {
					return nil, cli.RefusedErrorf("parameter %q is used %d time(s) in the function body; pass --force to remove it anyway",
						params[index].name, uses)
				}
			}
			params = slices.Delete(params, index, index+1)
			for i := range sites {
				if index < len(sites[i].args) {
					sites[i].args = slices.Delete(sites[i].args, index, index+1)
				}
			}
		case "reorder":
			if len(op.Order) != len(params) {
				return nil, cli.UsageErrorf("ops[%d]: reorder order must list all %d parameter indices", opIndex, len(params))
			}
			seen := make([]bool, len(params))
			for _, idx := range op.Order {
				if idx < 0 || idx >= len(params) || seen[idx] {
					return nil, cli.UsageErrorf("ops[%d]: reorder order must be a permutation of 0-%d", opIndex, len(params)-1)
				}
				seen[idx] = true
			}
			newParams := make([]refactorSigParam, len(params))
			for newIdx, oldIdx := range op.Order {
				newParams[newIdx] = params[oldIdx]
			}
			params = newParams
			for i := range sites {
				for _, arg := range sites[i].call.Arguments() {
					if arg.Kind == ast.KindSpreadElement {
						return nil, cli.RefusedErrorf("call site %s uses a spread argument; reorder cannot rewrite it", siteLoc(sites[i]))
					}
				}
				if len(sites[i].args) != len(op.Order) {
					return nil, cli.RefusedErrorf("call site %s passes %d argument(s) but the function has %d parameter(s); reorder cannot rewrite it safely",
						siteLoc(sites[i]), len(sites[i].args), len(op.Order))
				}
				newArgs := make([]string, len(sites[i].args))
				for newIdx, oldIdx := range op.Order {
					newArgs[newIdx] = sites[i].args[oldIdx]
				}
				sites[i].args = newArgs
			}
		default:
			return nil, cli.UsageErrorf("ops[%d]: unknown op %q (want add, remove, or reorder)", opIndex, op.Op)
		}
	}

	// Render edits: the declaration's parameter list and every call site's
	// argument list.
	edits := make(map[string][]icore.TextChange)
	paramTexts := make([]string, len(params))
	for i, p := range params {
		paramTexts[i] = p.text
	}
	paramList := fn.ParameterList()
	edits[file.FileName()] = append(edits[file.FileName()], icore.TextChange{
		TextRange: icore.NewTextRange(refactorListStart(file, paramList.Pos(), paramList.End()), paramList.End()),
		NewText:   strings.Join(paramTexts, ", "),
	})
	for _, site := range sites {
		argList := site.call.ArgumentList()
		if argList == nil {
			return nil, cli.RefusedErrorf("call site %s has no argument list (new expression without parentheses); add () first", siteLoc(site))
		}
		edits[site.file.FileName()] = append(edits[site.file.FileName()], icore.TextChange{
			TextRange: icore.NewTextRange(refactorListStart(site.file, argList.Pos(), argList.End()), argList.End()),
			NewText:   strings.Join(site.args, ", "),
		})
	}

	var es core.EditSet
	for fileName, changes := range edits {
		es.Edits = append(es.Edits, core.FileEdit{FileName: fileName, Edits: changes})
	}
	notes = append(notes, fmt.Sprintf("updated the declaration and %d call site(s)", len(sites)))
	return finishRefactorTx(ctx, ws, es, &f.tx, notes)
}

// refactorListStart skips leading trivia of a node list interior unless the
// list is empty.
func refactorListStart(file *ast.SourceFile, pos int, end int) int {
	if pos >= end {
		return pos
	}
	start := scanner.SkipTrivia(file.Text(), pos)
	if start > end {
		return pos
	}
	return start
}

// refactorSignatureFunction maps a declaration to its function-like node: the
// declaration itself, or the initializer of a variable declaration holding an
// arrow function / function expression.
func refactorSignatureFunction(decl *ast.Node) (root *ast.Node, fn *ast.Node) {
	switch decl.Kind {
	case ast.KindFunctionDeclaration, ast.KindMethodDeclaration, ast.KindConstructor,
		ast.KindFunctionExpression, ast.KindArrowFunction:
		return decl, decl
	case ast.KindVariableDeclaration:
		init := decl.Initializer()
		if init != nil && (init.Kind == ast.KindArrowFunction || init.Kind == ast.KindFunctionExpression) {
			return decl, init
		}
	}
	return decl, nil
}

// refactorParamBodyUses counts identifier references to a parameter inside
// the function body.
func refactorParamBodyUses(ctx context.Context, ws *core.Workspace, file *ast.SourceFile, fn *ast.Node, param *ast.Node) int {
	body := fn.Body()
	if body == nil {
		return 0
	}
	checker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()
	name := ""
	if param.Name() != nil && param.Name().Kind == ast.KindIdentifier {
		name = param.Name().Text()
	}
	uses := 0
	for _, id := range refactorCollectIdentifiers(body) {
		if id.Text() != name {
			continue
		}
		sym := checker.GetSymbolAtLocation(id)
		if sym != nil && slices.Contains(sym.Declarations, param) {
			uses++
		}
	}
	return uses
}
