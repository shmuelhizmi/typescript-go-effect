package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	tscore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/diagnostics"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
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
			fs.StringVar(&f.withDiff, "with-diff", "", "speculative check: unified diff file to apply in-memory (- reads stdin)")
			fs.StringVar(&f.withEdits, "with-edits", "", `speculative check: edits JSON file (- reads stdin); schema: {"edits":[{"file":"src/a.ts","edits":[{"pos":0,"end":3,"newText":"x"}]}],"ops":[{"kind":"create|delete|rename","path":"…","newPath":"…","content":"…"}]} with byte-offset pos/end`)
			fs.BoolVar(&f.failOnRegression, "fail-on-regression", false, "exit 1 when the speculative check finds new errors")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			f := flags.(*checkFlags)
			if f.withDiff != "" || f.withEdits != "" {
				return runCheckSpeculative(ctx, ws, f, args)
			}
			return runCheck(ctx, ws, f, args)
		},
	})
}

type checkFlags struct {
	suggestions      bool
	severity         string
	code             string
	pathGlob         string
	withDiff         string
	withEdits        string
	failOnRegression bool

	// stdin is the reader used when the patch/edits argument is `-`;
	// overridable by tests. Defaults to os.Stdin.
	stdin io.Reader
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

// ---------------------------------------------------------------------------
// Speculative check (--with-diff / --with-edits)

// CheckDeltaResult is the `check --with-diff` / `--with-edits` result: the
// diagnostics delta a change would cause, computed against an in-memory
// overlay program. Disk is never touched.
type CheckDeltaResult struct {
	NewErrors      []core.SpecDiag `json:"newErrors"`
	FixedErrors    []core.SpecDiag `json:"fixedErrors"`
	MovedCount     int             `json:"movedCount"`
	UnchangedCount int             `json:"unchangedCount"`
	Summary        string          `json:"summary"`
}

var _ cli.Texter = (*CheckDeltaResult)(nil)

func (r *CheckDeltaResult) WriteText(w io.Writer) error {
	for _, d := range r.NewErrors {
		if _, err := fmt.Fprintf(w, "new   %s:%d:%d %s %s: %s\n", d.File, d.Range.Line, d.Range.Col, d.Category, d.Code, d.Message); err != nil {
			return err
		}
	}
	for _, d := range r.FixedErrors {
		if _, err := fmt.Fprintf(w, "fixed %s:%d:%d %s %s: %s\n", d.File, d.Range.Line, d.Range.Col, d.Category, d.Code, d.Message); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, r.Summary)
	return err
}

func runCheckSpeculative(ctx context.Context, ws *core.Workspace, flags *checkFlags, args []string) (*CheckDeltaResult, error) {
	if flags.withDiff != "" && flags.withEdits != "" {
		return nil, cli.UsageErrorf("--with-diff and --with-edits are mutually exclusive")
	}
	if len(args) > 0 {
		return nil, cli.UsageErrorf("speculative check takes no positional arguments (got %q)", args[0])
	}

	var contents map[string]string
	var deleted []string
	var err error
	if flags.withDiff != "" {
		text, readErr := readSpecInput(ws, flags, flags.withDiff)
		if readErr != nil {
			return nil, readErr
		}
		contents, deleted, err = overlayFromPatchText(ws, text)
	} else {
		text, readErr := readSpecInput(ws, flags, flags.withEdits)
		if readErr != nil {
			return nil, readErr
		}
		contents, deleted, err = overlayFromEditsJSON(ws, text)
	}
	if err != nil {
		return nil, err
	}

	specWs, err := core.SpeculativeWorkspace(ws, contents, deleted, false)
	if err != nil {
		return nil, fmt.Errorf("building speculative program: %w", err)
	}
	delta := core.DiagnosticsDelta(core.CollectDiagnostics(ctx, ws), core.CollectDiagnostics(ctx, specWs))

	result := &CheckDeltaResult{
		NewErrors:      delta.New,
		FixedErrors:    delta.Fixed,
		MovedCount:     len(delta.Moved),
		UnchangedCount: delta.UnchangedCount,
		Summary: fmt.Sprintf("speculative check: %d new, %d fixed, %d moved, %d unchanged",
			len(delta.New), len(delta.Fixed), len(delta.Moved), delta.UnchangedCount),
	}
	if flags.failOnRegression && len(result.NewErrors) > 0 {
		return result, cli.Errorf(cli.ExitFailed, "speculative check found %d new error(s)", len(result.NewErrors))
	}
	return result, nil
}

// readSpecInput reads the patch/edits argument: `-` for stdin, otherwise a
// file path resolved against the invocation cwd (then the project root).
func readSpecInput(ws *core.Workspace, flags *checkFlags, arg string) (string, error) {
	if arg == "-" {
		stdin := flags.stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return string(data), nil
	}
	if text, ok := ws.FS.ReadFile(ws.AbsPath(arg)); ok {
		return text, nil
	}
	if text, ok := ws.FS.ReadFile(tspath.GetNormalizedAbsolutePath(arg, ws.RootDir)); ok {
		return text, nil
	}
	return "", fmt.Errorf("cannot read %s: %w", arg, core.ErrNotFound)
}

// resolvePatchPath resolves a diff-relative path: project root first (diffs
// are conventionally rooted at the repo/project root), invocation cwd as a
// fallback for existing files.
func resolvePatchPath(ws *core.Workspace, rel string) string {
	fromRoot := tspath.GetNormalizedAbsolutePath(rel, ws.RootDir)
	if ws.FS.FileExists(fromRoot) {
		return fromRoot
	}
	fromCwd := tspath.GetNormalizedAbsolutePath(rel, ws.Cwd)
	if ws.FS.FileExists(fromCwd) {
		return fromCwd
	}
	return fromRoot // creations land relative to the project root
}

// overlayFromPatchText parses a unified diff and applies it to the current
// file contents, producing the overlay inputs for the speculative program.
func overlayFromPatchText(ws *core.Workspace, text string) (map[string]string, []string, error) {
	patches, err := core.ParseUnifiedDiff(text)
	if err != nil {
		return nil, nil, err
	}
	contents := make(map[string]string, len(patches))
	var deleted []string
	for rel, patch := range patches {
		abs := resolvePatchPath(ws, rel)
		switch {
		case patch.IsDelete:
			if !ws.FS.FileExists(abs) {
				return nil, nil, fmt.Errorf("patch deletes %s, which does not exist: %w", rel, core.ErrNotFound)
			}
			deleted = append(deleted, abs)
		case patch.IsNew:
			if ws.FS.FileExists(abs) {
				return nil, nil, fmt.Errorf("patch creates %s, which already exists: %w", rel, core.ErrInvalidArgument)
			}
			newText, err := patch.Apply("")
			if err != nil {
				return nil, nil, fmt.Errorf("applying patch for %s: %w", rel, err)
			}
			contents[abs] = newText
		default:
			oldText, err := currentFileText(ws, abs)
			if err != nil {
				return nil, nil, fmt.Errorf("patch edits %s: %w", rel, err)
			}
			newText, err := patch.Apply(oldText)
			if err != nil {
				return nil, nil, fmt.Errorf("applying patch for %s: %w", rel, err)
			}
			contents[abs] = newText
		}
	}
	return contents, deleted, nil
}

// currentFileText returns the current content of a file, preferring the
// program's snapshot (mirrors core's unexported readFileText).
func currentFileText(ws *core.Workspace, abs string) (string, error) {
	if file := ws.Program.GetSourceFile(abs); file != nil {
		return file.Text(), nil
	}
	if text, ok := ws.FS.ReadFile(abs); ok {
		return text, nil
	}
	return "", fmt.Errorf("cannot read %s: %w", ws.RelPath(abs), core.ErrNotFound)
}

// editsJSON is the documented --with-edits schema (the JSON form of
// core.EditSet, with byte-offset ranges).
type editsJSON struct {
	Edits []struct {
		File  string `json:"file"`
		Edits []struct {
			Pos     int    `json:"pos"`
			End     int    `json:"end"`
			NewText string `json:"newText"`
		} `json:"edits"`
	} `json:"edits"`
	Ops []struct {
		Kind    string `json:"kind"`
		Path    string `json:"path"`
		NewPath string `json:"newPath,omitempty"`
		Content string `json:"content,omitempty"`
	} `json:"ops,omitempty"`
}

func overlayFromEditsJSON(ws *core.Workspace, text string) (map[string]string, []string, error) {
	var parsed editsJSON
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, nil, cli.UsageErrorf("invalid --with-edits JSON: %v", err)
	}
	var es core.EditSet
	for _, fe := range parsed.Edits {
		fileEdit := core.FileEdit{FileName: ws.AbsPath(fe.File)}
		for _, e := range fe.Edits {
			fileEdit.Edits = append(fileEdit.Edits, tscore.TextChange{
				TextRange: tscore.NewTextRange(e.Pos, e.End),
				NewText:   e.NewText,
			})
		}
		es.Edits = append(es.Edits, fileEdit)
	}
	for _, op := range parsed.Ops {
		kind := core.FileOpKind(op.Kind)
		switch kind {
		case core.FileOpCreate, core.FileOpDelete, core.FileOpRename:
		default:
			return nil, nil, cli.UsageErrorf("invalid op kind %q (want create, delete, or rename)", op.Kind)
		}
		newPath := ""
		if op.NewPath != "" {
			newPath = ws.AbsPath(op.NewPath)
		}
		es.Ops = append(es.Ops, core.FileOp{
			Kind:    kind,
			Path:    ws.AbsPath(op.Path),
			NewPath: newPath,
			Content: op.Content,
		})
	}
	if es.IsEmpty() {
		return nil, nil, cli.UsageErrorf("--with-edits JSON contains no edits or ops")
	}
	return core.OverlayFromEditSet(ws, es)
}
