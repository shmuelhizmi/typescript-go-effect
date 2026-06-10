package cmds

import (
	"strings"
	"testing"
)

// The parser is pure (no workspace), so these tests run without bundled libs.

func parseEditScriptOK(t *testing.T, src string) []editOp {
	t.Helper()
	ops, errs := parseEditScript(src)
	if len(errs) != 0 {
		t.Fatalf("parseEditScript errors: %v", errs)
	}
	return ops
}

func TestEditParseMoveBeforeAfter(t *testing.T) {
	t.Parallel()
	ops := parseEditScriptOK(t, "move src/a.ts#f1 after src/a.ts#f2\nmove src/a.ts#f1 before src/a.ts#f2\n")
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2", len(ops))
	}
	op := ops[0]
	if op.Line != 1 || op.Verb != "move" || op.Sym != "src/a.ts#f1" {
		t.Errorf("op = %+v", op)
	}
	if op.Place != (editPlace{Kind: "after", Sym: "src/a.ts#f2"}) {
		t.Errorf("place = %+v", op.Place)
	}
	if op.Raw != "move src/a.ts#f1 after src/a.ts#f2" {
		t.Errorf("raw = %q", op.Raw)
	}
	if ops[1].Place.Kind != "before" || ops[1].Line != 2 {
		t.Errorf("second op = %+v", ops[1])
	}
}

func TestEditParseMoveTopEnd(t *testing.T) {
	t.Parallel()
	ops := parseEditScriptOK(t, "move src/a.ts#Router end src/router.ts\nmove src/a.ts#Router top src/router.ts\n")
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2", len(ops))
	}
	if ops[0].Place != (editPlace{Kind: "end", Path: "src/router.ts"}) {
		t.Errorf("place = %+v", ops[0].Place)
	}
	if ops[0].Raw != "move src/a.ts#Router end src/router.ts" {
		t.Errorf("raw = %q", ops[0].Raw)
	}
	if ops[1].Place != (editPlace{Kind: "top", Path: "src/router.ts"}) {
		t.Errorf("place = %+v", ops[1].Place)
	}
}

func TestEditParseInsertVariants(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"insert after src/a.ts#f1 <<EOF",
		"export function g() {}",
		"EOF",
		"insert before src/a.ts#f1 <<EOF",
		"// x",
		"EOF",
		"insert top src/a.ts <<EOF",
		"import \"./polyfill\";",
		"EOF",
		"insert end src/a.ts <<EOF",
		"export {};",
		"EOF",
		"insert into src/a.ts#Router <<EOF",
		"  remove(path: string) {",
		"    this.routes = this.routes.filter(r => r.path !== path);",
		"  }",
		"EOF",
		"",
	}, "\n")
	ops := parseEditScriptOK(t, src)
	if len(ops) != 5 {
		t.Fatalf("got %d ops, want 5", len(ops))
	}
	if ops[0].Verb != "insert" || ops[0].Sym != "" || ops[0].Place != (editPlace{Kind: "after", Sym: "src/a.ts#f1"}) {
		t.Errorf("insert after = %+v", ops[0])
	}
	if ops[0].Body != "export function g() {}" {
		t.Errorf("body = %q", ops[0].Body)
	}
	if ops[0].Raw != "insert after src/a.ts#f1 (+1 lines)" {
		t.Errorf("raw = %q", ops[0].Raw)
	}
	if ops[2].Place != (editPlace{Kind: "top", Path: "src/a.ts"}) {
		t.Errorf("insert top = %+v", ops[2])
	}
	if ops[3].Place != (editPlace{Kind: "end", Path: "src/a.ts"}) {
		t.Errorf("insert end = %+v", ops[3])
	}
	into := ops[4]
	if into.Place != (editPlace{Kind: "into", Sym: "src/a.ts#Router"}) {
		t.Errorf("insert into = %+v", into)
	}
	if into.Raw != "insert into src/a.ts#Router (+3 lines)" {
		t.Errorf("raw = %q", into.Raw)
	}
	if into.Line != 13 {
		t.Errorf("line = %d, want 13", into.Line)
	}
}

func TestEditParseReplaceAndDelete(t *testing.T) {
	t.Parallel()
	src := "replace src/a.ts#f1 <<EOF\nfunction f1() {\n  return 2;\n}\nEOF\ndelete src/a.ts#dead\n"
	ops := parseEditScriptOK(t, src)
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2", len(ops))
	}
	if ops[0].Verb != "replace" || ops[0].Sym != "src/a.ts#f1" {
		t.Errorf("replace = %+v", ops[0])
	}
	if ops[0].Body != "function f1() {\n  return 2;\n}" {
		t.Errorf("body = %q", ops[0].Body)
	}
	if ops[0].Raw != "replace src/a.ts#f1 (+3 lines)" {
		t.Errorf("raw = %q", ops[0].Raw)
	}
	if ops[1].Verb != "delete" || ops[1].Sym != "src/a.ts#dead" || ops[1].Raw != "delete src/a.ts#dead" {
		t.Errorf("delete = %+v", ops[1])
	}
	if ops[1].Line != 6 {
		t.Errorf("delete line = %d, want 6", ops[1].Line)
	}
}

func TestEditParseCustomHeredocTag(t *testing.T) {
	t.Parallel()
	src := "replace src/a.ts#f <<BODY_1\nEOF\nconst x = 1;\nBODY_1\n"
	ops := parseEditScriptOK(t, src)
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	// A line reading "EOF" is ordinary body content under a custom tag.
	if ops[0].Body != "EOF\nconst x = 1;" {
		t.Errorf("body = %q", ops[0].Body)
	}
}

func TestEditParseUnterminatedHeredoc(t *testing.T) {
	t.Parallel()
	src := "delete src/a.ts#dead\nreplace src/a.ts#f <<EOF\nconst x = 1;\n"
	ops, errs := parseEditScript(src)
	if len(ops) != 1 {
		t.Errorf("got %d ops, want 1 (the delete)", len(ops))
	}
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(errs), errs)
	}
	want := `line 2: unterminated heredoc <<EOF`
	if errs[0].Error() != want {
		t.Errorf("error = %q, want %q", errs[0].Error(), want)
	}
}

func TestEditParseCRLF(t *testing.T) {
	t.Parallel()
	src := "move src/a.ts#f1 after src/a.ts#f2\r\nreplace src/a.ts#f1 <<EOF\r\nconst x = 1;\r\nEOF\r\n"
	ops := parseEditScriptOK(t, src)
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2", len(ops))
	}
	if ops[1].Body != "const x = 1;" {
		t.Errorf("body = %q (CRLF should be normalized to LF)", ops[1].Body)
	}
}

func TestEditParseCommentsAndBlanks(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"# tidy server.ts",
		"",
		"   # indented full-line comment",
		"delete src/a.ts#dead   # trailing comment",
		"move src/a.ts#f1 after src/a.ts#f2 # ditto",
		"",
	}, "\n")
	ops := parseEditScriptOK(t, src)
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2", len(ops))
	}
	if ops[0].Sym != "src/a.ts#dead" || ops[0].Line != 4 {
		t.Errorf("op = %+v", ops[0])
	}
	if ops[1].Place.Sym != "src/a.ts#f2" {
		t.Errorf("trailing comment not stripped: %+v", ops[1])
	}
}

func TestEditParseHashInsideSymbolAndQuotes(t *testing.T) {
	t.Parallel()
	// `#` inside a token (symbol IDs) or a quoted string is not a comment.
	ops := parseEditScriptOK(t, "delete \"src/my dir/a.ts#f # not a comment\"\n")
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	if ops[0].Sym != "src/my dir/a.ts#f # not a comment" {
		t.Errorf("sym = %q", ops[0].Sym)
	}
	if ops[0].Raw != `delete "src/my dir/a.ts#f # not a comment"` {
		t.Errorf("raw = %q", ops[0].Raw)
	}
}

func TestEditParseQuotedPathWithSpace(t *testing.T) {
	t.Parallel()
	ops := parseEditScriptOK(t, "move src/a.ts#f1 end \"src/sub dir/b.ts\"\n")
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	if ops[0].Place != (editPlace{Kind: "end", Path: "src/sub dir/b.ts"}) {
		t.Errorf("place = %+v", ops[0].Place)
	}
	if ops[0].Raw != `move src/a.ts#f1 end "src/sub dir/b.ts"` {
		t.Errorf("raw = %q", ops[0].Raw)
	}
}

func TestEditParseMultiError(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"mv src/a.ts#f1 after src/a.ts#f2",   // unknown verb
		"delete src/a.ts#dead extra",         // wrong arity
		"move src/a.ts#f1 below src/a.ts#f2", // bad place keyword
		"delete src/a.ts#dead",
	}, "\n")
	ops, errs := parseEditScript(src)
	if len(errs) != 3 {
		t.Fatalf("got %d errors, want 3: %v", len(errs), errs)
	}
	if got, want := errs[0].Error(), `line 1: unknown verb "mv" (want move|insert|replace|delete)`; got != want {
		t.Errorf("errs[0] = %q, want %q", got, want)
	}
	if !strings.HasPrefix(errs[1].Error(), "line 2: delete takes") {
		t.Errorf("errs[1] = %q", errs[1].Error())
	}
	if got, want := errs[2].Error(), `line 3: bad place keyword "below" (want before|after|top|end)`; got != want {
		t.Errorf("errs[2] = %q, want %q", got, want)
	}
	if len(ops) != 1 || ops[0].Line != 4 {
		t.Errorf("ops = %+v, want only the line-4 delete", ops)
	}
}

func TestEditParseEmptyScript(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"", "\n", "# only comments\n\n  \n"} {
		ops, errs := parseEditScript(src)
		if len(ops) != 0 || len(errs) != 0 {
			t.Errorf("parseEditScript(%q) = %v, %v; want no ops, no errors", src, ops, errs)
		}
	}
}

func TestEditParseHeredocBodyVerbatim(t *testing.T) {
	t.Parallel()
	src := "insert into src/a.ts#C <<EOF\n  indented(\"  spaces  \") // # not a comment\n\n\tlast\nEOF\n"
	ops := parseEditScriptOK(t, src)
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	want := "  indented(\"  spaces  \") // # not a comment\n\n\tlast"
	if ops[0].Body != want {
		t.Errorf("body = %q, want %q (verbatim, no trailing newline)", ops[0].Body, want)
	}
	if ops[0].Raw != "insert into src/a.ts#C (+3 lines)" {
		t.Errorf("raw = %q", ops[0].Raw)
	}
}

func TestEditParseMoveWithHeredocRefused(t *testing.T) {
	t.Parallel()
	src := "move src/a.ts#f1 after src/a.ts#f2 <<EOF\nbody\nEOF\ndelete src/a.ts#x <<EOF\nbody\nEOF\n"
	ops, errs := parseEditScript(src)
	if len(ops) != 0 {
		t.Errorf("ops = %+v, want none", ops)
	}
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %v", len(errs), errs)
	}
	if got, want := errs[0].Error(), "line 1: move does not take a heredoc body"; got != want {
		t.Errorf("errs[0] = %q, want %q", got, want)
	}
	if got, want := errs[1].Error(), "line 4: delete does not take a heredoc body"; got != want {
		t.Errorf("errs[1] = %q, want %q", got, want)
	}
}

func TestEditParseInsertWithoutHeredocRefused(t *testing.T) {
	t.Parallel()
	ops, errs := parseEditScript("insert after src/a.ts#f1\nreplace src/a.ts#f1\n")
	if len(ops) != 0 {
		t.Errorf("ops = %+v, want none", ops)
	}
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %v", len(errs), errs)
	}
	if got, want := errs[0].Error(), "line 1: insert requires a heredoc body (<<TAG)"; got != want {
		t.Errorf("errs[0] = %q, want %q", got, want)
	}
	if got, want := errs[1].Error(), "line 2: replace requires a heredoc body (<<TAG)"; got != want {
		t.Errorf("errs[1] = %q, want %q", got, want)
	}
}

func TestEditParseMoveIntoRefused(t *testing.T) {
	t.Parallel()
	_, errs := parseEditScript("move src/a.ts#f1 into src/a.ts#C\n")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `bad place keyword "into"`) {
		t.Fatalf("errs = %v, want a bad-place error for into", errs)
	}
}
