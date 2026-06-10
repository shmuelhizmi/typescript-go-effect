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
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

func init() {
	cli.Register(cli.Command{
		Family:       "type",
		Name:         "explain-error",
		Summary:      "Decompose a diagnostic into its causal message chain, with an assignability drill-down",
		NeedsProgram: true,
		Flags:        func(fs *flag.FlagSet) any { return &explainErrorFlags{} },
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runTypeExplainError(ctx, ws, flags.(*explainErrorFlags), args)
		},
	})
}

type explainErrorFlags struct{}

// ChainEntry is one step in the diagnostic's causal chain: the diagnostic
// itself at depth 0, its message chain at increasing depths, and related
// information as depth-1 siblings flagged Related.
type ChainEntry struct {
	Depth    int    `json:"depth"`
	Message  string `json:"message"`
	Code     string `json:"code"`
	Location string `json:"location,omitempty"` // file:line:col
	Related  bool   `json:"related,omitempty"`
}

// ExplainDrilldown is the property-level decomposition appended for
// assignability errors: the source/target type displays plus per-property
// mismatches.
type ExplainDrilldown struct {
	Source   string                  `json:"source"`
	Target   string                  `json:"target"`
	Problems []*AssignabilityProblem `json:"problems,omitempty"`
	Note     string                  `json:"note,omitempty"`
}

// ExplainErrorResult is the `type explain-error` result.
type ExplainErrorResult struct {
	Diagnostic *Diagnostic       `json:"diagnostic"`
	Chain      []*ChainEntry     `json:"chain"`
	Drilldown  *ExplainDrilldown `json:"drilldown,omitempty"`
	// Alternatives are diagRefs of other diagnostics covering the same
	// position when the position form was ambiguous.
	Alternatives []string `json:"alternatives,omitempty"`
}

var _ cli.Texter = (*ExplainErrorResult)(nil)

func (r *ExplainErrorResult) WriteText(w io.Writer) error {
	d := r.Diagnostic
	if _, err := fmt.Fprintf(w, "%s:%d:%d %s %s  (diagRef %s)\n", d.File, d.Range.Line, d.Range.Col, d.Category, d.Code, d.DiagRef); err != nil {
		return err
	}
	for _, entry := range r.Chain {
		marker := ""
		if entry.Related {
			marker = "related "
		}
		location := ""
		if entry.Location != "" {
			location = "  [" + entry.Location + "]"
		}
		if _, err := fmt.Fprintf(w, "%s%s%s %s%s\n", cli.Indent(entry.Depth+1), marker, entry.Code, entry.Message, location); err != nil {
			return err
		}
	}
	if r.Drilldown != nil {
		if _, err := fmt.Fprintf(w, "drilldown: %s\n  -> %s\n", r.Drilldown.Source, r.Drilldown.Target); err != nil {
			return err
		}
		if err := writeProblemsText(w, r.Drilldown.Problems, 1); err != nil {
			return err
		}
	}
	for _, alt := range r.Alternatives {
		if _, err := fmt.Fprintf(w, "also at this position: %s\n", alt); err != nil {
			return err
		}
	}
	return nil
}

// assignabilityErrorCodes is the TS2322 family for which explain-error
// attempts a property-level drill-down.
var assignabilityErrorCodes = map[int32]bool{
	2322: true, // Type 'X' is not assignable to type 'Y'.
	2345: true, // Argument of type 'X' is not assignable to parameter of type 'Y'.
	2739: true, // Type 'X' is missing the following properties from type 'Y': …
	2740: true, // Type 'X' is missing the following properties from type 'Y': … and N more.
	2741: true, // Property 'p' is missing in type 'X' but required in type 'Y'.
}

// parseDiagRef parses check's `file:pos:TSnnnn` diagnostic reference form.
func parseDiagRef(arg string) (file string, pos int, code int32, ok bool) {
	last := strings.LastIndexByte(arg, ':')
	if last < 0 {
		return "", 0, 0, false
	}
	codeText := strings.ToUpper(arg[last+1:])
	if !strings.HasPrefix(codeText, "TS") {
		return "", 0, 0, false
	}
	parsedCode, err := strconv.Atoi(codeText[2:])
	if err != nil {
		return "", 0, 0, false
	}
	rest := arg[:last]
	prev := strings.LastIndexByte(rest, ':')
	if prev < 0 {
		return "", 0, 0, false
	}
	parsedPos, err := strconv.Atoi(rest[prev+1:])
	if err != nil || rest[:prev] == "" {
		return "", 0, 0, false
	}
	return rest[:prev], parsedPos, int32(parsedCode), true
}

func runTypeExplainError(ctx context.Context, ws *core.Workspace, flags *explainErrorFlags, args []string) (*ExplainErrorResult, error) {
	if len(args) != 1 {
		return nil, cli.UsageErrorf("type explain-error takes exactly one argument (a diagRef file:pos:TSnnnn from check, or a position file:line:col)")
	}
	arg := args[0]

	var fileName string
	var byRef bool
	var refPos int
	var refCode int32
	if f, p, c, ok := parseDiagRef(arg); ok {
		fileName, refPos, refCode, byRef = f, p, c, true
	} else if f, line, col, err := core.ParsePosition(arg); err == nil {
		file, ferr := ws.FileOf(f)
		if ferr != nil {
			return nil, ferr
		}
		pos, perr := ws.LineColToPos(file, line, col)
		if perr != nil {
			return nil, perr
		}
		fileName, refPos = f, pos
	} else {
		return nil, cli.UsageErrorf("malformed argument %q (want diagRef file:pos:TSnnnn or position file:line:col)", arg)
	}

	file, err := ws.FileOf(fileName)
	if err != nil {
		return nil, err
	}

	// Re-run this file's diagnostics and locate the referenced one.
	var diags []*ast.Diagnostic
	diags = append(diags, ws.Program.GetSyntacticDiagnostics(ctx, file)...)
	diags = append(diags, ws.Program.GetSemanticDiagnostics(ctx, file)...)

	var match *ast.Diagnostic
	var alternatives []string
	if byRef {
		for _, d := range diags {
			if d.Pos() == refPos && d.Code() == refCode {
				match = d
				break
			}
		}
		if match == nil {
			return nil, cli.NotFoundErrorf("no diagnostic TS%d at %s offset %d (the file may have changed since check ran)", refCode, ws.RelPath(file.FileName()), refPos)
		}
	} else {
		for _, d := range diags {
			if d.Pos() <= refPos && refPos < max(d.End(), d.Pos()+1) {
				if match == nil {
					match = d
				} else {
					alternatives = append(alternatives, fmt.Sprintf("%s:%d:TS%d", ws.RelPath(file.FileName()), d.Pos(), d.Code()))
				}
			}
		}
		if match == nil {
			return nil, cli.NotFoundErrorf("no diagnostic at %s", arg)
		}
	}

	result := &ExplainErrorResult{
		Diagnostic:   convertDiagnostic(ws, match),
		Chain:        buildDiagnosticChain(ws, match),
		Alternatives: alternatives,
	}
	if assignabilityErrorCodes[match.Code()] {
		result.Drilldown = buildAssignabilityDrilldown(ctx, ws, file, match)
	}
	return result, nil
}

// buildDiagnosticChain flattens the diagnostic's message chain into depth-
// annotated entries, then appends the related information as depth-1
// siblings carrying their own locations.
func buildDiagnosticChain(ws *core.Workspace, diag *ast.Diagnostic) []*ChainEntry {
	var entries []*ChainEntry
	var walk func(d *ast.Diagnostic, depth int)
	walk = func(d *ast.Diagnostic, depth int) {
		entries = append(entries, &ChainEntry{
			Depth:    depth,
			Message:  d.String(),
			Code:     fmt.Sprintf("TS%d", d.Code()),
			Location: diagnosticLocation(ws, d),
		})
		for _, chained := range d.MessageChain() {
			walk(chained, depth+1)
		}
	}
	walk(diag, 0)
	for _, related := range diag.RelatedInformation() {
		entries = append(entries, &ChainEntry{
			Depth:    1,
			Message:  related.String(),
			Code:     fmt.Sprintf("TS%d", related.Code()),
			Location: diagnosticLocation(ws, related),
			Related:  true,
		})
	}
	return entries
}

func diagnosticLocation(ws *core.Workspace, d *ast.Diagnostic) string {
	file := d.File()
	if file == nil {
		return ""
	}
	line, col := ws.PosToLineCol(file, d.Pos())
	return fmt.Sprintf("%s:%d:%d", ws.RelPath(file.FileName()), line, col)
}

// buildAssignabilityDrilldown derives the source (actual) and target
// (expected) types at the diagnostic's node and reuses the `type assignable`
// property drill-down. Returns nil when either type cannot be derived.
func buildAssignabilityDrilldown(ctx context.Context, ws *core.Workspace, file *ast.SourceFile, diag *ast.Diagnostic) *ExplainDrilldown {
	c, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()
	node := nodeForDiagnostic(file, diag)
	if node == nil {
		return nil
	}
	source, target := assignabilityPair(c, node)
	if source == nil || target == nil {
		return nil
	}
	return &ExplainDrilldown{
		Source:   c.TypeToStringEx(source, node, core.TypeDisplayFlags, nil),
		Target:   c.TypeToStringEx(target, node, core.TypeDisplayFlags, nil),
		Problems: drillAssignability(c, source, target, 1),
		Note:     "problems are an approximate property-wise drill-down, not the checker's native elaboration chain",
	}
}

// nodeForDiagnostic finds the widest node that starts at the diagnostic's
// position and ends within its range.
func nodeForDiagnostic(file *ast.SourceFile, diag *ast.Diagnostic) *ast.Node {
	node := astnav.GetTouchingToken(file, diag.Pos())
	if node == nil {
		return nil
	}
	for parent := node.Parent; parent != nil &&
		parent.Kind != ast.KindSourceFile &&
		parent.End() <= diag.End() &&
		astnav.GetStartOfNode(parent, file, false /*includeJSDoc*/) >= diag.Pos(); parent = node.Parent {
		node = parent
	}
	return node
}

// assignabilityPair derives (actual, expected) types for the diagnostic
// node: the contextual type when the checker exposes one, otherwise the
// three common parent contexts (variable annotation, parameter declared
// type, function return annotation).
func assignabilityPair(c *checker.Checker, node *ast.Node) (source *checker.Type, target *checker.Type) {
	parent := node.Parent

	// The diagnostic is often reported on a declaration name: compare the
	// initializer's type against the declared annotation.
	if parent != nil && parent.Name() == node {
		switch parent.Kind {
		case ast.KindVariableDeclaration, ast.KindPropertyDeclaration, ast.KindParameter, ast.KindPropertyAssignment:
			if annotation, initializer := parent.Type(), parent.Initializer(); annotation != nil && initializer != nil {
				return c.GetTypeAtLocation(initializer), c.GetTypeFromTypeNode(annotation)
			}
		}
	}

	if !ast.IsExpressionNode(node) {
		return nil, nil
	}
	source = c.GetTypeAtLocation(node)
	if source == nil {
		return nil, nil
	}
	if target = c.GetContextualType(node, checker.ContextFlagsNone); target != nil {
		return source, target
	}

	// Fallbacks for positions without a recoverable contextual type.
	if parent == nil {
		return nil, nil
	}
	switch parent.Kind {
	case ast.KindVariableDeclaration, ast.KindPropertyDeclaration, ast.KindParameter:
		if annotation := parent.Type(); annotation != nil && parent.Initializer() == node {
			return source, c.GetTypeFromTypeNode(annotation)
		}
	case ast.KindReturnStatement:
		if fn := ast.FindAncestor(parent, ast.IsFunctionLikeDeclaration); fn != nil {
			if annotation := fn.Type(); annotation != nil {
				return source, c.GetTypeFromTypeNode(annotation)
			}
		}
	}
	return nil, nil
}
