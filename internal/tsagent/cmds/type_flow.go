package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

func init() {
	cli.Register(cli.Command{
		Family:       "type",
		Name:         "flow",
		Summary:      "Control-flow narrowing trace for a variable: its flow type at every reference",
		NeedsProgram: true,
		Flags:        func(fs *flag.FlagSet) any { return &flowFlags{} },
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runTypeFlow(ctx, ws, flags.(*flowFlags), args)
		},
	})
}

type flowFlags struct{}

// FlowRow is the flow type of the variable at one reference site.
type FlowRow struct {
	Line         int    `json:"line"`
	Col          int    `json:"col"`
	Context      string `json:"context"` // trimmed source line
	Type         string `json:"type"`
	NarrowedFrom string `json:"narrowedFrom,omitempty"` // declared type, when different
	Declaration  bool   `json:"declaration,omitempty"`

	pos int // sort key, not serialized
}

// FlowResult is the `type flow` result.
type FlowResult struct {
	Target       string     `json:"target"`
	Symbol       string     `json:"symbol"`
	File         string     `json:"file"`
	DeclaredType string     `json:"declaredType"`
	Rows         []*FlowRow `json:"rows"`
	Note         string     `json:"note,omitempty"`
}

var _ cli.Texter = (*FlowResult)(nil)

func (r *FlowResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s  (declared: %s)  %s\n", r.Symbol, r.DeclaredType, r.File); err != nil {
		return err
	}
	contextWidth := 0
	for _, row := range r.Rows {
		contextWidth = max(contextWidth, len(row.Context))
	}
	for _, row := range r.Rows {
		suffix := ""
		if row.NarrowedFrom != "" {
			suffix = fmt.Sprintf("  (narrowed from %s)", row.NarrowedFrom)
		}
		if row.Declaration {
			suffix += "  [declaration]"
		}
		if _, err := fmt.Fprintf(w, "%s%4d:%-4d %-*s  %s%s\n", cli.Indent(1), row.Line, row.Col, contextWidth, row.Context, row.Type, suffix); err != nil {
			return err
		}
	}
	if r.Note != "" {
		if _, err := fmt.Fprintf(w, "note: %s\n", r.Note); err != nil {
			return err
		}
	}
	return nil
}

func runTypeFlow(ctx context.Context, ws *core.Workspace, flags *flowFlags, args []string) (*FlowResult, error) {
	if len(args) != 1 {
		return nil, cli.UsageErrorf("type flow takes exactly one target argument (file:line:col or symbol ID)")
	}
	target, err := ws.ResolveTarget(ctx, parseOperandSpec(args[0]))
	if err != nil {
		return nil, err
	}
	symbol := target.Symbol
	if symbol == nil || symbol.Flags&ast.SymbolFlagsVariable == 0 {
		return nil, cli.UsageErrorf("type flow target must be a variable or parameter (got %s)", args[0])
	}
	decl := symbol.ValueDeclaration
	if decl == nil && len(symbol.Declarations) > 0 {
		decl = symbol.Declarations[0]
	}
	if decl == nil {
		return nil, cli.NotFoundErrorf("variable %s has no declaration", args[0])
	}
	declName := ast.GetNameOfDeclaration(decl)
	if declName == nil {
		declName = decl
	}
	file := ast.GetSourceFileOfNode(decl)
	if file == nil {
		return nil, cli.NotFoundErrorf("variable %s has no source file", args[0])
	}

	// Collect read/write references, restricted to the declaring file.
	entries := ws.LS.GetReferencedSymbolsForNode(ctx, declName.Pos(), declName, ws.Program.GetSourceFiles())
	var refNodes []*ast.Node
	for _, entry := range entries {
		for _, ref := range entry.References() {
			if !ref.IsNodeEntry() || ref.Node() == nil || ref.Node() == declName {
				continue
			}
			if ast.GetSourceFileOfNode(ref.Node()) != file {
				continue
			}
			refNodes = append(refNodes, ref.Node())
		}
	}

	c, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()
	declaredType := c.GetTypeOfSymbol(symbol)
	if declaredType == nil {
		return nil, cli.NotFoundErrorf("no type for %s", args[0])
	}
	declaredDisplay := c.TypeToStringEx(declaredType, declName, core.TypeDisplayFlags, nil)

	result := &FlowResult{
		Target:       args[0],
		Symbol:       symbol.Name,
		File:         ws.RelPath(file.FileName()),
		DeclaredType: declaredDisplay,
	}

	// Declaration row first.
	declPos := astnav.GetStartOfNode(declName, file, false /*includeJSDoc*/)
	declLine, declCol := ws.PosToLineCol(file, declPos)
	result.Rows = append(result.Rows, &FlowRow{
		Line:        declLine,
		Col:         declCol,
		Context:     sourceLineAt(file, declLine),
		Type:        declaredDisplay,
		Declaration: true,
		pos:         declPos,
	})

	narrowingSeen := false
	for _, refNode := range refNodes {
		pos := astnav.GetStartOfNode(refNode, file, false /*includeJSDoc*/)
		line, col := ws.PosToLineCol(file, pos)
		row := &FlowRow{Line: line, Col: col, Context: sourceLineAt(file, line), pos: pos}
		// GetTypeAtLocation on an identifier expression reference goes
		// through getTypeOfExpression and applies control-flow analysis,
		// returning the narrowed type at that location.
		flowType := c.GetTypeAtLocation(refNode)
		if flowType == nil {
			flowType = declaredType
		}
		row.Type = c.TypeToStringEx(flowType, refNode, core.TypeDisplayFlags, nil)
		if row.Type != declaredDisplay {
			row.NarrowedFrom = declaredDisplay
			narrowingSeen = true
		}
		result.Rows = append(result.Rows, row)
	}
	slices.SortStableFunc(result.Rows, func(a, b *FlowRow) int { return a.pos - b.pos })

	if !narrowingSeen && len(refNodes) > 0 && isNarrowableType(declaredType) {
		result.Note = "narrowingUnavailable: every reference reports the declared type; either no reference is in a narrowed position or flow analysis did not apply"
	}
	return result, nil
}

// isNarrowableType reports whether narrowing could even change the display:
// unions, any/unknown, and non-literal-widened types qualify.
func isNarrowableType(t *checker.Type) bool {
	return t.Flags()&(checker.TypeFlagsUnion|checker.TypeFlagsAny|checker.TypeFlagsUnknown) != 0
}

// sourceLineAt returns the trimmed source text of a 1-based line.
func sourceLineAt(file *ast.SourceFile, line int) string {
	lineStarts := file.ECMALineMap()
	if line < 1 || line > len(lineStarts) {
		return ""
	}
	start := int(lineStarts[line-1])
	end := len(file.Text())
	if line < len(lineStarts) {
		end = int(lineStarts[line])
	}
	return strings.TrimSpace(file.Text()[start:end])
}
