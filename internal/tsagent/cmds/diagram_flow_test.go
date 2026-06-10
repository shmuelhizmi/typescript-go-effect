package cmds

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func diagramFlowFiles() map[string]any {
	return map[string]any{
		"/project/src/flow.ts": `export function classify(n: number): string {
	let label = "";
	if (n < 0) {
		label = "negative";
	} else {
		label = "positive";
	}
	while (n > 10) {
		n = n / 2;
		if (n === 42) {
			return "answer";
		}
	}
	throw new Error(label);
}
`,
	}
}

func TestDiagramFlowIfElseAndLoop(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramFlowFiles())
	result, err := runDiagramFlow(context.Background(), ws, &diagramFlowFlags{out: "json", name: "classify"}, nil)
	if err != nil {
		t.Fatalf("runDiagramFlow: %v", err)
	}
	var graph struct {
		Nodes []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"nodes"`
		Edges []struct {
			From  string `json:"from"`
			To    string `json:"to"`
			Label string `json:"label"`
		} `json:"edges"`
		Direction string `json:"direction"`
	}
	if err := json.Unmarshal([]byte(result.Diagram), &graph); err != nil {
		t.Fatalf("diagram is not JSON: %v\n%s", err, result.Diagram)
	}
	if graph.Direction != "TD" {
		t.Errorf("direction = %q, want TD", graph.Direction)
	}

	idByLabel := func(substr string) string {
		for _, n := range graph.Nodes {
			if strings.Contains(n.Label, substr) {
				return n.ID
			}
		}
		t.Fatalf("no node labeled with %q in %+v", substr, graph.Nodes)
		return ""
	}
	hasEdge := func(from, to, label string) bool {
		for _, e := range graph.Edges {
			if e.From == from && e.To == to && (label == "" || e.Label == label) {
				return true
			}
		}
		return false
	}

	ifNode := idByLabel("if (n < 0)")
	whileNode := idByLabel("while (n > 10)")
	thenNode := idByLabel(`label = "negative"`)
	elseNode := idByLabel(`label = "positive"`)
	throwNode := idByLabel("throw new Error(label)")
	returnNode := idByLabel(`return "answer"`)
	endNode := idByLabel("END")
	innerIf := idByLabel("if (n === 42)")

	if !hasEdge(ifNode, thenNode, "then") || !hasEdge(ifNode, elseNode, "else") {
		t.Errorf("missing if branches: %+v", graph.Edges)
	}
	// Both branches merge at the loop header.
	if !hasEdge(thenNode, whileNode, "") || !hasEdge(elseNode, whileNode, "") {
		t.Errorf("branches must merge at while: %+v", graph.Edges)
	}
	// Loop body loops back to the condition; false exits to the throw.
	bodyNode := idByLabel("n = n / 2")
	if !hasEdge(whileNode, bodyNode, "true") {
		t.Errorf("missing loop entry edge: %+v", graph.Edges)
	}
	if !hasEdge(innerIf, whileNode, "else") {
		t.Errorf("missing loop-back edge from inner if: %+v", graph.Edges)
	}
	if !hasEdge(whileNode, throwNode, "false") {
		t.Errorf("missing loop exit edge: %+v", graph.Edges)
	}
	// return/throw go straight to END.
	if !hasEdge(returnNode, endNode, "") || !hasEdge(throwNode, endNode, "") {
		t.Errorf("return/throw must edge to END: %+v", graph.Edges)
	}

	// Labels carry line numbers and are capped at 40 chars.
	for _, n := range graph.Nodes {
		if strings.Contains(n.Label, "if (n < 0)") && !strings.HasPrefix(n.Label, "L3:") {
			t.Errorf("if label = %q, want L3: prefix", n.Label)
		}
	}

	// Mermaid output is a TD flowchart.
	mermaid, err := runDiagramFlow(context.Background(), ws, &diagramFlowFlags{name: "classify"}, nil)
	if err != nil {
		t.Fatalf("runDiagramFlow mermaid: %v", err)
	}
	if !strings.HasPrefix(mermaid.Diagram, "flowchart TD\n") {
		t.Errorf("mermaid = %q…, want flowchart TD", mermaid.Diagram[:min(40, len(mermaid.Diagram))])
	}
	if !strings.Contains(mermaid.Diagram, "END") {
		t.Error("mermaid output should contain the END node")
	}
}

func TestDiagramFlowPositionTarget(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, diagramFlowFiles())
	// A position inside the body resolves to the enclosing function.
	result, err := runDiagramFlow(context.Background(), ws, &diagramFlowFlags{out: "mermaid"}, []string{"src/flow.ts:4:3"})
	if err != nil {
		t.Fatalf("runDiagramFlow position: %v", err)
	}
	if !strings.Contains(result.Diagram, "START classify") {
		t.Errorf("diagram missing START classify:\n%s", result.Diagram)
	}
	if result.Stats["nodes"] < 8 {
		t.Errorf("stats = %+v, want >= 8 nodes", result.Stats)
	}
}
