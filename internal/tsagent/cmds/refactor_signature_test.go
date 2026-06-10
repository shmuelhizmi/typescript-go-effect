package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

func TestRefactorSignatureAddWithDefault(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/api.ts":  "export function fetchUser(id: string): string {\n\treturn id;\n}\n",
		"/project/src/main.ts": "import { fetchUser } from \"./api\";\nexport const u = fetchUser(\"1\");\n",
	})
	f := &refactorSignatureFlags{
		target: refactorTargetFlags{name: "fetchUser"},
		tx:     refactorTxFlags{apply: true},
		ops:    `[{"op":"add","name":"retries","type":"number","default":"3"}]`,
	}
	result, err := runRefactorSignature(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorSignature: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	api := readWorkspaceFile(t, ws, "/project/src/api.ts")
	if !strings.Contains(api, "fetchUser(id: string, retries: number = 3)") {
		t.Errorf("api.ts after signature add = %q", api)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "fetchUser(\"1\")") {
		t.Errorf("appended defaulted param must not touch call sites: %q", main)
	}
}

func TestRefactorSignatureAddRequiredUsesFillWith(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/api.ts":  "export function send(msg: string): string {\n\treturn msg;\n}\n",
		"/project/src/main.ts": "import { send } from \"./api\";\nexport const r = send(\"hello\");\n",
	})
	f := &refactorSignatureFlags{
		target:   refactorTargetFlags{name: "send"},
		tx:       refactorTxFlags{apply: true},
		ops:      `[{"op":"add","name":"urgent","type":"boolean","index":0}]`,
		fillWith: "false",
	}
	result, err := runRefactorSignature(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorSignature: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	api := readWorkspaceFile(t, ws, "/project/src/api.ts")
	if !strings.Contains(api, "send(urgent: boolean, msg: string)") {
		t.Errorf("api.ts after signature add at index = %q", api)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "send(false, \"hello\")") {
		t.Errorf("call site should receive the fill expression: %q", main)
	}
}

func TestRefactorSignatureAddRequiredRefusesWithoutFill(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/api.ts":  "export function send(msg: string): string {\n\treturn msg;\n}\n",
		"/project/src/main.ts": "import { send } from \"./api\";\nexport const r = send(\"hello\");\n",
	})
	f := &refactorSignatureFlags{
		target: refactorTargetFlags{name: "send"},
		ops:    `[{"op":"add","name":"urgent","type":"boolean","index":0}]`,
	}
	_, err := runRefactorSignature(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "src/main.ts:2:") {
		t.Errorf("refusal should list the call sites needing a value: %v", err)
	}
}

func TestRefactorSignatureReorderRewritesCallSites(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/api.ts":  "export function pair(a: number, b: string): string {\n\treturn b + a;\n}\n",
		"/project/src/main.ts": "import { pair } from \"./api\";\nexport const p = pair(1, \"x\");\n",
	})
	f := &refactorSignatureFlags{
		target: refactorTargetFlags{name: "pair"},
		tx:     refactorTxFlags{apply: true},
		ops:    `[{"op":"reorder","order":[1,0]}]`,
	}
	result, err := runRefactorSignature(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorSignature: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	api := readWorkspaceFile(t, ws, "/project/src/api.ts")
	if !strings.Contains(api, "pair(b: string, a: number)") {
		t.Errorf("api.ts after reorder = %q", api)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "pair(\"x\", 1)") {
		t.Errorf("call site arguments should be reordered: %q", main)
	}
}

func TestRefactorSignatureRemoveUnusedParam(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/api.ts":  "export function greet(name: string, unused: number): string {\n\treturn name;\n}\n",
		"/project/src/main.ts": "import { greet } from \"./api\";\nexport const g = greet(\"a\", 1);\n",
	})
	f := &refactorSignatureFlags{
		target: refactorTargetFlags{name: "greet"},
		tx:     refactorTxFlags{apply: true},
		ops:    `[{"op":"remove","name":"unused"}]`,
	}
	result, err := runRefactorSignature(context.Background(), ws, f, nil)
	if err != nil {
		t.Fatalf("runRefactorSignature: %v", err)
	}
	if !result.Applied || len(result.NewErrors) != 0 {
		t.Fatalf("result = %+v, want clean apply", result)
	}
	api := readWorkspaceFile(t, ws, "/project/src/api.ts")
	if !strings.Contains(api, "greet(name: string)") {
		t.Errorf("api.ts after remove = %q", api)
	}
	main := readWorkspaceFile(t, ws, "/project/src/main.ts")
	if !strings.Contains(main, "greet(\"a\")") {
		t.Errorf("call-site argument should be deleted: %q", main)
	}
}

func TestRefactorSignatureRemoveRefusesUsedParam(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/api.ts": "export function greet(name: string): string {\n\treturn name;\n}\n",
	})
	f := &refactorSignatureFlags{
		target: refactorTargetFlags{name: "greet"},
		ops:    `[{"op":"remove","name":"name"}]`,
	}
	_, err := runRefactorSignature(context.Background(), ws, f, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitRefused {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal should mention --force: %v", err)
	}
}

func TestRefactorSignatureValidatesOps(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/api.ts": "export function f(a: number): number {\n\treturn a;\n}\n",
	})
	ctx := context.Background()
	f := &refactorSignatureFlags{target: refactorTargetFlags{name: "f"}}
	if _, err := runRefactorSignature(ctx, ws, f, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("missing --ops: got %v, want usage error", err)
	}
	f = &refactorSignatureFlags{target: refactorTargetFlags{name: "f"}, ops: `[{"op":"explode"}]`}
	if _, err := runRefactorSignature(ctx, ws, f, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("unknown op: got %v, want usage error", err)
	}
	f = &refactorSignatureFlags{target: refactorTargetFlags{name: "f"}, ops: `[{"op":"reorder","order":[0,0]}]`}
	if _, err := runRefactorSignature(ctx, ws, f, nil); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("bad permutation: got %v, want usage error", err)
	}
}
