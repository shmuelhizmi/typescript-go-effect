package core

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/diagnostics"
	"github.com/microsoft/typescript-go/internal/ls/lsconv"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// edits.go is the canonical edit model and transaction engine (§2.4): every
// mutating command converts its language-service output into an EditSet and
// hands it to Execute, which renders dry-run diffs, runs the diagnostics
// gate against a speculative overlay program, and applies atomically.

// FileEdit is a set of byte-offset text changes within one file.
type FileEdit struct {
	FileName string
	Edits    []core.TextChange
}

// FileOpKind classifies a file-level operation.
type FileOpKind string

const (
	FileOpCreate FileOpKind = "create"
	FileOpDelete FileOpKind = "delete"
	FileOpRename FileOpKind = "rename"
)

// FileOp is a file-level create/delete/rename operation. Within a
// transaction, text edits are applied first (addressed at pre-rename paths),
// then file ops in order.
type FileOp struct {
	Kind    FileOpKind
	Path    string
	NewPath string // rename only
	Content string // create only
}

// EditSet is the canonical edit collection every mutating command produces.
type EditSet struct {
	Edits []FileEdit
	Ops   []FileOp
}

// IsEmpty reports whether the edit set contains no work.
func (es *EditSet) IsEmpty() bool {
	return len(es.Edits) == 0 && len(es.Ops) == 0
}

// ---------------------------------------------------------------------------
// LSP conversion

// textScript adapts raw file content to the lsconv.Script interface for
// files that are not part of the program (e.g. tsconfig.json).
type textScript struct {
	fileName string
	text     string
}

func (s *textScript) FileName() string { return s.fileName }
func (s *textScript) Text() string     { return s.text }

func scriptFor(ws *Workspace, fileName string) (lsconv.Script, error) {
	if file := ws.Program.GetSourceFile(fileName); file != nil {
		return file, nil
	}
	text, ok := ws.FS.ReadFile(fileName)
	if !ok {
		return nil, fmt.Errorf("cannot read %s: %w", fileName, ErrNotFound)
	}
	return &textScript{fileName: fileName, text: text}, nil
}

// readFileText returns the current content of a file, preferring the
// program's snapshot.
func readFileText(ws *Workspace, fileName string) (string, error) {
	if file := ws.Program.GetSourceFile(fileName); file != nil {
		return file.Text(), nil
	}
	if text, ok := ws.FS.ReadFile(fileName); ok {
		return text, nil
	}
	return "", fmt.Errorf("cannot read %s: %w", fileName, ErrNotFound)
}

// AddTextEdits converts LSP text edits for one file into byte-offset edits.
func (es *EditSet) AddTextEdits(ws *Workspace, fileName string, edits []*lsproto.TextEdit) error {
	if len(edits) == 0 {
		return nil
	}
	script, err := scriptFor(ws, fileName)
	if err != nil {
		return err
	}
	fe := FileEdit{FileName: tspath.NormalizePath(fileName)}
	for _, edit := range edits {
		fe.Edits = append(fe.Edits, core.TextChange{
			TextRange: ws.Conv.FromLSPRange(script, edit.Range),
			NewText:   edit.NewText,
		})
	}
	es.Edits = append(es.Edits, fe)
	return nil
}

// AddWorkspaceEdit converts an lsproto.WorkspaceEdit (both the Changes map
// and DocumentChanges forms) into the edit set.
func (es *EditSet) AddWorkspaceEdit(ws *Workspace, edit *lsproto.WorkspaceEdit) error {
	if edit == nil {
		return nil
	}
	if edit.Changes != nil {
		for _, uri := range slices.Sorted(maps.Keys(*edit.Changes)) {
			if err := es.AddTextEdits(ws, uri.FileName(), (*edit.Changes)[uri]); err != nil {
				return err
			}
		}
	}
	if edit.DocumentChanges != nil {
		if err := es.AddDocumentChanges(ws, *edit.DocumentChanges); err != nil {
			return err
		}
	}
	return nil
}

// AddDocumentChanges converts the LSP document-change union list (text edits
// plus create/rename/delete file operations) into the edit set. This is the
// shape returned by ls.GetEditsForFileRename.
func (es *EditSet) AddDocumentChanges(ws *Workspace, changes []lsproto.TextDocumentEditOrCreateFileOrRenameFileOrDeleteFile) error {
	for _, change := range changes {
		switch {
		case change.TextDocumentEdit != nil:
			edits := make([]*lsproto.TextEdit, 0, len(change.TextDocumentEdit.Edits))
			for _, e := range change.TextDocumentEdit.Edits {
				switch {
				case e.TextEdit != nil:
					edits = append(edits, e.TextEdit)
				case e.AnnotatedTextEdit != nil:
					edits = append(edits, &lsproto.TextEdit{Range: e.AnnotatedTextEdit.Range, NewText: e.AnnotatedTextEdit.NewText})
				case e.SnippetTextEdit != nil:
					return fmt.Errorf("snippet text edits are not supported: %w", ErrInvalidArgument)
				}
			}
			if err := es.AddTextEdits(ws, change.TextDocumentEdit.TextDocument.Uri.FileName(), edits); err != nil {
				return err
			}
		case change.CreateFile != nil:
			es.Ops = append(es.Ops, FileOp{Kind: FileOpCreate, Path: change.CreateFile.Uri.FileName()})
		case change.RenameFile != nil:
			es.Ops = append(es.Ops, FileOp{Kind: FileOpRename, Path: change.RenameFile.OldUri.FileName(), NewPath: change.RenameFile.NewUri.FileName()})
		case change.DeleteFile != nil:
			es.Ops = append(es.Ops, FileOp{Kind: FileOpDelete, Path: change.DeleteFile.Uri.FileName()})
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Applying edits

// normalizeTextChanges sorts edits ascending by position (the contract of
// core.ApplyBulkEdits), drops exact duplicates, and rejects overlaps.
func normalizeTextChanges(fileName string, textLen int, edits []core.TextChange) ([]core.TextChange, error) {
	sorted := slices.Clone(edits)
	slices.SortStableFunc(sorted, func(a, b core.TextChange) int {
		if a.Pos() != b.Pos() {
			return a.Pos() - b.Pos()
		}
		return a.End() - b.End()
	})
	result := sorted[:0]
	lastEnd := 0
	for i, edit := range sorted {
		if edit.Pos() < 0 || edit.End() < edit.Pos() || edit.End() > textLen {
			return nil, fmt.Errorf("edit range [%d,%d) out of bounds for %s (len %d): %w", edit.Pos(), edit.End(), fileName, textLen, ErrInvalidArgument)
		}
		if i > 0 {
			prev := sorted[i-1]
			if edit.Pos() == prev.Pos() && edit.End() == prev.End() && edit.NewText == prev.NewText {
				continue // exact duplicate (e.g. the same edit reported twice)
			}
		}
		if edit.Pos() < lastEnd {
			return nil, fmt.Errorf("overlapping edits at [%d,%d) in %s: %w", edit.Pos(), edit.End(), fileName, ErrInvalidArgument)
		}
		lastEnd = edit.End()
		result = append(result, edit)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Transaction engine

// TxOpts configures a transaction run.
type TxOpts struct {
	// Apply writes the result to the workspace FS; otherwise the run is a
	// dry-run that only reports diffs.
	Apply bool
	// AllowErrors applies even when the diagnostics gate finds new errors.
	AllowErrors bool
	// SingleThreaded builds the speculative gate program single-threaded
	// (used by tests).
	SingleThreaded bool
}

// FileDiff is the rendered diff of one affected file.
type FileDiff struct {
	File    string `json:"file"`
	NewFile string `json:"newFile,omitempty"` // rename target
	Kind    string `json:"kind"`              // edit | create | delete | rename
	Diff    string `json:"diff"`
}

// TxDiag is one diagnostic in the transaction's diagnostics delta.
type TxDiag struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// TxResult is the result of a transaction run (dry-run or apply).
type TxResult struct {
	Applied bool `json:"applied"`
	// Refused is set when the diagnostics gate found new errors and
	// AllowErrors was off; nothing was written. The CLI layer converts this
	// to exit code 4.
	Refused      bool       `json:"refused,omitempty"`
	FilesChanged []string   `json:"filesChanged"`
	Diffs        []FileDiff `json:"diffs"`
	NewErrors    []TxDiag   `json:"newErrors,omitempty"`
	FixedErrors  int        `json:"fixedErrors"`
	Notes        []string   `json:"notes,omitempty"`
}

// WriteText renders the compact text form: the unified diffs followed by a
// summary line (satisfies the cli Texter interface structurally).
func (r *TxResult) WriteText(w io.Writer) error {
	for _, d := range r.Diffs {
		if d.Diff == "" && d.Kind == "rename" {
			if _, err := fmt.Fprintf(w, "rename %s -> %s (no content changes)\n", d.File, d.NewFile); err != nil {
				return err
			}
			continue
		}
		if _, err := io.WriteString(w, d.Diff); err != nil {
			return err
		}
	}
	var summary string
	switch {
	case r.Refused:
		summary = fmt.Sprintf("refused: %d new error(s) would be introduced", len(r.NewErrors))
	case r.Applied:
		summary = fmt.Sprintf("applied: %d file(s) changed", len(r.FilesChanged))
	default:
		summary = fmt.Sprintf("dry-run: %d file(s) would change (pass --apply to write)", len(r.FilesChanged))
	}
	if _, err := fmt.Fprintln(w, summary); err != nil {
		return err
	}
	if r.Applied || r.Refused {
		if _, err := fmt.Fprintf(w, "diagnostics: %d new, %d fixed\n", len(r.NewErrors), r.FixedErrors); err != nil {
			return err
		}
		for _, d := range r.NewErrors {
			if _, err := fmt.Fprintf(w, "  new %s:%d:%d %s: %s\n", d.File, d.Line, d.Col, d.Code, d.Message); err != nil {
				return err
			}
		}
	}
	for _, note := range r.Notes {
		if _, err := fmt.Fprintf(w, "note: %s\n", note); err != nil {
			return err
		}
	}
	return nil
}

type editedFile struct {
	oldText string
	newText string
}

type renamePair struct {
	from    string // abs old path
	to      string // abs new path
	oldText string // pre-edit content (for diff rendering)
	content string // final content written at `to`
}

// txPlan is the fully-materialized result of an EditSet against the current
// workspace state.
type txPlan struct {
	diffs        []FileDiff
	filesChanged []string
	writes       map[string]string // abs path → content (edits in place + creates)
	renames      []renamePair
	deletes      []string // abs paths (delete ops)

	overlayContents map[string]string // final state for the speculative FS
	overlayDeleted  []string
	renameAlias     map[string]string // canonical new abs path → old rel path (diag delta keying)
}

func computeTxPlan(ws *Workspace, es EditSet) (*txPlan, error) {
	// Merge edits per file.
	byFile := make(map[string][]core.TextChange)
	for _, fe := range es.Edits {
		abs := tspath.GetNormalizedAbsolutePath(fe.FileName, ws.Cwd)
		byFile[abs] = append(byFile[abs], fe.Edits...)
	}

	edited := make(map[string]editedFile)
	for _, abs := range slices.Sorted(maps.Keys(byFile)) {
		oldText, err := readFileText(ws, abs)
		if err != nil {
			return nil, err
		}
		edits, err := normalizeTextChanges(ws.RelPath(abs), len(oldText), byFile[abs])
		if err != nil {
			return nil, err
		}
		newText := core.ApplyBulkEdits(oldText, edits)
		if newText != oldText {
			edited[abs] = editedFile{oldText: oldText, newText: newText}
		}
	}

	created := make(map[string]string)
	deleted := make(map[string]string) // abs → old content (for diff)
	var renames []renamePair
	renamedAway := make(map[string]bool)

	currentlyExists := func(abs string) bool {
		if _, ok := created[abs]; ok {
			return true
		}
		if _, ok := deleted[abs]; ok {
			return false
		}
		if renamedAway[abs] {
			return false
		}
		if slices.ContainsFunc(renames, func(r renamePair) bool { return r.to == abs }) {
			return true
		}
		return ws.FS.FileExists(abs)
	}

	for _, op := range es.Ops {
		switch op.Kind {
		case FileOpCreate:
			abs := tspath.GetNormalizedAbsolutePath(op.Path, ws.Cwd)
			if currentlyExists(abs) {
				return nil, fmt.Errorf("create %s: file already exists: %w", ws.RelPath(abs), ErrInvalidArgument)
			}
			created[abs] = op.Content
		case FileOpDelete:
			abs := tspath.GetNormalizedAbsolutePath(op.Path, ws.Cwd)
			if _, ok := created[abs]; ok {
				delete(created, abs) // created then deleted: net no-op
				continue
			}
			if !currentlyExists(abs) {
				return nil, fmt.Errorf("delete %s: file does not exist: %w", ws.RelPath(abs), ErrNotFound)
			}
			if e, ok := edited[abs]; ok {
				deleted[abs] = e.oldText
				delete(edited, abs)
			} else {
				oldText, err := readFileText(ws, abs)
				if err != nil {
					return nil, err
				}
				deleted[abs] = oldText
			}
		case FileOpRename:
			from := tspath.GetNormalizedAbsolutePath(op.Path, ws.Cwd)
			to := tspath.GetNormalizedAbsolutePath(op.NewPath, ws.Cwd)
			if from == to {
				return nil, fmt.Errorf("rename %s: source and destination are the same: %w", ws.RelPath(from), ErrInvalidArgument)
			}
			if currentlyExists(to) {
				return nil, fmt.Errorf("rename %s -> %s: destination already exists: %w", ws.RelPath(from), ws.RelPath(to), ErrInvalidArgument)
			}
			var pair renamePair
			if e, ok := edited[from]; ok {
				pair = renamePair{from: from, to: to, oldText: e.oldText, content: e.newText}
				delete(edited, from)
			} else {
				if content, ok := created[from]; ok {
					delete(created, from)
					created[to] = content
					continue
				}
				if !currentlyExists(from) {
					return nil, fmt.Errorf("rename %s: file does not exist: %w", ws.RelPath(from), ErrNotFound)
				}
				oldText, err := readFileText(ws, from)
				if err != nil {
					return nil, err
				}
				pair = renamePair{from: from, to: to, oldText: oldText, content: oldText}
			}
			renamedAway[from] = true
			renames = append(renames, pair)
		default:
			return nil, fmt.Errorf("unknown file op kind %q: %w", op.Kind, ErrInvalidArgument)
		}
	}

	plan := &txPlan{
		writes:          make(map[string]string),
		overlayContents: make(map[string]string),
		renameAlias:     make(map[string]string),
	}
	canonical := func(abs string) string {
		return tspath.GetCanonicalFileName(abs, ws.FS.UseCaseSensitiveFileNames())
	}

	for _, abs := range slices.Sorted(maps.Keys(edited)) {
		e := edited[abs]
		rel := ws.RelPath(abs)
		plan.diffs = append(plan.diffs, FileDiff{
			File: rel,
			Kind: "edit",
			Diff: UnifiedDiff("a/"+rel, "b/"+rel, e.oldText, e.newText),
		})
		plan.writes[abs] = e.newText
		plan.overlayContents[abs] = e.newText
		plan.filesChanged = append(plan.filesChanged, rel)
	}
	for _, abs := range slices.Sorted(maps.Keys(created)) {
		rel := ws.RelPath(abs)
		plan.diffs = append(plan.diffs, FileDiff{
			File: rel,
			Kind: "create",
			Diff: UnifiedDiff("/dev/null", "b/"+rel, "", created[abs]),
		})
		plan.writes[abs] = created[abs]
		plan.overlayContents[abs] = created[abs]
		plan.filesChanged = append(plan.filesChanged, rel)
	}
	for _, pair := range renames {
		oldRel := ws.RelPath(pair.from)
		newRel := ws.RelPath(pair.to)
		plan.diffs = append(plan.diffs, FileDiff{
			File:    oldRel,
			NewFile: newRel,
			Kind:    "rename",
			Diff:    UnifiedDiff("a/"+oldRel, "b/"+newRel, pair.oldText, pair.content),
		})
		plan.renames = append(plan.renames, pair)
		plan.overlayContents[pair.to] = pair.content
		plan.overlayDeleted = append(plan.overlayDeleted, pair.from)
		plan.renameAlias[canonical(pair.to)] = oldRel
		plan.filesChanged = append(plan.filesChanged, newRel)
	}
	for _, abs := range slices.Sorted(maps.Keys(deleted)) {
		rel := ws.RelPath(abs)
		plan.diffs = append(plan.diffs, FileDiff{
			File: rel,
			Kind: "delete",
			Diff: UnifiedDiff("a/"+rel, "/dev/null", deleted[abs], ""),
		})
		plan.deletes = append(plan.deletes, abs)
		plan.overlayDeleted = append(plan.overlayDeleted, abs)
		plan.filesChanged = append(plan.filesChanged, rel)
	}
	slices.Sort(plan.filesChanged)
	return plan, nil
}

// Execute runs an edit set as a transaction. Dry-run (the default) returns
// diffs without touching the FS. Apply first builds a speculative program
// over an overlay FS and compares error diagnostics with the current
// program; when new errors appear and AllowErrors is off, the result is
// marked Refused and nothing is written. All file IO goes through ws.FS.
func Execute(ctx context.Context, ws *Workspace, es EditSet, opts TxOpts) (*TxResult, error) {
	plan, err := computeTxPlan(ws, es)
	if err != nil {
		return nil, err
	}
	result := &TxResult{
		FilesChanged: plan.filesChanged,
		Diffs:        plan.diffs,
	}
	if !opts.Apply {
		return result, nil
	}
	if len(plan.filesChanged) == 0 {
		result.Applied = true
		return result, nil
	}

	// Diagnostics gate: build the post-edit program speculatively and diff
	// error diagnostics against the current program.
	newErrors, fixed, err := diagnosticsDelta(ctx, ws, plan, opts.SingleThreaded)
	if err != nil {
		return nil, fmt.Errorf("building speculative program for the diagnostics gate: %w", err)
	}
	result.NewErrors = newErrors
	result.FixedErrors = fixed
	if len(newErrors) > 0 && !opts.AllowErrors {
		result.Refused = true
		return result, nil
	}

	// Write phase: in-place edits and creates first, then renames, then
	// deletes. All IO via ws.FS so in-memory test file systems work.
	for _, abs := range slices.Sorted(maps.Keys(plan.writes)) {
		if err := ws.FS.WriteFile(abs, plan.writes[abs]); err != nil {
			return nil, fmt.Errorf("writing %s: %w", ws.RelPath(abs), err)
		}
	}
	for _, pair := range plan.renames {
		if err := ws.FS.WriteFile(pair.to, pair.content); err != nil {
			return nil, fmt.Errorf("writing %s: %w", ws.RelPath(pair.to), err)
		}
		if err := ws.FS.Remove(pair.from); err != nil {
			return nil, fmt.Errorf("removing %s: %w", ws.RelPath(pair.from), err)
		}
	}
	for _, abs := range plan.deletes {
		if err := ws.FS.Remove(abs); err != nil {
			return nil, fmt.Errorf("removing %s: %w", ws.RelPath(abs), err)
		}
	}
	result.Applied = true
	return result, nil
}

// ---------------------------------------------------------------------------
// Diagnostics delta

type txDiagEntry struct {
	key  string
	diag TxDiag
}

// diagnosticsDelta builds a speculative program over an overlay FS holding
// the transaction's final state and returns the diagnostics that would be
// introduced (new) and the count of diagnostics that would disappear
// (fixed). Identity is fuzzy — (file, code, message) as a multiset — so
// diagnostics that merely move are not reported as new+fixed pairs; renamed
// files are keyed by their pre-rename path on both sides.
func diagnosticsDelta(ctx context.Context, ws *Workspace, plan *txPlan, singleThreaded bool) ([]TxDiag, int, error) {
	overlay := NewOverlayFS(ws.FS, plan.overlayContents, plan.overlayDeleted)
	overlayWs, err := NewWorkspace(Options{
		Project:        ws.ConfigPath,
		Cwd:            ws.Cwd,
		FS:             overlay,
		SingleThreaded: singleThreaded,
	})
	if err != nil {
		return nil, 0, err
	}

	baseline := collectErrorDiags(ctx, ws, nil)
	next := collectErrorDiags(ctx, overlayWs, plan.renameAlias)

	baseCount := make(map[string]int, len(baseline))
	for _, entry := range baseline {
		baseCount[entry.key]++
	}
	var newErrors []TxDiag
	for _, entry := range next {
		if baseCount[entry.key] > 0 {
			baseCount[entry.key]--
			continue
		}
		newErrors = append(newErrors, entry.diag)
	}
	fixed := 0
	for _, count := range baseCount {
		fixed += count
	}
	return newErrors, fixed, nil
}

// collectErrorDiags gathers error-category syntactic and semantic
// diagnostics for all project files (libs, node_modules, and declaration
// files excluded). fileKeyAlias maps canonical absolute paths to alternate
// display-relative paths for delta keying (used for rename targets).
func collectErrorDiags(ctx context.Context, ws *Workspace, fileKeyAlias map[string]string) []txDiagEntry {
	var entries []txDiagEntry
	for _, file := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(file) || file.IsDeclarationFile || strings.Contains(file.FileName(), "/node_modules/") {
			continue
		}
		diags := ws.Program.GetSyntacticDiagnostics(ctx, file)
		diags = append(diags, ws.Program.GetSemanticDiagnostics(ctx, file)...)
		rel := ws.RelPath(file.FileName())
		keyFile := rel
		if alias, ok := fileKeyAlias[tspath.GetCanonicalFileName(file.FileName(), ws.FS.UseCaseSensitiveFileNames())]; ok {
			keyFile = alias
		}
		for _, diag := range diags {
			if diag.Category() != diagnostics.CategoryError {
				continue
			}
			line, col := ws.PosToLineCol(file, diag.Pos())
			code := fmt.Sprintf("TS%d", diag.Code())
			message := diag.String()
			entries = append(entries, txDiagEntry{
				key: keyFile + "\x00" + code + "\x00" + message,
				diag: TxDiag{
					File:    rel,
					Line:    line,
					Col:     col,
					Code:    code,
					Message: message,
				},
			})
		}
	}
	return entries
}
