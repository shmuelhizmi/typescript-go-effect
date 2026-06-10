package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// api_docs.go implements `api docs` (spec §4.9): structured JSDoc + resolved
// type display per exported symbol. JSDoc is read through the AST's lazy
// JSDoc accessor (node.JSDoc(file), the same path hover uses), with tags
// mapped from the KindJSDoc*Tag node kinds.

func init() {
	cli.Register(cli.Command{
		Family:       "api",
		Name:         "docs",
		Summary:      "Structured docs (JSDoc + resolved types) per exported symbol",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &apiDocsFlags{}
			fs.StringVar(&f.symbol, "symbol", "", "document this symbol id (path#qualified.name)")
			fs.StringVar(&f.name, "name", "", "document this declaration by name (must be unambiguous)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runApiDocs(ctx, ws, flags.(*apiDocsFlags), args)
		},
	})
}

type apiDocsFlags struct {
	symbol string
	name   string
}

// ApiDocTag is one JSDoc tag (@param/@returns/@deprecated/@example/…).
type ApiDocTag struct {
	Tag  string `json:"tag"`
	Name string `json:"name,omitempty"` // entity name for @param/@property
	Text string `json:"text,omitempty"`
}

// ApiDocJSDoc is the parsed JSDoc of one symbol.
type ApiDocJSDoc struct {
	// Summary is the first paragraph of the leading comment.
	Summary string      `json:"summary,omitempty"`
	Tags    []ApiDocTag `json:"tags,omitempty"`
}

// ApiDoc is the structured documentation of one symbol.
type ApiDoc struct {
	Name       string       `json:"name"`
	SymbolID   string       `json:"symbolId,omitempty"`
	Kind       string       `json:"kind"`
	Signature  string       `json:"signature,omitempty"` // signature for callables, type display otherwise
	JSDoc      *ApiDocJSDoc `json:"jsdoc,omitempty"`
	DeclaredAt string       `json:"declaredAt,omitempty"`
}

// ApiDocsResult is the `api docs` result.
type ApiDocsResult struct {
	Docs []*ApiDoc
}

var _ cli.Lister = (*ApiDocsResult)(nil)

func (r *ApiDocsResult) Total() int     { return len(r.Docs) }
func (r *ApiDocsResult) Item(i int) any { return r.Docs[i] }

func (r *ApiDocsResult) WriteItemText(w io.Writer, item any) error {
	d := item.(*ApiDoc)
	if _, err := fmt.Fprintf(w, "%s %s  %s  (%s)\n", d.Kind, d.Name, d.Signature, d.DeclaredAt); err != nil {
		return err
	}
	if d.JSDoc != nil {
		if d.JSDoc.Summary != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", strings.ReplaceAll(d.JSDoc.Summary, "\n", "\n  ")); err != nil {
				return err
			}
		}
		for _, tag := range d.JSDoc.Tags {
			line := "  @" + tag.Tag
			if tag.Name != "" {
				line += " " + tag.Name
			}
			if tag.Text != "" {
				line += " — " + strings.ReplaceAll(tag.Text, "\n", " ")
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	return nil
}

func runApiDocs(ctx context.Context, ws *core.Workspace, flags *apiDocsFlags, args []string) (*ApiDocsResult, error) {
	result := &ApiDocsResult{}

	// Targeted form: --symbol/--name documents one symbol.
	if flags.symbol != "" || flags.name != "" {
		if len(args) > 0 {
			return nil, cli.UsageErrorf("positional targets and --symbol/--name are mutually exclusive")
		}
		target, err := ws.ResolveTarget(ctx, core.TargetSpec{Symbol: flags.symbol, Name: flags.name})
		if err != nil {
			return nil, err
		}
		if target.Symbol == nil {
			return nil, cli.NotFoundErrorf("target does not resolve to a symbol")
		}
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, target.File)
		defer done()
		doc := apiDocOfSymbol(ws, fileChecker, target.Symbol, target.Symbol.Name)
		if doc == nil {
			return nil, cli.NotFoundErrorf("target symbol has no declaration")
		}
		result.Docs = append(result.Docs, doc)
		return result, nil
	}

	// Symbol-id positionals mixed with entry files.
	var entryArgs []string
	for _, arg := range args {
		if strings.ContainsAny(arg, "#@") {
			target, err := ws.ResolveTarget(ctx, core.TargetSpec{Symbol: arg})
			if err != nil {
				return nil, err
			}
			if target.Symbol == nil {
				return nil, cli.NotFoundErrorf("symbol %s does not resolve", arg)
			}
			fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, target.File)
			doc := apiDocOfSymbol(ws, fileChecker, target.Symbol, target.Symbol.Name)
			done()
			if doc != nil {
				result.Docs = append(result.Docs, doc)
			}
			continue
		}
		entryArgs = append(entryArgs, arg)
	}
	if len(entryArgs) > 0 || len(result.Docs) == 0 {
		entries, err := resolveApiEntries(ws, entryArgs)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			docs, err := apiDocsOfEntry(ctx, ws, entry)
			if err != nil {
				return nil, err
			}
			result.Docs = append(result.Docs, docs...)
		}
	}
	slices.SortFunc(result.Docs, func(a, b *ApiDoc) int {
		if c := strings.Compare(a.DeclaredAt, b.DeclaredAt); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return result, nil
}

// apiDocsOfEntry documents every export of one entry module (same export
// enumeration as `api surface`).
func apiDocsOfEntry(ctx context.Context, ws *core.Workspace, file *ast.SourceFile) ([]*ApiDoc, error) {
	fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()
	moduleSymbol := file.AsNode().Symbol()
	if moduleSymbol == nil {
		return nil, fmt.Errorf("%s is not a module (no top-level import/export): %w", ws.RelPath(file.FileName()), core.ErrInvalidArgument)
	}
	moduleSymbol = fileChecker.GetMergedSymbol(moduleSymbol)
	var docs []*ApiDoc
	for _, symbol := range fileChecker.GetExportsOfModule(moduleSymbol) {
		if strings.HasPrefix(symbol.Name, ast.InternalSymbolNamePrefix) {
			continue
		}
		if doc := apiDocOfSymbol(ws, fileChecker, symbol, symbol.Name); doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

// apiDocOfSymbol renders one symbol: alias-resolved declaration site, kind,
// signature/type display, and parsed JSDoc.
func apiDocOfSymbol(ws *core.Workspace, c *checker.Checker, symbol *ast.Symbol, exportedName string) *ApiDoc {
	target := symbol
	if symbol.Flags&ast.SymbolFlagsAlias != 0 {
		if resolved := c.GetAliasedSymbol(symbol); resolved != nil {
			target = resolved
		}
	}
	decl := target.ValueDeclaration
	if decl == nil && len(target.Declarations) > 0 {
		decl = target.Declarations[0]
	}
	if decl == nil {
		return nil
	}
	declFile := ast.GetSourceFileOfNode(decl)

	doc := &ApiDoc{
		Name:     exportedName,
		SymbolID: core.EncodeSymbolID(ws, target),
		Kind:     core.DeclarationKind(decl),
	}
	if declFile != nil {
		pos := decl.Pos()
		if name := ast.GetNameOfDeclaration(decl); name != nil {
			pos = astnav.GetStartOfNode(name, declFile, false /*includeJSDoc*/)
		}
		line, col := ws.PosToLineCol(declFile, pos)
		doc.DeclaredAt = fmt.Sprintf("%s:%d:%d", ws.RelPath(declFile.FileName()), line, col)
	}
	doc.Signature = apiDocDisplay(c, target, decl, declFile)
	doc.JSDoc = extractJSDoc(decl, declFile)
	return doc
}

// apiDocDisplay renders the signature string for callables and the resolved
// type display for everything else.
func apiDocDisplay(c *checker.Checker, symbol *ast.Symbol, decl *ast.Node, file *ast.SourceFile) string {
	if ast.IsFunctionLikeDeclaration(decl) {
		if sig := c.GetSignatureFromDeclaration(decl); sig != nil {
			return c.SignatureToStringEx(sig, decl, checker.TypeFormatFlagsNoTruncation, nil)
		}
	}
	var t *checker.Type
	if symbol.Flags&ast.SymbolFlagsValue != 0 {
		t = c.GetTypeOfSymbol(symbol)
	} else {
		t = c.GetDeclaredTypeOfSymbol(symbol)
	}
	if t == nil {
		return ""
	}
	return c.TypeToStringEx(t, decl, checker.TypeFormatFlagsNoTruncation, nil)
}

// ---------------------------------------------------------------------------
// JSDoc extraction (shared with `map outline --detail full`)

// jsdocNodeFor returns the closest leading JSDoc comment node of a
// declaration, widening sole variable declarators to their statement (where
// the comment syntactically attaches).
func jsdocNodeFor(decl *ast.Node, file *ast.SourceFile) *ast.Node {
	candidates := []*ast.Node{decl}
	if decl.Kind == ast.KindVariableDeclaration {
		if list := decl.Parent; list != nil && list.Kind == ast.KindVariableDeclarationList {
			if statement := list.Parent; statement != nil && statement.Kind == ast.KindVariableStatement {
				candidates = append(candidates, statement)
			}
		}
	}
	for _, candidate := range candidates {
		if docs := candidate.JSDoc(file); len(docs) > 0 {
			return docs[len(docs)-1]
		}
	}
	return nil
}

// jsdocCommentText concatenates JSDoc comment parts (text and {@link …}
// nodes) into one plain string.
func jsdocCommentText(comments []*ast.Node) string {
	var b strings.Builder
	for _, comment := range comments {
		switch comment.Kind {
		case ast.KindJSDocText:
			b.WriteString(comment.Text())
		case ast.KindJSDocLink, ast.KindJSDocLinkCode, ast.KindJSDocLinkPlain:
			b.WriteString(scanner.GetTextOfNode(comment))
		}
	}
	return strings.TrimSpace(b.String())
}

// jsdocFirstLine returns the first non-empty line of a declaration's leading
// JSDoc comment, or "".
func jsdocFirstLine(decl *ast.Node, file *ast.SourceFile) string {
	jsdoc := jsdocNodeFor(decl, file)
	if jsdoc == nil {
		return ""
	}
	text := jsdocCommentText(jsdoc.Comments())
	for line := range strings.SplitSeq(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// extractJSDoc parses a declaration's leading JSDoc into a summary (first
// paragraph) plus tags. Returns nil when there is no JSDoc.
func extractJSDoc(decl *ast.Node, file *ast.SourceFile) *ApiDocJSDoc {
	jsdoc := jsdocNodeFor(decl, file)
	if jsdoc == nil {
		return nil
	}
	parsed := &ApiDocJSDoc{}
	full := jsdocCommentText(jsdoc.Comments())
	if full != "" {
		paragraphs := strings.SplitN(strings.ReplaceAll(full, "\r\n", "\n"), "\n\n", 2)
		parsed.Summary = strings.TrimSpace(paragraphs[0])
	}
	if jsdoc.Kind == ast.KindJSDoc {
		if tags := jsdoc.AsJSDoc().Tags; tags != nil {
			for _, tag := range tags.Nodes {
				entry := ApiDocTag{Tag: tag.TagName().Text()}
				switch tag.Kind {
				case ast.KindJSDocParameterTag, ast.KindJSDocPropertyTag:
					if name := tag.Name(); name != nil {
						entry.Name = jsdocEntityNameText(name)
					}
				}
				entry.Text = strings.TrimPrefix(jsdocCommentText(tag.Comments()), "- ")
				parsed.Tags = append(parsed.Tags, entry)
			}
		}
	}
	if parsed.Summary == "" && len(parsed.Tags) == 0 {
		return nil
	}
	return parsed
}

// jsdocEntityNameText renders an entity name (a.b.c) as plain text.
func jsdocEntityNameText(name *ast.Node) string {
	if name.Kind == ast.KindIdentifier || name.Kind == ast.KindPrivateIdentifier {
		return name.Text()
	}
	return strings.TrimSpace(scanner.GetTextOfNode(name))
}
