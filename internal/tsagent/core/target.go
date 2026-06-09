package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/binder"
)

// TargetSpec carries the three addressing forms accepted by symbol-targeting
// commands: --at file:line:col, --symbol id, --name X [--kind k].
type TargetSpec struct {
	At     string
	Symbol string
	Name   string
	Kind   string
}

// Target is a resolved code location.
type Target struct {
	File   *ast.SourceFile
	Node   *ast.Node
	Symbol *ast.Symbol
	Pos    int
}

// ParsePosition splits `file:line:col` (1-based line/col, UTF-8 byte columns).
func ParsePosition(arg string) (file string, line int, col int, err error) {
	rest := arg
	lastColon := strings.LastIndexByte(rest, ':')
	if lastColon < 0 {
		return "", 0, 0, fmt.Errorf("malformed position %q (want file:line:col): %w", arg, ErrInvalidArgument)
	}
	colText := rest[lastColon+1:]
	rest = rest[:lastColon]
	prevColon := strings.LastIndexByte(rest, ':')
	if prevColon < 0 {
		return "", 0, 0, fmt.Errorf("malformed position %q (want file:line:col): %w", arg, ErrInvalidArgument)
	}
	lineText := rest[prevColon+1:]
	file = rest[:prevColon]
	line, lineErr := strconv.Atoi(lineText)
	col, colErr := strconv.Atoi(colText)
	if file == "" || lineErr != nil || colErr != nil {
		return "", 0, 0, fmt.Errorf("malformed position %q (want file:line:col): %w", arg, ErrInvalidArgument)
	}
	return file, line, col, nil
}

// ResolveTarget resolves a TargetSpec to a concrete node/symbol.
func (w *Workspace) ResolveTarget(ctx context.Context, spec TargetSpec) (*Target, error) {
	specified := 0
	for _, s := range []string{spec.At, spec.Symbol, spec.Name} {
		if s != "" {
			specified++
		}
	}
	if specified != 1 {
		return nil, fmt.Errorf("exactly one of --at, --symbol, or --name must be given: %w", ErrInvalidArgument)
	}
	switch {
	case spec.At != "":
		return w.resolveAt(ctx, spec.At)
	case spec.Symbol != "":
		return w.resolveSymbolID(ctx, spec.Symbol)
	default:
		return w.resolveName(ctx, spec.Name, spec.Kind)
	}
}

func (w *Workspace) resolveAt(ctx context.Context, at string) (*Target, error) {
	fileName, line, col, err := ParsePosition(at)
	if err != nil {
		return nil, err
	}
	file, err := w.FileOf(fileName)
	if err != nil {
		return nil, err
	}
	pos, err := w.LineColToPos(file, line, col)
	if err != nil {
		return nil, err
	}
	token := astnav.GetTouchingToken(file, pos)
	if token == nil {
		return nil, fmt.Errorf("no token at %s: %w", at, ErrNotFound)
	}
	checker, done := w.Program.GetTypeCheckerForFile(ctx, file)
	defer done()
	symbol := checker.GetSymbolAtLocation(token)
	return &Target{File: file, Node: token, Symbol: symbol, Pos: pos}, nil
}

func (w *Workspace) resolveSymbolID(ctx context.Context, id string) (*Target, error) {
	symbol, decl, err := DecodeSymbolID(ctx, w, id)
	if err != nil {
		return nil, err
	}
	if decl == nil {
		return nil, fmt.Errorf("symbol %q has no declaration: %w", id, ErrNotFound)
	}
	file := ast.GetSourceFileOfNode(decl)
	return &Target{File: file, Node: decl, Symbol: symbol, Pos: decl.Pos()}, nil
}

func (w *Workspace) resolveName(ctx context.Context, name string, kind string) (*Target, error) {
	var matches []*ast.Node
	for _, file := range w.Program.SourceFiles() {
		if w.Program.IsLibFile(file) || strings.Contains(file.FileName(), "/node_modules/") {
			continue
		}
		for _, decl := range file.GetDeclarationMap()[name] {
			if kind != "" && !KindMatchesFilter(DeclarationKind(decl), map[string]bool{kind: true}) {
				continue
			}
			matches = append(matches, decl)
		}
	}
	if len(matches) == 0 {
		if kind != "" {
			return nil, fmt.Errorf("no %s named %q: %w", kind, name, ErrNotFound)
		}
		return nil, fmt.Errorf("no symbol named %q: %w", name, ErrNotFound)
	}
	if len(matches) > 1 {
		descriptions := make([]string, 0, min(len(matches), 5))
		for _, decl := range matches[:min(len(matches), 5)] {
			file := ast.GetSourceFileOfNode(decl)
			line, col := w.PosToLineCol(file, astnav.GetStartOfNode(decl, file, false /*includeJSDoc*/))
			descriptions = append(descriptions, fmt.Sprintf("%s %s:%d:%d", DeclarationKind(decl), w.RelPath(file.FileName()), line, col))
		}
		return nil, fmt.Errorf("name %q is ambiguous (%d matches: %s): %w", name, len(matches), strings.Join(descriptions, ", "), ErrInvalidArgument)
	}
	decl := matches[0]
	file := ast.GetSourceFileOfNode(decl)
	binder.BindSourceFile(file)
	symbol := decl.Symbol()
	if symbol == nil {
		checker, done := w.Program.GetTypeCheckerForFile(ctx, file)
		defer done()
		if nameNode := ast.GetNameOfDeclaration(decl); nameNode != nil {
			symbol = checker.GetSymbolAtLocation(nameNode)
		}
	}
	return &Target{File: file, Node: decl, Symbol: symbol, Pos: decl.Pos()}, nil
}
