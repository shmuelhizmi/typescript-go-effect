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
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// edit.go is the executor of the `tsagent edit` script DSL (plan Part B): a
// batched, line-oriented symbol-edit language (move/insert/replace/delete)
// that compiles a whole script into ONE core.EditSet and runs it as ONE
// transaction. Unlike the refactor family, `edit` APPLIES BY DEFAULT — the
// diagnostics gate still refuses edits that would introduce new type errors
// (exit 4), and --dry-run previews without writing.
//
// Pipeline: parse (all syntax errors at once, exit 2) → resolve every symbol
// and path against the original program (all failures at once, with
// closest-ID suggestions, exit 3) → validate per-op legality and pre-check
// claimed-range conflicts with line attribution (exit 2) → core.Execute.
// All byte offsets are computed against the ORIGINAL file text; the
// transaction engine merges and applies them in one pass.

func init() {
	cli.Register(cli.Command{
		Family:       "edit",
		Name:         "",
		Summary:      "Run a batched symbol-edit script (move/insert/replace/delete); APPLIES by default, --dry-run to preview",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &editFlags{}
			fs.Var(&f.exprs, "e", "one edit-script line (repeatable; lines are joined with newlines)")
			fs.BoolVar(&f.dryRun, "dry-run", false, "print per-op results and diffs without writing")
			fs.BoolVar(&f.allowErrors, "allow-errors", false, "apply even if new diagnostics would be introduced")
			fs.BoolVar(&f.strictGate, "strict-gate", false, "also refuse on new unused-symbol diagnostics (TS6133 etc.; non-gating by default)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runEdit(ctx, ws, flags.(*editFlags), args)
		},
	})
}

// editLineFlags is the repeatable -e flag. Values may themselves contain
// newlines (heredocs work through a single -e), and String() joins with
// newlines so daemon-forwarded flags re-parse identically.
type editLineFlags []string

func (l *editLineFlags) String() string     { return strings.Join(*l, "\n") }
func (l *editLineFlags) Set(v string) error { *l = append(*l, v); return nil }

type editFlags struct {
	exprs       editLineFlags
	dryRun      bool
	allowErrors bool
	strictGate  bool

	// stdin is the reader used when the script argument is `-`; overridable
	// by tests. Defaults to os.Stdin.
	stdin io.Reader
}

// EditOpReport is the per-op entry of the edit result: the script line, the
// normalized op text, and the files the op touches.
type EditOpReport struct {
	Line  int      `json:"line"`
	Op    string   `json:"op"`
	Files []string `json:"files"`
	Notes []string `json:"notes,omitempty"`
}

// EditScriptResult is the `tsagent edit` result: one report per op plus the
// transaction outcome.
type EditScriptResult struct {
	Ops []EditOpReport `json:"ops"`
	Tx  *core.TxResult `json:"tx"`
}

// WriteText renders `ok line N: <op>` per op, the unified diffs, and an
// applied/dry-run summary line.
func (r *EditScriptResult) WriteText(w io.Writer) error {
	var err error
	p := func(format string, args ...any) {
		if err == nil {
			_, err = fmt.Fprintf(w, format, args...)
		}
	}
	for _, op := range r.Ops {
		line := fmt.Sprintf("ok line %d: %s", op.Line, op.Op)
		if len(op.Notes) > 0 {
			line += " (" + strings.Join(op.Notes, "; ") + ")"
		}
		p("%s\n", line)
	}
	if len(r.Tx.Diffs) > 0 {
		p("\n")
	}
	for _, d := range r.Tx.Diffs {
		p("%s", d.Diff)
	}
	if r.Tx.Applied {
		p("applied: %d file(s) changed, %d new errors, %d fixed\n", len(r.Tx.FilesChanged), len(r.Tx.NewErrors), r.Tx.FixedErrors)
		for _, e := range r.Tx.NewErrors {
			p("  new %s:%d:%d %s: %s\n", e.File, e.Line, e.Col, e.Code, e.Message)
		}
		for _, e := range r.Tx.UnusedWarnings {
			p("  unused %s:%d:%d %s: %s\n", e.File, e.Line, e.Col, e.Code, e.Message)
		}
	} else {
		p("dry-run: %d file(s) would change\n", len(r.Tx.FilesChanged))
	}
	for _, note := range r.Tx.Notes {
		p("note: %s\n", note)
	}
	return err
}

func runEdit(ctx context.Context, ws *core.Workspace, f *editFlags, args []string) (*EditScriptResult, error) {
	script, err := editScriptSource(ws, f, args)
	if err != nil {
		return nil, err
	}

	ops, parseErrs := parseEditScript(script)
	if len(parseErrs) > 0 {
		msgs := make([]string, len(parseErrs))
		for i, e := range parseErrs {
			msgs[i] = e.Error()
		}
		return nil, cli.UsageErrorf("%s", strings.Join(msgs, "\n"))
	}
	if len(ops) == 0 {
		return nil, cli.UsageErrorf("the script contains no operations")
	}

	resolved, err := editResolveOps(ctx, ws, ops)
	if err != nil {
		return nil, err
	}

	b := &editBuilder{ws: ws, claims: map[string][]editClaim{}}
	for _, r := range resolved {
		if err := b.buildOp(ctx, r); err != nil {
			return nil, err
		}
	}
	b.validateInsertConflicts()
	if len(b.msgs) > 0 {
		return nil, cli.UsageErrorf("%s", strings.Join(b.msgs, "\n"))
	}
	emptyNotes := editDropEmptiedFiles(ws, &b.es)

	tx, err := core.Execute(ctx, ws, b.es, core.TxOpts{Apply: !f.dryRun, AllowErrors: f.allowErrors, StrictGate: f.strictGate})
	if err != nil {
		return nil, err
	}
	if tx.Refused {
		return nil, cli.RefusedErrorf("edits would introduce %d new error(s) (pass --allow-errors to apply anyway):\n%s",
			len(tx.NewErrors), editDiagsText(tx.NewErrors))
	}
	tx.Notes = append(tx.Notes, emptyNotes...)
	return &EditScriptResult{Ops: b.reports, Tx: tx}, nil
}

// editDropEmptiedFiles converts a file's edits into a file deletion when the
// edited result would contain only whitespace — the same hygiene cross-file
// moves apply when they empty their source file. Returns the notes to attach
// to the transaction result.
func editDropEmptiedFiles(ws *core.Workspace, es *core.EditSet) []string {
	if len(es.Edits) == 0 {
		return nil
	}
	contents, _, err := core.OverlayFromEditSet(ws, *es)
	if err != nil {
		return nil // Execute reports the underlying problem
	}
	emptied := map[string]bool{}
	var emptiedList []string
	var notes []string
	for _, fe := range es.Edits {
		abs := tspath.GetNormalizedAbsolutePath(fe.FileName, ws.Cwd)
		if emptied[abs] {
			continue
		}
		text, ok := contents[abs]
		if ok && strings.TrimSpace(text) == "" && ws.Program.GetSourceFile(abs) != nil {
			emptied[abs] = true
			emptiedList = append(emptiedList, abs)
			notes = append(notes, fmt.Sprintf("%s became empty and was deleted", ws.RelPath(abs)))
		}
	}
	if len(emptied) == 0 {
		return nil
	}
	kept := es.Edits[:0]
	for _, fe := range es.Edits {
		if !emptied[tspath.GetNormalizedAbsolutePath(fe.FileName, ws.Cwd)] {
			kept = append(kept, fe)
		}
	}
	es.Edits = kept
	for _, abs := range emptiedList {
		es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpDelete, Path: abs})
	}
	return notes
}

// editScriptSource picks the script text: exactly one positional argument (a
// file path, or `-` for stdin) XOR one-or-more -e lines.
func editScriptSource(ws *core.Workspace, f *editFlags, args []string) (string, error) {
	switch {
	case len(f.exprs) > 0 && len(args) > 0:
		return "", cli.UsageErrorf("-e and a positional script argument are mutually exclusive")
	case len(f.exprs) > 0:
		return strings.Join(f.exprs, "\n"), nil
	case len(args) == 1:
		return refactorReadInput(ws, f.stdin, args[0])
	case len(args) > 1:
		return "", cli.UsageErrorf("edit takes exactly one script argument (a file path, or - for stdin)")
	default:
		return "", cli.UsageErrorf("missing script: pass a script file, - for stdin, or one or more -e '<op line>' flags")
	}
}

func editDiagsText(diags []core.TxDiag) string {
	lines := make([]string, 0, len(diags))
	for _, d := range diags {
		lines = append(lines, fmt.Sprintf("  %s:%d:%d error %s: %s", d.File, d.Line, d.Col, d.Code, d.Message))
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Resolution (against the ORIGINAL program)

// editResolvedOp is a parsed op with its symbols and files resolved.
type editResolvedOp struct {
	op editOp
	// Operand of move/replace/delete.
	symbol   *ast.Symbol
	decl     *ast.Node // primary declaration as decoded
	declNode *ast.Node // widened deletion node (sole declarators → statement)
	declFile *ast.SourceFile
	// group holds ALL (widened) declarations of a function/method overload
	// group addressed by a bare ID: delete/replace/within-file move operate
	// on the whole group. nil for single declarations and `~N`-addressed
	// signatures.
	group []*ast.Node
	// Anchor of before/after/into places.
	anchorDecl  *ast.Node
	anchorNode  *ast.Node
	anchorFile  *ast.SourceFile
	anchorGroup []*ast.Node // overload group of a bare-ID anchor (before → first, after → last)
	// Target file of top/end places.
	destFile *ast.SourceFile
}

// editOpNodes returns the declaration nodes an op operates on: the whole
// overload group for bare group IDs, the single widened node otherwise.
func (r *editResolvedOp) editOpNodes() []*ast.Node {
	if len(r.group) > 1 {
		return r.group
	}
	return []*ast.Node{r.declNode}
}

// editResolveOps resolves every op's symbol IDs and file paths. All failures
// are reported together (exit 3; exit 2 when every failure is a malformed or
// ambiguous ID rather than a miss), with closest-ID suggestions for unknown
// symbols whose file part resolves.
func editResolveOps(ctx context.Context, ws *core.Workspace, ops []editOp) ([]editResolvedOp, error) {
	var msgs []string
	sawNotFound := false
	resolveSym := func(line int, id string) (*ast.Symbol, *ast.Node, []*ast.Node, bool) {
		symbol, decl, err := core.DecodeSymbolID(ctx, ws, id)
		if err != nil {
			switch {
			case errors.Is(err, core.ErrBadSymbolAddress):
				// Self-explanatory addressing errors (ambiguous merged IDs,
				// ordinal out of range, position misses) surface verbatim.
				sawNotFound = true
				msgs = append(msgs, fmt.Sprintf("line %d: %v", line, err))
			case errors.Is(err, core.ErrNotFound):
				sawNotFound = true
				msgs = append(msgs, fmt.Sprintf("line %d: unknown symbol %s%s", line, id, editClosestIDs(ws, id)))
			default:
				msgs = append(msgs, fmt.Sprintf("line %d: %v", line, err))
			}
			return nil, nil, nil, false
		}
		if decl == nil {
			sawNotFound = true
			msgs = append(msgs, fmt.Sprintf("line %d: symbol %s has no declaration", line, id))
			return nil, nil, nil, false
		}
		// A bare ID (no ~N ordinal) over an overload group addresses the
		// whole group.
		var group []*ast.Node
		if !editIDHasOrdinal(id) {
			if decls := core.OverloadGroupDecls(symbol, ast.GetSourceFileOfNode(decl)); len(decls) > 1 {
				group = make([]*ast.Node, len(decls))
				for i, d := range decls {
					group[i] = refactorDeletionNode(d)
				}
			}
		}
		return symbol, decl, group, true
	}

	resolved := make([]editResolvedOp, 0, len(ops))
	for _, op := range ops {
		r := editResolvedOp{op: op}
		ok := true
		if op.Sym != "" {
			if symbol, decl, group, k := resolveSym(op.Line, op.Sym); k {
				r.symbol, r.decl = symbol, decl
				r.declNode = refactorDeletionNode(decl)
				r.declFile = ast.GetSourceFileOfNode(decl)
				r.group = group
			} else {
				ok = false
			}
		}
		switch op.Place.Kind {
		case "before", "after", "into":
			if _, decl, group, k := resolveSym(op.Line, op.Place.Sym); k {
				r.anchorDecl = decl
				r.anchorNode = refactorDeletionNode(decl)
				r.anchorFile = ast.GetSourceFileOfNode(decl)
				r.anchorGroup = group
			} else {
				ok = false
			}
		case "top", "end":
			abs := refactorResolveEditPath(ws, op.Place.Path)
			if file := ws.Program.GetSourceFile(abs); file != nil {
				r.destFile = file
			} else {
				sawNotFound = true
				msgs = append(msgs, fmt.Sprintf("line %d: %s is not part of the program (edit cannot create files; use refactor mv-symbol --to %s --create)",
					op.Line, op.Place.Path, op.Place.Path))
				ok = false
			}
		}
		if ok {
			resolved = append(resolved, r)
		}
	}
	if len(msgs) > 0 {
		code := cli.ExitNotFound
		if !sawNotFound {
			code = cli.ExitUsage
		}
		return nil, cli.Errorf(code, "%s", strings.Join(msgs, "\n"))
	}
	return resolved, nil
}

// editClosestIDs renders the "; closest: …" suffix for an unknown symbol ID,
// when its file part resolves to a program file.
func editClosestIDs(ws *core.Workspace, id string) string {
	hash := strings.IndexByte(id, '#')
	if hash < 0 {
		return ""
	}
	file, err := ws.FileOf(id[:hash])
	if err != nil {
		return ""
	}
	ids := core.SuggestSymbolIDs(ws, file, id, 3)
	if len(ids) == 0 {
		return ""
	}
	return "; closest: " + strings.Join(ids, ", ")
}

// ---------------------------------------------------------------------------
// Build: ops → one EditSet, with legality checks and conflict pre-validation

// editClaim is one claimed non-zero-width byte range of a file, attributed to
// the op line that claimed it. Zero-width inserts never claim ranges (they
// cannot conflict; equal-position inserts apply in script order because the
// transaction engine sorts stably and ApplyBulkEdits applies sequentially).
type editClaim struct {
	line     int
	verb     string
	pos, end int
}

type editBuilder struct {
	ws      *core.Workspace
	es      core.EditSet
	claims  map[string][]editClaim
	inserts []editInsertMark
	msgs    []string
	reports []EditOpReport
}

// editInsertMark records a zero-width insert position for post-build conflict
// validation: an insert inside a range another op REPLACES is a genuine
// conflict (reported with line attribution); inserts inside a deleted/moved
// range compose (the engine lands them at the vacated spot).
type editInsertMark struct {
	line int
	raw  string
	file string
	pos  int
}

func (b *editBuilder) failf(op editOp, format string, args ...any) {
	b.msgs = append(b.msgs, fmt.Sprintf("line %d: ", op.Line)+fmt.Sprintf(format, args...))
}

// claim records a non-zero-width range an op rewrites and reports an overlap
// with any previously claimed range of the same file.
func (b *editBuilder) claim(op editOp, fileName string, r icore.TextRange) {
	for _, c := range b.claims[fileName] {
		if r.Pos() < c.end && c.pos < r.End() {
			b.msgs = append(b.msgs, fmt.Sprintf("line %d: %s conflicts with %s at line %d (overlapping ranges in %s)",
				op.Line, op.Raw, c.verb, c.line, b.ws.RelPath(fileName)))
			return
		}
	}
	b.claims[fileName] = append(b.claims[fileName], editClaim{line: op.Line, verb: op.Verb, pos: r.Pos(), end: r.End()})
}

func (b *editBuilder) addEdit(fileName string, change icore.TextChange) {
	b.es.Edits = append(b.es.Edits, core.FileEdit{FileName: fileName, Edits: []icore.TextChange{change}})
}

func (b *editBuilder) report(op editOp, notes []string, files ...string) {
	slices.Sort(files)
	b.reports = append(b.reports, EditOpReport{Line: op.Line, Op: op.Raw, Files: slices.Compact(files), Notes: notes})
}

// buildOp compiles one resolved op into edits. Legality failures collect into
// b.msgs (reported together, exit 2); a non-nil error aborts immediately
// (cross-file move refusals keep their own exit codes).
func (b *editBuilder) buildOp(ctx context.Context, r editResolvedOp) error {
	op := r.op
	switch op.Verb {
	case "delete":
		text := r.declFile.Text()
		nodes := r.editOpNodes()
		for _, node := range nodes {
			dr := editDeletionRange(text, refactorDeletionRange(r.declFile, node))
			b.claim(op, r.declFile.FileName(), dr)
			b.addEdit(r.declFile.FileName(), icore.TextChange{TextRange: dr, NewText: ""})
		}
		b.report(op, editGroupNotes(nodes, "deleted"), b.ws.RelPath(r.declFile.FileName()))
	case "replace":
		nodes := r.editOpNodes()
		dr, contiguous := editContiguousRange(r.declFile, nodes)
		if !contiguous {
			b.failf(op, "cannot replace %s: the %d overload declarations are not contiguous; replace each ~N individually", op.Sym, len(nodes))
			return nil
		}
		b.claim(op, r.declFile.FileName(), dr)
		newText := editNormalizeEOL(r.declFile.Text(), editEnsureNewline(op.Body))
		b.addEdit(r.declFile.FileName(), icore.TextChange{TextRange: dr, NewText: newText})
		b.report(op, editGroupNotes(nodes, "replaced"), b.ws.RelPath(r.declFile.FileName()))
	case "insert":
		file, pos, prefix, err := editInsertPos(r)
		if err != nil {
			b.failf(op, "%v", err)
			return nil
		}
		block := editSeparateBlock(r, file, pos, editEnsureNewline(op.Body))
		if prefix != "" {
			// Mid-line separator insert (enum member after a member without a
			// trailing comma): the original line break at pos survives, so the
			// block must not bring a second one.
			if rest := file.Text()[pos:]; strings.HasPrefix(rest, "\n") || strings.HasPrefix(rest, "\r\n") {
				block = strings.TrimSuffix(block, "\n")
			}
		}
		newText := editNormalizeEOL(file.Text(), prefix+block)
		b.inserts = append(b.inserts, editInsertMark{line: op.Line, raw: op.Raw, file: file.FileName(), pos: pos})
		b.addEdit(file.FileName(), icore.TextChange{TextRange: icore.NewTextRange(pos, pos), NewText: newText})
		b.report(op, nil, b.ws.RelPath(file.FileName()))
	case "move":
		return b.buildMove(ctx, r)
	}
	return nil
}

// editGroupNotes annotates an op that expanded over a whole overload group.
func editGroupNotes(nodes []*ast.Node, verb string) []string {
	if len(nodes) < 2 {
		return nil
	}
	return []string{fmt.Sprintf("%s all %d overload declarations", verb, len(nodes))}
}

// editContiguousRange merges the deletion ranges of an op's declarations into
// one range. ok is false when anything other than whitespace separates two
// consecutive declarations (a non-contiguous overload group).
func editContiguousRange(file *ast.SourceFile, nodes []*ast.Node) (icore.TextRange, bool) {
	text := file.Text()
	ranges := make([]icore.TextRange, len(nodes))
	for i, n := range nodes {
		ranges[i] = refactorDeletionRange(file, n)
	}
	for i := 1; i < len(ranges); i++ {
		gapStart, gapEnd := ranges[i-1].End(), ranges[i].Pos()
		if gapEnd > gapStart && strings.TrimSpace(text[gapStart:gapEnd]) != "" {
			return icore.TextRange{}, false
		}
	}
	return icore.NewTextRange(ranges[0].Pos(), ranges[len(ranges)-1].End()), true
}

// validateInsertConflicts reports inserts whose position falls strictly
// inside a range another op replaces (line-attributed, like every other
// conflict). Inserts inside deleted/moved ranges compose instead: the engine
// repositions them to the vacated spot.
func (b *editBuilder) validateInsertConflicts() {
	for _, m := range b.inserts {
		for _, c := range b.claims[m.file] {
			if c.pos < m.pos && m.pos < c.end && c.verb == "replace" {
				b.msgs = append(b.msgs, fmt.Sprintf("line %d: %s conflicts with %s at line %d (overlapping ranges in %s)",
					m.line, m.raw, c.verb, c.line, b.ws.RelPath(m.file)))
			}
		}
	}
}

// editDeletionRange applies blank-line hygiene to a deletion range: one extra
// newline is eaten when the deletion would leave a double blank line
// (refactorCollapseBlankAfterDeletion), and a deletion at the very top of the
// file also eats the blank line(s) it would leave at BOF.
func editDeletionRange(text string, dr icore.TextRange) icore.TextRange {
	r := refactorCollapseBlankAfterDeletion(text, dr)
	if r.Pos() != 0 {
		return r
	}
	end := r.End()
	for end < len(text) {
		switch {
		case text[end] == '\n':
			end++
		case text[end] == '\r' && end+1 < len(text) && text[end+1] == '\n':
			end += 2
		default:
			return icore.NewTextRange(0, end)
		}
	}
	return icore.NewTextRange(0, end)
}

// editNormalizeEOL rewrites inserted text to the target file's dominant EOL:
// heredoc bodies arrive LF-normalized from the parser, so writing them into
// a CRLF-dominant file verbatim would produce mixed line endings.
func editNormalizeEOL(fileText string, s string) string {
	crlf := strings.Count(fileText, "\r\n")
	if crlf == 0 || crlf <= strings.Count(fileText, "\n")-crlf {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// editIDHasOrdinal reports whether a symbol ID ends with an explicit `~N`
// declaration ordinal.
func editIDHasOrdinal(id string) bool {
	tilde := strings.LastIndexByte(id, '~')
	if tilde < 0 || tilde == len(id)-1 {
		return false
	}
	_, err := strconv.Atoi(id[tilde+1:])
	return err == nil
}

// editInsertPos resolves an insert op's file, byte offset, and a separator
// prefix the inserted text must carry (a `,` when appending an enum member
// after a member without a trailing comma). `top` lands below any shebang and
// directive prologue (`"use client";`), never above it.
func editInsertPos(r editResolvedOp) (*ast.SourceFile, int, string, error) {
	// A bare overload-group anchor means "the whole group": before → ahead of
	// the first signature, after → past the last declaration.
	first, last := r.anchorNode, r.anchorNode
	if len(r.anchorGroup) > 1 {
		first, last = r.anchorGroup[0], r.anchorGroup[len(r.anchorGroup)-1]
	}
	switch r.op.Place.Kind {
	case "before":
		return r.anchorFile, refactorDeletionRange(r.anchorFile, first).Pos(), "", nil
	case "after":
		return r.anchorFile, refactorDeletionRange(r.anchorFile, last).End(), "", nil
	case "top":
		return r.destFile, importInsertOffset(r.destFile), "", nil
	case "end":
		return r.destFile, len(r.destFile.Text()), "", nil
	case "into":
		pos, prefix, err := editInsertIntoPos(r.anchorFile, r.anchorDecl)
		if err != nil {
			return nil, 0, "", err
		}
		return r.anchorFile, pos, prefix, nil
	}
	return nil, 0, "", fmt.Errorf("unknown insert place %q", r.op.Place.Kind)
}

// editSeparateBlock prepares inserted raw code for the target position: it
// never glues to a non-newline, and a top-level insert is separated from the
// adjacent declaration by exactly one blank line — before/after from their
// anchor, `end` from the last declaration, `top` from the first declaration
// (inserts into class bodies keep the single-newline behavior).
func editSeparateBlock(r editResolvedOp, file *ast.SourceFile, pos int, newText string) string {
	text := file.Text()
	if pos > 0 && text[pos-1] != '\n' {
		newText = "\n" + newText
	}
	switch r.op.Place.Kind {
	case "top":
		if !editBlankLineAt(text, pos) {
			newText += "\n"
		}
		return newText
	case "end":
		if !editBlankLineBefore(text, pos) {
			newText = "\n" + newText
		}
		return newText
	}
	if r.anchorNode == nil || r.anchorNode.Parent == nil || r.anchorNode.Parent.Kind != ast.KindSourceFile {
		return newText
	}
	switch r.op.Place.Kind {
	case "after":
		if !editBlankLineBefore(text, pos) {
			newText = "\n" + newText
		}
	case "before":
		if !editBlankLineAt(text, pos) {
			newText += "\n"
		}
	}
	return newText
}

// editBlankLineBefore reports whether the text directly before pos already
// ends with a blank line (or pos is the start of the file).
func editBlankLineBefore(text string, pos int) bool {
	return pos == 0 || (pos >= 2 && text[pos-1] == '\n' && text[pos-2] == '\n')
}

// editBlankLineAt reports whether pos sits on a blank line (or at EOF).
func editBlankLineAt(text string, pos int) bool {
	return pos >= len(text) || text[pos] == '\n'
}

// editInsertIntoPos computes the offset for `insert into <container>`: after
// the last member's deletion range, or right after the opening brace (skipping
// blank trivia) when the body is empty. Enum members are comma-separated, so
// for enums the offset lands AFTER the last member's trailing comma (start of
// the next line when the comma ends its line) — and when the last member has
// no trailing comma, the returned prefix carries the `,` the insertion must
// add to keep the body syntactically valid (the inserted body itself is raw;
// commas inside it are the caller's responsibility).
func editInsertIntoPos(file *ast.SourceFile, container *ast.Node) (pos int, prefix string, err error) {
	var list *ast.NodeList
	switch container.Kind {
	case ast.KindClassDeclaration, ast.KindClassExpression, ast.KindInterfaceDeclaration, ast.KindEnumDeclaration:
		list = container.MemberList()
	case ast.KindModuleDeclaration:
		body := container.Body()
		if body == nil || body.Kind != ast.KindModuleBlock {
			return 0, "", fmt.Errorf("insert into: the namespace has no block body")
		}
		list = body.AsModuleBlock().Statements
	default:
		return 0, "", fmt.Errorf("insert into requires a class, interface, enum, or namespace target")
	}
	if list == nil {
		return 0, "", fmt.Errorf("insert into: the container has no member list")
	}
	text := file.Text()
	if n := len(list.Nodes); n > 0 {
		last := list.Nodes[n-1]
		if container.Kind == ast.KindEnumDeclaration {
			j := last.End()
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			if j >= len(text) || text[j] != ',' {
				// No trailing comma: insert right after the member, adding one.
				return last.End(), ",", nil
			}
			j++ // past the comma
			k := j
			for k < len(text) && (text[k] == ' ' || text[k] == '\t') {
				k++
			}
			if k < len(text) && text[k] == '\r' {
				k++
			}
			if k < len(text) && text[k] == '\n' {
				return k + 1, "", nil // start of the line after the comma
			}
			return j, "", nil // comma followed by non-whitespace (e.g. a comment)
		}
		return refactorDeletionRange(file, last).End(), "", nil
	}
	pos = list.Pos()
	for pos < len(text) && pos < container.End() &&
		(text[pos] == ' ' || text[pos] == '\t' || text[pos] == '\r' || text[pos] == '\n') {
		pos++
	}
	return pos, "", nil
}

// buildMove classifies a move op: within-file/same-container moves are pure
// text reorders; top-level moves anchored in another file (or at top/end of
// another file) go through the cross-file planner with import management.
func (b *editBuilder) buildMove(ctx context.Context, r editResolvedOp) error {
	op := r.op
	topLevel := r.declNode.Parent != nil && r.declNode.Parent.Kind == ast.KindSourceFile
	container := editClassLikeContainer(r.decl)

	// move <sym> top|end <path>
	if r.destFile != nil {
		switch {
		case container != nil:
			b.failf(op, "cannot move a member across containers; use delete + insert into")
		case !topLevel:
			b.failf(op, "cannot move a function-local declaration")
		case r.destFile == r.declFile:
			pos := importInsertOffset(r.declFile)
			if op.Place.Kind == "end" {
				pos = len(r.declFile.Text())
			}
			b.reorder(op, r.declFile, r.editOpNodes(), pos)
		default:
			insertPos := importInsertOffset(r.destFile)
			if op.Place.Kind == "end" {
				insertPos = -1 // planner appends, handling the trailing newline
			}
			return b.crossFileMove(ctx, r, symbolMoveDest{fileAbs: r.destFile.FileName(), file: r.destFile, insertPos: insertPos})
		}
		return nil
	}

	// move <sym> before|after <sym>
	if r.anchorDecl == r.decl || r.anchorNode == r.declNode ||
		slices.Contains(r.group, r.anchorNode) || slices.Contains(r.anchorGroup, r.declNode) {
		b.failf(op, "cannot move a declaration relative to itself")
		return nil
	}
	anchorTop := r.anchorNode.Parent != nil && r.anchorNode.Parent.Kind == ast.KindSourceFile
	anchorContainer := editClassLikeContainer(r.anchorDecl)
	anchorPos := func() int {
		// A bare overload-group anchor means "the whole group".
		first, last := r.anchorNode, r.anchorNode
		if len(r.anchorGroup) > 1 {
			first, last = r.anchorGroup[0], r.anchorGroup[len(r.anchorGroup)-1]
		}
		if op.Place.Kind == "after" {
			return refactorDeletionRange(r.anchorFile, last).End()
		}
		return refactorDeletionRange(r.anchorFile, first).Pos()
	}
	switch {
	case container != nil && anchorContainer == container:
		b.reorder(op, r.declFile, r.editOpNodes(), anchorPos())
	case container != nil:
		b.failf(op, "cannot move a member across containers; use delete + insert into")
	case !topLevel:
		b.failf(op, "cannot move a function-local declaration")
	case !anchorTop:
		b.failf(op, "the move anchor must be a top-level declaration")
	case r.anchorFile == r.declFile:
		b.reorder(op, r.declFile, r.editOpNodes(), anchorPos())
	default:
		return b.crossFileMove(ctx, r, symbolMoveDest{fileAbs: r.anchorFile.FileName(), file: r.anchorFile, insertPos: anchorPos()})
	}
	return nil
}

// reorder emits a within-file move: delete each declaration's trivia-aware
// range and re-insert the exact same bytes (newline-terminated) at insertPos
// — bare-ID overload groups move as one block, in source order. Both offsets
// address the original text; the engine merges them. Top-level moves get
// blank-line hygiene: one blank line separates the re-inserted block from its
// before/after anchor, and a deletion that would leave a double blank line
// (or a blank line at BOF) eats the extra newline(s).
func (b *editBuilder) reorder(op editOp, file *ast.SourceFile, nodes []*ast.Node, insertPos int) {
	text := file.Text()
	topLevel := nodes[0].Parent != nil && nodes[0].Parent.Kind == ast.KindSourceFile
	var sb strings.Builder
	for _, node := range nodes {
		dr := refactorDeletionRange(file, node)
		sb.WriteString(editEnsureNewline(text[dr.Pos():dr.End()]))
		delRange := dr
		if topLevel {
			delRange = editDeletionRange(text, dr)
		}
		b.claim(op, file.FileName(), delRange)
		b.addEdit(file.FileName(), icore.TextChange{TextRange: delRange, NewText: ""})
	}
	moved := sb.String()
	if insertPos > 0 && text[insertPos-1] != '\n' {
		moved = "\n" + moved
	}
	if topLevel {
		switch op.Place.Kind {
		case "after", "end":
			if !editBlankLineBefore(text, insertPos) {
				moved = "\n" + moved
			}
		case "before", "top":
			if !editBlankLineAt(text, insertPos) {
				moved += "\n"
			}
		}
	}
	b.addEdit(file.FileName(), icore.TextChange{TextRange: icore.NewTextRange(insertPos, insertPos), NewText: moved})
	notes := editGroupNotes(nodes, "moved")
	if op.WithDeps {
		notes = append(notes, "with-deps has no effect on within-file moves")
	}
	b.report(op, notes, b.ws.RelPath(file.FileName()))
}

// crossFileMove plans a cross-file move through the shared mv-symbol planner
// (imports rewritten both ways). Planner refusals abort the whole script with
// the op's line attached, keeping their exit codes (typically 4).
func (b *editBuilder) crossFileMove(ctx context.Context, r editResolvedOp, dest symbolMoveDest) error {
	op := r.op
	if r.symbol == nil {
		b.failf(op, "the moved declaration has no resolvable symbol")
		return nil
	}
	editsBefore, opsBefore := len(b.es.Edits), len(b.es.Ops)
	notes, err := planSymbolMove(ctx, b.ws, r.declNode, r.symbol, dest, op.WithDeps, &b.es)
	if err != nil {
		return cli.Errorf(cli.ExitCode(err), "line %d: %v", op.Line, err)
	}
	b.claim(op, r.declFile.FileName(), refactorDeletionRange(r.declFile, r.declNode))
	var files []string
	for _, fe := range b.es.Edits[editsBefore:] {
		files = append(files, b.ws.RelPath(fe.FileName))
	}
	for _, fop := range b.es.Ops[opsBefore:] {
		files = append(files, b.ws.RelPath(fop.Path))
	}
	b.report(op, notes, files...)
	return nil
}

// editClassLikeContainer returns the class-like node a declaration is a
// member of (classes and interfaces), or nil.
func editClassLikeContainer(decl *ast.Node) *ast.Node {
	if decl == nil || decl.Parent == nil {
		return nil
	}
	switch decl.Parent.Kind {
	case ast.KindClassDeclaration, ast.KindClassExpression, ast.KindInterfaceDeclaration:
		return decl.Parent
	}
	return nil
}

func editEnsureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
