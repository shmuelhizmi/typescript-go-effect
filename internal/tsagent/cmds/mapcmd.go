package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/binder"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/vfs/vfsmatch"
)

const maxSignatureLen = 120

func init() {
	cli.Register(cli.Command{
		Family:       "map",
		Name:         "outline",
		Summary:      "Symbol tree per file/folder with depth and kind filters",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &outlineFlags{}
			fs.StringVar(&f.depth, "depth", "all", "outline depth: N, top-level, or all")
			fs.BoolVar(&f.exportedOnly, "exported-only", false, "only include exported declarations")
			fs.StringVar(&f.kind, "kind", "", "comma-separated kind filter (class,interface,function,...)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runMapOutline(ctx, ws, flags.(*outlineFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "map",
		Name:         "search",
		Summary:      "Project-wide fuzzy symbol search returning symbol IDs",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &searchFlags{}
			fs.StringVar(&f.kind, "kind", "", "comma-separated kind filter")
			fs.BoolVar(&f.exportedOnly, "exported-only", false, "only include exported declarations")
			fs.StringVar(&f.pathGlob, "path-glob", "", "only include files matching this glob")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runMapSearch(ctx, ws, flags.(*searchFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "map",
		Name:         "files",
		Summary:      "Program file inventory with classification",
		NeedsProgram: true,
		Flags:        func(fs *flag.FlagSet) any { return &filesFlags{} },
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runMapFiles(ctx, ws, flags.(*filesFlags), args)
		},
	})
	cli.Register(cli.Command{
		Family:       "map",
		Name:         "stats",
		Summary:      "Per-directory counts of files, lines, symbols, and exports",
		NeedsProgram: true,
		Flags:        func(fs *flag.FlagSet) any { return &statsFlags{} },
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runMapStats(ctx, ws, flags.(*statsFlags), args)
		},
	})
}

// projectFiles selects program source files filtered to the given paths
// (files or directories). With no paths, all project files (excluding libs
// and node_modules) are returned in program order.
func projectFiles(ws *core.Workspace, paths []string) ([]*ast.SourceFile, error) {
	// Accept both cwd-relative and project-root-relative paths (output paths
	// are rendered root-relative, so agents will echo those back).
	abs := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		cwdRelative := ws.AbsPath(p)
		abs = append(abs, cwdRelative)
		if rootRelative := tspath.GetNormalizedAbsolutePath(p, ws.RootDir); rootRelative != cwdRelative {
			abs = append(abs, rootRelative)
		}
	}
	comparePathsOptions := tspath.ComparePathsOptions{
		CurrentDirectory:          ws.Cwd,
		UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
	}
	var files []*ast.SourceFile
	for _, file := range ws.Program.SourceFiles() {
		if len(abs) == 0 {
			if ws.Program.IsLibFile(file) || isInNodeModules(file.FileName()) || file.IsDeclarationFile {
				continue
			}
			files = append(files, file)
			continue
		}
		for _, p := range abs {
			if tspath.ComparePaths(file.FileName(), p, comparePathsOptions) == 0 ||
				tspath.ContainsPath(p, file.FileName(), comparePathsOptions) {
				files = append(files, file)
				break
			}
		}
	}
	if len(abs) > 0 && len(files) == 0 {
		return nil, cli.NotFoundErrorf("no program files under %s", strings.Join(paths, ", "))
	}
	return files, nil
}

func isInNodeModules(fileName string) bool {
	return strings.Contains(fileName, "/node_modules/")
}

// ---------------------------------------------------------------------------
// map outline

type outlineFlags struct {
	depth        string
	exportedOnly bool
	kind         string
}

// OutlineEntry is one node in the symbol tree.
type OutlineEntry struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	SymbolID  string          `json:"symbolId,omitempty"`
	Exported  bool            `json:"exported,omitempty"`
	Line      int             `json:"line"`
	EndLine   int             `json:"endLine"`
	Signature string          `json:"signature,omitempty"`
	Children  []*OutlineEntry `json:"children,omitempty"`
}

// FileOutline is the symbol tree of a single file.
type FileOutline struct {
	File    string          `json:"file"`
	Entries []*OutlineEntry `json:"entries"`
}

// OutlineResult is the `map outline` result, one item per file.
type OutlineResult struct {
	Files []*FileOutline
}

var _ cli.Lister = (*OutlineResult)(nil)

func (r *OutlineResult) Total() int     { return len(r.Files) }
func (r *OutlineResult) Item(i int) any { return r.Files[i] }

func (r *OutlineResult) WriteItemText(w io.Writer, item any) error {
	fileOutline := item.(*FileOutline)
	if _, err := fmt.Fprintf(w, "%s\n", fileOutline.File); err != nil {
		return err
	}
	return writeOutlineEntriesText(w, fileOutline.Entries, 1)
}

func writeOutlineEntriesText(w io.Writer, entries []*OutlineEntry, depth int) error {
	for _, e := range entries {
		exported := ""
		if e.Exported {
			exported = "  [export]"
		}
		signature := ""
		if e.Signature != "" {
			signature = "  " + e.Signature
		}
		if _, err := fmt.Fprintf(w, "%s%s %s%s%s  (%d-%d)\n", cli.Indent(depth), e.Kind, e.Name, signature, exported, e.Line, e.EndLine); err != nil {
			return err
		}
		if err := writeOutlineEntriesText(w, e.Children, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func parseDepth(s string) (int, error) {
	switch s {
	case "", "all":
		return 0, nil
	case "top-level":
		return 1, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, cli.UsageErrorf("invalid --depth %q (want a positive number, top-level, or all)", s)
	}
	return n, nil
}

func runMapOutline(ctx context.Context, ws *core.Workspace, flags *outlineFlags, args []string) (*OutlineResult, error) {
	maxDepth, err := parseDepth(flags.depth)
	if err != nil {
		return nil, err
	}
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	kindFilter := cli.CommaSet(flags.kind)

	result := &OutlineResult{}
	for _, file := range files {
		binder.BindSourceFile(file)
		fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
		walker := &outlineWalker{
			ws:           ws,
			checker:      fileChecker,
			file:         file,
			maxDepth:     maxDepth,
			kindFilter:   kindFilter,
			exportedOnly: flags.exportedOnly,
		}
		entries := walker.entriesForStatements(file.Statements.Nodes, 1)
		done()
		result.Files = append(result.Files, &FileOutline{File: ws.RelPath(file.FileName()), Entries: entries})
	}
	return result, nil
}

type outlineWalker struct {
	ws           *core.Workspace
	checker      *checker.Checker
	file         *ast.SourceFile
	maxDepth     int // 0 = unlimited
	kindFilter   map[string]bool
	exportedOnly bool
}

func (w *outlineWalker) entriesForStatements(statements []*ast.Node, depth int) []*OutlineEntry {
	var entries []*OutlineEntry
	for _, statement := range statements {
		switch statement.Kind {
		case ast.KindVariableStatement:
			for _, decl := range statement.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
				if e := w.entry(decl, depth, nil); e != nil {
					entries = append(entries, e)
				}
			}
		case ast.KindFunctionDeclaration, ast.KindTypeAliasDeclaration:
			if e := w.entry(statement, depth, nil); e != nil {
				entries = append(entries, e)
			}
		case ast.KindClassDeclaration, ast.KindInterfaceDeclaration:
			if e := w.entry(statement, depth, w.memberEntries(statement.Members(), depth+1)); e != nil {
				entries = append(entries, e)
			}
		case ast.KindEnumDeclaration:
			if e := w.entry(statement, depth, w.memberEntries(statement.AsEnumDeclaration().Members.Nodes, depth+1)); e != nil {
				entries = append(entries, e)
			}
		case ast.KindModuleDeclaration:
			var children []*OutlineEntry
			if body := statement.Body(); body != nil && body.Kind == ast.KindModuleBlock && w.withinDepth(depth+1) {
				children = w.entriesForStatements(body.AsModuleBlock().Statements.Nodes, depth+1)
			}
			if e := w.entry(statement, depth, children); e != nil {
				entries = append(entries, e)
			}
		}
	}
	return entries
}

func (w *outlineWalker) memberEntries(members []*ast.Node, depth int) []*OutlineEntry {
	if !w.withinDepth(depth) {
		return nil
	}
	var entries []*OutlineEntry
	for _, member := range members {
		switch member.Kind {
		case ast.KindMethodDeclaration, ast.KindMethodSignature, ast.KindPropertyDeclaration,
			ast.KindPropertySignature, ast.KindConstructor, ast.KindGetAccessor, ast.KindSetAccessor,
			ast.KindEnumMember:
			if e := w.memberEntry(member); e != nil {
				entries = append(entries, e)
			}
		}
	}
	return entries
}

func (w *outlineWalker) withinDepth(depth int) bool {
	return w.maxDepth == 0 || depth <= w.maxDepth
}

// entry builds a top-level (statement) entry, applying exported-only and
// kind filters.
func (w *outlineWalker) entry(node *ast.Node, depth int, children []*OutlineEntry) *OutlineEntry {
	if !w.withinDepth(depth) {
		return nil
	}
	exported := core.IsExportedDeclaration(node)
	if w.exportedOnly && !exported {
		return nil
	}
	kind := core.DeclarationKind(node)
	if !core.KindMatchesFilter(kind, w.kindFilter) {
		return nil
	}
	e := w.newEntry(node, kind)
	if e == nil {
		return nil
	}
	e.Exported = exported
	e.Children = children
	return e
}

func (w *outlineWalker) memberEntry(node *ast.Node) *OutlineEntry {
	return w.newEntry(node, core.DeclarationKind(node))
}

func (w *outlineWalker) newEntry(node *ast.Node, kind string) *OutlineEntry {
	name := ast.GetDeclarationName(node)
	if name == "" {
		if node.Kind == ast.KindConstructor {
			name = "constructor"
		} else {
			return nil
		}
	}
	start := astnav.GetStartOfNode(node, w.file, false /*includeJSDoc*/)
	line, _ := w.ws.PosToLineCol(w.file, start)
	endLine, _ := w.ws.PosToLineCol(w.file, node.End())
	return &OutlineEntry{
		Name:      name,
		Kind:      kind,
		SymbolID:  core.EncodeSymbolID(w.ws, node.Symbol()),
		Line:      line,
		EndLine:   endLine,
		Signature: w.signature(node, kind),
	}
}

// signature renders a one-line signature: SignatureToString for callables,
// TypeToString for values, both capped at 120 chars.
func (w *outlineWalker) signature(node *ast.Node, kind string) string {
	switch node.Kind {
	case ast.KindFunctionDeclaration, ast.KindMethodDeclaration, ast.KindMethodSignature,
		ast.KindConstructor, ast.KindGetAccessor, ast.KindSetAccessor:
		sig := w.checker.GetSignatureFromDeclaration(node)
		if sig == nil {
			return ""
		}
		return cli.Truncate(w.checker.SignatureToStringEx(sig, w.file.AsNode(), checker.TypeFormatFlagsNone, nil), maxSignatureLen)
	case ast.KindVariableDeclaration, ast.KindPropertyDeclaration, ast.KindPropertySignature,
		ast.KindTypeAliasDeclaration:
		t := w.checker.GetTypeAtLocation(node)
		if t == nil {
			return ""
		}
		return cli.Truncate(": "+w.checker.TypeToString(t), maxSignatureLen)
	}
	return ""
}

// ---------------------------------------------------------------------------
// map search

type searchFlags struct {
	kind         string
	exportedOnly bool
	pathGlob     string
}

// SearchMatch is one fuzzy symbol search hit.
type SearchMatch struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	SymbolID string `json:"symbolId,omitempty"`
	Exported bool   `json:"exported,omitempty"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	score    int
}

// SearchResult is the `map search` result.
type SearchResult struct {
	Matches []*SearchMatch
}

var _ cli.Lister = (*SearchResult)(nil)

func (r *SearchResult) Total() int     { return len(r.Matches) }
func (r *SearchResult) Item(i int) any { return r.Matches[i] }

func (r *SearchResult) WriteItemText(w io.Writer, item any) error {
	m := item.(*SearchMatch)
	exported := ""
	if m.Exported {
		exported = "  [export]"
	}
	id := ""
	if m.SymbolID != "" {
		id = "  " + m.SymbolID
	}
	_, err := fmt.Fprintf(w, "%s %s  %s:%d:%d%s%s\n", m.Kind, m.Name, m.File, m.Line, m.Col, exported, id)
	return err
}

// fuzzyMatchScore matches s against pattern: every pattern rune must appear
// in order; upper-case pattern runes match exactly. Returns the number of
// skipped runes, or -1 when s does not match.
func fuzzyMatchScore(s string, pattern string) int {
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

func runMapSearch(ctx context.Context, ws *core.Workspace, flags *searchFlags, args []string) (*SearchResult, error) {
	if len(args) != 1 {
		return nil, cli.UsageErrorf("map search takes exactly one query argument")
	}
	query := args[0]
	kindFilter := cli.CommaSet(flags.kind)
	var globMatcher *vfsmatch.SpecMatcher
	if flags.pathGlob != "" {
		globMatcher = vfsmatch.NewSpecMatcher([]string{flags.pathGlob}, ws.RootDir, vfsmatch.UsageFiles, ws.FS.UseCaseSensitiveFileNames())
		if globMatcher == nil {
			return nil, cli.UsageErrorf("invalid --path-glob %q", flags.pathGlob)
		}
	}

	result := &SearchResult{}
	for _, file := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(file) || isInNodeModules(file.FileName()) {
			continue
		}
		if globMatcher != nil && globMatcher.MatchIndex(file.FileName()) < 0 {
			continue
		}
		bound := false
		for name, declarations := range file.GetDeclarationMap() {
			score := fuzzyMatchScore(name, query)
			if score < 0 {
				continue
			}
			for _, decl := range declarations {
				kind := core.DeclarationKind(decl)
				if !core.KindMatchesFilter(kind, kindFilter) {
					continue
				}
				exported := core.IsExportedDeclaration(decl)
				if flags.exportedOnly && !exported {
					continue
				}
				if !bound {
					binder.BindSourceFile(file)
					bound = true
				}
				nameNode := ast.GetNameOfDeclaration(decl)
				pos := astnav.GetStartOfNode(decl, file, false /*includeJSDoc*/)
				if nameNode != nil {
					pos = astnav.GetStartOfNode(nameNode, file, false /*includeJSDoc*/)
				}
				line, col := ws.PosToLineCol(file, pos)
				result.Matches = append(result.Matches, &SearchMatch{
					Name:     name,
					Kind:     kind,
					SymbolID: core.EncodeSymbolID(ws, decl.Symbol()),
					Exported: exported,
					File:     ws.RelPath(file.FileName()),
					Line:     line,
					Col:      col,
					score:    score,
				})
			}
		}
	}
	slices.SortFunc(result.Matches, func(a, b *SearchMatch) int {
		if a.score != b.score {
			return a.score - b.score
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
	return result, nil
}

// ---------------------------------------------------------------------------
// map files

type filesFlags struct{}

// FileInfo is one program file with classification.
type FileInfo struct {
	File        string `json:"file"`
	Kind        string `json:"kind"` // source | declaration | lib | external
	Root        bool   `json:"root,omitempty"`
	Declaration bool   `json:"declaration,omitempty"`
	Lines       int    `json:"lines"`
}

// FilesResult is the `map files` result.
type FilesResult struct {
	Files []*FileInfo
}

var _ cli.Lister = (*FilesResult)(nil)

func (r *FilesResult) Total() int     { return len(r.Files) }
func (r *FilesResult) Item(i int) any { return r.Files[i] }

func (r *FilesResult) WriteItemText(w io.Writer, item any) error {
	f := item.(*FileInfo)
	root := ""
	if f.Root {
		root = "  [root]"
	}
	_, err := fmt.Fprintf(w, "%-12s %s%s  (%d lines)\n", f.Kind, f.File, root, f.Lines)
	return err
}

func runMapFiles(ctx context.Context, ws *core.Workspace, flags *filesFlags, args []string) (*FilesResult, error) {
	rootNames := make(map[string]bool, len(ws.Config.FileNames()))
	for _, name := range ws.Config.FileNames() {
		rootNames[name] = true
	}
	result := &FilesResult{}
	for _, file := range ws.Program.SourceFiles() {
		kind := "source"
		switch {
		case ws.Program.IsLibFile(file):
			kind = "lib"
		case isInNodeModules(file.FileName()):
			kind = "external"
		case file.IsDeclarationFile:
			kind = "declaration"
		}
		result.Files = append(result.Files, &FileInfo{
			File:        ws.RelPath(file.FileName()),
			Kind:        kind,
			Root:        rootNames[file.FileName()],
			Declaration: file.IsDeclarationFile,
			Lines:       len(file.ECMALineMap()),
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// map stats

type statsFlags struct{}

// DirStats aggregates counts for one directory.
type DirStats struct {
	Dir           string         `json:"dir"`
	Files         int            `json:"files"`
	Lines         int            `json:"lines"`
	SymbolsByKind map[string]int `json:"symbolsByKind"`
	Exports       int            `json:"exports"`
}

// StatsResult is the `map stats` result.
type StatsResult struct {
	Dirs []*DirStats
}

var _ cli.Lister = (*StatsResult)(nil)

func (r *StatsResult) Total() int     { return len(r.Dirs) }
func (r *StatsResult) Item(i int) any { return r.Dirs[i] }

func (r *StatsResult) WriteItemText(w io.Writer, item any) error {
	d := item.(*DirStats)
	kinds := make([]string, 0, len(d.SymbolsByKind))
	for kind := range d.SymbolsByKind {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s:%d", kind, d.SymbolsByKind[kind]))
	}
	_, err := fmt.Fprintf(w, "%s  files:%d lines:%d exports:%d  %s\n", d.Dir, d.Files, d.Lines, d.Exports, strings.Join(parts, " "))
	return err
}

func runMapStats(ctx context.Context, ws *core.Workspace, flags *statsFlags, args []string) (*StatsResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	byDir := make(map[string]*DirStats)
	for _, file := range files {
		rel := ws.RelPath(file.FileName())
		dir := tspath.GetDirectoryPath(rel)
		if dir == "" {
			dir = "."
		}
		stats, ok := byDir[dir]
		if !ok {
			stats = &DirStats{Dir: dir, SymbolsByKind: make(map[string]int)}
			byDir[dir] = stats
		}
		stats.Files++
		stats.Lines += len(file.ECMALineMap())
		countDeclarations(file, stats)
	}
	result := &StatsResult{Dirs: make([]*DirStats, 0, len(byDir))}
	for _, stats := range byDir {
		result.Dirs = append(result.Dirs, stats)
	}
	slices.SortFunc(result.Dirs, func(a, b *DirStats) int { return strings.Compare(a.Dir, b.Dir) })
	return result, nil
}

func countDeclarations(file *ast.SourceFile, stats *DirStats) {
	for _, declarations := range file.GetDeclarationMap() {
		for _, decl := range declarations {
			kind := core.DeclarationKind(decl)
			if kind == "unknown" || kind == "import" || kind == "parameter" || kind == "typeparameter" {
				continue
			}
			stats.SymbolsByKind[kind]++
			if core.IsExportedDeclaration(decl) {
				stats.Exports++
			}
		}
	}
}
