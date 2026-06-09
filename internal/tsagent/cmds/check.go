package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/diagnostics"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/vfs/vfsmatch"
)

func init() {
	// check is a single-command family: `tsagent check [paths…]`.
	cli.Register(cli.Command{
		Family:       "check",
		Name:         "",
		Summary:      "Batch diagnostics with severity/code/path filters",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &checkFlags{}
			fs.BoolVar(&f.suggestions, "suggestions", false, "include suggestion diagnostics")
			fs.StringVar(&f.severity, "severity", "", "comma-separated severity filter (error,warning,suggestion,message)")
			fs.StringVar(&f.code, "code", "", "comma-separated code filter (TS2322 or 2322)")
			fs.StringVar(&f.pathGlob, "path-glob", "", "only include files matching this glob")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runCheck(ctx, ws, flags.(*checkFlags), args)
		},
	})
}

type checkFlags struct {
	suggestions bool
	severity    string
	code        string
	pathGlob    string
}

// Range is a 1-based line/col range (UTF-8 byte columns).
type Range struct {
	Line    int `json:"line"`
	Col     int `json:"col"`
	EndLine int `json:"endLine"`
	EndCol  int `json:"endCol"`
}

// RelatedInfo is a related location attached to a diagnostic.
type RelatedInfo struct {
	File    string `json:"file"`
	Range   Range  `json:"range"`
	Message string `json:"message"`
}

// Diagnostic is one diagnostic in the stable check schema.
type Diagnostic struct {
	File     string        `json:"file"`
	Range    Range         `json:"range"`
	Code     string        `json:"code"`
	Category string        `json:"category"`
	Message  string        `json:"message"`
	Related  []RelatedInfo `json:"related,omitempty"`
	// DiagRef is a stable reference `file:pos:code` for chaining into other
	// commands (e.g. type explain-error).
	DiagRef string `json:"diagRef"`
}

// CheckResult is the `check` result.
type CheckResult struct {
	Diagnostics []*Diagnostic
}

var _ cli.Lister = (*CheckResult)(nil)

func (r *CheckResult) Total() int     { return len(r.Diagnostics) }
func (r *CheckResult) Item(i int) any { return r.Diagnostics[i] }

func (r *CheckResult) WriteItemText(w io.Writer, item any) error {
	d := item.(*Diagnostic)
	if _, err := fmt.Fprintf(w, "%s:%d:%d %s %s: %s\n", d.File, d.Range.Line, d.Range.Col, d.Category, d.Code, d.Message); err != nil {
		return err
	}
	for _, related := range d.Related {
		if _, err := fmt.Fprintf(w, "  related %s:%d:%d: %s\n", related.File, related.Range.Line, related.Range.Col, related.Message); err != nil {
			return err
		}
	}
	return nil
}

func categoryName(category diagnostics.Category) string {
	switch category {
	case diagnostics.CategoryError:
		return "error"
	case diagnostics.CategoryWarning:
		return "warning"
	case diagnostics.CategorySuggestion:
		return "suggestion"
	default:
		return "message"
	}
}

func parseCodeFilter(s string) (map[int32]bool, error) {
	if s == "" {
		return nil, nil
	}
	codes := make(map[int32]bool)
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(part)), "TS"))
		var code int32
		if _, err := fmt.Sscanf(part, "%d", &code); err != nil {
			return nil, cli.UsageErrorf("invalid --code entry %q (want TS2322 or 2322)", part)
		}
		codes[code] = true
	}
	return codes, nil
}

func runCheck(ctx context.Context, ws *core.Workspace, flags *checkFlags, args []string) (*CheckResult, error) {
	codeFilter, err := parseCodeFilter(flags.code)
	if err != nil {
		return nil, err
	}
	severityFilter := cli.CommaSet(flags.severity)
	for severity := range severityFilter {
		switch severity {
		case "error", "warning", "suggestion", "message":
		default:
			return nil, cli.UsageErrorf("invalid --severity entry %q (want error, warning, suggestion, or message)", severity)
		}
	}
	var globMatcher *vfsmatch.SpecMatcher
	if flags.pathGlob != "" {
		globMatcher = vfsmatch.NewSpecMatcher([]string{flags.pathGlob}, ws.RootDir, vfsmatch.UsageFiles, ws.FS.UseCaseSensitiveFileNames())
		if globMatcher == nil {
			return nil, cli.UsageErrorf("invalid --path-glob %q", flags.pathGlob)
		}
	}

	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}

	result := &CheckResult{}
	for _, file := range files {
		if globMatcher != nil && globMatcher.MatchIndex(file.FileName()) < 0 {
			continue
		}
		var diags []*ast.Diagnostic
		diags = append(diags, ws.Program.GetSyntacticDiagnostics(ctx, file)...)
		diags = append(diags, ws.Program.GetSemanticDiagnostics(ctx, file)...)
		if flags.suggestions {
			diags = append(diags, ws.Program.GetSuggestionDiagnostics(ctx, file)...)
		}
		for _, diag := range diags {
			converted := convertDiagnostic(ws, diag)
			if len(severityFilter) > 0 && !severityFilter[converted.Category] {
				continue
			}
			if len(codeFilter) > 0 && !codeFilter[diag.Code()] {
				continue
			}
			result.Diagnostics = append(result.Diagnostics, converted)
		}
	}
	slices.SortFunc(result.Diagnostics, func(a, b *Diagnostic) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Range.Line != b.Range.Line {
			return a.Range.Line - b.Range.Line
		}
		return a.Range.Col - b.Range.Col
	})
	return result, nil
}

func convertDiagnostic(ws *core.Workspace, diag *ast.Diagnostic) *Diagnostic {
	file := diag.File()
	converted := &Diagnostic{
		Code:     fmt.Sprintf("TS%d", diag.Code()),
		Category: categoryName(diag.Category()),
		Message:  flattenMessage(diag),
	}
	if file != nil {
		converted.File = ws.RelPath(file.FileName())
		converted.Range = makeRange(ws, file, diag.Pos(), diag.End())
		converted.DiagRef = fmt.Sprintf("%s:%d:TS%d", converted.File, diag.Pos(), diag.Code())
	}
	for _, related := range diag.RelatedInformation() {
		info := RelatedInfo{Message: related.String()}
		if relatedFile := related.File(); relatedFile != nil {
			info.File = ws.RelPath(relatedFile.FileName())
			info.Range = makeRange(ws, relatedFile, related.Pos(), related.End())
		}
		converted.Related = append(converted.Related, info)
	}
	return converted
}

func makeRange(ws *core.Workspace, file *ast.SourceFile, pos int, end int) Range {
	line, col := ws.PosToLineCol(file, pos)
	endLine, endCol := ws.PosToLineCol(file, end)
	return Range{Line: line, Col: col, EndLine: endLine, EndCol: endCol}
}

// flattenMessage renders the diagnostic message including its message chain,
// indented one level per chain depth.
func flattenMessage(diag *ast.Diagnostic) string {
	var sb strings.Builder
	writeMessageChain(&sb, diag, 0)
	return sb.String()
}

func writeMessageChain(sb *strings.Builder, diag *ast.Diagnostic, depth int) {
	if depth > 0 {
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("  ", depth))
	}
	sb.WriteString(diag.String())
	for _, chained := range diag.MessageChain() {
		writeMessageChain(sb, chained, depth+1)
	}
}
