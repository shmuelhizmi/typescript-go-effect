package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// diagram_state.go implements `diagram state <type-target>` (spec §4.6):
// derive a state machine from a discriminated union. States are the union
// members' discriminant literal values; transitions come from project
// functions whose first parameter type relates to the union and whose return
// type relates to it (assignability-based, on one checker). v1 limitations:
// only direct function signatures are scanned (top-level function
// declarations and function-valued top-level consts) — no method chains, no
// Promise unwrapping.

func init() {
	cli.Register(cli.Command{
		Family:       "diagram",
		Name:         "state",
		Summary:      "State machine from a discriminated union (states + transition functions)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &diagramStateFlags{}
			fs.StringVar(&f.out, "out", "mermaid", "diagram syntax: mermaid, dot, or json")
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id of the union type alias")
			fs.StringVar(&f.name, "name", "", "target type alias name (must be unambiguous)")
			fs.StringVar(&f.discriminant, "discriminant", "", "discriminant property name (default: auto-detected)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runDiagramState(ctx, ws, flags.(*diagramStateFlags), args)
		},
	})
}

type diagramStateFlags struct {
	out          string
	symbol       string
	name         string
	discriminant string
}

// stateTransition is one labeled state-machine edge.
type stateTransition struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

// stateMachine is the JSON form of the derived state machine.
type stateMachine struct {
	Type         string            `json:"type"`
	Discriminant string            `json:"discriminant"`
	States       []string          `json:"states"`
	Transitions  []stateTransition `json:"transitions"`
}

func runDiagramState(ctx context.Context, ws *core.Workspace, flags *diagramStateFlags, args []string) (*DiagramResult, error) {
	syntax, err := parseDiagramSyntax(flags.out)
	if err != nil {
		return nil, err
	}
	spec := core.TargetSpec{Symbol: flags.symbol, Name: flags.name}
	if spec.Symbol == "" && spec.Name == "" {
		if len(args) != 1 {
			return nil, cli.UsageErrorf("diagram state takes exactly one type target (symbol id or name; or --symbol/--name)")
		}
		spec = guessRefactorTargetSpec(args[0])
	} else if len(args) > 0 {
		return nil, cli.UsageErrorf("positional target and --symbol/--name are mutually exclusive")
	}
	target, err := ws.ResolveTarget(ctx, spec)
	if err != nil {
		return nil, err
	}
	if target.Symbol == nil {
		return nil, cli.NotFoundErrorf("target does not resolve to a symbol")
	}
	if !slices.ContainsFunc(target.Symbol.Declarations, func(d *ast.Node) bool {
		return d.Kind == ast.KindTypeAliasDeclaration
	}) {
		return nil, cli.UsageErrorf("diagram state targets a union type alias (got %s)", core.DeclarationKind(target.Node))
	}

	// One checker for the whole analysis so type identities line up.
	c, done := ws.Program.GetTypeChecker(ctx)
	defer done()

	union := c.GetDeclaredTypeOfSymbol(target.Symbol)
	if union == nil || !union.IsUnion() {
		return nil, cli.UsageErrorf("type %s is not a union", target.Symbol.Name)
	}
	members := union.Types()

	discriminant, err := stateDiscriminant(c, members, flags.discriminant)
	if err != nil {
		return nil, err
	}

	machine := &stateMachine{Type: target.Symbol.Name, Discriminant: discriminant}
	states := make([]string, len(members))
	for i, member := range members {
		states[i] = stateLiteralValue(c, member, discriminant)
		machine.States = append(machine.States, states[i])
	}

	machine.Transitions = stateTransitions(ctx, ws, c, union, members, states)
	stats := map[string]int{"states": len(machine.States), "transitions": len(machine.Transitions)}
	return renderStateMachine(machine, syntax, stats)
}

// stateDiscriminant returns the validated --discriminant, or auto-detects the
// first property (in declaration order of the first member) whose type is a
// literal in every member.
func stateDiscriminant(c *checker.Checker, members []*checker.Type, requested string) (string, error) {
	isLiteralInAll := func(name string) bool {
		for _, member := range members {
			prop := c.GetPropertyOfType(member, name)
			if prop == nil {
				return false
			}
			t := c.GetTypeOfSymbol(prop)
			if t == nil || t.Flags()&checker.TypeFlagsLiteral == 0 {
				return false
			}
		}
		return true
	}
	if requested != "" {
		if !isLiteralInAll(requested) {
			return "", cli.NotFoundErrorf("property %q is not a literal discriminant of every union member", requested)
		}
		return requested, nil
	}
	for _, prop := range c.GetPropertiesOfType(members[0]) {
		if isLiteralInAll(prop.Name) {
			return prop.Name, nil
		}
	}
	return "", cli.NotFoundErrorf("no common literal discriminant property found; pass --discriminant <name>")
}

// stateLiteralValue renders a member's discriminant literal value.
func stateLiteralValue(c *checker.Checker, member *checker.Type, discriminant string) string {
	prop := c.GetPropertyOfType(member, discriminant)
	t := c.GetTypeOfSymbol(prop)
	if t.Flags()&checker.TypeFlagsLiteral != 0 {
		return fmt.Sprintf("%v", t.AsLiteralType().Value())
	}
	return c.TypeToString(t)
}

// stateTransitions scans top-level functions (declarations and
// function-valued consts) whose first parameter relates to the union: edges
// go from the parameter's state(s) to the return type's state(s), labeled
// with the function name. A whole-union parameter or return fans out to
// every state.
func stateTransitions(ctx context.Context, ws *core.Workspace, c *checker.Checker, union *checker.Type, members []*checker.Type, states []string) []stateTransition {
	// memberStates returns the state indices a type can be: the members
	// assignable to it (itself for a single member, all for the union).
	memberStates := func(t *checker.Type) []int {
		if t == nil || t.Flags()&(checker.TypeFlagsAny|checker.TypeFlagsUnknown|checker.TypeFlagsNever|checker.TypeFlagsVoid) != 0 {
			return nil
		}
		if !c.IsTypeAssignableTo(t, union) {
			return nil
		}
		var indices []int
		for i, member := range members {
			if c.IsTypeAssignableTo(member, t) {
				indices = append(indices, i)
			}
		}
		return indices
	}

	var transitions []stateTransition
	seen := make(map[stateTransition]bool)
	files, err := projectFiles(ws, nil)
	if err != nil {
		return nil
	}
	for _, file := range files {
		for _, fn := range stateCandidateFunctions(file) {
			params := fn.node.Parameters()
			if len(params) == 0 {
				continue
			}
			sig := c.GetSignatureFromDeclaration(fn.node)
			if sig == nil {
				continue
			}
			from := memberStates(c.GetTypeAtLocation(params[0]))
			to := memberStates(c.GetReturnTypeOfSignature(sig))
			if len(from) == 0 || len(to) == 0 {
				continue
			}
			for _, f := range from {
				for _, t := range to {
					transition := stateTransition{From: states[f], To: states[t], Label: fn.name}
					if !seen[transition] {
						seen[transition] = true
						transitions = append(transitions, transition)
					}
				}
			}
		}
	}
	slices.SortFunc(transitions, func(a, b stateTransition) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		if c := strings.Compare(a.To, b.To); c != 0 {
			return c
		}
		return strings.Compare(a.Label, b.Label)
	})
	return transitions
}

type stateCandidate struct {
	name string
	node *ast.Node // function-like declaration
}

// stateCandidateFunctions collects top-level function declarations and
// function-valued variable declarations.
func stateCandidateFunctions(file *ast.SourceFile) []stateCandidate {
	var candidates []stateCandidate
	for _, statement := range file.Statements.Nodes {
		switch statement.Kind {
		case ast.KindFunctionDeclaration:
			if name := ast.GetDeclarationName(statement); name != "" && statement.Body() != nil {
				candidates = append(candidates, stateCandidate{name: name, node: statement})
			}
		case ast.KindVariableStatement:
			for _, decl := range statement.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
				init := decl.Initializer()
				if init == nil {
					continue
				}
				if init.Kind == ast.KindArrowFunction || init.Kind == ast.KindFunctionExpression {
					if name := ast.GetDeclarationName(decl); name != "" {
						candidates = append(candidates, stateCandidate{name: name, node: init})
					}
				}
			}
		}
	}
	return candidates
}

// ---------------------------------------------------------------------------
// rendering

func renderStateMachine(machine *stateMachine, syntax string, stats map[string]int) (*DiagramResult, error) {
	var text string
	switch syntax {
	case syntaxMermaid:
		text = machine.emitMermaid()
	case syntaxDot:
		text = machine.emitDot()
	case syntaxJSON:
		data, err := json.MarshalIndent(machine, "", "  ")
		if err != nil {
			return nil, err
		}
		text = string(data) + "\n"
	}
	return &DiagramResult{Syntax: syntax, Diagram: text, Stats: stats}, nil
}

func (m *stateMachine) emitMermaid() string {
	alloc := newDiagramIDAllocator()
	var b strings.Builder
	b.WriteString("stateDiagram-v2\n")
	for _, state := range m.States {
		fmt.Fprintf(&b, "  %s: %s\n", alloc.id(state), mermaidLabel(state))
	}
	for _, t := range m.Transitions {
		fmt.Fprintf(&b, "  %s --> %s: %s\n", alloc.id(t.From), alloc.id(t.To), mermaidLabel(t.Label))
	}
	return b.String()
}

func (m *stateMachine) emitDot() string {
	alloc := newDiagramIDAllocator()
	var b strings.Builder
	b.WriteString("digraph states {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=ellipse];\n")
	for _, state := range m.States {
		fmt.Fprintf(&b, "  %s [label=\"%s\"];\n", alloc.id(state), dotLabel(state))
	}
	for _, t := range m.Transitions {
		fmt.Fprintf(&b, "  %s -> %s [label=\"%s\"];\n", alloc.id(t.From), alloc.id(t.To), dotLabel(t.Label))
	}
	b.WriteString("}\n")
	return b.String()
}
