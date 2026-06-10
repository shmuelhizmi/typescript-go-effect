package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/binder"
)

// Symbol IDs are stable handles of the form `relpath#seg1.seg2` where the
// segments are declaration names from the module root: module exports →
// members, extended through every named scope (functions, methods,
// namespaces, and `const f = () => {}`-style declarators). Blocks
// (if/for/try/...) are transparent. Declarations under anonymous scopes
// (callbacks, IIFEs) fall back to the position encoding `relpath@pos`. A
// `~N` suffix on the final segment selects the N-th declaration of
// merged/overloaded symbols (symbol-table resolution) or the N-th
// source-order match when sibling scopes declare the same name.

// EncodeSymbolID renders a stable ID for a symbol, or "" when the symbol has
// no declarations (e.g. some synthesized symbols). Symbols whose primary
// declaration is not reachable by qualified name use the position fallback;
// use EncodeDeclID to get scope-qualified IDs for locals.
func EncodeSymbolID(ws *Workspace, symbol *ast.Symbol) string {
	if symbol == nil {
		return ""
	}
	decl := primaryDeclaration(symbol)
	if decl == nil {
		return ""
	}
	file := ast.GetSourceFileOfNode(decl)
	if file == nil {
		return ""
	}
	rel := ws.RelPath(file.FileName())
	if id, ok := encodeViaSymbolParents(rel, symbol); ok {
		return id
	}
	return positionFallbackID(rel, file, decl)
}

// EncodeDeclID renders a stable ID for a declaration node. It is
// byte-identical to EncodeSymbolID for every declaration reachable through
// the symbol-parent walk (top-level declarations, class/interface/enum
// members, exported namespace members); declarations nested in named scopes
// additionally get qualified `path#scope1.scope2.name` IDs, with a `~N`
// ordinal only when several declarations share the same name path. Anything
// under an anonymous function-like keeps the `path@pos` fallback.
func EncodeDeclID(ws *Workspace, node *ast.Node) string {
	if node == nil {
		return ""
	}
	file := ast.GetSourceFileOfNode(node)
	if file == nil {
		return ""
	}
	binder.BindSourceFile(file)
	rel := ws.RelPath(file.FileName())
	if symbol := node.Symbol(); symbol != nil {
		if id, ok := encodeViaSymbolParents(rel, symbol); ok {
			return id
		}
	}
	if segments, ok := scopeChainSegments(node); ok {
		if id, ok := verifyScopeChainID(file, rel, segments, node); ok {
			return id
		}
	}
	return positionFallbackID(rel, file, node)
}

// primaryDeclaration picks the declaration node DecodeSymbolID returns by
// default for a symbol.
func primaryDeclaration(symbol *ast.Symbol) *ast.Node {
	if symbol.ValueDeclaration != nil {
		return symbol.ValueDeclaration
	}
	if len(symbol.Declarations) > 0 {
		return symbol.Declarations[0]
	}
	return nil
}

// encodeViaSymbolParents renders the qualified ID through the symbol parent
// chain (module exports → members). ok is false when the chain does not
// reach the file and the outermost symbol is not a top-level declaration.
func encodeViaSymbolParents(rel string, symbol *ast.Symbol) (string, bool) {
	var segments []string
	top := symbol
	for cur := symbol; cur != nil; cur = cur.Parent {
		if isFileModuleSymbol(cur) {
			return rel + "#" + strings.Join(segments, "."), true
		}
		segments = append([]string{encodeSegment(cur.Name)}, segments...)
		top = cur
	}

	// The parent chain did not reach the file's module symbol (non-exported
	// top-level declarations are not parented to it, and script files have
	// no module symbol). When the outermost symbol in the chain is declared
	// at the top level, the qualified form still decodes through file locals.
	topDecl := primaryDeclaration(top)
	if topDecl != nil && topDecl.Parent != nil && (topDecl.Parent.Kind == ast.KindSourceFile ||
		(topDecl.Parent.Kind == ast.KindVariableDeclarationList && isTopLevelVariableDeclaration(topDecl))) {
		return rel + "#" + strings.Join(segments, "."), true
	}
	return "", false
}

// verifyScopeChainID resolves a candidate segment chain exactly the way
// DecodeSymbolID does and renders the ID only when it round-trips back to
// node, appending a `~N` source-order ordinal when sibling declarations
// share the path.
func verifyScopeChainID(file *ast.SourceFile, rel string, segments []string, node *ast.Node) (string, bool) {
	symbol, matches, err := resolveChain(file, segments)
	if err != nil {
		return "", false
	}
	if symbol != nil {
		// The full chain resolved through symbol tables. Only usable when
		// decode would hand back this very node.
		if primaryDeclaration(symbol) == node {
			return rel + "#" + strings.Join(segments, "."), true
		}
		return "", false
	}
	idx := slices.Index(matches, node)
	if idx < 0 {
		return "", false
	}
	id := rel + "#" + strings.Join(segments, ".")
	if len(matches) > 1 {
		id += "~" + strconv.Itoa(idx)
	}
	return id, true
}

// positionFallbackID renders the `relpath@pos` fallback for a declaration.
func positionFallbackID(rel string, file *ast.SourceFile, decl *ast.Node) string {
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
// declaration node. Qualified names resolve through symbol tables first
// (module exports → members, then file locals); segments the tables cannot
// resolve continue from the deepest resolved declaration through named-scope
// declarations (ScopeDeclarations). A bare name matching several sibling
// scope declarations is an error listing the `name~N` candidates.
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

	symbol, matches, err := resolveChain(file, segments)
	if err != nil {
		if errors.Is(err, ErrInvalidArgument) {
			return nil, nil, fmt.Errorf("symbol %q: %w", id, err)
		}
		return nil, nil, fmt.Errorf("symbol %q: %w", id, ErrNotFound)
	}
	if symbol != nil {
		decl := symbol.ValueDeclaration
		if declIndex >= 0 && declIndex < len(symbol.Declarations) {
			decl = symbol.Declarations[declIndex]
		}
		if decl == nil && len(symbol.Declarations) > 0 {
			decl = symbol.Declarations[0]
		}
		return symbol, decl, nil
	}

	// Scope-resolved final segment.
	var decl *ast.Node
	switch {
	case declIndex >= 0:
		if declIndex >= len(matches) {
			return nil, nil, fmt.Errorf("symbol %q: ordinal ~%d out of range (%d declarations): %w", id, declIndex, len(matches), ErrNotFound)
		}
		decl = matches[declIndex]
	case len(matches) > 1:
		name := segments[len(segments)-1]
		candidates := make([]string, len(matches))
		for i, m := range matches {
			line, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(m, file, false /*includeJSDoc*/))
			candidates[i] = fmt.Sprintf("%s~%d (line %d)", name, i, line)
		}
		return nil, nil, fmt.Errorf("symbol %q is ambiguous (%d declarations: %s): %w",
			id, len(matches), strings.Join(candidates, ", "), ErrInvalidArgument)
	default:
		decl = matches[0]
	}
	declSymbol := decl.Symbol()
	if declSymbol == nil {
		checker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		defer done()
		if name := ast.GetNameOfDeclaration(decl); name != nil {
			declSymbol = checker.GetSymbolAtLocation(name)
		}
	}
	return declSymbol, decl, nil
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

// resolveChain resolves dot segments against a bound file: symbol tables
// first (module exports and file locals at the root, exports/members below),
// then named-scope declarations from the deepest resolved declaration. On
// success exactly one of the results is set: a symbol when the final segment
// resolved through tables, or the source-ordered candidate declarations when
// it resolved through scope matching. Errors are ErrNotFound for misses and
// ErrInvalidArgument for ambiguous intermediate segments.
func resolveChain(file *ast.SourceFile, segments []string) (*ast.Symbol, []*ast.Node, error) {
	var curSymbol *ast.Symbol // set when the previous segment was table-resolved
	var curDecls []*ast.Node  // scope-search roots for the next segment
	for i, seg := range segments {
		if seg == "" {
			return nil, nil, fmt.Errorf("empty name segment: %w", ErrInvalidArgument)
		}
		name := decodeSegment(seg)

		var next *ast.Symbol
		if i == 0 {
			if moduleSymbol := file.AsNode().Symbol(); moduleSymbol != nil {
				next = lookupMember(moduleSymbol, name)
			}
			if next == nil {
				if locals := file.AsNode().Locals(); locals != nil {
					next = locals[name]
				}
			}
		} else if curSymbol != nil {
			next = lookupMember(curSymbol, name)
		}
		if next != nil {
			curSymbol = next
			curDecls = next.Declarations
			if i == len(segments)-1 {
				return next, nil, nil
			}
			continue
		}

		// Scope fallback from the deepest resolved declaration(s).
		var matches []*ast.Node
		for _, root := range curDecls {
			for _, d := range ScopeDeclarations(root) {
				if scopeDeclarationName(d) == name {
					matches = append(matches, d)
				}
			}
		}
		if len(matches) == 0 {
			return nil, nil, fmt.Errorf("segment %q: %w", seg, ErrNotFound)
		}
		if i == len(segments)-1 {
			return nil, matches, nil
		}
		if len(matches) > 1 {
			return nil, nil, fmt.Errorf("segment %q is ambiguous (%d sibling declarations): %w", seg, len(matches), ErrInvalidArgument)
		}
		curSymbol = matches[0].Symbol()
		curDecls = matches[:1]
	}
	return nil, nil, fmt.Errorf("empty qualified name: %w", ErrInvalidArgument)
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

// FuzzyMatchScore matches s against pattern: every pattern rune must appear
// in order; upper-case pattern runes match exactly. Returns the number of
// skipped runes, or -1 when s does not match.
func FuzzyMatchScore(s string, pattern string) int {
	score := 0
	for _, p := range pattern {
		exact := unicode.IsUpper(p)
		for {
			c, size := utf8.DecodeRuneInString(s)
			if size == 0 {
				return -1
			}
			s = s[size:]
			if exact && c == p || !exact && unicode.ToLower(c) == unicode.ToLower(p) {
				break
			}
			score++
		}
	}
	return score
}

// SuggestSymbolIDs returns up to max qualified symbol IDs declared in file,
// ranked by fuzzy similarity to the qualified-name part of badQualifiedPath
// (the text after '#', or the whole string when there is no '#'). Used for
// "unknown symbol ...; closest: ..." error messages.
func SuggestSymbolIDs(ws *Workspace, file *ast.SourceFile, badQualifiedPath string, max int) []string {
	if max <= 0 {
		return nil
	}
	binder.BindSourceFile(file)
	pattern := badQualifiedPath
	if hash := strings.IndexByte(pattern, '#'); hash >= 0 {
		pattern = pattern[hash+1:]
	}
	if tilde := strings.LastIndexByte(pattern, '~'); tilde >= 0 {
		if _, err := strconv.Atoi(pattern[tilde+1:]); err == nil {
			pattern = pattern[:tilde]
		}
	}
	type scored struct {
		id    string
		score int
	}
	var ranked []scored
	seen := make(map[string]bool)
	for _, decl := range allFileDeclarations(file) {
		id := EncodeDeclID(ws, decl)
		hash := strings.IndexByte(id, '#')
		if hash < 0 || seen[id] {
			continue
		}
		seen[id] = true
		score := bidiFuzzyScore(id[hash+1:], pattern)
		if score < 0 {
			continue
		}
		ranked = append(ranked, scored{id, score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score < ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})
	ids := make([]string, 0, min(max, len(ranked)))
	for _, r := range ranked[:min(max, len(ranked))] {
		ids = append(ids, r.id)
	}
	return ids
}

// bidiFuzzyScore scores similarity in both directions so that both missing
// and extra characters in the query still match, plus a length-difference
// penalty so that near-equal-length candidates outrank short prefixes;
// -1 means no match either way.
func bidiFuzzyScore(s string, pattern string) int {
	a := FuzzyMatchScore(s, pattern)
	b := FuzzyMatchScore(pattern, s)
	var best int
	switch {
	case a < 0 && b < 0:
		return -1
	case a < 0:
		best = b
	case b < 0:
		best = a
	default:
		best = min(a, b)
	}
	lenDiff := utf8.RuneCountInString(s) - utf8.RuneCountInString(pattern)
	if lenDiff < 0 {
		lenDiff = -lenDiff
	}
	return best + lenDiff
}
