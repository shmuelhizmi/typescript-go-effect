package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// resolveMoveTarget resolves a symbol by name and returns the widened
// declaration node the planner expects.
func resolveMoveTarget(t *testing.T, ws *core.Workspace, name string) (*ast.Node, *ast.Symbol) {
	t.Helper()
	target, err := ws.ResolveTarget(context.Background(), core.TargetSpec{Name: name})
	if err != nil {
		t.Fatalf("ResolveTarget(%q): %v", name, err)
	}
	if target.Symbol == nil || len(target.Symbol.Declarations) == 0 {
		t.Fatalf("target %q has no declarations", name)
	}
	return refactorDeletionNode(target.Symbol.Declarations[0]), target.Symbol
}

func TestSymbolMoveAnchoredInsertPos(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "/** Doubles a number. */\nexport function double(n: number): number {\n\treturn n * 2;\n}\n",
		"/project/src/math.ts": "export const PI = 3.14;\nexport const TAU = 6.28;\n",
		"/project/src/main.ts": "import { double } from \"./util\";\nimport { PI } from \"./math\";\nexport const d = double(PI);\n",
	})
	declNode, symbol := resolveMoveTarget(t, ws, "double")
	destFile := ws.Program.GetSourceFile("/project/src/math.ts")
	if destFile == nil {
		t.Fatal("missing dest file")
	}
	// Anchor at the start of the TAU statement: the moved text lands between
	// PI and TAU.
	anchor := strings.Index(destFile.Text(), "export const TAU")
	var es core.EditSet
	notes, err := planSymbolMove(context.Background(), ws, declNode, symbol,
		symbolMoveDest{fileAbs: "/project/src/math.ts", file: destFile, insertPos: anchor}, &es)
	if err != nil {
		t.Fatalf("planSymbolMove: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
	result, err := core.Execute(context.Background(), ws, es, core.TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	math := readWorkspaceFile(t, ws, "/project/src/math.ts")
	want := "export const PI = 3.14;\n/** Doubles a number. */\nexport function double(n: number): number {\n\treturn n * 2;\n}\nexport const TAU = 6.28;\n"
	if math != want {
		t.Errorf("math.ts = %q, want %q", math, want)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "import { double } from \"./math\";") {
		t.Errorf("importer not rewritten: %q", main)
	}
}

func TestSymbolMoveAnchoredKeepsDepImportsAtTop(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/base.ts": "export const BASE = 10;\n",
		"/project/src/util.ts": "import { BASE } from \"./base\";\nexport function scaled(n: number): number {\n\treturn n * BASE;\n}\nexport const ten = BASE;\n",
		"/project/src/dest.ts": "export const first = 1;\nexport const last = 2;\n",
		"/project/src/main.ts": "import { scaled } from \"./util\";\nexport const s = scaled(2);\n",
	})
	declNode, symbol := resolveMoveTarget(t, ws, "scaled")
	destFile := ws.Program.GetSourceFile("/project/src/dest.ts")
	anchor := strings.Index(destFile.Text(), "export const last")
	var es core.EditSet
	if _, err := planSymbolMove(context.Background(), ws, declNode, symbol,
		symbolMoveDest{fileAbs: "/project/src/dest.ts", file: destFile, insertPos: anchor}, &es); err != nil {
		t.Fatalf("planSymbolMove: %v", err)
	}
	result, err := core.Execute(context.Background(), ws, es, core.TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	dest := readWorkspaceFile(t, ws, "/project/src/dest.ts")
	want := "import { BASE } from \"./base\";\nexport const first = 1;\nexport function scaled(n: number): number {\n\treturn n * BASE;\n}\nexport const last = 2;\n"
	if dest != want {
		t.Errorf("dest.ts = %q, want %q", dest, want)
	}
}

func TestSymbolMoveBatchesIntoOneEditSet(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "export function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\n",
		"/project/src/math.ts": "export const PI = 3.14;\n",
	})
	decl1, sym1 := resolveMoveTarget(t, ws, "f1")
	decl2, sym2 := resolveMoveTarget(t, ws, "f2")
	destFile := ws.Program.GetSourceFile("/project/src/math.ts")
	dest := symbolMoveDest{fileAbs: "/project/src/math.ts", file: destFile, insertPos: -1}
	var es core.EditSet
	if _, err := planSymbolMove(context.Background(), ws, decl1, sym1, dest, &es); err != nil {
		t.Fatalf("planSymbolMove f1: %v", err)
	}
	if _, err := planSymbolMove(context.Background(), ws, decl2, sym2, dest, &es); err != nil {
		t.Fatalf("planSymbolMove f2: %v", err)
	}
	result, err := core.Execute(context.Background(), ws, es, core.TxOpts{Apply: true, SingleThreaded: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	math := readWorkspaceFile(t, ws, "/project/src/math.ts")
	if !strings.Contains(math, "function f1") || !strings.Contains(math, "function f2") {
		t.Errorf("math.ts should hold both moved functions: %q", math)
	}
	util := readWorkspaceFile(t, ws, "/project/src/util.ts")
	if strings.Contains(util, "f1") || strings.Contains(util, "f2") {
		t.Errorf("util.ts should no longer hold the moved functions: %q", util)
	}
}

func TestSymbolMoveRefusesPendingCreateDestination(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/util.ts": "export function f1(): number {\n\treturn 1;\n}\nexport function f2(): number {\n\treturn 2;\n}\n",
	})
	decl1, sym1 := resolveMoveTarget(t, ws, "f1")
	decl2, sym2 := resolveMoveTarget(t, ws, "f2")
	dest := symbolMoveDest{fileAbs: "/project/src/new.ts", create: true, insertPos: -1}
	var es core.EditSet
	if _, err := planSymbolMove(context.Background(), ws, decl1, sym1, dest, &es); err != nil {
		t.Fatalf("planSymbolMove f1: %v", err)
	}
	_, err := planSymbolMove(context.Background(), ws, decl2, sym2, dest, &es)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "is being created by another op in this transaction") {
		t.Errorf("refusal message = %v", err)
	}
}
