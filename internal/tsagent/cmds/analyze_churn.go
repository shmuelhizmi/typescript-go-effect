package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/binder"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// analyze churn-risk (§4.5): join symbol reference counts with git change
// frequency. Symbols in frequently-changed files that are referenced from
// many places are the "blast radius" list an agent should be careful around.
// Requires git; works on shallow clones with whatever history is present.

func init() {
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "churn-risk",
		Summary:      "High-churn × high-reference symbols (requires git history)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &churnRiskFlags{}
			fs.StringVar(&f.since, "since", "6 months ago", "git log --since window")
			fs.IntVar(&f.top, "top", 25, "max symbols reported (files are scanned in churn order)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeChurnRisk(ctx, ws, flags.(*churnRiskFlags), args)
		},
	})
}

type churnRiskFlags struct {
	since string
	top   int
}

// ChurnRiskRow is one exported symbol scored by churn × reference fan-in.
type ChurnRiskRow struct {
	SymbolID string  `json:"symbolId,omitempty"`
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Churn    int     `json:"churn"` // commits touching the file in the window
	Refs     int     `json:"refs"`  // references outside the declaration
	Risk     float64 `json:"risk"`  // churn × log2(1+refs)
}

// ChurnRiskResult is the `analyze churn-risk` result, rows sorted by risk
// descending.
type ChurnRiskResult struct {
	Since string          `json:"since"`
	Rows  []*ChurnRiskRow `json:"rows"`
}

var _ cli.Lister = (*ChurnRiskResult)(nil)

func (r *ChurnRiskResult) Total() int     { return len(r.Rows) }
func (r *ChurnRiskResult) Item(i int) any { return r.Rows[i] }

func (r *ChurnRiskResult) WriteItemText(w io.Writer, item any) error {
	row := item.(*ChurnRiskRow)
	_, err := fmt.Fprintf(w, "%7.1f  %s  %s  (churn:%d refs:%d)\n", row.Risk, row.Name, row.File, row.Churn, row.Refs)
	return err
}

func runAnalyzeChurnRisk(ctx context.Context, ws *core.Workspace, flags *churnRiskFlags, args []string) (*ChurnRiskResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	churnByFile, err := gitChurnCounts(ctx, ws, flags.since)
	if err != nil {
		return nil, err
	}

	// Touched program files in churn order (desc), restricted to the
	// requested paths.
	type touchedFile struct {
		file  *ast.SourceFile
		churn int
	}
	var touched []touchedFile
	for _, file := range files {
		if churn := churnByFile[file.FileName()]; churn > 0 {
			touched = append(touched, touchedFile{file: file, churn: churn})
		}
	}
	slices.SortStableFunc(touched, func(a, b touchedFile) int {
		if d := b.churn - a.churn; d != 0 {
			return d
		}
		return strings.Compare(a.file.FileName(), b.file.FileName())
	})

	allFiles, err := projectFiles(ws, nil)
	if err != nil {
		return nil, err
	}
	index := buildIdentifierIndex(allFiles)

	top := flags.top
	if top <= 0 {
		top = 25
	}
	result := &ChurnRiskResult{Since: flags.since, Rows: []*ChurnRiskRow{}}
	for _, tf := range touched {
		if len(result.Rows) >= top {
			break
		}
		binder.BindSourceFile(tf.file)
		for _, cand := range deadCodeCandidates(tf.file) {
			if !cand.exported {
				continue
			}
			refs := churnReferenceCount(ctx, ws, index, cand)
			result.Rows = append(result.Rows, &ChurnRiskRow{
				SymbolID: core.EncodeSymbolID(ws, cand.decl.Symbol()),
				Name:     cand.name,
				File:     ws.RelPath(tf.file.FileName()),
				Churn:    tf.churn,
				Refs:     refs,
				Risk:     float64(tf.churn) * math.Log2(1+float64(refs)),
			})
		}
	}

	slices.SortStableFunc(result.Rows, func(a, b *ChurnRiskRow) int {
		switch {
		case a.Risk < b.Risk:
			return 1
		case a.Risk > b.Risk:
			return -1
		}
		if d := b.Churn - a.Churn; d != 0 {
			return d
		}
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(result.Rows) > top {
		result.Rows = result.Rows[:top]
	}
	return result, nil
}

// churnReferenceCount counts references to a candidate outside its own
// declarations: the identifier index proves zero for free; otherwise
// find-all-references resolves how many occurrences actually bind here.
func churnReferenceCount(ctx context.Context, ws *core.Workspace, index *identifierIndex, cand *deadCodeCandidate) int {
	declarations := candidateDeclarations(cand)
	insideOwnDeclarations := func(file *ast.SourceFile, pos int, end int) bool {
		for _, decl := range declarations {
			if ast.GetSourceFileOfNode(decl) == file && pos >= decl.Pos() && end <= decl.End() {
				return true
			}
		}
		return false
	}
	outside := 0
	for _, occ := range index.occurrences[cand.name] {
		if !insideOwnDeclarations(occ.file, occ.pos, occ.end) {
			outside++
		}
	}
	if outside == 0 {
		return 0
	}
	refPos := astnav.GetStartOfNode(cand.nameNode, cand.file, false /*includeJSDoc*/)
	entries := ws.LS.GetReferencedSymbolsForNode(ctx, refPos, cand.nameNode, ws.Program.GetSourceFiles())
	refs := 0
	for _, entry := range entries {
		for _, ref := range entry.References() {
			node := ref.Node()
			if node == nil {
				continue
			}
			file := ast.GetSourceFileOfNode(node)
			if file == nil || !insideOwnDeclarations(file, node.Pos(), node.End()) {
				refs++
			}
		}
	}
	return refs
}

// gitChurnCounts runs `git log --since … --numstat` over the project
// directory and returns commit-touch counts keyed by the workspace's absolute
// file names. Errors out (exit 1) when the project is not in a git work tree.
func gitChurnCounts(ctx context.Context, ws *core.Workspace, since string) (map[string]int, error) {
	realRoot := tspath.NormalizePath(ws.FS.Realpath(ws.RootDir))
	gitTop, err := gitOutput(ctx, realRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, cli.Errorf(cli.ExitFailed, "analyze churn-risk requires git history: %s is not inside a git work tree (%v)", ws.RootDir, err)
	}
	gitTop = tspath.NormalizePath(strings.TrimSpace(gitTop))

	relDir := tspath.ConvertToRelativePath(realRoot, tspath.ComparePathsOptions{
		CurrentDirectory:          gitTop,
		UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
	})
	pathspec := relDir
	prefix := ""
	if relDir == "" || relDir == "." {
		pathspec = "."
	} else {
		prefix = relDir + "/"
	}

	out, err := gitOutput(ctx, gitTop, "log", "--since="+since, "--numstat", "--format=%H", "--", pathspec)
	if err != nil {
		return nil, cli.Errorf(cli.ExitFailed, "analyze churn-risk: git log failed: %v", err)
	}

	churn := make(map[string]int)
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		// numstat rows are "added<TAB>deleted<TAB>path"; commit-hash and blank
		// lines have no tab.
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		gitPath := parts[2]
		// Renames render as "old => new" (possibly with a {a => b} infix);
		// attribute the touch to the new path, best effort.
		if strings.Contains(gitPath, " => ") {
			gitPath = churnRenameNewPath(gitPath)
		}
		rel := strings.TrimPrefix(gitPath, prefix)
		abs := tspath.CombinePaths(ws.RootDir, rel)
		churn[abs]++
	}
	return churn, nil
}

// churnRenameNewPath resolves a git rename numstat path ("a/{old => new}/c"
// or "old => new") to the post-rename path.
func churnRenameNewPath(path string) string {
	if open := strings.IndexByte(path, '{'); open >= 0 {
		if closing := strings.IndexByte(path[open:], '}'); closing >= 0 {
			inner := path[open+1 : open+closing]
			if arrow := strings.Index(inner, " => "); arrow >= 0 {
				return strings.ReplaceAll(path[:open]+inner[arrow+4:]+path[open+closing+1:], "//", "/")
			}
		}
	}
	if arrow := strings.Index(path, " => "); arrow >= 0 {
		return path[arrow+4:]
	}
	return path
}
