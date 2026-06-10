package cmds

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// diagram_flow.go implements `diagram flow <target>` (spec §4.6): the
// control-flow graph of one function, built structurally from the AST (not
// the checker's flow nodes). v1 limitations: switch cases are modeled without
// fallthrough (every case merges after the switch), exceptions are modeled as
// a single try-entry → catch edge, and labeled break/continue target the
// innermost construct.

const flowLabelMaxLen = 40

func init() {
	cli.Register(cli.Command{
		Family:       "diagram",
		Name:         "flow",
		Summary:      "Control-flow graph of one function (structural CFG)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &diagramFlowFlags{}
			fs.StringVar(&f.out, "out", "mermaid", "diagram syntax: mermaid, dot, or json")
			fs.StringVar(&f.symbol, "symbol", "", "target symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "target declaration name (must be unambiguous)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runDiagramFlow(ctx, ws, flags.(*diagramFlowFlags), args)
		},
	})
}

type diagramFlowFlags struct {
	out    string
	symbol string
	name   string
}

func runDiagramFlow(ctx context.Context, ws *core.Workspace, flags *diagramFlowFlags, args []string) (*DiagramResult, error) {
	syntax, err := parseDiagramSyntax(flags.out)
	if err != nil {
		return nil, err
	}
	fn, file, err := resolveFlowFunction(ctx, ws, flags, args)
	if err != nil {
		return nil, err
	}

	b := &cfgBuilder{
		ws:    ws,
		file:  file,
		alloc: newDiagramIDAllocator(),
		graph: &flowGraph{Direction: "TD"},
	}
	name := ast.GetDeclarationName(fn)
	if name == "" {
		name = "(anonymous)"
	}
	start := b.newNode(fmt.Sprintf("START %s", name))
	start.Class = "root"
	b.end = b.newNode("END")

	body := fn.Body()
	var entry *flowNode
	var exits []cfgExit
	switch {
	case body == nil:
		return nil, cli.NotFoundErrorf("target function has no body")
	case body.Kind == ast.KindBlock:
		entry, exits = b.buildStatements(body.AsBlock().Statements.Nodes)
	default:
		// Arrow function with an expression body.
		entry = b.statementNode(body)
		exits = []cfgExit{{node: entry}}
	}
	if entry == nil {
		entry = b.end
	}
	b.edge(start, entry, "")
	for _, exit := range exits {
		b.edge(exit.node, b.end, exit.label)
	}

	stats := map[string]int{"nodes": len(b.graph.Nodes), "edges": len(b.graph.Edges)}
	return renderFlowDiagram(b.graph, syntax, stats)
}

// resolveFlowFunction resolves the target to a function-like declaration with
// a body (walking up from position targets).
func resolveFlowFunction(ctx context.Context, ws *core.Workspace, flags *diagramFlowFlags, args []string) (*ast.Node, *ast.SourceFile, error) {
	spec := core.TargetSpec{Symbol: flags.symbol, Name: flags.name}
	if spec.Symbol == "" && spec.Name == "" {
		if len(args) != 1 {
			return nil, nil, cli.UsageErrorf("diagram flow takes exactly one target (file:line:col, symbol id, or name; or --symbol/--name)")
		}
		spec = guessRefactorTargetSpec(args[0])
	} else if len(args) > 0 {
		return nil, nil, cli.UsageErrorf("positional target and --symbol/--name are mutually exclusive")
	}
	target, err := ws.ResolveTarget(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	node := target.Node
	if target.Symbol != nil {
		for _, decl := range target.Symbol.Declarations {
			if isFlowFunction(decl) {
				return decl, ast.GetSourceFileOfNode(decl), nil
			}
			// `const f = () => …`: descend into the initializer.
			if decl.Kind == ast.KindVariableDeclaration {
				if init := decl.Initializer(); init != nil && isFlowFunction(init) {
					return init, ast.GetSourceFileOfNode(init), nil
				}
			}
		}
	}
	if fn := ast.FindAncestor(node, isFlowFunction); fn != nil {
		return fn, ast.GetSourceFileOfNode(fn), nil
	}
	return nil, nil, cli.NotFoundErrorf("target does not resolve to a function with a body")
}

func isFlowFunction(node *ast.Node) bool {
	return ast.IsFunctionLikeDeclaration(node) && node.Body() != nil
}

// ---------------------------------------------------------------------------
// CFG builder

// cfgExit is a dangling edge awaiting its destination (the next statement's
// entry, or END).
type cfgExit struct {
	node  *flowNode
	label string
}

// cfgLoop tracks the innermost enclosing breakable/continuable construct.
type cfgLoop struct {
	continueTarget *flowNode  // nil for switch
	breaks         *[]cfgExit // break statements land here
}

type cfgBuilder struct {
	ws    *core.Workspace
	file  *ast.SourceFile
	alloc *diagramIDAllocator
	graph *flowGraph
	end   *flowNode
	seq   int
	loops []*cfgLoop
}

func (b *cfgBuilder) newNode(label string) *flowNode {
	b.seq++
	node := &flowNode{ID: b.alloc.id(fmt.Sprintf("n%d", b.seq)), Label: label}
	b.graph.Nodes = append(b.graph.Nodes, node)
	return node
}

// statementNode makes a node labeled with the statement's line and trimmed
// source text.
func (b *cfgBuilder) statementNode(node *ast.Node) *flowNode {
	return b.newNode(b.nodeLabel(node, refactorNodeText(b.file, node)))
}

func (b *cfgBuilder) nodeLabel(node *ast.Node, text string) string {
	line, _ := b.ws.PosToLineCol(b.file, astnav.GetStartOfNode(node, b.file, false /*includeJSDoc*/))
	text = strings.Join(strings.Fields(text), " ") // collapse newlines/indentation
	return fmt.Sprintf("L%d: %s", line, cli.Truncate(text, flowLabelMaxLen))
}

func (b *cfgBuilder) edge(from *flowNode, to *flowNode, label string) {
	b.graph.Edges = append(b.graph.Edges, &flowEdge{From: from.ID, To: to.ID, Label: label})
}

// connect wires every pending exit to the entry node.
func (b *cfgBuilder) connect(exits []cfgExit, entry *flowNode) {
	for _, exit := range exits {
		b.edge(exit.node, entry, exit.label)
	}
}

// buildStatements builds a statement sequence: returns the entry node (nil
// when the sequence contributes no flow nodes) and the dangling exits.
func (b *cfgBuilder) buildStatements(statements []*ast.Node) (*flowNode, []cfgExit) {
	var entry *flowNode
	var pending []cfgExit
	started := false
	for _, statement := range statements {
		stmtEntry, stmtExits := b.buildStatement(statement)
		if stmtEntry == nil {
			continue // contributes no flow (e.g. nested declarations, empty blocks)
		}
		if !started {
			entry = stmtEntry
			started = true
		} else {
			b.connect(pending, stmtEntry)
		}
		pending = stmtExits
	}
	if !started {
		return nil, nil
	}
	return entry, pending
}

// buildStatement builds one statement, returning its entry node and exits.
// A nil entry means the statement is transparent (no control flow of its
// own). Statements that never fall through (return/throw/break/continue)
// return an entry with no exits.
func (b *cfgBuilder) buildStatement(statement *ast.Node) (*flowNode, []cfgExit) {
	switch statement.Kind {
	case ast.KindBlock:
		return b.buildStatements(statement.AsBlock().Statements.Nodes)

	case ast.KindIfStatement:
		ifStmt := statement.AsIfStatement()
		cond := b.newNode(b.nodeLabel(statement, "if ("+refactorNodeText(b.file, ifStmt.Expression)+")"))
		var exits []cfgExit
		thenEntry, thenExits := b.buildStatement(ifStmt.ThenStatement)
		if thenEntry != nil {
			b.edge(cond, thenEntry, "then")
			exits = append(exits, thenExits...)
		} else {
			exits = append(exits, cfgExit{node: cond, label: "then"})
		}
		if ifStmt.ElseStatement != nil {
			elseEntry, elseExits := b.buildStatement(ifStmt.ElseStatement)
			if elseEntry != nil {
				b.edge(cond, elseEntry, "else")
				exits = append(exits, elseExits...)
			} else {
				exits = append(exits, cfgExit{node: cond, label: "else"})
			}
		} else {
			exits = append(exits, cfgExit{node: cond, label: "else"})
		}
		return cond, exits

	case ast.KindWhileStatement:
		whileStmt := statement.AsWhileStatement()
		cond := b.newNode(b.nodeLabel(statement, "while ("+refactorNodeText(b.file, whileStmt.Expression)+")"))
		exits := b.buildLoopBody(cond, cond, whileStmt.Statement, "true")
		return cond, append(exits, cfgExit{node: cond, label: "false"})

	case ast.KindDoStatement:
		doStmt := statement.AsDoStatement()
		cond := b.newNode(b.nodeLabel(statement, "do … while ("+refactorNodeText(b.file, doStmt.Expression)+")"))
		var breaks []cfgExit
		b.loops = append(b.loops, &cfgLoop{continueTarget: cond, breaks: &breaks})
		bodyEntry, bodyExits := b.buildStatement(doStmt.Statement)
		b.loops = b.loops[:len(b.loops)-1]
		entry := cond
		if bodyEntry != nil {
			entry = bodyEntry
			b.connect(bodyExits, cond)
			b.edge(cond, bodyEntry, "true")
		}
		return entry, append(breaks, cfgExit{node: cond, label: "false"})

	case ast.KindForStatement:
		forStmt := statement.AsForStatement()
		header := "for (…)"
		if forStmt.Condition != nil {
			header = "for (…; " + refactorNodeText(b.file, forStmt.Condition) + "; …)"
		}
		cond := b.newNode(b.nodeLabel(statement, header))
		exits := b.buildLoopBody(cond, cond, forStmt.Statement, "loop")
		return cond, append(exits, cfgExit{node: cond, label: "exit"})

	case ast.KindForInStatement, ast.KindForOfStatement:
		forStmt := statement.AsForInOrOfStatement()
		keyword := "for…in"
		if statement.Kind == ast.KindForOfStatement {
			keyword = "for…of"
		}
		cond := b.newNode(b.nodeLabel(statement, keyword+" ("+refactorNodeText(b.file, forStmt.Expression)+")"))
		exits := b.buildLoopBody(cond, cond, forStmt.Statement, "next")
		return cond, append(exits, cfgExit{node: cond, label: "done"})

	case ast.KindSwitchStatement:
		return b.buildSwitch(statement)

	case ast.KindReturnStatement, ast.KindThrowStatement:
		node := b.statementNode(statement)
		b.edge(node, b.end, "")
		return node, nil

	case ast.KindBreakStatement:
		node := b.statementNode(statement)
		if loop := b.innermostBreakable(); loop != nil {
			*loop.breaks = append(*loop.breaks, cfgExit{node: node})
		} else {
			b.edge(node, b.end, "")
		}
		return node, nil

	case ast.KindContinueStatement:
		node := b.statementNode(statement)
		if loop := b.innermostLoop(); loop != nil {
			b.edge(node, loop.continueTarget, "continue")
		} else {
			b.edge(node, b.end, "")
		}
		return node, nil

	case ast.KindTryStatement:
		return b.buildTry(statement)

	case ast.KindLabeledStatement:
		return b.buildStatement(statement.AsLabeledStatement().Statement)

	case ast.KindEmptyStatement, ast.KindFunctionDeclaration, ast.KindClassDeclaration,
		ast.KindInterfaceDeclaration, ast.KindTypeAliasDeclaration:
		return nil, nil // no control flow at this level

	default:
		node := b.statementNode(statement)
		return node, []cfgExit{{node: node}}
	}
}

// buildLoopBody builds a loop body hanging off cond: body exits loop back to
// continueTarget, breaks become loop exits.
func (b *cfgBuilder) buildLoopBody(cond *flowNode, continueTarget *flowNode, body *ast.Node, enterLabel string) []cfgExit {
	var breaks []cfgExit
	b.loops = append(b.loops, &cfgLoop{continueTarget: continueTarget, breaks: &breaks})
	bodyEntry, bodyExits := b.buildStatement(body)
	b.loops = b.loops[:len(b.loops)-1]
	if bodyEntry != nil {
		b.edge(cond, bodyEntry, enterLabel)
		for _, exit := range bodyExits {
			b.edge(exit.node, continueTarget, exit.label)
		}
	}
	return breaks
}

// buildSwitch fans out one edge per case clause; every clause merges after
// the switch (v1: no fallthrough modeling). break inside the switch also
// lands on the merge point.
func (b *cfgBuilder) buildSwitch(statement *ast.Node) (*flowNode, []cfgExit) {
	switchStmt := statement.AsSwitchStatement()
	sw := b.newNode(b.nodeLabel(statement, "switch ("+refactorNodeText(b.file, switchStmt.Expression)+")"))
	var exits []cfgExit
	var breaks []cfgExit
	b.loops = append(b.loops, &cfgLoop{breaks: &breaks})
	hasDefault := false
	for _, clause := range switchStmt.CaseBlock.AsCaseBlock().Clauses.Nodes {
		label := "default"
		if clause.Kind == ast.KindCaseClause {
			label = "case " + cli.Truncate(refactorNodeText(b.file, clause.Expression()), flowLabelMaxLen)
		} else {
			hasDefault = true
		}
		clauseEntry, clauseExits := b.buildStatements(clause.AsCaseOrDefaultClause().Statements.Nodes)
		if clauseEntry != nil {
			b.edge(sw, clauseEntry, label)
			exits = append(exits, clauseExits...)
		} else {
			exits = append(exits, cfgExit{node: sw, label: label})
		}
	}
	b.loops = b.loops[:len(b.loops)-1]
	exits = append(exits, breaks...)
	if !hasDefault {
		exits = append(exits, cfgExit{node: sw, label: "no match"})
	}
	return sw, exits
}

// buildTry models try/catch/finally: a try → catch "exception" edge from the
// try entry (v1 approximation), with both arms merging through finally.
func (b *cfgBuilder) buildTry(statement *ast.Node) (*flowNode, []cfgExit) {
	tryStmt := statement.AsTryStatement()
	tryEntry, tryExits := b.buildStatement(tryStmt.TryBlock)
	if tryEntry == nil {
		tryEntry = b.newNode(b.nodeLabel(statement, "try {}"))
		tryExits = []cfgExit{{node: tryEntry}}
	}
	exits := tryExits
	if tryStmt.CatchClause != nil {
		catchEntry, catchExits := b.buildStatement(tryStmt.CatchClause.AsCatchClause().Block)
		if catchEntry == nil {
			catchEntry = b.newNode(b.nodeLabel(tryStmt.CatchClause, "catch {}"))
			catchExits = []cfgExit{{node: catchEntry}}
		}
		b.edge(tryEntry, catchEntry, "exception")
		exits = append(exits, catchExits...)
	}
	if tryStmt.FinallyBlock != nil {
		finallyEntry, finallyExits := b.buildStatement(tryStmt.FinallyBlock)
		if finallyEntry != nil {
			b.connect(exits, finallyEntry)
			exits = finallyExits
		}
	}
	return tryEntry, exits
}

func (b *cfgBuilder) innermostLoop() *cfgLoop {
	for i := len(b.loops) - 1; i >= 0; i-- {
		if b.loops[i].continueTarget != nil {
			return b.loops[i]
		}
	}
	return nil
}

func (b *cfgBuilder) innermostBreakable() *cfgLoop {
	if len(b.loops) == 0 {
		return nil
	}
	return b.loops[len(b.loops)-1]
}
