package cmds

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

func init() {
	cli.Register(cli.Command{
		Family:       "api",
		Name:         "surface",
		Summary:      "Extract the public API surface as a stable, sorted, digestible report",
		NeedsProgram: true,
		Flags:        func(fs *flag.FlagSet) any { return &apiSurfaceFlags{} },
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runApiSurface(ctx, ws, args)
		},
	})
	cli.Register(cli.Command{
		Family:       "api",
		Name:         "diff",
		Summary:      "Breaking-change classification between two API surfaces (git refs)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &apiDiffFlags{}
			fs.StringVar(&f.base, "base", "", "git ref of the base surface (required)")
			fs.StringVar(&f.head, "head", "", "git ref of the head surface (default: working tree)")
			fs.StringVar(&f.failOn, "fail-on", "", "exit 1 when changes of this class exist: breaking or possibly-breaking")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runApiDiff(ctx, ws, flags.(*apiDiffFlags), args)
		},
	})
}

type apiSurfaceFlags struct{}

type apiDiffFlags struct {
	base   string
	head   string
	failOn string
}

// ApiExport is one exported symbol in the API surface.
type ApiExport struct {
	Entry      string `json:"entry"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Type       string `json:"type"`
	DeclaredAt string `json:"declaredAt"`
}

// ApiSurfaceResult is the `api surface` result: every export of the entry
// files with fully-resolved types, sorted by (entry, name), plus a sha256
// digest of the canonical JSON — deterministic and diffable across commits.
type ApiSurfaceResult struct {
	Digest  string       `json:"digest"`
	Exports []*ApiExport `json:"exports"`
}

var _ cli.Texter = (*ApiSurfaceResult)(nil)

func (r *ApiSurfaceResult) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "digest %s\n", r.Digest); err != nil {
		return err
	}
	for _, e := range r.Exports {
		if _, err := fmt.Fprintf(w, "%s %s %s: %s (%s)\n", e.Entry, e.Kind, e.Name, e.Type, e.DeclaredAt); err != nil {
			return err
		}
	}
	return nil
}

func runApiSurface(ctx context.Context, ws *core.Workspace, args []string) (*ApiSurfaceResult, error) {
	entries, err := resolveApiEntries(ws, args)
	if err != nil {
		return nil, err
	}
	result := &ApiSurfaceResult{Exports: []*ApiExport{}}
	for _, entry := range entries {
		exports, err := surfaceOfEntry(ctx, ws, entry)
		if err != nil {
			return nil, err
		}
		result.Exports = append(result.Exports, exports...)
	}
	slices.SortFunc(result.Exports, func(a, b *ApiExport) int {
		if c := strings.Compare(a.Entry, b.Entry); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	digestInput, err := json.Marshal(result.Exports)
	if err != nil {
		return nil, err
	}
	result.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(digestInput))
	return result, nil
}

// resolveApiEntries resolves explicit entry-file arguments, or defaults to
// index.ts/index.tsx at the project root and under src/. (package.json
// main/types/exports resolution is intentionally out of scope for v1.)
func resolveApiEntries(ws *core.Workspace, args []string) ([]*ast.SourceFile, error) {
	if len(args) > 0 {
		entries := make([]*ast.SourceFile, 0, len(args))
		for _, arg := range args {
			file, err := ws.FileOf(arg)
			if err != nil {
				return nil, err
			}
			entries = append(entries, file)
		}
		return entries, nil
	}
	var entries []*ast.SourceFile
	for _, dir := range []string{ws.RootDir, tspath.CombinePaths(ws.RootDir, "src")} {
		for _, name := range []string{"index.ts", "index.tsx"} {
			if file := ws.Program.GetSourceFile(tspath.CombinePaths(dir, name)); file != nil {
				entries = append(entries, file)
			}
		}
	}
	if len(entries) == 0 {
		return nil, cli.UsageErrorf("no index.ts/index.tsx found at the project root or src/; pass entry files explicitly: api surface <entry-files…>")
	}
	return entries, nil
}

// surfaceOfEntry renders every export of one entry module. The module symbol
// is taken from the bound source-file node (file.AsNode().Symbol(), merged
// through the checker) and expanded with checker.GetExportsOfModule.
func surfaceOfEntry(ctx context.Context, ws *core.Workspace, file *ast.SourceFile) ([]*ApiExport, error) {
	entryRel := ws.RelPath(file.FileName())
	fileChecker, done := ws.Program.GetTypeCheckerForFile(ctx, file)
	defer done()

	moduleSymbol := file.AsNode().Symbol()
	if moduleSymbol == nil {
		return nil, fmt.Errorf("%s is not a module (no top-level import/export): %w", entryRel, core.ErrInvalidArgument)
	}
	moduleSymbol = fileChecker.GetMergedSymbol(moduleSymbol)

	var exports []*ApiExport
	for _, symbol := range fileChecker.GetExportsOfModule(moduleSymbol) {
		if strings.HasPrefix(symbol.Name, ast.InternalSymbolNamePrefix) {
			continue
		}
		// Re-exports: resolve through the alias for the declaration site and
		// type, but keep the exported name.
		target := symbol
		if symbol.Flags&ast.SymbolFlagsAlias != 0 {
			if resolved := fileChecker.GetAliasedSymbol(symbol); resolved != nil {
				target = resolved
			}
		}
		decl := target.ValueDeclaration
		if decl == nil && len(target.Declarations) > 0 {
			decl = target.Declarations[0]
		}

		kind := "unknown"
		declaredAt := ""
		if decl != nil {
			kind = core.DeclarationKind(decl)
			if declFile := ast.GetSourceFileOfNode(decl); declFile != nil {
				pos := decl.Pos()
				if name := ast.GetNameOfDeclaration(decl); name != nil {
					pos = astnav.GetStartOfNode(name, declFile, false /*includeJSDoc*/)
				}
				line, col := ws.PosToLineCol(declFile, pos)
				declaredAt = fmt.Sprintf("%s:%d:%d", ws.RelPath(declFile.FileName()), line, col)
			}
		}

		var t *checker.Type
		if target.Flags&ast.SymbolFlagsValue != 0 {
			t = fileChecker.GetTypeOfSymbol(target)
		} else {
			t = fileChecker.GetDeclaredTypeOfSymbol(target)
		}
		typeString := ""
		if t != nil {
			typeString = fileChecker.TypeToStringEx(t, decl, checker.TypeFormatFlagsNoTruncation, nil)
		}

		exports = append(exports, &ApiExport{
			Entry:      entryRel,
			Name:       symbol.Name,
			Kind:       kind,
			Type:       typeString,
			DeclaredAt: declaredAt,
		})
	}
	return exports, nil
}

// ---------------------------------------------------------------------------
// api diff

// ApiChange is one classified difference between two surfaces.
type ApiChange struct {
	Entry    string `json:"entry"`
	Name     string `json:"name"`
	Rule     string `json:"rule"`
	BaseType string `json:"baseType,omitempty"`
	HeadType string `json:"headType,omitempty"`
}

// ApiDiffResult is the `api diff` result.
type ApiDiffResult struct {
	Breaking         []*ApiChange `json:"breaking"`
	PossiblyBreaking []*ApiChange `json:"possiblyBreaking"`
	Additive         []*ApiChange `json:"additive"`
	DigestBase       string       `json:"digestBase"`
	DigestHead       string       `json:"digestHead"`
}

var _ cli.Texter = (*ApiDiffResult)(nil)

func (r *ApiDiffResult) WriteText(w io.Writer) error {
	writeGroup := func(label string, changes []*ApiChange) error {
		for _, c := range changes {
			line := fmt.Sprintf("%s %s#%s (%s)", label, c.Entry, c.Name, c.Rule)
			if c.BaseType != "" || c.HeadType != "" {
				line += fmt.Sprintf(": %s -> %s", c.BaseType, c.HeadType)
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		return nil
	}
	if err := writeGroup("breaking", r.Breaking); err != nil {
		return err
	}
	if err := writeGroup("possibly-breaking", r.PossiblyBreaking); err != nil {
		return err
	}
	if err := writeGroup("additive", r.Additive); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%d breaking, %d possibly-breaking, %d additive (base %s, head %s)\n",
		len(r.Breaking), len(r.PossiblyBreaking), len(r.Additive), r.DigestBase, r.DigestHead)
	return err
}

func runApiDiff(ctx context.Context, ws *core.Workspace, flags *apiDiffFlags, args []string) (*ApiDiffResult, error) {
	if flags.base == "" {
		return nil, cli.UsageErrorf("api diff requires --base <git-ref>")
	}
	switch flags.failOn {
	case "", "breaking", "possibly-breaking":
	default:
		return nil, cli.UsageErrorf("invalid --fail-on %q (want breaking or possibly-breaking)", flags.failOn)
	}

	baseWs, err := workspaceAtGitRef(ctx, ws, flags.base)
	if err != nil {
		return nil, err
	}
	baseSurface, err := runApiSurface(ctx, baseWs, args)
	if err != nil {
		return nil, fmt.Errorf("computing surface at %s: %w", flags.base, err)
	}

	headWs := ws
	if flags.head != "" {
		headWs, err = workspaceAtGitRef(ctx, ws, flags.head)
		if err != nil {
			return nil, err
		}
	}
	headSurface, err := runApiSurface(ctx, headWs, args)
	if err != nil {
		return nil, fmt.Errorf("computing head surface: %w", err)
	}

	result := compareSurfaces(baseSurface, headSurface)
	failed := false
	switch flags.failOn {
	case "breaking":
		failed = len(result.Breaking) > 0
	case "possibly-breaking":
		failed = len(result.Breaking) > 0 || len(result.PossiblyBreaking) > 0
	}
	if failed {
		return result, cli.Errorf(cli.ExitFailed, "api diff found %d breaking and %d possibly-breaking change(s)",
			len(result.Breaking), len(result.PossiblyBreaking))
	}
	return result, nil
}

// compareSurfaces classifies per-export changes between two surfaces:
// removed export → breaking; type or kind change → possibly-breaking (v1
// string compare); added export → additive.
func compareSurfaces(base *ApiSurfaceResult, head *ApiSurfaceResult) *ApiDiffResult {
	result := &ApiDiffResult{
		Breaking:         []*ApiChange{},
		PossiblyBreaking: []*ApiChange{},
		Additive:         []*ApiChange{},
		DigestBase:       base.Digest,
		DigestHead:       head.Digest,
	}
	key := func(e *ApiExport) string { return e.Entry + "\x00" + e.Name }
	baseByKey := make(map[string]*ApiExport, len(base.Exports))
	for _, e := range base.Exports {
		baseByKey[key(e)] = e
	}
	headByKey := make(map[string]*ApiExport, len(head.Exports))
	for _, e := range head.Exports {
		headByKey[key(e)] = e
	}

	for _, b := range base.Exports {
		h, ok := headByKey[key(b)]
		if !ok {
			result.Breaking = append(result.Breaking, &ApiChange{
				Entry: b.Entry, Name: b.Name, Rule: "removed export", BaseType: b.Type,
			})
			continue
		}
		switch {
		case b.Type != h.Type:
			result.PossiblyBreaking = append(result.PossiblyBreaking, &ApiChange{
				Entry: b.Entry, Name: b.Name, Rule: "type changed", BaseType: b.Type, HeadType: h.Type,
			})
		case b.Kind != h.Kind:
			result.PossiblyBreaking = append(result.PossiblyBreaking, &ApiChange{
				Entry: b.Entry, Name: b.Name, Rule: fmt.Sprintf("kind changed (%s -> %s)", b.Kind, h.Kind),
			})
		}
	}
	for _, h := range head.Exports {
		if _, ok := baseByKey[key(h)]; !ok {
			result.Additive = append(result.Additive, &ApiChange{
				Entry: h.Entry, Name: h.Name, Rule: "added export", HeadType: h.Type,
			})
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// git materialization

// apiSourceExtensions are the file types materialized from a git ref into
// the overlay (program inputs plus config files).
var apiSourceExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".json"}

func hasApiSourceExtension(path string) bool {
	for _, ext := range apiSourceExtensions {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// workspaceAtGitRef builds a Workspace whose project files all come from a
// git ref, without checking anything out: every project file present in the
// ref is layered into an overlay FS via `git show <ref>:<path>`, and files
// that exist on disk but not in the ref are added to the overlay's deleted
// set. Requires the project to live inside a git work tree on a real file
// system.
func workspaceAtGitRef(ctx context.Context, ws *core.Workspace, ref string) (*core.Workspace, error) {
	// Resolve symlinks (e.g. /tmp → /private/tmp on macOS) so the project
	// root compares correctly against git's toplevel.
	realRoot := tspath.NormalizePath(ws.FS.Realpath(ws.RootDir))
	gitTop, err := gitOutput(ctx, realRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git work tree: %w", ws.RootDir, err)
	}
	gitTop = tspath.NormalizePath(strings.TrimSpace(gitTop))

	relDir := tspath.ConvertToRelativePath(realRoot, tspath.ComparePathsOptions{
		CurrentDirectory:          gitTop,
		UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
	})
	if strings.HasPrefix(relDir, "..") || tspath.IsRootedDiskPath(relDir) {
		return nil, fmt.Errorf("project root %s is not inside the git work tree %s: %w", ws.RootDir, gitTop, core.ErrInvalidArgument)
	}
	pathspec := relDir
	prefix := ""
	if relDir == "" || relDir == "." {
		pathspec = "."
	} else {
		prefix = relDir + "/"
	}

	listing, err := gitOutput(ctx, gitTop, "ls-tree", "-r", "--name-only", "-z", ref, "--", pathspec)
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s: %w", ref, err)
	}

	// Overlay keys must use the workspace's own root spelling (ws.RootDir),
	// not git's resolved one, so the speculative program's lookups hit them.
	contents := make(map[string]string)
	for name := range strings.SplitSeq(listing, "\x00") {
		if name == "" || !hasApiSourceExtension(name) || strings.Contains(name, "node_modules/") {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		content, err := gitOutput(ctx, gitTop, "show", ref+":"+name)
		if err != nil {
			return nil, fmt.Errorf("git show %s:%s: %w", ref, name, err)
		}
		contents[tspath.CombinePaths(ws.RootDir, rel)] = content
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("no project files found at git ref %s under %s: %w", ref, ws.RootDir, core.ErrNotFound)
	}

	// Files on disk that are absent from the ref must disappear in the
	// speculative program.
	var deleted []string
	canonical := func(p string) string {
		return tspath.GetCanonicalFileName(p, ws.FS.UseCaseSensitiveFileNames())
	}
	inRef := make(map[string]bool, len(contents))
	for abs := range contents {
		inRef[canonical(abs)] = true
	}
	for _, abs := range listProjectFilesOnDisk(ws) {
		if !inRef[canonical(abs)] {
			deleted = append(deleted, abs)
		}
	}

	return core.SpeculativeWorkspace(ws, contents, deleted, false)
}

// listProjectFilesOnDisk enumerates source/config files under the project
// root on the workspace FS, skipping node_modules, .git, and hidden dirs.
func listProjectFilesOnDisk(ws *core.Workspace) []string {
	var files []string
	var walk func(dir string)
	walk = func(dir string) {
		entries := ws.FS.GetAccessibleEntries(dir)
		for _, name := range entries.Files {
			if hasApiSourceExtension(name) {
				files = append(files, tspath.CombinePaths(dir, name))
			}
		}
		for _, name := range entries.Directories {
			if name == "node_modules" || strings.HasPrefix(name, ".") {
				continue
			}
			walk(tspath.CombinePaths(dir, name))
		}
	}
	walk(ws.RootDir)
	return files
}

// gitOutput runs a git subcommand in dir and returns its stdout.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
