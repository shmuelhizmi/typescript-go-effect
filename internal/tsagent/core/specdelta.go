package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/diagnostics"
)

// specdelta.go is the diagnostics-delta engine for speculative checks
// (`check --with-diff` / `--with-edits`, plan §1.10): snapshot the
// diagnostics of two programs (base and speculative) and classify every
// diagnostic as unchanged, moved, new, or fixed. The shape is shared so
// later phases (snapshots, the serve daemon) can reuse it.

// SpecRange is a 1-based line/col range (UTF-8 byte columns). It mirrors the
// `check` schema's range shape.
type SpecRange struct {
	Line    int `json:"line"`
	Col     int `json:"col"`
	EndLine int `json:"endLine"`
	EndCol  int `json:"endCol"`
}

// SpecDiag is one diagnostic in a project-wide snapshot. The JSON shape
// mirrors the `check` command's diagnostic schema so speculative results
// look like ordinary check results.
type SpecDiag struct {
	File     string    `json:"file"`
	Range    SpecRange `json:"range"`
	Code     string    `json:"code"`
	Category string    `json:"category"`
	Message  string    `json:"message"`
	// DiagRef is the stable `file:pos:code` reference used across commands.
	DiagRef string `json:"diagRef"`

	// Byte offsets, kept out of the JSON schema; they feed exact-identity
	// matching in DiagnosticsDelta.
	pos int
	end int
}

// exactKey is the strict diagnostic identity (file, code, pos, end, message).
func (d *SpecDiag) exactKey() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s", d.File, d.Code, d.pos, d.end, d.Message)
}

// fuzzyKey ignores positions so diagnostics that merely shifted under an
// edit still match (file, code, message).
func (d *SpecDiag) fuzzyKey() string {
	return d.File + "\x00" + d.Code + "\x00" + d.Message
}

// CollectDiagnostics gathers error- and warning-category syntactic and
// semantic diagnostics for every project file (lib files, declaration files,
// and node_modules excluded), sorted by (file, pos).
func CollectDiagnostics(ctx context.Context, ws *Workspace) []SpecDiag {
	var result []SpecDiag
	for _, file := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(file) || file.IsDeclarationFile || strings.Contains(file.FileName(), "/node_modules/") {
			continue
		}
		diags := ws.Program.GetSyntacticDiagnostics(ctx, file)
		diags = append(diags, ws.Program.GetSemanticDiagnostics(ctx, file)...)
		rel := ws.RelPath(file.FileName())
		for _, diag := range diags {
			category := diag.Category()
			if category != diagnostics.CategoryError && category != diagnostics.CategoryWarning {
				continue
			}
			line, col := ws.PosToLineCol(file, diag.Pos())
			endLine, endCol := ws.PosToLineCol(file, diag.End())
			code := fmt.Sprintf("TS%d", diag.Code())
			result = append(result, SpecDiag{
				File:     rel,
				Range:    SpecRange{Line: line, Col: col, EndLine: endLine, EndCol: endCol},
				Code:     code,
				Category: categoryString(category),
				Message:  diag.String(),
				DiagRef:  fmt.Sprintf("%s:%d:%s", rel, diag.Pos(), code),
				pos:      diag.Pos(),
				end:      diag.End(),
			})
		}
	}
	slices.SortFunc(result, func(a, b SpecDiag) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.pos != b.pos {
			return a.pos - b.pos
		}
		return strings.Compare(a.Code, b.Code)
	})
	return result
}

func categoryString(category diagnostics.Category) string {
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

// DiagDelta classifies the diagnostics of a speculative program against a
// base snapshot.
type DiagDelta struct {
	// New are diagnostics present only in the speculative snapshot.
	New []SpecDiag `json:"new"`
	// Fixed are diagnostics present only in the base snapshot.
	Fixed []SpecDiag `json:"fixed"`
	// Moved are diagnostics that match a base diagnostic on (file, code,
	// message) but at a different position; they count as unchanged for
	// gating purposes. Entries carry the speculative (new) position.
	Moved []SpecDiag `json:"moved"`
	// UnchangedCount counts exact-identity matches (Moved not included).
	UnchangedCount int `json:"unchangedCount"`
}

// DiagnosticsDelta matches the speculative snapshot against the base
// snapshot per plan §1.10: exact identity (file, code, pos, end, message)
// first, then a fuzzy (file, code, message) multiset pass that classifies
// position-shifted diagnostics as moved instead of new+fixed pairs.
func DiagnosticsDelta(base []SpecDiag, spec []SpecDiag) DiagDelta {
	delta := DiagDelta{}
	matchedBase := make([]bool, len(base))

	exact := make(map[string][]int, len(base))
	for i := range base {
		key := base[i].exactKey()
		exact[key] = append(exact[key], i)
	}
	var unmatchedSpec []int
	for i := range spec {
		key := spec[i].exactKey()
		if idxs := exact[key]; len(idxs) > 0 {
			matchedBase[idxs[0]] = true
			exact[key] = idxs[1:]
			delta.UnchangedCount++
			continue
		}
		unmatchedSpec = append(unmatchedSpec, i)
	}

	fuzzy := make(map[string][]int)
	for i := range base {
		if !matchedBase[i] {
			key := base[i].fuzzyKey()
			fuzzy[key] = append(fuzzy[key], i)
		}
	}
	for _, i := range unmatchedSpec {
		key := spec[i].fuzzyKey()
		if idxs := fuzzy[key]; len(idxs) > 0 {
			matchedBase[idxs[0]] = true
			fuzzy[key] = idxs[1:]
			delta.Moved = append(delta.Moved, spec[i])
			continue
		}
		delta.New = append(delta.New, spec[i])
	}
	for i := range base {
		if !matchedBase[i] {
			delta.Fixed = append(delta.Fixed, base[i])
		}
	}
	return delta
}

// SpeculativeWorkspace builds a second Workspace whose file system layers
// the given contents (and deletions) over ws's file system, with the same
// project config. Disk is never touched. This mirrors the (unexported)
// bootstrap inside edits.go's diagnosticsDelta; it is re-stated here as the
// shared primitive for `check --with-diff` and `api diff`.
func SpeculativeWorkspace(ws *Workspace, contents map[string]string, deleted []string, singleThreaded bool) (*Workspace, error) {
	overlay := NewOverlayFS(ws.FS, contents, deleted)
	return NewWorkspace(Options{
		Project:        ws.ConfigPath,
		Cwd:            ws.Cwd,
		FS:             overlay,
		SingleThreaded: singleThreaded,
	})
}

// OverlayFromEditSet materializes an EditSet's final state into overlay
// contents and a deleted set (the inputs of SpeculativeWorkspace) without
// applying anything to disk. It reuses the transaction engine's plan
// computation, so semantics (edit normalization, create/delete/rename
// ordering) are identical to `Execute`.
func OverlayFromEditSet(ws *Workspace, es EditSet) (contents map[string]string, deleted []string, err error) {
	plan, err := computeTxPlan(ws, es)
	if err != nil {
		return nil, nil, err
	}
	return plan.overlayContents, plan.overlayDeleted, nil
}
