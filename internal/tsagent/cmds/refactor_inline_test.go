package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

func TestRefactorInlineConstVariable(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "const factor = 2;\nexport const x = factor * 3;\nexport const y = { factor };\n",
	})
	f := &refactorInlineFlags{
		target: refactorTargetFlags{name: "factor"},
		tx:     refactorTxFlags{apply: true},
	}
	result, err := runRefactorInline(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorInline: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	got := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(got, "const factor") {
		t.Errorf("declaration should be deleted: %q", got)
	}
	if !strings.Contains(got, "export const x = 2 * 3;") {
		t.Errorf("read site should be substituted: %q", got)
	}
	if !strings.Contains(got, "export const y = { factor: 2 };") {
		t.Errorf("shorthand property should expand: %q", got)
	}
}

func TestRefactorInlineConstAcrossFilesRemovesImport(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export const NAME = \"tsagent\";\n",
		"/project/src/b.ts": "import { NAME } from \"./a\";\nexport const msg = NAME;\n",
	})
	f := &refactorInlineFlags{
		target: refactorTargetFlags{name: "NAME"},
		tx:     refactorTxFlags{apply: true},
	}
	result, err := runRefactorInline(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorInline: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	b := readWorkspaceFile(t, ws, "/project/src/b.ts")
	if strings.Contains(b, "import") {
		t.Errorf("import of the inlined symbol should be removed: %q", b)
	}
	if !strings.Contains(b, "export const msg = \"tsagent\";") {
		t.Errorf("usage should be substituted: %q", b)
	}
	a := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(a, "NAME") {
		t.Errorf("declaration should be deleted: %q", a)
	}
}

func TestRefactorInlineSimpleFunction(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "function add(a: number, b: number): number {\n\treturn a + b;\n}\nexport const s = add(1, 2 + 3);\n",
	})
	f := &refactorInlineFlags{
		target: refactorTargetFlags{name: "add"},
		tx:     refactorTxFlags{apply: true},
	}
	result, err := runRefactorInline(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorInline: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	got := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if strings.Contains(got, "function add") {
		t.Errorf("function declaration should be deleted: %q", got)
	}
	if !strings.Contains(got, "export const s = (1 + (2 + 3));") {
		t.Errorf("call should be replaced with the substituted body: %q", got)
	}
}

func TestRefactorInlineRefusesNonConstVariable(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "let counter = 0;\nexport const c = counter;\n",
	})
	f := &refactorInlineFlags{target: refactorTargetFlags{name: "counter"}}
	_, err := runRefactorInline(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Errorf("expected a refusal for let variables, got %v", err)
	}
}

func TestRefactorInlineRefusesFunctionUsedAsValue(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "function id(x: number): number {\n\treturn x;\n}\nexport const cb = id;\n",
	})
	f := &refactorInlineFlags{target: refactorTargetFlags{name: "id"}}
	_, err := runRefactorInline(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Errorf("expected a refusal for value usage, got %v", err)
	}
}

func TestRefactorInlineRefusesSideEffectArgUsedTwice(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "let n = 0;\nfunction next(): number {\n\treturn ++n;\n}\nfunction twice(a: number): number {\n\treturn a + a;\n}\nexport const r = twice(next());\n",
	})
	f := &refactorInlineFlags{target: refactorTargetFlags{name: "twice"}}
	_, err := runRefactorInline(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "side effects") {
		t.Errorf("refusal should mention side effects: %v", err)
	}
}

func TestRefactorInlineRefusesComplexFunctionBody(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "function log(x: number): number {\n\tconst y = x + 1;\n\treturn y;\n}\nexport const r = log(1);\n",
	})
	f := &refactorInlineFlags{target: refactorTargetFlags{name: "log"}}
	_, err := runRefactorInline(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Errorf("expected a refusal for multi-statement bodies, got %v", err)
	}
}
