package cmds

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/report"
)

// The Structure group is pure measurement of file and AST size/shape — line
// counts, comment density, function metrics, directory rollups. Everything here
// is cheap (AST/source only, no type checker) and carries no pass/fail opinion.

func structureProvider(key string, build func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error)) {
	registerProvider(reportProvider{
		Key:   key,
		Group: "Structure",
		Build: func(ctx context.Context, ws *core.Workspace, o reportOptions) ([]*report.Page, error) {
			p, err := build(ctx, ws, o)
			if err != nil {
				return nil, err
			}
			return []*report.Page{p}, nil
		},
	})
}

func init() {
	structureProvider("file-size", func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		files, err := projectFiles(ws, nil)
		if err != nil {
			return nil, err
		}
		type fl struct {
			path string
			loc  int
		}
		list := make([]fl, 0, len(files))
		leaves := make([]leafMetric, 0, len(files))
		maxLoc := 0
		for _, f := range files {
			loc := len(f.ECMALineMap())
			p := ws.RelPath(f.FileName())
			list = append(list, fl{p, loc})
			if loc > maxLoc {
				maxLoc = loc
			}
			leaves = append(leaves, leafMetric{path: p, value: float64(loc), tip: []report.TipKV{{K: "lines", V: strconv.Itoa(loc)}}})
		}
		slices.SortFunc(list, func(a, b fl) int {
			if a.loc != b.loc {
				return b.loc - a.loc
			}
			return strings.Compare(a.path, b.path)
		})
		rows := make([]report.Row, 0, len(list))
		for _, f := range topNSlice(list, o.top) {
			rows = append(rows, report.Row{
				{Text: f.path, Class: "path"},
				{Text: strconv.Itoa(f.loc), Class: "r", Meter: &report.MeterSpec{Val: float64(f.loc), Max: float64(maxLoc), Color: "var(--c-hot)"}},
			})
		}
		return &report.Page{
			ID: "file-size", Label: "File size", Group: "Structure", Badge: badgeCount(len(files)),
			Body: []report.Section{
				report.Table("Largest files (lines of code)", "No files in the project.", []report.Col{
					{Header: "File"}, {Header: "Lines", Right: true},
				}, rows),
				report.Treemap("File sizes", "Every file sized and colored by line count. Hover for detail.", buildFileTree(leaves)),
			},
		}, nil
	})

	structureProvider("comments", func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		files, err := projectFiles(ws, nil)
		if err != nil {
			return nil, err
		}
		type cm struct {
			path     string
			loc      int
			comments int
			pct      float64
		}
		list := make([]cm, 0, len(files))
		leaves := make([]leafMetric, 0, len(files))
		for _, f := range files {
			loc := len(f.ECMALineMap())
			comments := commentLineCount(f)
			pct := 0.0
			if loc > 0 {
				pct = float64(comments) / float64(loc)
			}
			p := ws.RelPath(f.FileName())
			list = append(list, cm{p, loc, comments, pct})
			leaves = append(leaves, leafMetric{
				path: p, value: float64(loc), heat: report.Heat(pct),
				tip: []report.TipKV{{K: "comment %", V: pctStr(pct)}, {K: "comment lines", V: strconv.Itoa(comments)}, {K: "lines", V: strconv.Itoa(loc)}},
			})
		}
		slices.SortFunc(list, func(a, b cm) int {
			if a.pct != b.pct {
				if a.pct < b.pct {
					return 1
				}
				return -1
			}
			return strings.Compare(a.path, b.path)
		})
		rows := make([]report.Row, 0, len(list))
		for _, f := range topNSlice(list, o.top) {
			rows = append(rows, report.Row{
				{Text: f.path, Class: "path"},
				{Text: pctStr(f.pct), Class: "r", Meter: &report.MeterSpec{Val: f.pct, Max: 1, Color: "var(--c-accent)"}},
				{Text: strconv.Itoa(f.comments), Class: "r mono dim"},
				{Text: strconv.Itoa(f.loc), Class: "r mono dim"},
			})
		}
		return &report.Page{
			ID: "comments", Label: "Comment density", Group: "Structure", Badge: badgeCount(len(files)),
			Body: []report.Section{
				report.Table("Comment density (comment lines ÷ total lines)", "No files in the project.", []report.Col{
					{Header: "File"}, {Header: "Comment %", Right: true}, {Header: "Comment lines", Right: true}, {Header: "Lines", Right: true},
				}, rows),
				report.Treemap("Comment density", "Files sized by line count, colored by comment percentage.", buildFileTree(leaves)),
			},
		}, nil
	})

	structureProvider("functions", func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		files, err := projectFiles(ws, nil)
		if err != nil {
			return nil, err
		}
		var fns []fnStat
		for _, file := range files {
			for _, fn := range functionLikeNodesWithBody(file) {
				start := astnav.GetStartOfNode(fn, file, false /*includeJSDoc*/)
				startLine, _ := ws.PosToLineCol(file, start)
				endLine, _ := ws.PosToLineCol(file, fn.End())
				fns = append(fns, fnStat{
					name:   functionDisplayName(fn),
					loc:    fmt.Sprintf("%s:%d", ws.RelPath(file.FileName()), startLine),
					lines:  endLine - startLine + 1,
					depth:  maxNestingDepth(fn.Body()),
					params: len(fn.Parameters()),
				})
			}
		}
		return &report.Page{
			ID: "functions", Label: "Function metrics", Group: "Structure", Badge: badgeCount(len(fns)),
			Body: []report.Section{
				fnTable("Longest functions (lines)", fns, o.top, func(f fnStat) int { return f.lines }, "lines", "var(--c-hot)"),
				fnTable("Deepest nesting", fns, o.top, func(f fnStat) int { return f.depth }, "depth", "var(--c-accent2)"),
				fnTable("Most parameters", fns, o.top, func(f fnStat) int { return f.params }, "params", "var(--c-accent)"),
			},
		}, nil
	})

	structureProvider("dir-stats", func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		files, err := projectFiles(ws, nil)
		if err != nil {
			return nil, err
		}
		type dir struct {
			files, loc, comments int
		}
		dirs := map[string]*dir{}
		var order []string
		totalLoc, totalComments := 0, 0
		for _, f := range files {
			loc := len(f.ECMALineMap())
			comments := commentLineCount(f)
			d := path.Dir(ws.RelPath(f.FileName()))
			if d == "." || d == "" {
				d = "(root)"
			}
			e := dirs[d]
			if e == nil {
				e = &dir{}
				dirs[d] = e
				order = append(order, d)
			}
			e.files++
			e.loc += loc
			e.comments += comments
			totalLoc += loc
			totalComments += comments
		}
		slices.SortFunc(order, func(a, b string) int {
			if dirs[a].loc != dirs[b].loc {
				return dirs[b].loc - dirs[a].loc
			}
			return strings.Compare(a, b)
		})
		maxLoc := 0
		for _, d := range dirs {
			if d.loc > maxLoc {
				maxLoc = d.loc
			}
		}
		rows := make([]report.Row, 0, len(order))
		for _, name := range order {
			e := dirs[name]
			pct := 0.0
			if e.loc > 0 {
				pct = float64(e.comments) / float64(e.loc)
			}
			rows = append(rows, report.Row{
				{Text: name, Class: "path"},
				{Text: strconv.Itoa(e.files), Class: "r mono dim"},
				{Text: strconv.Itoa(e.loc), Class: "r", Meter: &report.MeterSpec{Val: float64(e.loc), Max: float64(maxLoc), Color: "var(--c-check)"}},
				{Text: pctStr(pct), Class: "r mono dim"},
			})
		}
		overallPct := 0.0
		if totalLoc > 0 {
			overallPct = float64(totalComments) / float64(totalLoc)
		}
		avgLoc := 0
		if len(files) > 0 {
			avgLoc = totalLoc / len(files)
		}
		return &report.Page{
			ID: "dir-stats", Label: "Directories", Group: "Structure", Badge: badgeCount(len(order)),
			Body: []report.Section{
				report.KPIs(
					report.KPI{Key: "Files", Value: strconv.Itoa(len(files))},
					report.KPI{Key: "Directories", Value: strconv.Itoa(len(order))},
					report.KPI{Key: "Lines", Value: compact(totalLoc)},
					report.KPI{Key: "Avg file", Value: strconv.Itoa(avgLoc), Unit: "loc"},
					report.KPI{Key: "Comments", Value: pctStr(overallPct)},
				),
				report.Table("Per-directory rollup", "No directories.", []report.Col{
					{Header: "Directory"}, {Header: "Files", Right: true}, {Header: "Lines", Right: true}, {Header: "Comment %", Right: true},
				}, rows),
			},
		}, nil
	})
}

// fnStat is one function's AST size/shape measurements.
type fnStat struct {
	name, loc            string
	lines, depth, params int
}

// fnTable builds a ranking table of functions by a chosen integer metric.
func fnTable(title string, fns []fnStat, top int, metric func(fnStat) int, unit, color string) report.Section {
	sorted := append([]fnStat(nil), fns...)
	slices.SortFunc(sorted, func(a, b fnStat) int {
		if metric(a) != metric(b) {
			return metric(b) - metric(a)
		}
		return strings.Compare(a.loc, b.loc)
	})
	maxV := 0
	for _, f := range sorted {
		if metric(f) > maxV {
			maxV = metric(f)
		}
	}
	rows := make([]report.Row, 0, top)
	for _, f := range topNSlice(sorted, top) {
		rows = append(rows, report.Row{
			{Text: f.name, Class: "sym"},
			{Text: strconv.Itoa(metric(f)), Class: "r", Meter: &report.MeterSpec{Val: float64(metric(f)), Max: float64(maxV), Color: color}},
			{Text: f.loc, Class: "path dim"},
		})
	}
	return report.Table(title, "No functions in the project.", []report.Col{
		{Header: "Function"}, {Header: strings.ToTitle(unit[:1]) + unit[1:], Right: true}, {Header: "Location"},
	}, rows)
}

// commentLineCount counts the distinct source lines covered by a comment,
// lexing the file with trivia preserved. It is an approximate measurement: a
// trailing comment shares its line with code, and that line is counted.
func commentLineCount(file *ast.SourceFile) int {
	sc := scanner.NewScanner()
	sc.SetSkipTrivia(false)
	sc.SetText(file.Text())
	lines := map[int]struct{}{}
	for {
		k := sc.Scan()
		if k == ast.KindEndOfFile {
			break
		}
		if k != ast.KindSingleLineCommentTrivia && k != ast.KindMultiLineCommentTrivia {
			continue
		}
		startLine, _ := scanner.GetECMALineAndUTF16CharacterOfPosition(file, sc.TokenStart())
		endPos := sc.TokenEnd()
		if endPos > sc.TokenStart() {
			endPos-- // End is exclusive; stay on the comment's last line
		}
		endLine, _ := scanner.GetECMALineAndUTF16CharacterOfPosition(file, endPos)
		for l := startLine; l <= endLine; l++ {
			lines[l] = struct{}{}
		}
	}
	return len(lines)
}

// maxNestingDepth returns the deepest control-flow nesting in a function body,
// mirroring branchMetrics' depth-carrying walk without descending into nested
// functions.
func maxNestingDepth(body *ast.Node) int {
	maxDepth := 0
	var visit func(node *ast.Node, depth int)
	visit = func(node *ast.Node, depth int) {
		node.ForEachChild(func(child *ast.Node) bool {
			if isAnalyzableFunction(child) {
				return false
			}
			childDepth := depth
			switch child.Kind {
			case ast.KindIfStatement, ast.KindConditionalExpression, ast.KindCaseClause, ast.KindCatchClause,
				ast.KindForStatement, ast.KindForInStatement, ast.KindForOfStatement,
				ast.KindWhileStatement, ast.KindDoStatement:
				childDepth = depth + 1
				if childDepth > maxDepth {
					maxDepth = childDepth
				}
			}
			visit(child, childDepth)
			return false
		})
	}
	visit(body, 0)
	return maxDepth
}

// ---- treemap tree builder (shared by file-size and comments) ----

// leafMetric is one file's contribution to a structure treemap.
type leafMetric struct {
	path  string
	value float64
	heat  *float64
	tip   []report.TipKV
}

// buildFileTree turns a flat per-file metric list into a directory hierarchy.
// Values sum up the tree; heats (when present) aggregate as a value-weighted
// average so folders are colored by their files' average. Single-child folder
// chains are collapsed for a tidier treemap.
func buildFileTree(leaves []leafMetric) *report.TreeNode {
	root := &report.TreeNode{}
	index := map[string]*report.TreeNode{"": root}
	for _, lf := range leaves {
		parts := strings.Split(lf.path, "/")
		prefix := ""
		parent := root
		for i, p := range parts {
			if p == "" {
				continue
			}
			if prefix == "" {
				prefix = p
			} else {
				prefix += "/" + p
			}
			node := index[prefix]
			if node == nil {
				node = &report.TreeNode{Name: p}
				index[prefix] = node
				parent.Children = append(parent.Children, node)
			}
			if i == len(parts)-1 {
				node.Path = lf.path
				node.Value = lf.value
				node.Heat = lf.heat
				node.Tip = lf.tip
			}
			parent = node
		}
	}
	aggregateMetric(root)
	collapseMetric(root)
	return root
}

func aggregateMetric(n *report.TreeNode) {
	if len(n.Children) == 0 {
		return
	}
	var sum, heatWeighted float64
	hasHeat := false
	for _, ch := range n.Children {
		aggregateMetric(ch)
		sum += ch.Value
		if ch.Heat != nil {
			heatWeighted += *ch.Heat * ch.Value
			hasHeat = true
		}
	}
	n.Value = sum
	if hasHeat && sum > 0 {
		n.Heat = report.Heat(heatWeighted / sum)
	}
	slices.SortFunc(n.Children, func(a, b *report.TreeNode) int {
		if a.Value != b.Value {
			if a.Value < b.Value {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

func collapseMetric(n *report.TreeNode) {
	for len(n.Children) == 1 && len(n.Children[0].Children) > 0 && n.Name != "" {
		child := n.Children[0]
		n.Name += "/" + child.Name
		n.Children = child.Children
	}
	for _, ch := range n.Children {
		collapseMetric(ch)
	}
}

// ---- small helpers ----

func topNSlice[T any](s []T, n int) []T {
	if n > 0 && len(s) > n {
		return s[:n]
	}
	return s
}

func pctStr(frac float64) string { return fmt.Sprintf("%.0f%%", frac*100) }
