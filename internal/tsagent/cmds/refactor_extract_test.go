package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

func TestRefactorExtractConstant(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function calc(): number {\n\tconst total = 1 + 2 * 3;\n\treturn total;\n}\n",
	})
	f := &refactorExtractFlags{
		tx:        refactorTxFlags{apply: true},
		rangeSpec: "src/a.ts:2:20-2:25", // `2 * 3`
		into:      "constant",
		name:      "six",
	}
	result, err := runRefactorExtract(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorExtract: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	got := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.Contains(got, "\tconst six = 2 * 3;\n\tconst total = 1 + six;") {
		t.Errorf("a.ts after extract constant = %q", got)
	}
}

func TestRefactorExtractConstantRefusesPartialNode(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function calc(): number {\n\tconst total = 1 + 2 * 3;\n\treturn total;\n}\n",
	})
	f := &refactorExtractFlags{
		rangeSpec: "src/a.ts:2:16-2:21", // `1 + 2` — not a node (precedence)
		into:      "constant",
		name:      "bad",
	}
	_, err := runRefactorExtract(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Errorf("expected a refusal for a non-node range, got %v", err)
	}
}

func TestRefactorExtractFunctionWithInputAndOutput(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function process(items: number[]): number {\n\tlet sum = 0;\n\tfor (const item of items) {\n\t\tsum = sum + item;\n\t}\n\tconst doubled = sum * 2;\n\treturn doubled;\n}\n",
	})
	f := &refactorExtractFlags{
		tx:        refactorTxFlags{apply: true},
		rangeSpec: "src/a.ts:6:2-6:26", // `const doubled = sum * 2;`
		into:      "function",
		name:      "doubleIt",
	}
	result, err := runRefactorExtract(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorExtract: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply: %v", result, result.NewErrors)
	}
	got := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.Contains(got, "const doubled = doubleIt(sum);") {
		t.Errorf("range should be replaced with a call: %q", got)
	}
	if !strings.Contains(got, "function doubleIt(sum: number) {") {
		t.Errorf("extracted function with typed parameter expected: %q", got)
	}
	if !strings.Contains(got, "return doubled;\n}") {
		t.Errorf("extracted function should return the output variable: %q", got)
	}
}

func TestRefactorExtractFunctionAsyncWhenRangeAwaits(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export async function load(): Promise<number> {\n\tconst data = await Promise.resolve(1);\n\treturn data;\n}\n",
	})
	f := &refactorExtractFlags{
		tx:        refactorTxFlags{apply: true},
		rangeSpec: "src/a.ts:2:2-2:40", // `const data = await Promise.resolve(1);`
		into:      "function",
		name:      "fetchData",
	}
	result, err := runRefactorExtract(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorExtract: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply: %v", result, result.NewErrors)
	}
	got := readWorkspaceFile(t, ws, "/project/src/a.ts")
	if !strings.Contains(got, "const data = await fetchData();") {
		t.Errorf("call should be awaited: %q", got)
	}
	if !strings.Contains(got, "async function fetchData() {") {
		t.Errorf("extracted function should be async: %q", got)
	}
}

func TestRefactorExtractFunctionRefusesReturnInRange(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "export function f(x: number): number {\n\tif (x > 0) {\n\t\treturn 1;\n\t}\n\treturn 0;\n}\n",
	})
	f := &refactorExtractFlags{
		rangeSpec: "src/a.ts:2:2-4:3", // the if statement containing a return
		into:      "function",
		name:      "positiveCheck",
	}
	_, err := runRefactorExtract(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "return") {
		t.Errorf("refusal should mention the return statement: %v", err)
	}
}

func TestRefactorExtractValidatesFlags(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const a = 1;\n"})
	ctx := context.Background()
	if _, err := runRefactorExtract(ctx, ws, &refactorExtractFlags{into: "constant", name: "x"}, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("missing --range: got %v, want usage error", err)
	}
	f := &refactorExtractFlags{rangeSpec: "src/a.ts:1:1-1:5", into: "type", name: "x"}
	if _, err := runRefactorExtract(ctx, ws, f, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("bad --into: got %v, want usage error", err)
	}
	f = &refactorExtractFlags{rangeSpec: "src/a.ts:1:1-1:5", into: "constant"}
	if _, err := runRefactorExtract(ctx, ws, f, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("missing --name: got %v, want usage error", err)
	}
}
