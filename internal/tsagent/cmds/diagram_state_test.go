package cmds

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
)

func diagramStateFiles() map[string]any {
	return map[string]any{
		"/project/src/machine.ts": `export interface Idle {
	kind: "idle";
}
export interface Loading {
	kind: "loading";
	pct: number;
}
export interface Done {
	kind: "done";
	result: string;
}
export type State = Idle | Loading | Done;

export function start(s: Idle): Loading {
	return { kind: "loading", pct: 0 };
}

export function finish(s: Loading): Done {
	return { kind: "done", result: "ok" };
}

export const reset = (s: State): Idle => ({ kind: "idle" });

// Unrelated function: must not contribute transitions.
export function plain(n: number): number {
	return n;
}
`,
	}
}

func TestDiagramStateMachine(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramStateFiles())
	result, err := runDiagramState(context.Background(), ws, &diagramStateFlags{out: "json", name: "State"}, nil)
	if err != nil {
		t.Fatalf("runDiagramState: %v", err)
	}
	var machine stateMachine
	if err := json.Unmarshal([]byte(result.Diagram), &machine); err != nil {
		t.Fatalf("diagram is not JSON: %v\n%s", err, result.Diagram)
	}
	if machine.Discriminant != "kind" {
		t.Errorf("discriminant = %q, want kind (auto-detected)", machine.Discriminant)
	}
	if len(machine.States) != 3 {
		t.Fatalf("states = %v, want 3", machine.States)
	}
	stateSet := map[string]bool{}
	for _, s := range machine.States {
		stateSet[s] = true
	}
	for _, want := range []string{"idle", "loading", "done"} {
		if !stateSet[want] {
			t.Errorf("missing state %q in %v", want, machine.States)
		}
	}

	transitionSet := map[stateTransition]bool{}
	for _, tr := range machine.Transitions {
		transitionSet[tr] = true
	}
	if !transitionSet[stateTransition{From: "idle", To: "loading", Label: "start"}] {
		t.Errorf("missing idle->loading start transition: %+v", machine.Transitions)
	}
	if !transitionSet[stateTransition{From: "loading", To: "done", Label: "finish"}] {
		t.Errorf("missing loading->done finish transition: %+v", machine.Transitions)
	}
	// reset takes the whole union: an edge from every state to idle.
	for _, from := range []string{"idle", "loading", "done"} {
		if !transitionSet[stateTransition{From: from, To: "idle", Label: "reset"}] {
			t.Errorf("missing %s->idle reset transition: %+v", from, machine.Transitions)
		}
	}
	for tr := range transitionSet {
		if tr.Label == "plain" {
			t.Errorf("unrelated function leaked into transitions: %+v", tr)
		}
	}
	if result.Stats["states"] != 3 || result.Stats["transitions"] != 5 {
		t.Errorf("stats = %+v, want states:3 transitions:5", result.Stats)
	}
}

func TestDiagramStateMermaidAndErrors(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramStateFiles())
	ctx := context.Background()

	result, err := runDiagramState(ctx, ws, &diagramStateFlags{name: "State"}, nil)
	if err != nil {
		t.Fatalf("runDiagramState mermaid: %v", err)
	}
	if !strings.HasPrefix(result.Diagram, "stateDiagram-v2\n") {
		t.Errorf("mermaid header missing:\n%s", result.Diagram)
	}
	if !strings.Contains(result.Diagram, "idle --> loading: start") {
		t.Errorf("mermaid missing transition:\n%s", result.Diagram)
	}

	// Explicit discriminant works; a bogus one is a not-found error.
	if _, err := runDiagramState(ctx, ws, &diagramStateFlags{name: "State", discriminant: "kind"}, nil); err != nil {
		t.Errorf("explicit --discriminant kind: %v", err)
	}
	if _, err := runDiagramState(ctx, ws, &diagramStateFlags{name: "State", discriminant: "pct"}, nil); cli.ExitCode(err) != cli.ExitNotFound {
		t.Errorf("bogus discriminant: err = %v, want not-found", err)
	}

	// A non-union target is a usage error.
	if _, err := runDiagramState(ctx, ws, &diagramStateFlags{name: "Idle"}, nil); cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("non-alias target: err = %v, want usage error", err)
	}
}
