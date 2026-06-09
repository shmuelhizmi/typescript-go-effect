package core

import (
	"fmt"
	"strings"
)

// diff.go implements a small, dependency-free unified-diff renderer and a
// matching parser. The renderer is used by the transaction engine to report
// dry-run results (§2.4); the parser feeds `check --with-diff` (Phase 6),
// which turns a patch into speculative overlay contents.

const diffContextLines = 3

// maxLCSCells caps the O(n*m) LCS table. Larger inputs degrade to a single
// replace hunk for the changed middle section (still a valid diff).
const maxLCSCells = 4_000_000

// splitDiffLines splits text into lines without their trailing newlines and
// reports whether the text ends with a newline. Empty text has no lines.
func splitDiffLines(text string) (lines []string, endsWithNewline bool) {
	if text == "" {
		return nil, true
	}
	endsWithNewline = strings.HasSuffix(text, "\n")
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n"), endsWithNewline
}

type diffOpKind int

const (
	diffEqual diffOpKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffOpKind
	line string
}

// diffLineOps computes a line-level edit script from a to b using LCS with
// common prefix/suffix trimming.
func diffLineOps(a []string, b []string) []diffOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	for _, line := range a[:prefix] {
		ops = append(ops, diffOp{diffEqual, line})
	}
	ops = append(ops, lcsOps(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix])...)
	for _, line := range a[len(a)-suffix:] {
		ops = append(ops, diffOp{diffEqual, line})
	}
	return ops
}

func lcsOps(a []string, b []string) []diffOp {
	var ops []diffOp
	if len(a)*len(b) > maxLCSCells || len(a) == 0 || len(b) == 0 {
		// Degenerate or oversized: whole-block replace.
		for _, line := range a {
			ops = append(ops, diffOp{diffDelete, line})
		}
		for _, line := range b {
			ops = append(ops, diffOp{diffInsert, line})
		}
		return ops
	}
	n, m := len(a), len(b)
	// dp[i*(m+1)+j] = LCS length of a[i:] and b[j:].
	dp := make([]int, (n+1)*(m+1))
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i*(m+1)+j] = dp[(i+1)*(m+1)+j+1] + 1
			} else {
				dp[i*(m+1)+j] = max(dp[(i+1)*(m+1)+j], dp[i*(m+1)+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{diffEqual, a[i]})
			i++
			j++
		case dp[(i+1)*(m+1)+j] >= dp[i*(m+1)+j+1]:
			ops = append(ops, diffOp{diffDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{diffInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{diffDelete, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{diffInsert, b[j]})
	}
	return ops
}

// UnifiedDiff renders a unified diff between oldText and newText. The labels
// are emitted verbatim in the `---`/`+++` header (callers pass "a/path",
// "b/path", or "/dev/null"). Returns "" when the texts are identical.
func UnifiedDiff(oldLabel string, newLabel string, oldText string, newText string) string {
	if oldText == newText {
		return ""
	}
	aLines, aNL := splitDiffLines(oldText)
	bLines, bNL := splitDiffLines(newText)
	ops := diffLineOps(aLines, bLines)
	// A newline-only difference on the final line produces an all-equal op
	// list; force the last line into a change so the diff is non-empty.
	if aNL != bNL && len(ops) > 0 && ops[len(ops)-1].kind == diffEqual {
		last := ops[len(ops)-1].line
		ops = append(ops[:len(ops)-1], diffOp{diffDelete, last}, diffOp{diffInsert, last})
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldLabel, newLabel)

	// Per-op cumulative a/b line offsets, for hunk headers.
	aOffset := make([]int, len(ops)+1)
	bOffset := make([]int, len(ops)+1)
	for i, op := range ops {
		aOffset[i+1] = aOffset[i]
		bOffset[i+1] = bOffset[i]
		if op.kind != diffInsert {
			aOffset[i+1]++
		}
		if op.kind != diffDelete {
			bOffset[i+1]++
		}
	}

	writeLine := func(op diffOp, opIndex int) {
		marker := byte(' ')
		switch op.kind {
		case diffDelete:
			marker = '-'
		case diffInsert:
			marker = '+'
		}
		sb.WriteByte(marker)
		sb.WriteString(op.line)
		sb.WriteByte('\n')
		noNewline := false
		if op.kind != diffInsert && !aNL && aOffset[opIndex+1] == len(aLines) {
			noNewline = true
		}
		if op.kind != diffDelete && !bNL && bOffset[opIndex+1] == len(bLines) {
			noNewline = true
		}
		if noNewline {
			sb.WriteString("\\ No newline at end of file\n")
		}
	}

	// Group changed ops into hunks with diffContextLines of context, merging
	// hunks whose context would overlap.
	i := 0
	for i < len(ops) {
		if ops[i].kind == diffEqual {
			i++
			continue
		}
		start := max(i-diffContextLines, 0)
		end := i
		for j := i; j < len(ops); j++ {
			if ops[j].kind != diffEqual {
				end = j + 1
				continue
			}
			// Stop if the run of equal ops is longer than twice the context
			// (would not merge with a following hunk).
			runEnd := j
			for runEnd < len(ops) && ops[runEnd].kind == diffEqual {
				runEnd++
			}
			if runEnd-j > 2*diffContextLines || runEnd == len(ops) {
				break
			}
			j = runEnd - 1
		}
		end = min(end+diffContextLines, len(ops))

		oldCount := aOffset[end] - aOffset[start]
		newCount := bOffset[end] - bOffset[start]
		oldStart := aOffset[start] + 1
		if oldCount == 0 {
			oldStart = aOffset[start]
		}
		newStart := bOffset[start] + 1
		if newCount == 0 {
			newStart = bOffset[start]
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for j := start; j < end; j++ {
			writeLine(ops[j], j)
		}
		i = end
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Parsing

type parsedDiffLine struct {
	op        byte // ' ', '-', '+'
	text      string
	noNewline bool
}

type parsedHunk struct {
	oldStart, oldCount int
	newStart, newCount int
	lines              []parsedDiffLine
}

// FileDiffPatch is the parsed diff of one file within a unified diff.
type FileDiffPatch struct {
	OldPath  string
	NewPath  string
	IsNew    bool
	IsDelete bool
	hunks    []parsedHunk
}

// ParseUnifiedDiff parses a (possibly multi-file) unified diff. The result is
// keyed by the path the patch produces (the old path for deletions). `a/`
// and `b/` prefixes are stripped; `/dev/null` marks creations and deletions.
func ParseUnifiedDiff(text string) (map[string]*FileDiffPatch, error) {
	patches := make(map[string]*FileDiffPatch)
	var current *FileDiffPatch
	var hunk *parsedHunk
	lineNo := 0

	finishFile := func() {
		if current == nil {
			return
		}
		if hunk != nil {
			current.hunks = append(current.hunks, *hunk)
			hunk = nil
		}
		key := current.NewPath
		if current.IsDelete {
			key = current.OldPath
		}
		patches[key] = current
		current = nil
	}

	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		lineNo++
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "--- "):
			finishFile()
			oldPath := parseDiffPath(line[len("--- "):])
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
				return nil, fmt.Errorf("diff line %d: %q not followed by a +++ header: %w", lineNo, line, ErrInvalidArgument)
			}
			newPath := parseDiffPath(lines[i+1][len("+++ "):])
			i++
			lineNo++
			current = &FileDiffPatch{
				OldPath:  oldPath,
				NewPath:  newPath,
				IsNew:    oldPath == "/dev/null",
				IsDelete: newPath == "/dev/null",
			}
		case strings.HasPrefix(line, "@@ "):
			if current == nil {
				return nil, fmt.Errorf("diff line %d: hunk header outside of a file section: %w", lineNo, ErrInvalidArgument)
			}
			if hunk != nil {
				current.hunks = append(current.hunks, *hunk)
			}
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, fmt.Errorf("diff line %d: %w", lineNo, err)
			}
			hunk = h
		case strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file" applies to the previous line.
			if hunk != nil && len(hunk.lines) > 0 {
				hunk.lines[len(hunk.lines)-1].noNewline = true
			}
		case hunk != nil && (line == "" && i == len(lines)-1):
			// trailing empty split artifact; ignore
		case hunk != nil && len(line) > 0 && (line[0] == ' ' || line[0] == '-' || line[0] == '+'):
			hunk.lines = append(hunk.lines, parsedDiffLine{op: line[0], text: line[1:]})
		case hunk != nil && line == "":
			// Some tools trim the single space of an empty context line.
			hunk.lines = append(hunk.lines, parsedDiffLine{op: ' ', text: ""})
		default:
			// git headers ("diff --git", "index …"), comments, etc.: ignore.
		}
	}
	finishFile()
	if len(patches) == 0 {
		return nil, fmt.Errorf("no file sections found in diff: %w", ErrInvalidArgument)
	}
	return patches, nil
}

func parseDiffPath(s string) string {
	// Strip a trailing timestamp ("path\t2024-01-01 ...") if present.
	if tab := strings.IndexByte(s, '\t'); tab >= 0 {
		s = s[:tab]
	}
	s = strings.TrimSpace(s)
	if s == "/dev/null" {
		return s
	}
	if rest, ok := strings.CutPrefix(s, "a/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(s, "b/"); ok {
		return rest
	}
	return s
}

func parseHunkHeader(line string) (*parsedHunk, error) {
	// @@ -oldStart[,oldCount] +newStart[,newCount] @@ [section]
	rest, ok := strings.CutPrefix(line, "@@ ")
	if !ok {
		return nil, fmt.Errorf("malformed hunk header %q: %w", line, ErrInvalidArgument)
	}
	end := strings.Index(rest, " @@")
	if end < 0 {
		return nil, fmt.Errorf("malformed hunk header %q: %w", line, ErrInvalidArgument)
	}
	fields := strings.Fields(rest[:end])
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "-") || !strings.HasPrefix(fields[1], "+") {
		return nil, fmt.Errorf("malformed hunk header %q: %w", line, ErrInvalidArgument)
	}
	h := &parsedHunk{}
	var err error
	if h.oldStart, h.oldCount, err = parseHunkRange(fields[0][1:]); err != nil {
		return nil, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	if h.newStart, h.newCount, err = parseHunkRange(fields[1][1:]); err != nil {
		return nil, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	return h, nil
}

func parseHunkRange(s string) (start int, count int, err error) {
	count = 1
	startText, countText, hasCount := strings.Cut(s, ",")
	if _, err := fmt.Sscanf(startText, "%d", &start); err != nil {
		return 0, 0, fmt.Errorf("bad hunk range %q: %w", s, ErrInvalidArgument)
	}
	if hasCount {
		if _, err := fmt.Sscanf(countText, "%d", &count); err != nil {
			return 0, 0, fmt.Errorf("bad hunk range %q: %w", s, ErrInvalidArgument)
		}
	}
	return start, count, nil
}

// Apply applies the patch to the old file content, verifying that context
// and deleted lines match.
func (p *FileDiffPatch) Apply(old string) (string, error) {
	oldLines, oldNL := splitDiffLines(old)
	type outLine struct {
		text      string
		noNewline bool
	}
	var out []outLine
	cursor := 0 // 0-based index into oldLines

	copyOld := func(to int) {
		for ; cursor < to; cursor++ {
			out = append(out, outLine{text: oldLines[cursor], noNewline: cursor == len(oldLines)-1 && !oldNL})
		}
	}

	for hi, h := range p.hunks {
		start := h.oldStart - 1
		if h.oldCount == 0 {
			// Pure insertion: oldStart is the line *before* the insertion.
			start = h.oldStart
		}
		if start < cursor || start > len(oldLines) {
			return "", fmt.Errorf("hunk %d does not fit (old line %d): %w", hi+1, h.oldStart, ErrInvalidArgument)
		}
		copyOld(start)
		for _, ln := range h.lines {
			switch ln.op {
			case ' ', '-':
				if cursor >= len(oldLines) || oldLines[cursor] != ln.text {
					got := "<eof>"
					if cursor < len(oldLines) {
						got = oldLines[cursor]
					}
					return "", fmt.Errorf("hunk %d context mismatch at old line %d: want %q, got %q: %w", hi+1, cursor+1, ln.text, got, ErrInvalidArgument)
				}
				if ln.op == ' ' {
					out = append(out, outLine{text: ln.text, noNewline: ln.noNewline})
				}
				cursor++
			case '+':
				out = append(out, outLine{text: ln.text, noNewline: ln.noNewline})
			}
		}
	}
	copyOld(len(oldLines))

	var sb strings.Builder
	for i, l := range out {
		sb.WriteString(l.text)
		if !(i == len(out)-1 && l.noNewline) {
			sb.WriteByte('\n')
		}
	}
	return sb.String(), nil
}
