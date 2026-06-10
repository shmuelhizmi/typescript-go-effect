package cmds

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
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
	if len(b.msgs) > 0 {
		return nil, cli.UsageErrorf("%s", strings.Join(b.msgs, "\n"))
	}

	tx, err := core.Execute(ctx, ws, b.es, core.TxOpts{Apply: !f.dryRun, AllowErrors: f.allowErrors, StrictGate: f.strictGate})
	if err != nil {
		return nil, err
	}
	if tx.Refused {
		return nil, cli.RefusedErrorf("edits would introduce %d new error(s) (pass --allow-errors to apply anyway):\n%s",
			len(tx.NewErrors), editDiagsText(tx.NewErrors))
	}
	return &EditScriptResult{Ops: b.reports, Tx: tx}, nil
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
	// Anchor of before/after/into places.
	anchorDecl *ast.Node
	anchorNode *ast.Node
	anchorFile *ast.SourceFile
	// Target file of top/end places.
	destFile *ast.SourceFile
}

// editResolveOps resolves every op's symbol IDs and file paths. All failures
// are reported together (exit 3; exit 2 when every failure is a malformed or
// ambiguous ID rather than a miss), with closest-ID suggestions for unknown
// symbols whose file part resolves.
func editResolveOps(ctx context.Context, ws *core.Workspace, ops []editOp) ([]editResolvedOp, error) {
	var msgs []string
	sawNotFound := false
	resolveSym := func(line int, id string) (*ast.Symbol, *ast.Node, bool) {
		symbol, decl, err := core.DecodeSymbolID(ctx, ws, id)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				sawNotFound = true
				msgs = append(msgs, fmt.Sprintf("line %d: unknown symbol %s%s", line, id, editClosestIDs(ws, id)))
			} else {
				msgs = append(msgs, fmt.Sprintf("line %d: %v", line, err))
			}
			return nil, nil, false
		}
		if decl == nil {
			sawNotFound = true
			msgs = append(msgs, fmt.Sprintf("line %d: symbol %s has no declaration", line, id))
			return nil, nil, false
		}
		return symbol, decl, true
	}

	resolved := make([]editResolvedOp, 0, len(ops))
	for _, op := range ops {
		r := editResolvedOp{op: op}
		ok := true
		if op.Sym != "" {
			if symbol, decl, k := resolveSym(op.Line, op.Sym); k {
				r.symbol, r.decl = symbol, decl
				r.declNode = refactorDeletionNode(decl)
				r.declFile = ast.GetSourceFileOfNode(decl)
			} else {
				ok = false
			}
		}
		switch op.Place.Kind {
		case "before", "after", "into":
			if _, decl, k := resolveSym(op.Line, op.Place.Sym); k {
				r.anchorDecl = decl
				r.anchorNode = refactorDeletionNode(decl)
				r.anchorFile = ast.GetSourceFileOfNode(decl)
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
	msgs    []string
	reports []EditOpReport
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
		dr := refactorCollapseBlankAfterDeletion(r.declFile.Text(), refactorDeletionRange(r.declFile, r.declNode))
		b.claim(op, r.declFile.FileName(), dr)
		b.addEdit(r.declFile.FileName(), icore.TextChange{TextRange: dr, NewText: ""})
		b.report(op, nil, b.ws.RelPath(r.declFile.FileName()))
	case "replace":
		dr := refactorDeletionRange(r.declFile, r.declNode)
		b.claim(op, r.declFile.FileName(), dr)
		b.addEdit(r.declFile.FileName(), icore.TextChange{TextRange: dr, NewText: editEnsureNewline(op.Body)})
		b.report(op, nil, b.ws.RelPath(r.declFile.FileName()))
	case "insert":
		file, pos, err := editInsertPos(r)
		if err != nil {
			b.failf(op, "%v", err)
			return nil
		}
		newText := editSeparateBlock(r, file, pos, editEnsureNewline(op.Body))
		b.addEdit(file.FileName(), icore.TextChange{TextRange: icore.NewTextRange(pos, pos), NewText: newText})
		b.report(op, nil, b.ws.RelPath(file.FileName()))
	case "move":
		return b.buildMove(ctx, r)
	}
	return nil
}

// editInsertPos resolves an insert op's file and byte offset. `top` lands
// below any shebang and directive prologue (`"use client";`), never above it.
func editInsertPos(r editResolvedOp) (*ast.SourceFile, int, error) {
	switch r.op.Place.Kind {
	case "before":
		return r.anchorFile, refactorDeletionRange(r.anchorFile, r.anchorNode).Pos(), nil
	case "after":
		return r.anchorFile, refactorDeletionRange(r.anchorFile, r.anchorNode).End(), nil
	case "top":
		return r.destFile, importInsertOffset(r.destFile), nil
	case "end":
		return r.destFile, len(r.destFile.Text()), nil
	case "into":
		pos, err := editInsertIntoPos(r.anchorFile, r.anchorDecl)
		if err != nil {
			return nil, 0, err
		}
		return r.anchorFile, pos, nil
	}
	return nil, 0, fmt.Errorf("unknown insert place %q", r.op.Place.Kind)
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
// blank trivia) when the body is empty.
func editInsertIntoPos(file *ast.SourceFile, container *ast.Node) (int, error) {
	var list *ast.NodeList
	switch container.Kind {
	case ast.KindClassDeclaration, ast.KindClassExpression, ast.KindInterfaceDeclaration, ast.KindEnumDeclaration:
		list = container.MemberList()
	case ast.KindModuleDeclaration:
		body := container.Body()
		if body == nil || body.Kind != ast.KindModuleBlock {
			return 0, fmt.Errorf("insert into: the namespace has no block body")
		}
		list = body.AsModuleBlock().Statements
	default:
		return 0, fmt.Errorf("insert into requires a class, interface, enum, or namespace target")
	}
	if list == nil {
		return 0, fmt.Errorf("insert into: the container has no member list")
	}
	if n := len(list.Nodes); n > 0 {
		return refactorDeletionRange(file, list.Nodes[n-1]).End(), nil
	}
	text := file.Text()
	pos := list.Pos()
	for pos < len(text) && pos < container.End() &&
		(text[pos] == ' ' || text[pos] == '\t' || text[pos] == '\r' || text[pos] == '\n') {
		pos++
	}
	return pos, nil
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
			b.reorder(op, r.declFile, r.declNode, pos)
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
	if r.anchorDecl == r.decl || r.anchorNode == r.declNode {
		b.failf(op, "cannot move a declaration relative to itself")
		return nil
	}
	anchorTop := r.anchorNode.Parent != nil && r.anchorNode.Parent.Kind == ast.KindSourceFile
	anchorContainer := editClassLikeContainer(r.anchorDecl)
	anchorRange := func() icore.TextRange { return refactorDeletionRange(r.anchorFile, r.anchorNode) }
	anchorPos := func() int {
		ar := anchorRange()
		if op.Place.Kind == "after" {
			return ar.End()
		}
		return ar.Pos()
	}
	switch {
	case container != nil && anchorContainer == container:
		b.reorder(op, r.declFile, r.declNode, anchorPos())
	case container != nil:
		b.failf(op, "cannot move a member across containers; use delete + insert into")
	case !topLevel:
		b.failf(op, "cannot move a function-local declaration")
	case !anchorTop:
		b.failf(op, "the move anchor must be a top-level declaration")
	case r.anchorFile == r.declFile:
		b.reorder(op, r.declFile, r.declNode, anchorPos())
	default:
		return b.crossFileMove(ctx, r, symbolMoveDest{fileAbs: r.anchorFile.FileName(), file: r.anchorFile, insertPos: anchorPos()})
	}
	return nil
}

// reorder emits a within-file move: delete the declaration's trivia-aware
// range and re-insert the exact same bytes (newline-terminated) at insertPos.
// Both offsets address the original text; the engine merges them. Top-level
// moves get blank-line hygiene: one blank line separates the re-inserted
// block from its before/after anchor, and a deletion that would leave a
// double blank line eats one extra newline.
func (b *editBuilder) reorder(op editOp, file *ast.SourceFile, node *ast.Node, insertPos int) {
	text := file.Text()
	dr := refactorDeletionRange(file, node)
	moved := editEnsureNewline(text[dr.Pos():dr.End()])
	if insertPos > 0 && text[insertPos-1] != '\n' {
		moved = "\n" + moved
	}
	delRange := dr
	if node.Parent != nil && node.Parent.Kind == ast.KindSourceFile {
		delRange = refactorCollapseBlankAfterDeletion(text, dr)
		switch op.Place.Kind {
		case "after":
			if !editBlankLineBefore(text, insertPos) {
				moved = "\n" + moved
			}
		case "before", "top":
			if !editBlankLineAt(text, insertPos) {
				moved += "\n"
			}
		case "end":
			if !editBlankLineBefore(text, insertPos) {
				moved = "\n" + moved
			}
		}
	}
	b.claim(op, file.FileName(), delRange)
	b.addEdit(file.FileName(), icore.TextChange{TextRange: delRange, NewText: ""})
	b.addEdit(file.FileName(), icore.TextChange{TextRange: icore.NewTextRange(insertPos, insertPos), NewText: moved})
	b.report(op, nil, b.ws.RelPath(file.FileName()))
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
