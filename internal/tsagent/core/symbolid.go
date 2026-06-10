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
// (if/for/try/...) are transparent. Quoted segments name ambient modules
// (`relpath#"virtual-thing".vt`). Declarations under anonymous scopes
// (callbacks, IIFEs) and computed-name members fall back to the position
// encoding `relpath@pos`. A `~N` suffix on the final segment selects the
// N-th SOURCE-ORDER declaration when several declarations share the same
// name path: merged declarations (interface+namespace+function), get/set
// accessor pairs, overload signatures, duplicate vars, and same-name
// declarations in sibling scopes. Bare IDs over several declarations are an
// error listing the `~N` alternatives — except function/method/constructor
// overload groups, where the bare ID addresses the whole group.

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
		// Merged non-overload symbols carry the primary declaration's
		// source-order ordinal so the printed ID stays decodable (a bare ID
		// over merged declarations is an ambiguity error); overload groups
		// decode bare as the whole group.
		if decls := orderedDeclsInFile(symbol, file); len(decls) > 1 && !isOverloadGroup(decls) {
			return id + mergedOrdinalSuffix(symbol, file, decl)
		}
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
			return id + mergedOrdinalSuffix(symbol, file, node)
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

// orderedDeclsInFile returns symbol's declarations located in file, in
// source order. This is the declaration list that `~N` ordinals index for
// merged symbols (the binder's Declarations order is symbol-internal, not
// source order).
func orderedDeclsInFile(symbol *ast.Symbol, file *ast.SourceFile) []*ast.Node {
	var decls []*ast.Node
	for _, d := range symbol.Declarations {
		if ast.GetSourceFileOfNode(d) == file {
			decls = append(decls, d)
		}
	}
	slices.SortStableFunc(decls, func(a, b *ast.Node) int { return a.Pos() - b.Pos() })
	return decls
}

// isOverloadGroup reports whether the (same-symbol) declarations form a
// function/method/constructor overload group: two or more signatures plus at
// most one implementation of the same callable. Bare IDs address the whole
// group; merged declarations of mixed kinds (interface+namespace, get/set
// pairs, duplicate vars) are not groups and require a `~N` ordinal.
func isOverloadGroup(decls []*ast.Node) bool {
	if len(decls) < 2 {
		return false
	}
	for _, d := range decls {
		switch d.Kind {
		case ast.KindFunctionDeclaration, ast.KindMethodDeclaration, ast.KindMethodSignature, ast.KindConstructor:
		default:
			return false
		}
	}
	return true
}

// OverloadGroupDecls returns the source-ordered declarations of symbol in
// file when they form a function/method/constructor overload group with more
// than one declaration; nil otherwise. A bare symbol ID (no `~N` ordinal)
// addresses the whole group, so batch editors can expand delete/replace/move
// over every signature.
func OverloadGroupDecls(symbol *ast.Symbol, file *ast.SourceFile) []*ast.Node {
	if symbol == nil || file == nil {
		return nil
	}
	decls := orderedDeclsInFile(symbol, file)
	if isOverloadGroup(decls) {
		return decls
	}
	return nil
}

// mergedOrdinalSuffix returns the `~N` source-order ordinal for node when its
// symbol has several declarations in file (merged declarations, get/set
// pairs, overload signatures, duplicate vars), making every printed ID unique
// and round-trippable. Unique declarations return "" so previously-unique IDs
// stay byte-identical.
func mergedOrdinalSuffix(symbol *ast.Symbol, file *ast.SourceFile, node *ast.Node) string {
	decls := orderedDeclsInFile(symbol, file)
	if len(decls) < 2 {
		return ""
	}
	idx := slices.Index(decls, node)
	if idx < 0 {
		return ""
	}
	return "~" + strconv.Itoa(idx)
}

// encodeViaSymbolParents renders the qualified ID through the symbol parent
// chain (module exports → members). ok is false when the chain does not
// reach the file and the outermost symbol is not a top-level declaration, or
// when a segment cannot be named decodably (computed-name members).
func encodeViaSymbolParents(rel string, symbol *ast.Symbol) (string, bool) {
	var segments []string
	top := symbol
	for cur := symbol; cur != nil; cur = cur.Parent {
		if isFileModuleSymbol(cur) {
			return rel + "#" + strings.Join(segments, "."), true
		}
		if cur.Name == ast.InternalSymbolNameComputed {
			// Computed-name members ([Symbol.iterator]() {…}) have no
			// decodable name segment: keep the position fallback.
			return "", false
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
	baseID := id
	segments := splitIDSegments(id[hash+1:])
	if len(segments) > 0 {
		last := segments[len(segments)-1]
		if tilde := strings.LastIndexByte(last, '~'); tilde >= 0 {
			if n, err := strconv.Atoi(last[tilde+1:]); err == nil {
				declIndex = n
				segments[len(segments)-1] = last[:tilde]
				baseID = id[:strings.LastIndexByte(id, '~')]
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
		decls := orderedDeclsInFile(symbol, file)
		if len(decls) == 0 {
			decls = symbol.Declarations
		}
		if declIndex >= 0 {
			if declIndex >= len(decls) {
				return nil, nil, ordinalRangeError(baseID, declIndex, len(decls))
			}
			return symbol, decls[declIndex], nil
		}
		// A bare ID over several same-file declarations is ambiguous — except
		// overload groups, which the bare ID addresses as a whole (the primary
		// declaration is returned; callers expand via OverloadGroupDecls).
		if len(decls) > 1 && !isOverloadGroup(decls) {
			return nil, nil, ambiguousDeclsError(ws, id, file, decls)
		}
		decl := symbol.ValueDeclaration
		if decl == nil && len(decls) > 0 {
			decl = decls[0]
		}
		return symbol, decl, nil
	}

	// Scope-resolved final segment.
	var decl *ast.Node
	switch {
	case declIndex >= 0:
		if declIndex >= len(matches) {
			return nil, nil, ordinalRangeError(baseID, declIndex, len(matches))
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
	var symbol *ast.Symbol
	if token != nil {
		checker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		defer done()
		symbol = checker.GetSymbolAtLocation(token)
	}
	// Positions within a computed property name ([Symbol.iterator]() {…}) are
	// the fallback IDs the encoder emits for computed-name members: resolve
	// them to the enclosing member declaration.
	if symbol == nil && token != nil {
		for n := token; n != nil && n.Kind != ast.KindSourceFile; n = n.Parent {
			if n.Kind == ast.KindComputedPropertyName {
				if member := n.Parent; member != nil {
					symbol = member.Symbol()
				}
				break
			}
		}
	}
	var decl *ast.Node
	if symbol != nil {
		decl = symbol.ValueDeclaration
		if decl == nil && len(symbol.Declarations) > 0 {
			decl = symbol.Declarations[0]
		}
	}
	// Positions in file-leading trivia resolve to the SourceFile's own symbol
	// — never hand that back as "the declaration" (a delete would wipe the
	// whole file); positions in trivia between declarations and at EOF
	// resolve to nothing.
	if decl == nil || decl.Kind == ast.KindSourceFile {
		return nil, nil, noDeclarationAtError(ws, file, path, pos)
	}
	return symbol, decl, nil
}

// noDeclarationAtError renders the @pos miss error with up to three
// top-level declaration IDs as suggestions.
func noDeclarationAtError(ws *Workspace, file *ast.SourceFile, path string, pos int) error {
	suffix := ""
	if ids := topLevelDeclIDs(ws, file, 3); len(ids) > 0 {
		suffix = "; top-level declarations: " + strings.Join(ids, ", ")
	}
	return BadAddressErrorf("no declaration at position %d in %s%s", pos, path, suffix)
}

// topLevelDeclIDs returns the qualified IDs of the first max top-level
// declarations of file (used as suggestions in @pos miss errors).
func topLevelDeclIDs(ws *Workspace, file *ast.SourceFile, max int) []string {
	var decls []*ast.Node
	for _, s := range file.Statements.Nodes {
		collectScopeDecls(s, &decls)
	}
	ids := make([]string, 0, max)
	for _, d := range decls {
		if id := EncodeDeclID(ws, d); strings.Contains(id, "#") {
			ids = append(ids, id)
			if len(ids) == max {
				break
			}
		}
	}
	return ids
}

// ordinalRangeError renders the out-of-range `~N` error for both decode
// phases: `ordinal ~7 out of range for src/m.ts#x (2 declarations: ~0..~1)`.
func ordinalRangeError(baseID string, declIndex int, count int) error {
	return BadAddressErrorf("ordinal ~%d out of range for %s (%d declarations: ~0..~%d)",
		declIndex, baseID, count, count-1)
}

// ambiguousDeclsError renders the bare-ID-over-merged-declarations error,
// listing every `~N` alternative with its kind and line.
func ambiguousDeclsError(ws *Workspace, id string, file *ast.SourceFile, decls []*ast.Node) error {
	parts := make([]string, len(decls))
	for i, d := range decls {
		line, _ := ws.PosToLineCol(file, astnav.GetStartOfNode(d, file, false /*includeJSDoc*/))
		parts[i] = fmt.Sprintf("~%d %s (line %d)", i, DeclarationKind(d), line)
	}
	return BadAddressErrorf("ambiguous: %s matches %d declarations: %s", id, len(decls), strings.Join(parts, ", "))
}

// splitIDSegments splits the qualified-name part of a symbol ID on '.'
// outside double quotes, so quoted ambient-module segments may contain dots
// (`"pkg/sub.thing".vt` is two segments).
func splitIDSegments(s string) []string {
	if !strings.Contains(s, `"`) {
		return strings.Split(s, ".")
	}
	var segments []string
	start := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '.':
			if !inQuote {
				segments = append(segments, s[start:i])
				start = i + 1
			}
		}
	}
	return append(segments, s[start:])
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
