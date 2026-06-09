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

// Symbol IDs are stable handles of the form `relpath#seg1.seg2` where the
// segments are declaration names from the module root (module exports →
// members). Locals that are not reachable by qualified name fall back to the
// position encoding `relpath@pos`. A `~N` suffix on the final segment selects
// the N-th declaration of merged/overloaded symbols.

// EncodeSymbolID renders a stable ID for a symbol, or "" when the symbol has
// no declarations (e.g. some synthesized symbols).
func EncodeSymbolID(ws *Workspace, symbol *ast.Symbol) string {
	if symbol == nil {
		return ""
	}
	decl := symbol.ValueDeclaration
	if decl == nil && len(symbol.Declarations) > 0 {
		decl = symbol.Declarations[0]
	}
	if decl == nil {
		return ""
	}
	file := ast.GetSourceFileOfNode(decl)
	if file == nil {
		return ""
	}
	rel := ws.RelPath(file.FileName())

	var segments []string
	top := symbol
	for cur := symbol; cur != nil; cur = cur.Parent {
		if isFileModuleSymbol(cur) {
			return rel + "#" + strings.Join(segments, ".")
		}
		segments = append([]string{encodeSegment(cur.Name)}, segments...)
		top = cur
	}

	// The parent chain did not reach the file's module symbol (non-exported
	// top-level declarations are not parented to it, and script files have
	// no module symbol). When the outermost symbol in the chain is declared
	// at the top level, the qualified form still decodes through file locals.
	topDecl := top.ValueDeclaration
	if topDecl == nil && len(top.Declarations) > 0 {
		topDecl = top.Declarations[0]
	}
	if topDecl != nil && topDecl.Parent != nil && (topDecl.Parent.Kind == ast.KindSourceFile ||
		(topDecl.Parent.Kind == ast.KindVariableDeclarationList && isTopLevelVariableDeclaration(topDecl))) {
		return rel + "#" + strings.Join(segments, ".")
	}

	// Position fallback for true locals.
	pos := decl.Pos()
	if name := ast.GetNameOfDeclaration(decl); name != nil {
		pos = astnav.GetStartOfNode(name, file, false /*includeJSDoc*/)
	}
	return fmt.Sprintf("%s@%d", rel, pos)
}

// encodeSegment renders internal binder names (prefixed with the invalid
// UTF-8 byte ast.InternalSymbolNamePrefix, e.g. constructors) in a readable,
// decodable form like "@constructor".
func encodeSegment(name string) string {
	if rest, ok := strings.CutPrefix(name, ast.InternalSymbolNamePrefix); ok {
		return "@" + rest
	}
	return name
}

// decodeSegment is the inverse of encodeSegment.
func decodeSegment(segment string) string {
	if rest, ok := strings.CutPrefix(segment, "@"); ok {
		return ast.InternalSymbolNamePrefix + rest
	}
	return segment
}

func isTopLevelVariableDeclaration(decl *ast.Node) bool {
	list := decl.Parent
	return list != nil && list.Parent != nil && list.Parent.Kind == ast.KindVariableStatement &&
		list.Parent.Parent != nil && list.Parent.Parent.Kind == ast.KindSourceFile
}

func isFileModuleSymbol(symbol *ast.Symbol) bool {
	for _, decl := range symbol.Declarations {
		if decl.Kind == ast.KindSourceFile {
			return true
		}
	}
	return false
}

// DecodeSymbolID resolves a symbol ID back to its symbol and primary
// declaration node.
func DecodeSymbolID(ctx context.Context, ws *Workspace, id string) (*ast.Symbol, *ast.Node, error) {
	if at := strings.LastIndexByte(id, '@'); at >= 0 && !strings.Contains(id, "#") {
		return decodePositionID(ctx, ws, id[:at], id[at+1:])
	}
	hash := strings.IndexByte(id, '#')
	if hash < 0 {
		return nil, nil, fmt.Errorf("malformed symbol id %q (want path#qualified.name or path@pos): %w", id, ErrInvalidArgument)
	}
	file, err := ws.FileOf(id[:hash])
	if err != nil {
		return nil, nil, err
	}
	binder.BindSourceFile(file)

	declIndex := -1
	segments := strings.Split(id[hash+1:], ".")
	if len(segments) > 0 {
		last := segments[len(segments)-1]
		if tilde := strings.LastIndexByte(last, '~'); tilde >= 0 {
			if n, err := strconv.Atoi(last[tilde+1:]); err == nil {
				declIndex = n
				segments[len(segments)-1] = last[:tilde]
			}
		}
	}

	symbol := resolveQualifiedName(file, segments)
	if symbol == nil {
		return nil, nil, fmt.Errorf("symbol %q: %w", id, ErrNotFound)
	}
	decl := symbol.ValueDeclaration
	if declIndex >= 0 && declIndex < len(symbol.Declarations) {
		decl = symbol.Declarations[declIndex]
	}
	if decl == nil && len(symbol.Declarations) > 0 {
		decl = symbol.Declarations[0]
	}
	return symbol, decl, nil
}

func decodePositionID(ctx context.Context, ws *Workspace, path string, posText string) (*ast.Symbol, *ast.Node, error) {
	pos, err := strconv.Atoi(posText)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed symbol id position %q: %w", posText, ErrInvalidArgument)
	}
	file, err := ws.FileOf(path)
	if err != nil {
		return nil, nil, err
	}
	if pos < 0 || pos > len(file.Text()) {
		return nil, nil, fmt.Errorf("position %d out of range for %s: %w", pos, path, ErrInvalidArgument)
	}
	token := astnav.GetTouchingToken(file, pos)
	if token == nil {
		return nil, nil, fmt.Errorf("no token at %s@%d: %w", path, pos, ErrNotFound)
	}
	checker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()
	symbol := checker.GetSymbolAtLocation(token)
	if symbol == nil {
		return nil, nil, fmt.Errorf("no symbol at %s@%d: %w", path, pos, ErrNotFound)
	}
	decl := symbol.ValueDeclaration
	if decl == nil && len(symbol.Declarations) > 0 {
		decl = symbol.Declarations[0]
	}
	return symbol, decl, nil
}

func resolveQualifiedName(file *ast.SourceFile, segments []string) *ast.Symbol {
	var cur *ast.Symbol
	for i, seg := range segments {
		if seg == "" {
			return nil
		}
		seg = decodeSegment(seg)
		var next *ast.Symbol
		if i == 0 {
			if moduleSymbol := file.AsNode().Symbol(); moduleSymbol != nil {
				next = lookupMember(moduleSymbol, seg)
			}
			if next == nil {
				if locals := file.AsNode().Locals(); locals != nil {
					next = locals[seg]
				}
			}
		} else {
			next = lookupMember(cur, seg)
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

func lookupMember(symbol *ast.Symbol, name string) *ast.Symbol {
	if symbol.Exports != nil {
		if s, ok := symbol.Exports[name]; ok {
			return s
		}
	}
	if symbol.Members != nil {
		if s, ok := symbol.Members[name]; ok {
			return s
		}
	}
	return nil
}
