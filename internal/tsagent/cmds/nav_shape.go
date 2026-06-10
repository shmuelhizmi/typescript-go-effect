package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

func init() {
	cli.Register(cli.Command{
		Family: "nav",
		Name:   "shape",
		Summary: "Find functions matching a signature pattern: '(<type>, …) => <type>'; " +
			"'*' is a wildcard, trailing ', ...' allows extra params",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &shapeFlags{}
			fs.StringVar(&f.kind, "kind", "", "comma-separated declaration filter: function, method, arrow (default all)")
			fs.BoolVar(&f.exportedOnly, "exported-only", false, "only exported functions (or members of exported classes/interfaces)")
			fs.StringVar(&f.pathGlob, "path-glob", "", "restrict to files matching this glob (*, **, ?)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runNavShape(ctx, ws, flags.(*shapeFlags), args)
		},
	})
}

type shapeFlags struct {
	kind         string
	exportedOnly bool
	pathGlob     string
}

// ShapeMatch is one function/method/arrow whose signature matches the pattern.
type ShapeMatch struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // function | method | arrow
	File      string `json:"file"`
	Line      int    `json:"line"`
	SymbolID  string `json:"symbolId,omitempty"`
	Signature string `json:"signature"`
}

// ShapeResult is the `nav shape` result.
type ShapeResult struct {
	Pattern string        `json:"pattern"`
	Matches []*ShapeMatch `json:"matches"`
}

var _ cli.Lister = (*ShapeResult)(nil)

func (r *ShapeResult) Total() int     { return len(r.Matches) }
func (r *ShapeResult) Item(i int) any { return r.Matches[i] }

func (r *ShapeResult) WriteItemText(w io.Writer, item any) error {
	m := item.(*ShapeMatch)
	id := ""
	if m.SymbolID != "" {
		id = "  " + m.SymbolID
	}
	_, err := fmt.Fprintf(w, "%s  %s  %s:%d%s  %s\n", m.Name, m.Kind, m.File, m.Line, id, m.Signature)
	return err
}

// shapePattern is a parsed signature pattern. Component texts are normalized
// (whitespace stripped) and may contain `*` wildcards.
type shapePattern struct {
	params   []string
	variadic bool // trailing ", ..." — candidates may have extra params
	ret      string
}

// shapeParamName strips an optional `name:` / `name?:` prefix from a param
// pattern component (so the spec example `(s: string) => …` works).
var shapeParamName = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*\?? *:`)

// parseShapePattern parses `(<paramType>, …) => <returnType>`. Top-level
// commas are split tracking ()/[]/{}/<> nesting; a `>` directly preceded by
// `=` is treated as part of `=>`, not a closing angle bracket.
func parseShapePattern(arg string) (*shapePattern, error) {
	s := strings.TrimSpace(arg)
	if !strings.HasPrefix(s, "(") {
		return nil, cli.UsageErrorf("malformed signature pattern %q (want '(<type>, …) => <type>')", arg)
	}
	paren, bracket, brace, angle := 0, 0, 0, 0
	closeIdx := -1
	var params []string
	segStart := 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			paren++
		case ')':
			paren--
			if paren == 0 {
				closeIdx = i
			}
		case '[':
			bracket++
		case ']':
			bracket--
		case '{':
			brace++
		case '}':
			brace--
		case '<':
			angle++
		case '>':
			if i > 0 && s[i-1] == '=' {
				break // the `=>` of a function type, not a closing angle
			}
			if angle > 0 {
				angle--
			}
		case ',':
			if paren == 1 && bracket == 0 && brace == 0 && angle == 0 {
				params = append(params, s[segStart:i])
				segStart = i + 1
			}
		}
		if closeIdx >= 0 {
			break
		}
	}
	if closeIdx < 0 {
		return nil, cli.UsageErrorf("malformed signature pattern %q: unbalanced parentheses", arg)
	}
	if last := strings.TrimSpace(s[segStart:closeIdx]); last != "" || len(params) > 0 {
		params = append(params, s[segStart:closeIdx])
	}
	rest := strings.TrimSpace(s[closeIdx+1:])
	ret, ok := strings.CutPrefix(rest, "=>")
	if !ok {
		return nil, cli.UsageErrorf("malformed signature pattern %q: missing '=>' return type", arg)
	}
	p := &shapePattern{ret: normalizeTypeText(ret)}
	if p.ret == "" {
		return nil, cli.UsageErrorf("malformed signature pattern %q: empty return type", arg)
	}
	for i, raw := range params {
		text := strings.TrimSpace(raw)
		if text == "..." {
			if i != len(params)-1 {
				return nil, cli.UsageErrorf("malformed signature pattern %q: '...' must be the last parameter", arg)
			}
			p.variadic = true
			break
		}
		text = strings.TrimSpace(shapeParamName.ReplaceAllString(text, ""))
		if text == "" {
			return nil, cli.UsageErrorf("malformed signature pattern %q: empty parameter %d", arg, i+1)
		}
		p.params = append(p.params, normalizeTypeText(text))
	}
	return p, nil
}

// normalizeTypeText strips all whitespace for textual type comparison.
func normalizeTypeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// wildcardMatch matches s against pattern where `*` matches any (possibly
// empty) substring. Both sides are expected to be normalized.
func wildcardMatch(pattern string, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		idx := strings.Index(s, mid)
		if idx < 0 {
			return false
		}
		s = s[idx+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// globToRegexp converts a path glob (`**` any path, `*` within a path
// segment, `?` one char) to an anchored regexp.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
				// swallow a slash right after ** so "src/**/x" matches "src/x"
				if i+1 < len(glob) && glob[i+1] == '/' {
					b.WriteString("/?")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func runNavShape(ctx context.Context, ws *core.Workspace, flags *shapeFlags, args []string) (*ShapeResult, error) {
	if len(args) != 1 {
		return nil, cli.UsageErrorf("expected exactly one signature pattern, e.g. '(string, *) => Promise<*>'")
	}
	pattern, err := parseShapePattern(args[0])
	if err != nil {
		return nil, err
	}
	kindFilter := cli.CommaSet(flags.kind)
	for kind := range kindFilter {
		switch kind {
		case "function", "method", "arrow":
		default:
			return nil, cli.UsageErrorf("invalid --kind entry %q (want function, method, or arrow)", kind)
		}
	}
	var pathRe *regexp.Regexp
	if flags.pathGlob != "" {
		pathRe, err = globToRegexp(flags.pathGlob)
		if err != nil {
			return nil, cli.UsageErrorf("invalid --path-glob %q: %v", flags.pathGlob, err)
		}
	}

	files, err := projectFiles(ws, nil)
	if err != nil {
		return nil, err
	}
	result := &ShapeResult{Pattern: args[0], Matches: []*ShapeMatch{}}
	for _, file := range files {
		rel := ws.RelPath(file.FileName())
		if pathRe != nil && !pathRe.MatchString(rel) {
			continue
		}
		candidates := collectShapeCandidates(file)
		if len(candidates) == 0 {
			continue
		}
		c, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		for _, cand := range candidates {
			if len(kindFilter) > 0 && !kindFilter[cand.kind] {
				continue
			}
			if flags.exportedOnly && !isShapeCandidateExported(cand) {
				continue
			}
			sig := matchShapeCandidate(c, cand, pattern)
			if sig == nil {
				continue
			}
			line, _ := declLineOf(ws, cand.owner)
			match := &ShapeMatch{
				Name:      cand.name,
				Kind:      cand.kind,
				File:      rel,
				Line:      line,
				Signature: c.SignatureToStringEx(sig, file.AsNode(), checker.TypeFormatFlagsNone, nil),
			}
			if nameNode := ast.GetNameOfDeclaration(cand.owner); nameNode != nil {
				match.SymbolID = core.EncodeSymbolID(ws, c.GetSymbolAtLocation(nameNode))
			}
			result.Matches = append(result.Matches, match)
		}
		done()
	}
	return result, nil
}

// shapeCandidate is one named function-like declaration. decl is the
// function-like node (whose type carries the call signatures); owner is the
// named declaration (the variable declaration for arrows/function
// expressions, otherwise decl itself).
type shapeCandidate struct {
	decl  *ast.Node
	owner *ast.Node
	kind  string
	name  string
}

// collectShapeCandidates walks the whole file collecting named function
// declarations, methods (class/interface/object literal), and arrow
// functions / function expressions bound to variable declarations.
func collectShapeCandidates(file *ast.SourceFile) []*shapeCandidate {
	var candidates []*shapeCandidate
	add := func(decl *ast.Node, owner *ast.Node, kind string) {
		name := ast.GetDeclarationName(owner)
		if name == "" {
			return
		}
		candidates = append(candidates, &shapeCandidate{decl: decl, owner: owner, kind: kind, name: name})
	}
	var visit func(node *ast.Node) bool
	visit = func(node *ast.Node) bool {
		switch node.Kind {
		case ast.KindFunctionDeclaration:
			add(node, node, "function")
		case ast.KindMethodDeclaration, ast.KindMethodSignature:
			add(node, node, "method")
		case ast.KindArrowFunction, ast.KindFunctionExpression:
			if node.Parent != nil && node.Parent.Kind == ast.KindVariableDeclaration &&
				node.Parent.Initializer() == node {
				add(node, node.Parent, "arrow")
			}
		}
		node.ForEachChild(visit)
		return false
	}
	file.AsNode().ForEachChild(visit)
	return candidates
}

// isShapeCandidateExported reports whether the candidate is exported:
// functions and arrow-bound variables carry an export modifier themselves;
// methods inherit from their enclosing class/interface.
func isShapeCandidateExported(cand *shapeCandidate) bool {
	if cand.kind == "method" {
		enclosing := ast.FindAncestor(cand.owner, func(n *ast.Node) bool {
			switch n.Kind {
			case ast.KindClassDeclaration, ast.KindClassExpression, ast.KindInterfaceDeclaration:
				return true
			}
			return false
		})
		return enclosing != nil && core.IsExportedDeclaration(enclosing)
	}
	return core.IsExportedDeclaration(cand.owner)
}

// matchShapeCandidate returns the first call signature of the candidate's
// type that matches the pattern (textually, on normalized TypeToString
// renderings), or nil.
func matchShapeCandidate(c *checker.Checker, cand *shapeCandidate, pattern *shapePattern) *checker.Signature {
	t := c.GetTypeAtLocation(cand.decl)
	if t == nil {
		return nil
	}
	for _, sig := range c.GetSignaturesOfType(t, checker.SignatureKindCall) {
		if matchSignature(c, sig, pattern) {
			return sig
		}
	}
	return nil
}

func matchSignature(c *checker.Checker, sig *checker.Signature, pattern *shapePattern) bool {
	params := sig.Parameters()
	if pattern.variadic {
		if len(params) < len(pattern.params) {
			return false
		}
	} else if len(params) != len(pattern.params) {
		return false
	}
	for i, pp := range pattern.params {
		paramType := c.GetTypeOfSymbol(params[i])
		if paramType == nil || !wildcardMatch(pp, normalizeTypeText(c.TypeToString(paramType))) {
			return false
		}
	}
	ret := c.GetReturnTypeOfSignature(sig)
	return ret != nil && wildcardMatch(pattern.ret, normalizeTypeText(c.TypeToString(ret)))
}
