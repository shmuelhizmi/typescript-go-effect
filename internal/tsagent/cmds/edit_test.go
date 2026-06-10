package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// runEditScript executes a script through runEdit via the stdin path (the
// same code path as `tsagent edit -`).
func runEditScript(t *testing.T, ws *core.Workspace, script string, f *editFlags) (*EditScriptResult, error) {
	t.Helper()
	if f == nil {
		f = &editFlags{}
	}
	f.stdin = strings.NewReader(script)
	return runEdit(context.Background(), ws, f, []string{"-"})
}

func mustRunEditScript(t *testing.T, ws *core.Workspace, script string) *EditScriptResult {
	t.Helper()
	result, err := runEditScript(t, ws, script, nil)
	if err != nil {
		t.Fatalf("runEdit: %v", err)
	}
	return result
}

// indexOrder asserts that the wants occur in text in the given order.
func indexOrder(t *testing.T, text string, wants ...string) {
	t.Helper()
	last := -1
	for _, want := range wants {
		idx := strings.Index(text, want)
		if idx < 0 {
			t.Fatalf("text does not contain %q:\n%s", want, text)
		}
		if idx <= last {
			t.Errorf("%q is out of order (index %d <= %d):\n%s", want, idx, last, text)
		}
		last = idx
	}
}

func renderEditText(t *testing.T, result *EditScriptResult) string {
	t.Helper()
	var sb strings.Builder
	if err := result.WriteText(&sb); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	return sb.String()
}

func TestEditMoveAfterWithinFileJSDocTravels(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "/** doc for f1 */\nexport function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\nexport function f3(): number {\n\treturn 3;\n}\n",
	})
	result := mustRunEditScript(t, ws, "move src/a.ts#f1 after src/a.ts#f2\n")
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply (apply is the default)", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "function f2", "/** doc for f1 */", "function f1", "function f3")
	out := renderEditText(t, result)
	if !strings.Contains(out, "ok line 1: move src/a.ts#f1 after src/a.ts#f2") {
		t.Errorf("text output missing the ok line:\n%s", out)
	}
	if !strings.Contains(out, "applied: 1 file(s) changed, 0 new errors, 0 fixed") {
		t.Errorf("text output missing the applied summary:\n%s", out)
	}
}

func TestEditMoveBeforeWithinFile(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	mustRunEditScript(t, ws, "move src/a.ts#f2 before src/a.ts#f1\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "function f2", "function f1")
}

func TestEditClassMemberReorder(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export class C {\n\t/** a doc */\n\ta(): number {\n\t\treturn 1;\n\t}\n\tb(): number {\n\t\treturn 2;\n\t}\n}\n",
	})
	result := mustRunEditScript(t, ws, "move src/a.ts#C.a after src/a.ts#C.b\n")
	if !result.Tx.Applied {
		t.Fatalf("tx = %+v, want applied", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "b(): number", "/** a doc */", "a(): number")
	if !strings.Contains(text, "}\n}") && !strings.Contains(text, "\t}\n}") {
		t.Errorf("class body looks malformed:\n%s", text)
	}
}

func TestEditMemberCrossContainerRefused(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export class C {\n\ta(): number {\n\t\treturn 1;\n\t}\n}\nexport class D {\n\tb(): number {\n\t\treturn 2;\n\t}\n}\n",
	})
	_, err := runEditScript(t, ws, "move src/a.ts#C.a after src/a.ts#D.b\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected exit 2, got %v", err)
	}
	want := "line 1: cannot move a member across containers; use delete + insert into"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestEditNestedLocalMoveRefused(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function outer(): number {\n\tfunction inner(): number {\n\t\treturn 1;\n\t}\n\treturn inner();\n}\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	_, err := runEditScript(t, ws, "move src/a.ts#outer.inner after src/a.ts#f2\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected exit 2, got %v", err)
	}
	if !strings.Contains(err.Error(), "line 1: cannot move a function-local declaration") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestEditCrossFileMoveWithBeforeAnchor(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "/** Helps. */\nexport function helper(): number {\n\treturn 7;\n}\nexport const other = 1;\n",
		"/project/src/main.ts": "import { helper } from \"./util\";\nexport function target(): number {\n\treturn 0;\n}\nexport const v = helper();\n",
	})
	result := mustRunEditScript(t, ws, "move src/util.ts#helper before src/main.ts#target\n")
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if strings.Contains(util, "helper") {
		t.Errorf("util.ts still contains the moved symbol:\n%s", util)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	indexOrder(t, main, "/** Helps. */", "export function helper", "export function target", "export const v = helper();")
	if strings.Contains(main, "from \"./util\"") {
		t.Errorf("main.ts should drop its import of the now-local symbol:\n%s", main)
	}
	// The op report covers both touched files.
	if len(result.Ops) != 1 {
		t.Fatalf("ops = %+v, want 1", result.Ops)
	}
	files := strings.Join(result.Ops[0].Files, ",")
	if !strings.Contains(files, "src/util.ts") || !strings.Contains(files, "src/main.ts") {
		t.Errorf("op files = %v, want both util.ts and main.ts", result.Ops[0].Files)
	}
}

func TestEditMoveEndToOtherFile(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(): number {\n\treturn 1;\n}\nexport const keep = 2;\n",
		"/project/src/b.ts": "export const existing = 0;\n",
	})
	result := mustRunEditScript(t, ws, "move src/a.ts#f end src/b.ts\n")
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	b := readWorkspaceFile(t, ws, "/project/src/b.ts")
	indexOrder(t, b, "export const existing", "export function f")
	if strings.Contains(readWorkspaceFile(t, ws, "/project/src/a.ts"), "function f") {
		t.Error("a.ts should no longer contain f")
	}
}

func TestEditInsertVariantsAndScriptOrderAtSamePos(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const lead = 0;\nexport function f1(): number {\n\treturn 1;\n}\n",
	})
	script := strings.Join([]string{
		"insert before src/a.ts#f1 <<EOF",
		"export const before1 = 1;",
		"EOF",
		"insert after src/a.ts#f1 <<EOF",
		"export const after1 = 2;",
		"EOF",
		"insert after src/a.ts#f1 <<EOF",
		"export const after2 = 3;",
		"EOF",
		"insert top src/a.ts <<EOF",
		"// header",
		"EOF",
		"insert end src/a.ts <<EOF",
		"export const atEnd = 4;",
		"EOF",
		"",
	}, "\n")
	result := mustRunEditScript(t, ws, script)
	if !result.Tx.Applied {
		t.Fatalf("tx = %+v, want applied", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	// Two zero-width inserts at the same offset land in script order.
	indexOrder(t, text, "// header", "const lead", "before1", "function f1", "after1", "after2", "atEnd")
	if !strings.HasPrefix(text, "// header\n") {
		t.Errorf("insert top must land at offset 0:\n%s", text)
	}
}

func TestEditInsertIntoClass(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export class C {\n\ta(): number {\n\t\treturn 1;\n\t}\n}\nexport class Empty {\n}\nexport class Inline {}\n",
	})
	script := strings.Join([]string{
		"insert into src/a.ts#C <<EOF",
		"\tb(): number {",
		"\t\treturn 2;",
		"\t}",
		"EOF",
		"insert into src/a.ts#Empty <<EOF",
		"\tz(): number {",
		"\t\treturn 9;",
		"\t}",
		"EOF",
		"insert into src/a.ts#Inline <<EOF",
		"\ty(): number {",
		"\t\treturn 8;",
		"\t}",
		"EOF",
		"",
	}, "\n")
	result := mustRunEditScript(t, ws, script)
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "a(): number", "b(): number", "class Empty", "z(): number", "class Inline", "y(): number")
	// The new member of C lands inside the braces, before the next class.
	cEnd := strings.Index(text, "class Empty")
	if b := strings.Index(text, "b(): number"); b > cEnd {
		t.Errorf("b() landed outside class C:\n%s", text)
	}
}

func TestEditInsertIntoNonContainerRefused(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(): number {\n\treturn 1;\n}\n",
	})
	_, err := runEditScript(t, ws, "insert into src/a.ts#f <<EOF\nconst x = 1;\nEOF\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected exit 2, got %v", err)
	}
	if !strings.Contains(err.Error(), "line 1: insert into requires a class, interface, enum, or namespace target") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestEditReplaceIncludesJSDoc(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "/** old doc */\nexport function f(): number {\n\treturn 1;\n}\nexport const v = f();\n",
	})
	script := strings.Join([]string{
		"replace src/a.ts#f <<EOF",
		"/** new doc */",
		"export function f(): number {",
		"\treturn 2;",
		"}",
		"EOF",
		"",
	}, "\n")
	result := mustRunEditScript(t, ws, script)
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(text, "old doc") {
		t.Errorf("replace must consume the leading JSDoc:\n%s", text)
	}
	indexOrder(t, text, "/** new doc */", "return 2;", "export const v = f();")
}

func TestEditDelete(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function keep(): number {\n\treturn 1;\n}\n/** dead */\nfunction dead(): number {\n\treturn 0;\n}\n",
	})
	result := mustRunEditScript(t, ws, "delete src/a.ts#dead\n")
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(text, "dead") {
		t.Errorf("dead (and its JSDoc) should be gone:\n%s", text)
	}
}

func TestEditTenOpBatchSingleTransaction(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\nfunction d1(): number {\n\treturn 0;\n}\nfunction d2(): number {\n\treturn 0;\n}\nexport class C {\n\tm(): number {\n\t\treturn 5;\n\t}\n}\n",
		"/project/src/b.ts": "export const existing = 0;\n",
	})
	script := strings.Join([]string{
		"move src/a.ts#f1 after src/a.ts#f2",
		"delete src/a.ts#d1",
		"delete src/a.ts#d2",
		"insert before src/a.ts#f2 <<EOF",
		"export const i1 = 1;",
		"EOF",
		"insert after src/a.ts#f2 <<EOF",
		"export const i2 = 2;",
		"EOF",
		"insert top src/a.ts <<EOF",
		"// top",
		"EOF",
		"insert end src/a.ts <<EOF",
		"export const i3 = 3;",
		"EOF",
		"insert into src/a.ts#C <<EOF",
		"\tn(): number {",
		"\t\treturn 6;",
		"\t}",
		"EOF",
		"replace src/a.ts#f2 <<EOF",
		"export function f2(): number {",
		"\treturn 22;",
		"}",
		"EOF",
		"insert end src/b.ts <<EOF",
		"export const i4 = 4;",
		"EOF",
		"",
	}, "\n")
	result := mustRunEditScript(t, ws, script)
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	if len(result.Ops) != 10 {
		t.Fatalf("got %d op reports, want 10", len(result.Ops))
	}
	if len(result.Tx.FilesChanged) != 2 {
		t.Errorf("filesChanged = %v, want a.ts and b.ts", result.Tx.FilesChanged)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	// The line-1 move and the `insert after f2` both land at f2's end offset:
	// script order wins, so the moved f1 precedes i2.
	indexOrder(t, text, "// top", "i1", "return 22;", "function f1", "i2", "n(): number", "i3")
	if strings.Contains(text, "d1") || strings.Contains(text, "d2") {
		t.Errorf("deleted symbols survived:\n%s", text)
	}
}

func TestEditConflictReplaceAndDeleteSameSymbol(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(): number {\n\treturn 1;\n}\n",
	})
	script := "replace src/a.ts#f <<EOF\nexport function f(): number {\n\treturn 2;\n}\nEOF\ndelete src/a.ts#f\n"
	_, err := runEditScript(t, ws, script, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected exit 2, got %v", err)
	}
	want := "line 6: delete src/a.ts#f conflicts with replace at line 1 (overlapping ranges in src/a.ts)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	// Nothing was written.
	if !strings.Contains(readWorkspaceFile(t, ws, "/project/src/a.ts"), "return 1;") {
		t.Error("a conflicting script must not modify files")
	}
}

func TestEditUnknownIDSuggestionsExit3(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function helper(): number {\n\treturn 1;\n}\nexport function other(): number {\n\treturn 2;\n}\n",
	})
	_, err := runEditScript(t, ws, "delete src/a.ts#helpr\ndelete src/a.ts#othr\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitNotFound {
		t.Fatalf("expected exit 3, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "line 1: unknown symbol src/a.ts#helpr; closest: src/a.ts#helper") {
		t.Errorf("missing line-1 suggestion: %q", msg)
	}
	if !strings.Contains(msg, "line 2: unknown symbol src/a.ts#othr; closest: src/a.ts#other") {
		t.Errorf("missing line-2 suggestion (all failures reported together): %q", msg)
	}
}

func TestEditSyntaxErrorsExit2(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const x = 1;\n",
	})
	_, err := runEditScript(t, ws, "frobnicate src/a.ts#x\nmove src/a.ts#x below src/a.ts#x\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected exit 2, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `line 1: unknown verb "frobnicate"`) || !strings.Contains(msg, `line 2: bad place keyword "below"`) {
		t.Errorf("all syntax errors must be reported together: %q", msg)
	}
}

func TestEditGateRefusalExit4AndAllowErrors(t *testing.T) {
	t.Parallel()
	files := map[string]any{
		"/project/src/a.ts": "export function used(): number {\n\treturn 1;\n}\n",
		"/project/src/b.ts": "import { used } from \"./a\";\nexport const x = used();\n",
	}
	ws := newTestWorkspace(t, files)
	_, err := runEditScript(t, ws, "delete src/a.ts#used\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected exit 4, got %v", err)
	}
	if !strings.Contains(err.Error(), "pass --allow-errors to apply anyway") || !strings.Contains(err.Error(), "error TS") {
		t.Errorf("refusal should list the new errors: %q", err.Error())
	}
	// Files untouched after the refusal.
	if !strings.Contains(readWorkspaceFile(t, ws, "/project/src/a.ts"), "function used") {
		t.Error("a refused apply must leave the files untouched")
	}

	// --allow-errors pushes it through.
	ws2 := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function used(): number {\n\treturn 1;\n}\n",
		"/project/src/b.ts": "import { used } from \"./a\";\nexport const x = used();\n",
	})
	result, err := runEditScript(t, ws2, "delete src/a.ts#used\n", &editFlags{allowErrors: true})
	if err != nil {
		t.Fatalf("--allow-errors apply: %v", err)
	}
	if !result.Tx.Applied || len(result.Tx.NewErrors) == 0 {
		t.Fatalf("tx = %+v, want applied with new errors reported", result.Tx)
	}
	// `used` was the file's only declaration, so the emptied file is deleted
	// outright (with a note) rather than left as a whitespace husk.
	if _, ok := ws2.FS.ReadFile("/project/src/a.ts"); ok {
		t.Error("--allow-errors should have applied the deletion and removed the emptied file")
	}
	foundEmptyNote := false
	for _, note := range result.Tx.Notes {
		if strings.Contains(note, "became empty and was deleted") {
			foundEmptyNote = true
		}
	}
	if !foundEmptyNote {
		t.Errorf("Notes = %v, want a became-empty deletion note", result.Tx.Notes)
	}
}

func TestEditUnusedHelperPassesGateByDefault(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "noUnusedLocals": true}}`,
		"/project/src/a.ts":      "export function f(): number {\n\treturn 1;\n}\n",
	})
	script := "insert after src/a.ts#f <<EOF\nfunction helper(): number {\n\treturn 2;\n}\nEOF\n"
	result, err := runEditScript(t, ws, script, nil)
	if err != nil {
		t.Fatalf("inserting an unused helper must pass the gate by default: %v", err)
	}
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want applied with no gating errors", result.Tx)
	}
	if len(result.Tx.UnusedWarnings) != 1 || result.Tx.UnusedWarnings[0].Code != "TS6133" {
		t.Errorf("UnusedWarnings = %+v, want one TS6133", result.Tx.UnusedWarnings)
	}
	found := false
	for _, note := range result.Tx.Notes {
		if strings.Contains(note, "unused-symbol diagnostic(s) introduced") && strings.Contains(note, "--strict-gate") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want the unused-symbol note", result.Tx.Notes)
	}
	out := renderEditText(t, result)
	if !strings.Contains(out, "unused src/a.ts") || !strings.Contains(out, "note: 1 unused-symbol diagnostic(s) introduced") {
		t.Errorf("text output should surface the unused warning and note:\n%s", out)
	}
}

func TestEditStrictGateRefusesUnusedHelper(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "noUnusedLocals": true}}`,
		"/project/src/a.ts":      "export function f(): number {\n\treturn 1;\n}\n",
	})
	script := "insert after src/a.ts#f <<EOF\nfunction helper(): number {\n\treturn 2;\n}\nEOF\n"
	_, err := runEditScript(t, ws, script, &editFlags{strictGate: true})
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("--strict-gate should refuse the unused helper, got %v", err)
	}
	if !strings.Contains(err.Error(), "TS6133") {
		t.Errorf("refusal should list the unused diagnostic: %q", err.Error())
	}
	if !strings.Contains(readWorkspaceFile(t, ws, "/project/src/a.ts"), "export function f") ||
		strings.Contains(readWorkspaceFile(t, ws, "/project/src/a.ts"), "helper") {
		t.Error("a refused apply must leave the files untouched")
	}
}

func TestEditRealTypeErrorStillRefusesByDefault(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext", "noUnusedLocals": true}}`,
		"/project/src/a.ts":      "export function f(): number {\n\treturn 1;\n}\n",
	})
	script := "insert after src/a.ts#f <<EOF\nexport const broken: number = \"nope\";\nEOF\n"
	_, err := runEditScript(t, ws, script, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("a real type error must still refuse, got %v", err)
	}
	if !strings.Contains(err.Error(), "TS2322") {
		t.Errorf("refusal should list the type error: %q", err.Error())
	}
}

func TestEditInsertAfterSeparatedByBlankLine(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(): number {\n\treturn 1;\n}\n\nexport function g(): number {\n\treturn 2;\n}\n",
	})
	script := "insert after src/a.ts#f <<EOF\nexport function added(): number {\n\treturn 3;\n}\nEOF\n"
	mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.Contains(text, "}\n\nexport function added") {
		t.Errorf("inserted block should be separated from f by one blank line:\n%s", text)
	}
	if strings.Contains(text, "}\nexport function added") || strings.Contains(text, "\n\n\n") {
		t.Errorf("want exactly one blank line around the insert:\n%s", text)
	}
}

func TestEditInsertBeforeFirstDeclSeparatedByBlankLine(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(): number {\n\treturn 1;\n}\n",
	})
	script := "insert before src/a.ts#f <<EOF\nexport function added(): number {\n\treturn 3;\n}\nEOF\n"
	mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	want := "export function added(): number {\n\treturn 3;\n}\n\nexport function f(): number {\n\treturn 1;\n}\n"
	if text != want {
		t.Errorf("a.ts = %q, want %q", text, want)
	}
}

func TestEditMoveAwayLeavesSingleBlankLine(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\n\nexport function f2(): number {\n\treturn 2;\n}\n\nexport function f3(): number {\n\treturn 3;\n}\n",
	})
	mustRunEditScript(t, ws, "move src/a.ts#f2 after src/a.ts#f3\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(text, "\n\n\n") {
		t.Errorf("the vacated spot must not leave a double blank line:\n%s", text)
	}
	indexOrder(t, text, "function f1", "function f3", "function f2")
	if !strings.Contains(text, "}\n\nexport function f2") {
		t.Errorf("the moved block should be separated from f3 by one blank line:\n%s", text)
	}
}

func TestEditDeleteBetweenDeclsLeavesSingleBlankLine(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\n\nfunction dead(): number {\n\treturn 0;\n}\n\nexport function f3(): number {\n\treturn 3;\n}\n",
	})
	mustRunEditScript(t, ws, "delete src/a.ts#dead\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	want := "export function f1(): number {\n\treturn 1;\n}\n\nexport function f3(): number {\n\treturn 3;\n}\n"
	if text != want {
		t.Errorf("a.ts = %q, want %q", text, want)
	}
}

func TestEditInsertEndSeparatedByBlankLine(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(): number {\n\treturn 1;\n}\n",
	})
	script := "insert end src/a.ts <<EOF\n// new\nEOF\n"
	mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	want := "export function f(): number {\n\treturn 1;\n}\n\n// new\n"
	if text != want {
		t.Errorf("a.ts = %q, want %q", text, want)
	}
}

func TestEditInsertEndFileWithoutTrailingNewline(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const a = 1;",
	})
	script := "insert end src/a.ts <<EOF\n// new\nEOF\n"
	mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	want := "export const a = 1;\n\n// new\n"
	if text != want {
		t.Errorf("a.ts = %q, want %q", text, want)
	}
}

func TestEditInsertTopSeparatedByBlankLine(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const a = 1;\n",
	})
	script := "insert top src/a.ts <<EOF\n// top\nEOF\n"
	mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	want := "// top\n\nexport const a = 1;\n"
	if text != want {
		t.Errorf("a.ts = %q, want %q", text, want)
	}
}

func TestEditMoveWithDepsExecutes(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "function helper(): number {\n\treturn 7;\n}\nexport function moveMe(): number {\n\treturn helper();\n}\nexport const keep = 1;\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
	})
	result := mustRunEditScript(t, ws, "move src/util.ts#moveMe end src/dest.ts with-deps\n")
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	dest := readWorkspaceFile(t, ws, "/project/src/dest.ts")
	indexOrder(t, dest, "export const unrelated = 0;", "function helper", "export function moveMe")
	if strings.Contains(dest, "export function helper") {
		t.Errorf("the moved dependency must stay unexported: %q", dest)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if strings.Contains(util, "helper") {
		t.Errorf("the dependency should be gone from the source: %q", util)
	}
}

func TestEditMoveWithoutWithDepsSuggestsModifier(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "function helper(): number {\n\treturn 7;\n}\nexport function moveMe(): number {\n\treturn helper();\n}\n",
		"/project/src/dest.ts": "export const unrelated = 0;\n",
	})
	_, err := runEditScript(t, ws, "move src/util.ts#moveMe end src/dest.ts\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "with-deps") {
		t.Errorf("refusal should suggest the with-deps modifier: %v", err)
	}
}

func TestEditInsertTopRespectsUseClientDirective(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "\"use client\";\nexport function f(): number {\n\treturn 1;\n}\n",
	})
	script := "insert top src/a.ts <<EOF\nexport const HEADER = 1;\nEOF\n"
	mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.HasPrefix(text, "\"use client\";\nexport const HEADER = 1;\n") {
		t.Errorf("insert top must land below the directive prologue:\n%s", text)
	}
}

func TestEditDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	result, err := runEditScript(t, ws, "move src/a.ts#f1 after src/a.ts#f2\n", &editFlags{dryRun: true})
	if err != nil {
		t.Fatalf("runEdit --dry-run: %v", err)
	}
	if result.Tx.Applied {
		t.Error("dry-run must not apply")
	}
	out := renderEditText(t, result)
	if !strings.Contains(out, "ok line 1: move") || !strings.Contains(out, "--- a/src/a.ts") || !strings.Contains(out, "dry-run: 1 file(s) would change") {
		t.Errorf("dry-run output should have ok lines + diff + footer:\n%s", out)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.HasPrefix(text, "export function f1") {
		t.Error("dry-run modified the file")
	}
}

func TestEditDashEFlags(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\nfunction dead(): number {\n\treturn 0;\n}\n",
	})
	f := &editFlags{exprs: editLineFlags{"move src/a.ts#f1 after src/a.ts#f2", "delete src/a.ts#dead"}}
	result, err := runEdit(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runEdit -e: %v", err)
	}
	if len(result.Ops) != 2 || !result.Tx.Applied {
		t.Fatalf("result = %+v, want 2 applied ops", result)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "function f2", "function f1")
	if strings.Contains(text, "dead") {
		t.Errorf("dead should be deleted:\n%s", text)
	}
}

func TestEditScriptSourceUsageErrors(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const x = 1;\n",
	})
	// -e and a positional script are mutually exclusive.
	f := &editFlags{exprs: editLineFlags{"delete src/a.ts#x"}, stdin: strings.NewReader("")}
	_, err := runEdit(context.Background(), ws, f, []string{"-"})
	if err == nil || cli.ExitCode(err) != cli.ExitUsage || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("-e + positional: got %v, want a usage error", err)
	}
	// Neither is a usage error too.
	_, err = runEdit(context.Background(), ws, &editFlags{}, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage || !strings.Contains(err.Error(), "missing script") {
		t.Errorf("no input: got %v, want a usage error", err)
	}
	// Empty script (comments only).
	_, err = runEditScript(t, ws, "# nothing\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage || !strings.Contains(err.Error(), "no operations") {
		t.Errorf("empty script: got %v, want a usage error", err)
	}
}

func TestEditTopEndUnknownFileExit3(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(): number {\n\treturn 1;\n}\n",
	})
	_, err := runEditScript(t, ws, "move src/a.ts#f end src/new.ts\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitNotFound {
		t.Fatalf("expected exit 3, got %v", err)
	}
	if !strings.Contains(err.Error(), "line 1: src/new.ts is not part of the program") ||
		!strings.Contains(err.Error(), "refactor mv-symbol") {
		t.Errorf("error = %q", err.Error())
	}
}

// editOverloadFixture has a 3-declaration function overload group plus an
// unrelated function.
func editOverloadFixture() map[string]any {
	return map[string]any{
		"/project/src/a.ts": "export function pick(a: number): number;\nexport function pick(a: string): string;\nexport function pick(a: unknown): unknown {\n\treturn a;\n}\n\nexport function other(): number {\n\treturn 1;\n}\n",
	}
}

func TestEditOverloadGroupDeleteAll(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, editOverloadFixture())
	result := mustRunEditScript(t, ws, "delete src/a.ts#pick\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(text, "pick") {
		t.Errorf("a bare overload-group ID must delete every declaration of the group:\n%s", text)
	}
	if !strings.Contains(text, "function other") {
		t.Errorf("unrelated declarations must survive:\n%s", text)
	}
	if len(result.Ops) != 1 || len(result.Ops[0].Notes) != 1 || !strings.Contains(result.Ops[0].Notes[0], "deleted all 3 overload declarations") {
		t.Errorf("Ops = %+v, want a 'deleted all 3 overload declarations' note", result.Ops)
	}
}

func TestEditOverloadGroupReplaceWhole(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, editOverloadFixture())
	script := "replace src/a.ts#pick <<EOF\nexport function pick(a: unknown): unknown {\n\treturn a;\n}\nEOF\n"
	result := mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if got := strings.Count(text, "function pick"); got != 1 {
		t.Errorf("replace over a contiguous overload group must replace the whole group; %d pick declaration(s) remain:\n%s", got, text)
	}
	if len(result.Ops) != 1 || len(result.Ops[0].Notes) != 1 || !strings.Contains(result.Ops[0].Notes[0], "replaced all 3 overload declarations") {
		t.Errorf("Ops = %+v, want a 'replaced all 3 overload declarations' note", result.Ops)
	}
}

func TestEditOverloadGroupReplaceNonContiguousRefused(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "interface I {\n\tm(a: number): void;\n}\ninterface I {\n\tm(a: string): void;\n}\nexport const probe: I = { m(_a: any) {} };\n",
	})
	script := "replace src/a.ts#I.m <<EOF\nm(a: number | string): void;\nEOF\n"
	_, err := runEditScript(t, ws, script, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected exit 2, got %v", err)
	}
	if !strings.Contains(err.Error(), "line 1: cannot replace src/a.ts#I.m: the 2 overload declarations are not contiguous; replace each ~N individually") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestEditOverloadGroupMoveWhole(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, editOverloadFixture())
	result := mustRunEditScript(t, ws, "move src/a.ts#pick after src/a.ts#other\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "function other", "pick(a: number)", "pick(a: string)", "pick(a: unknown)")
	if len(result.Ops) != 1 || len(result.Ops[0].Notes) != 1 || !strings.Contains(result.Ops[0].Notes[0], "moved all 3 overload declarations") {
		t.Errorf("Ops = %+v, want a 'moved all 3 overload declarations' note", result.Ops)
	}
}

func TestEditAmbiguousMergedDeclRequiresOrdinal(t *testing.T) {
	t.Parallel()
	files := map[string]any{
		"/project/src/a.ts": "var x = 1;\nvar x = 2;\nexport const y = x;\n",
	}
	ws := newTestWorkspace(t, files)
	_, err := runEditScript(t, ws, "delete src/a.ts#x\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitNotFound {
		t.Fatalf("expected exit 3 for a bare ambiguous ID, got %v", err)
	}
	if !strings.Contains(err.Error(), "matches 2 declarations") || !strings.Contains(err.Error(), "~0") || !strings.Contains(err.Error(), "~1") {
		t.Errorf("the ambiguity error must list the ~N candidates verbatim: %q", err.Error())
	}

	ws2 := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "var x = 1;\nvar x = 2;\nexport const y = x;\n",
	})
	mustRunEditScript(t, ws2, "delete src/a.ts#x~1\n")
	text := readWorkspaceFile(t, ws2, "/project/src/a.ts")
	if !strings.Contains(text, "var x = 1;") || strings.Contains(text, "var x = 2;") {
		t.Errorf("~1 must address exactly the second declaration:\n%s", text)
	}
}

func TestEditDeleteInsertAfterCompose(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\n\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	script := "delete src/a.ts#f1\ninsert after src/a.ts#f1 <<EOF\nexport function g(): number {\n\treturn 9;\n}\nEOF\n"
	result := mustRunEditScript(t, ws, script)
	if !result.Tx.Applied {
		t.Fatalf("tx = %+v, want applied", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(text, "f1") {
		t.Errorf("f1 must be deleted:\n%s", text)
	}
	indexOrder(t, text, "function g", "function f2")
}

func TestEditDeleteInsertBeforeCompose(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\n\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	script := "delete src/a.ts#f2\ninsert before src/a.ts#f2 <<EOF\nexport function g(): number {\n\treturn 9;\n}\nEOF\n"
	result := mustRunEditScript(t, ws, script)
	if !result.Tx.Applied {
		t.Fatalf("tx = %+v, want applied", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(text, "f2") {
		t.Errorf("f2 must be deleted:\n%s", text)
	}
	indexOrder(t, text, "function f1", "function g")
}

func TestEditEmptyHeredocBodyExit2(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\n",
	})
	_, err := runEditScript(t, ws, "insert after src/a.ts#f1 <<EOF\nEOF\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage || !strings.Contains(err.Error(), "line 1: empty body") {
		t.Errorf("empty heredoc body: got %v, want exit 2 'line 1: empty body'", err)
	}
	_, err = runEditScript(t, ws, "replace src/a.ts#f1 <<EOF\nEOF\n", nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage || !strings.Contains(err.Error(), "line 1: empty body") {
		t.Errorf("empty replace body: got %v, want exit 2 'line 1: empty body'", err)
	}
}

func TestEditDeleteFirstDeclCollapsesBOF(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\n\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	mustRunEditScript(t, ws, "delete src/a.ts#f1\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.HasPrefix(text, "export function f2") {
		t.Errorf("deleting the first declaration must not leave a blank line at the top of the file:\n%q", text)
	}
}

func TestEditInsertCRLFNormalized(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\r\n\treturn 1;\r\n}\r\n",
	})
	script := "insert after src/a.ts#f1 <<EOF\nexport function g(): number {\n\treturn 9;\n}\nEOF\n"
	mustRunEditScript(t, ws, script)
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.Contains(text, "export function g(): number {\r\n\treturn 9;\r\n}\r\n") {
		t.Errorf("inserted text must use the file's dominant CRLF line endings:\n%q", text)
	}
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Errorf("the result must not mix line endings:\n%q", text)
	}
}

func TestEditWithDepsNoteOnWithinFileMove(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	result := mustRunEditScript(t, ws, "move src/a.ts#f1 after src/a.ts#f2 with-deps\n")
	found := false
	for _, op := range result.Ops {
		for _, note := range op.Notes {
			if strings.Contains(note, "with-deps has no effect on within-file moves") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("Ops = %+v, want a with-deps-has-no-effect note", result.Ops)
	}
}

func TestEditInsertIntoReplacedContainerConflicts(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export class C {\n\tm(): number {\n\t\treturn 1;\n\t}\n}\n",
	})
	script := "replace src/a.ts#C <<EOF\nexport class C {\n\tm(): number {\n\t\treturn 2;\n\t}\n}\nEOF\ninsert into src/a.ts#C <<EOF\n\tx = 1;\nEOF\n"
	_, err := runEditScript(t, ws, script, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected exit 2, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "conflicts with replace at line 1") || !strings.Contains(msg, "overlapping ranges in src/a.ts") {
		t.Errorf("conflicts must be line-attributed: %q", msg)
	}
	if strings.Contains(msg, "overlapping edits at [") {
		t.Errorf("raw byte-offset engine errors must never surface: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Enum member insertion (round-3 FIX 4)

func TestEditInsertIntoEnumBothCommaStyles(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export enum WithComma {\n\tA = 1,\n}\nexport enum NoComma {\n\tB = 1\n}\nexport enum Empty {}\n",
	})
	script := strings.Join([]string{
		"insert into src/a.ts#WithComma <<EOF",
		"\tC = 2,",
		"EOF",
		"insert into src/a.ts#NoComma <<EOF",
		"\tD = 2",
		"EOF",
		"insert into src/a.ts#Empty <<EOF",
		"\tE = 1,",
		"EOF",
		"",
	}, "\n")
	result := mustRunEditScript(t, ws, script)
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.Contains(text, "export enum WithComma {\n\tA = 1,\n\tC = 2,\n}") {
		t.Errorf("trailing-comma enum: new member must land AFTER the comma, on its own line:\n%s", text)
	}
	if !strings.Contains(text, "export enum NoComma {\n\tB = 1,\n\tD = 2\n}") {
		t.Errorf("no-trailing-comma enum: a separating comma must be added to the previous member:\n%s", text)
	}
	if !strings.Contains(text, "export enum Empty {\n\tE = 1,\n}") {
		t.Errorf("empty enum: the member must land inside the braces:\n%s", text)
	}
}

// ---------------------------------------------------------------------------
// Leading line-comment travel (round-3 polish)

func TestEditMoveCarriesLeadingLineComments(t *testing.T) {
	t.Parallel()
	// Contiguous leading `//` comments (no blank line between them and the
	// declaration) belong to the declaration and travel with the move.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "// mutually recursive helpers\n// keep together\nexport function f1(): number {\n\treturn 1;\n}\n\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	result := mustRunEditScript(t, ws, "move src/a.ts#f1 after src/a.ts#f2\n")
	if !result.Tx.Applied || len(result.Tx.NewErrors) != 0 {
		t.Fatalf("tx = %+v, want clean apply", result.Tx)
	}
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "function f2", "// mutually recursive helpers", "// keep together", "function f1")
	if !strings.HasPrefix(text, "export function f2") {
		t.Errorf("no orphaned comment may remain at the top:\n%s", text)
	}
}

func TestEditMoveLeavesBlankSeparatedCommentBehind(t *testing.T) {
	t.Parallel()
	// A blank line between the comment block and the declaration detaches it:
	// the comment is a file header and stays put.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "// file header\n\nexport function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	mustRunEditScript(t, ws, "move src/a.ts#f1 after src/a.ts#f2\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.HasPrefix(text, "// file header\n") {
		t.Errorf("the blank-separated header must stay at the top:\n%s", text)
	}
	indexOrder(t, text, "// file header", "function f2", "function f1")
}

func TestEditInsertBeforeLandsAboveLeadingComments(t *testing.T) {
	t.Parallel()
	// `insert before X` must not split X from its leading line comments.
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f0(): number {\n\treturn 0;\n}\n\n// belongs to f1\nexport function f1(): number {\n\treturn 1;\n}\n",
	})
	mustRunEditScript(t, ws, "insert before src/a.ts#f1 <<EOF\nexport const mid = 1;\nEOF\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	indexOrder(t, text, "function f0", "const mid", "// belongs to f1", "function f1")
}

func TestEditDeleteRemovesLeadingLineComments(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function keep(): number {\n\treturn 0;\n}\n\n// explains gone\nfunction gone(): number {\n\treturn 1;\n}\n",
	})
	mustRunEditScript(t, ws, "delete src/a.ts#gone\n")
	text := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(text, "explains gone") || strings.Contains(text, "function gone") {
		t.Errorf("the declaration's leading comment must be deleted with it:\n%s", text)
	}
}
