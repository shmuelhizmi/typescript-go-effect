package cmds

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/ls"
	"github.com/microsoft/typescript-go/internal/ls/lsconv"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// check_fix.go implements `check fix` (spec §4.7): apply the language
// service's code fixes for given diagnostics through the standard transaction
// engine (dry-run by default, --apply with the diagnostics gate).
//
// Fixes are obtained by driving ls.ProvideCodeActions with a CodeActionParams
// scoped to each diagnostic. Limitations: fixes that need the auto-import
// index (the missing-import fix, and the class-implements fix's import adder)
// are reported as unfixable in one-shot mode because the index is built only
// by the LSP session; the isolated-declarations annotation fixes work fully.

func init() {
	cli.Register(cli.Command{
		Family:       "check",
		Name:         "fix",
		Summary:      "Apply language-service code fixes for diagnostics (dry-run by default)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &checkFixFlags{}
			fs.StringVar(&f.code, "code", "", "fix every fixable diagnostic with this code (TS2322 or 2322; comma-separated)")
			fs.StringVar(&f.fixID, "fix-id", "", "when a diagnostic has several fixes, pick the one whose title contains this text (default: first)")
			registerRefactorTxFlags(fs, &f.tx)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runCheckFix(ctx, ws, flags.(*checkFixFlags), args)
		},
	})
}

type checkFixFlags struct {
	code  string
	fixID string
	tx    refactorTxFlags
}

// FixOutcome is the per-diagnostic result of `check fix`.
type FixOutcome struct {
	DiagRef string `json:"diagRef"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Code    string `json:"code"`
	Fixed   bool   `json:"fixed"`
	// Action is the title of the applied code action (Fixed only).
	Action string `json:"action,omitempty"`
	// Reason explains why no fix was applied (unfixable only).
	Reason string `json:"reason,omitempty"`
}

// CheckFixResult is the `check fix` result: per-diagnostic outcomes plus the
// transaction (diffs, apply status, diagnostics delta).
type CheckFixResult struct {
	Outcomes []*FixOutcome  `json:"outcomes"`
	Tx       *core.TxResult `json:"tx,omitempty"`
}

var _ cli.Texter = (*CheckFixResult)(nil)

func (r *CheckFixResult) WriteText(w io.Writer) error {
	for _, o := range r.Outcomes {
		if o.Fixed {
			if _, err := fmt.Fprintf(w, "fix   %s:%d:%d %s: %s\n", o.File, o.Line, o.Col, o.Code, o.Action); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w, "nofix %s:%d:%d %s: %s\n", o.File, o.Line, o.Col, o.Code, o.Reason); err != nil {
				return err
			}
		}
	}
	if r.Tx != nil {
		return r.Tx.WriteText(w)
	}
	return nil
}

func runCheckFix(ctx context.Context, ws *core.Workspace, flags *checkFixFlags, args []string) (*CheckFixResult, error) {
	if (flags.code == "") == (len(args) == 0) {
		return nil, cli.UsageErrorf("check fix takes either diagRef arguments (file:pos:TSnnnn) or --code TSnnnn, not both")
	}

	diags, err := checkFixTargetDiagnostics(ctx, ws, flags, args)
	if err != nil {
		return nil, err
	}

	result := &CheckFixResult{}
	var es core.EditSet
	for _, diag := range diags {
		outcome := checkFixOutcomeFor(ws, diag)
		result.Outcomes = append(result.Outcomes, outcome)
		action, reason, err := checkFixActionFor(ctx, ws, diag, flags.fixID)
		if err != nil {
			return nil, err
		}
		if action == nil {
			outcome.Reason = reason
			continue
		}
		if err := es.AddWorkspaceEdit(ws, action.Edit); err != nil {
			return nil, err
		}
		outcome.Fixed = true
		outcome.Action = action.Title
	}

	fixable := 0
	for _, o := range result.Outcomes {
		if o.Fixed {
			fixable++
		}
	}
	if fixable == 0 {
		return result, cli.Errorf(cli.ExitFailed, "no fixable diagnostics (of %d)", len(result.Outcomes))
	}

	tx, err := finishRefactorTx(ctx, ws, es, &flags.tx, nil)
	if err != nil {
		return result, err
	}
	result.Tx = tx
	if fixable < len(result.Outcomes) {
		return result, cli.PartialErrorf("%d of %d diagnostic(s) had no applicable fix", len(result.Outcomes)-fixable, len(result.Outcomes))
	}
	return result, nil
}

func checkFixOutcomeFor(ws *core.Workspace, diag *ast.Diagnostic) *FixOutcome {
	file := diag.File()
	rel := ws.RelPath(file.FileName())
	line, col := ws.PosToLineCol(file, diag.Pos())
	return &FixOutcome{
		DiagRef: fmt.Sprintf("%s:%d:TS%d", rel, diag.Pos(), diag.Code()),
		File:    rel,
		Line:    line,
		Col:     col,
		Code:    fmt.Sprintf("TS%d", diag.Code()),
	}
}

// checkFixActionFor asks the language service for quickfix actions scoped to
// one diagnostic and picks one: the first, or the first whose title contains
// fixID (the LSP layer exposes action titles, not provider fix ids).
func checkFixActionFor(ctx context.Context, ws *core.Workspace, diag *ast.Diagnostic, fixID string) (*lsproto.CodeAction, string, error) {
	lspDiag := lsconv.DiagnosticToLSPPull(ctx, ws.Conv, diag, false /*reportStyleChecksAsWarnings*/)
	params := &lsproto.CodeActionParams{
		TextDocument: lsproto.TextDocumentIdentifier{Uri: ws.URI(diag.File().FileName())},
		Range:        lspDiag.Range,
		Context:      &lsproto.CodeActionContext{Diagnostics: []*lsproto.Diagnostic{lspDiag}},
	}
	response, err := ws.LS.ProvideCodeActions(ctx, params)
	if err != nil {
		if errors.Is(err, ls.ErrNeedsAutoImports) {
			return nil, "fix requires the auto-import index (available only in an editor/LSP session)", nil
		}
		return nil, "", err
	}
	if response.CommandOrCodeActionArray == nil {
		return nil, "no code fix available", nil
	}
	var candidates []*lsproto.CodeAction
	for _, entry := range *response.CommandOrCodeActionArray {
		action := entry.CodeAction
		// Keep only per-diagnostic quickfixes carrying edits; fix-all entries
		// have no Diagnostics attached.
		if action == nil || action.Edit == nil || action.Diagnostics == nil {
			continue
		}
		candidates = append(candidates, action)
	}
	if len(candidates) == 0 {
		return nil, "no code fix available", nil
	}
	if fixID != "" {
		for _, action := range candidates {
			if strings.Contains(strings.ToLower(action.Title), strings.ToLower(fixID)) {
				return action, "", nil
			}
		}
		return nil, fmt.Sprintf("no fix matching --fix-id %q (have: %s)", fixID, checkFixTitles(candidates)), nil
	}
	return candidates[0], "", nil
}

func checkFixTitles(actions []*lsproto.CodeAction) string {
	titles := make([]string, 0, len(actions))
	for _, a := range actions {
		titles = append(titles, strconv.Quote(a.Title))
	}
	return strings.Join(titles, ", ")
}

// checkFixTargetDiagnostics resolves the diagnostics to fix: every project
// diagnostic matching --code, or the explicit diagRef arguments.
func checkFixTargetDiagnostics(ctx context.Context, ws *core.Workspace, flags *checkFixFlags, args []string) ([]*ast.Diagnostic, error) {
	files, err := projectFiles(ws, nil)
	if err != nil {
		return nil, err
	}
	var all []*ast.Diagnostic
	for _, file := range files {
		var fileDiags []*ast.Diagnostic
		fileDiags = append(fileDiags, ws.Program.GetSyntacticDiagnostics(ctx, file)...)
		fileDiags = append(fileDiags, ws.Program.GetSemanticDiagnostics(ctx, file)...)
		// isolatedDeclarations-style fixes hang off declaration diagnostics.
		if ws.Program.Options().GetEmitDeclarations() {
			fileDiags = append(fileDiags, ws.Program.GetDeclarationDiagnostics(ctx, file)...)
		}
		for _, diag := range fileDiags {
			if diag.File() != nil {
				all = append(all, diag)
			}
		}
	}
	sortKey := func(d *ast.Diagnostic) string {
		return fmt.Sprintf("%s\x00%010d\x00%d", ws.RelPath(d.File().FileName()), d.Pos(), d.Code())
	}
	slices.SortFunc(all, func(a, b *ast.Diagnostic) int { return strings.Compare(sortKey(a), sortKey(b)) })

	if flags.code != "" {
		codeFilter, err := parseCodeFilter(flags.code)
		if err != nil {
			return nil, err
		}
		var matched []*ast.Diagnostic
		for _, diag := range all {
			if codeFilter[diag.Code()] {
				matched = append(matched, diag)
			}
		}
		if len(matched) == 0 {
			return nil, cli.NotFoundErrorf("no diagnostics with code %s", flags.code)
		}
		return matched, nil
	}

	var matched []*ast.Diagnostic
	for _, ref := range args {
		diag, err := checkFixDiagByRef(ws, all, ref)
		if err != nil {
			return nil, err
		}
		matched = append(matched, diag)
	}
	return matched, nil
}

// checkFixDiagByRef finds the diagnostic addressed by a `file:pos:TSnnnn`
// diagRef (the reference printed by `check`).
func checkFixDiagByRef(ws *core.Workspace, all []*ast.Diagnostic, ref string) (*ast.Diagnostic, error) {
	lastColon := strings.LastIndexByte(ref, ':')
	if lastColon < 0 {
		return nil, cli.UsageErrorf("malformed diagRef %q (want file:pos:TSnnnn)", ref)
	}
	codeText := strings.TrimPrefix(strings.ToUpper(ref[lastColon+1:]), "TS")
	rest := ref[:lastColon]
	prevColon := strings.LastIndexByte(rest, ':')
	if prevColon < 0 {
		return nil, cli.UsageErrorf("malformed diagRef %q (want file:pos:TSnnnn)", ref)
	}
	pos, posErr := strconv.Atoi(rest[prevColon+1:])
	code, codeErr := strconv.Atoi(codeText)
	file := rest[:prevColon]
	if file == "" || posErr != nil || codeErr != nil {
		return nil, cli.UsageErrorf("malformed diagRef %q (want file:pos:TSnnnn)", ref)
	}
	for _, diag := range all {
		if diag.Pos() == pos && int(diag.Code()) == code && ws.RelPath(diag.File().FileName()) == file {
			return diag, nil
		}
	}
	return nil, cli.NotFoundErrorf("no diagnostic matching %s (run `check` to list current diagRefs)", ref)
}
