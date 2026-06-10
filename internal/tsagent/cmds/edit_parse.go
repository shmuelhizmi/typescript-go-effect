package cmds

import (
	"fmt"
	"regexp"
	"strings"
)

// edit_parse.go is the parser for the `tsagent edit` script DSL (plan Part
// B). The language is line-oriented:
//
//	move    <sym> before|after <sym> [with-deps]  # reorder, or cross-file with anchor
//	move    <sym> top|end <path>     [with-deps]  # cross-file file-level position
//	insert  before|after <sym> <<TAG … TAG
//	insert  top|end <path>     <<TAG … TAG
//	insert  into <class-like-sym> <<TAG … TAG
//	replace <sym> <<TAG … TAG
//	delete  <sym>
//
// Blank lines and full-line `#` comments are skipped; trailing ` # comment`
// is stripped on op lines (but not inside a quoted string, and never inside
// heredoc bodies). Tokens are whitespace-separated; `"quoted strings"` allow
// paths/symbols with spaces. Raw code arrives via heredocs: an op line ending
// with `<<TAG` collects subsequent lines verbatim (no interpolation; CRLF is
// normalized to LF) until a line exactly equal to TAG. The parser is pure
// (no workspace): symbol resolution and validation happen in the executor.

// editPlace is the positional argument of move/insert ops.
type editPlace struct {
	Kind string // before|after|top|end|into
	Sym  string // anchor symbol ID (before/after/into)
	Path string // file path (top/end)
}

// editOp is one parsed op of an edit script.
type editOp struct {
	Line     int    // 1-based line of the op in the script
	Verb     string // move|insert|replace|delete
	Sym      string // operand symbol ID (move/replace/delete; empty for insert)
	Place    editPlace
	Body     string // heredoc body, verbatim (no trailing newline added)
	Raw      string // normalized one-line description for reports
	WithDeps bool   // move only: carry file-local unexported deps along
}

// parseEditScript parses an edit script. All errors are collected (each
// prefixed `line N:`) and returned together; ops that parsed cleanly are
// still returned so callers can attribute conflicts to lines.
func parseEditScript(src string) ([]editOp, []error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	var ops []editOp
	var errs []error
	i := 0
	for i < len(lines) {
		lineNo := i + 1
		line := lines[i]
		i++
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		tokens, terr := editTokenizeLine(line)
		if terr != nil {
			errs = append(errs, fmt.Errorf("line %d: %v", lineNo, terr))
			continue
		}
		if len(tokens) == 0 {
			continue
		}
		// Heredoc: the op line ends with <<TAG; the body runs until a line
		// exactly equal to TAG. Consumed before verb validation so a bad op
		// line does not leak its body lines into the op stream.
		body := ""
		hasBody := false
		if tag, ok := editHeredocTag(tokens[len(tokens)-1]); ok {
			tokens = tokens[:len(tokens)-1]
			var bodyLines []string
			terminated := false
			for i < len(lines) {
				if lines[i] == tag {
					i++
					terminated = true
					break
				}
				bodyLines = append(bodyLines, lines[i])
				i++
			}
			if !terminated {
				errs = append(errs, fmt.Errorf("line %d: unterminated heredoc <<%s", lineNo, tag))
				continue
			}
			body = strings.Join(bodyLines, "\n")
			hasBody = true
		}
		op, err := parseEditOpLine(lineNo, tokens, body, hasBody)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ops = append(ops, op)
	}
	return ops, errs
}

// editTokenizeLine splits an op line into whitespace-separated tokens.
// `"…"` quotes a token (spaces allowed, no escapes); an unquoted `#` at the
// start of a token begins a trailing comment.
func editTokenizeLine(line string) ([]string, error) {
	var tokens []string
	i, n := 0, len(line)
	for i < n {
		for i < n && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= n || line[i] == '#' {
			break
		}
		if line[i] == '"' {
			j := i + 1
			for j < n && line[j] != '"' {
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("unterminated quoted string")
			}
			tokens = append(tokens, line[i+1:j])
			i = j + 1
			continue
		}
		j := i
		for j < n && line[j] != ' ' && line[j] != '\t' {
			j++
		}
		tokens = append(tokens, line[i:j])
		i = j
	}
	return tokens, nil
}

var editHeredocTagRE = regexp.MustCompile(`^<<([A-Za-z0-9_]+)$`)

// editHeredocTag reports whether a token opens a heredoc and returns its tag.
func editHeredocTag(token string) (string, bool) {
	m := editHeredocTagRE.FindStringSubmatch(token)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// parseEditOpLine validates one tokenized op line against the grammar.
func parseEditOpLine(lineNo int, tokens []string, body string, hasBody bool) (editOp, error) {
	op := editOp{Line: lineNo, Verb: tokens[0], Body: body}
	switch op.Verb {
	case "move":
		if hasBody {
			return op, fmt.Errorf("line %d: move does not take a heredoc body", lineNo)
		}
		if len(tokens) == 5 && tokens[4] == "with-deps" {
			op.WithDeps = true
			tokens = tokens[:4]
		}
		if len(tokens) != 4 {
			return op, fmt.Errorf("line %d: move takes <sym> before|after <sym> or <sym> top|end <path>, optionally followed by with-deps", lineNo)
		}
		op.Sym = tokens[1]
		place, err := editParsePlace(lineNo, tokens[2], tokens[3], false /*allowInto*/)
		if err != nil {
			return op, err
		}
		op.Place = place
		op.Raw = fmt.Sprintf("move %s %s %s", editQuoteToken(op.Sym), place.Kind, editQuoteToken(editPlaceTarget(place)))
		if op.WithDeps {
			op.Raw += " with-deps"
		}
	case "insert":
		if len(tokens) != 3 {
			return op, fmt.Errorf("line %d: insert takes before|after <sym>, top|end <path>, or into <sym>, followed by <<TAG", lineNo)
		}
		place, err := editParsePlace(lineNo, tokens[1], tokens[2], true /*allowInto*/)
		if err != nil {
			return op, err
		}
		if !hasBody {
			return op, fmt.Errorf("line %d: insert requires a heredoc body (<<TAG)", lineNo)
		}
		op.Place = place
		op.Raw = fmt.Sprintf("insert %s %s (+%d lines)", place.Kind, editQuoteToken(editPlaceTarget(place)), editBodyLineCount(body))
	case "replace":
		if len(tokens) != 2 {
			return op, fmt.Errorf("line %d: replace takes <sym> <<TAG", lineNo)
		}
		if !hasBody {
			return op, fmt.Errorf("line %d: replace requires a heredoc body (<<TAG)", lineNo)
		}
		op.Sym = tokens[1]
		op.Raw = fmt.Sprintf("replace %s (+%d lines)", editQuoteToken(op.Sym), editBodyLineCount(body))
	case "delete":
		if hasBody {
			return op, fmt.Errorf("line %d: delete does not take a heredoc body", lineNo)
		}
		if len(tokens) != 2 {
			return op, fmt.Errorf("line %d: delete takes <sym>", lineNo)
		}
		op.Sym = tokens[1]
		op.Raw = "delete " + editQuoteToken(op.Sym)
	default:
		return op, fmt.Errorf("line %d: unknown verb %q (want move|insert|replace|delete)", lineNo, op.Verb)
	}
	return op, nil
}

// editParsePlace classifies a place keyword + target pair. before/after/into
// anchor at a symbol; top/end address a file.
func editParsePlace(lineNo int, keyword string, target string, allowInto bool) (editPlace, error) {
	switch keyword {
	case "before", "after":
		return editPlace{Kind: keyword, Sym: target}, nil
	case "top", "end":
		return editPlace{Kind: keyword, Path: target}, nil
	case "into":
		if allowInto {
			return editPlace{Kind: keyword, Sym: target}, nil
		}
	}
	want := "before|after|top|end"
	if allowInto {
		want += "|into"
	}
	return editPlace{}, fmt.Errorf("line %d: bad place keyword %q (want %s)", lineNo, keyword, want)
}

// editPlaceTarget returns the place's target token (symbol or path).
func editPlaceTarget(p editPlace) string {
	if p.Path != "" {
		return p.Path
	}
	return p.Sym
}

// editQuoteToken re-quotes a token for Raw rendering when it contains spaces.
func editQuoteToken(token string) string {
	if strings.ContainsAny(token, " \t") {
		return `"` + token + `"`
	}
	return token
}

// editBodyLineCount counts the lines of a heredoc body for `(+N lines)`
// reporting.
func editBodyLineCount(body string) int {
	if body == "" {
		return 0
	}
	return strings.Count(body, "\n") + 1
}
