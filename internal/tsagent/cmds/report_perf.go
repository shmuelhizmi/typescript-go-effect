package cmds

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/perf"
	"github.com/microsoft/typescript-go/internal/tsagent/report"
)

func init() {
	registerProvider(reportProvider{
		Key:   "perf",
		Group: "Performance",
		Build: func(ctx context.Context, ws *core.Workspace, o reportOptions) ([]*report.Page, error) {
			c, err := perf.Gather(ctx, ws, perf.Options{Emit: o.emit, SingleThreaded: o.singleThreaded || !o.parallelPerf})
			if err != nil {
				return nil, err
			}
			return perfPages(c.Report(o.includeLibs, o.top)), nil
		},
	})
}

// perfPages builds the Performance page from a perf report: the same content
// the old `perf report` HTML produced, now expressed through the shared toolkit.
func perfPages(r *perf.Report) []*report.Page {
	s := r.Summary
	st := s.Stats

	body := []report.Section{
		report.Hero("Total compile time", trimZeros(st.Total.Seconds()), "s",
			report.Tag{Tone: verdictTone(s), Word: verdictWord(s), Text: s.Verdict}),
		report.KPIs(
			report.KPI{Key: "Files", Value: compact(st.Files)},
			report.KPI{Key: "Types", Value: compact(st.Types)},
			report.KPI{Key: "Instantiations", Value: compact(st.Instantiations)},
			report.KPI{Key: "Inst / type", Value: fmt.Sprintf("%.1f", s.InstantiationsPerType), Warn: s.InstantiationsPerType >= 1.5},
			report.KPI{Key: "Peak memory", Value: fmt.Sprintf("%d", st.MemoryUsed/(1024*1024)), Unit: "MB"},
		),
		report.PhaseBar(
			report.Phase{Name: "parse", Color: "var(--c-parse)", Pct: s.ParsePct, Seconds: st.Parse.Seconds()},
			report.Phase{Name: "bind", Color: "var(--c-bind)", Pct: s.BindPct, Seconds: st.Bind.Seconds()},
			report.Phase{Name: "check", Color: "var(--c-check)", Pct: s.CheckPct, Seconds: st.Check.Seconds()},
			report.Phase{Name: "emit", Color: "var(--c-emit)", Pct: s.EmitPct, Seconds: st.Emit.Seconds()},
		),
	}

	if len(r.DepthLimits) > 0 {
		body = append(body, perfIssueGrid(r.DepthLimits))
	}

	body = append(body,
		report.Treemap("Where time goes", "Every file sized by total compile time, colored by intensity. Hover for detail.", perfTree(r.FileTree)),
		perfHotFilesTable(r.HotFiles),
		perfHotTypesTable(r.HotTypes),
		perfHotChecksTable(r.HotChecks),
	)

	return []*report.Page{{
		ID:    "perf",
		Label: "Performance",
		Group: "Performance",
		Badge: badgeCount(len(r.DepthLimits)),
		Body:  body,
	}}
}

func perfTree(n *perf.TreeNode) *report.TreeNode {
	rn := &report.TreeNode{
		Name:  n.Name,
		Path:  n.Path,
		Value: n.TotalMS,
		Tip: []report.TipKV{
			{K: "total", V: fmt1(n.TotalMS) + " ms"},
			{K: "check", V: fmt1(n.CheckMS) + " ms"},
			{K: "parse / bind", V: fmt1(n.ParseMS) + " / " + fmt1(n.BindMS) + " ms"},
			{K: "types", V: strconv.Itoa(n.Types)},
		},
	}
	for _, ch := range n.Children {
		rn.Children = append(rn.Children, perfTree(ch))
	}
	return rn
}

func perfIssueGrid(limits []*perf.DepthLimit) report.Section {
	counts := map[string]int{}
	var order []string
	for _, d := range limits {
		if counts[d.Name] == 0 {
			order = append(order, d.Name)
		}
		counts[d.Name]++
	}
	slices.SortFunc(order, func(a, b string) int { return counts[b] - counts[a] })
	items := make([]report.Issue, 0, len(order))
	for _, name := range order {
		items = append(items, report.Issue{Count: counts[name], Name: prettyGuard(name), Why: guardWhy(name)})
	}
	return report.IssueGrid(
		fmt.Sprintf("%d type-explosion guards fired", len(limits)),
		"The checker hit recursion / instantiation / union-size ceilings. Each is a near-pathological type worth simplifying.",
		items...,
	)
}

func perfHotFilesTable(files []*perf.HotFile) report.Section {
	maxTotal := 0.0
	for _, f := range files {
		if f.TotalMS > maxTotal {
			maxTotal = f.TotalMS
		}
	}
	rows := make([]report.Row, 0, len(files))
	for _, f := range files {
		rows = append(rows, report.Row{
			{Text: f.Path, Class: "path"},
			{Text: fmt1(f.TotalMS) + " ms", Class: "r", Meter: &report.MeterSpec{Val: f.TotalMS, Max: maxTotal, Color: "var(--c-hot)"}},
			{Text: fmt1(f.CheckMS), Class: "r mono dim"},
			{Text: strconv.Itoa(f.Types), Class: "r mono"},
		})
	}
	return report.Table("Hottest files", "No files attributed.", []report.Col{
		{Header: "File"}, {Header: "Total", Right: true}, {Header: "Check", Right: true}, {Header: "Types", Right: true},
	}, rows)
}

func perfHotTypesTable(types []*perf.HotType) report.Section {
	maxCount := 0
	for _, t := range types {
		if t.Count > maxCount {
			maxCount = t.Count
		}
	}
	rows := make([]report.Row, 0, len(types))
	for _, t := range types {
		origin := t.File
		if t.Line > 0 {
			origin = fmt.Sprintf("%s:%d", t.File, t.Line)
		}
		rows = append(rows, report.Row{
			{Text: t.Symbol, Class: "sym"},
			{Text: strconv.Itoa(t.Count), Class: "r", Meter: &report.MeterSpec{Val: float64(t.Count), Max: float64(maxCount), Color: "var(--c-accent2)"}},
			{Text: origin, Class: "path dim"},
		})
	}
	return report.Table("Most-instantiated types", "No types attributed.", []report.Col{
		{Header: "Symbol"}, {Header: "Instantiations", Right: true}, {Header: "Origin"},
	}, rows)
}

func perfHotChecksTable(checks []*perf.HotCheck) report.Section {
	maxDur := 0.0
	for _, c := range checks {
		if c.DurMS > maxDur {
			maxDur = c.DurMS
		}
	}
	rows := make([]report.Row, 0, len(checks))
	for _, c := range checks {
		loc := c.File
		if c.Line > 0 {
			loc = fmt.Sprintf("%s:%d", c.File, c.Line)
		}
		if loc == "" {
			loc = "—"
		}
		rows = append(rows, report.Row{
			{Text: c.Name, Class: "sym"},
			{Text: fmt1(c.DurMS) + " ms", Class: "r", Meter: &report.MeterSpec{Val: c.DurMS, Max: maxDur, Color: "var(--c-hot)"}},
			{Text: loc, Class: "path dim"},
		})
	}
	return report.Table("Slowest checker operations", "Nothing sampled above ~10ms — the type system is fast.", []report.Col{
		{Header: "Operation"}, {Header: "Duration", Right: true}, {Header: "Location"},
	}, rows)
}

// ---- presentation helpers (moved from the former perf/report.go) ----

func compact(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func trimZeros(secs float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", secs), "0"), ".")
}

func verdictWord(s *perf.Summary) string {
	switch {
	case s.CheckPct >= 55:
		return "check"
	case s.ParsePct >= 45:
		return "parse"
	case s.BindPct >= 30:
		return "bind"
	case s.EmitPct >= 35:
		return "emit"
	}
	return "balanced"
}

func verdictTone(s *perf.Summary) string {
	if s.DepthLimitHits > 0 || s.InstantiationsPerType >= 1.5 {
		return "warn"
	}
	return "ok"
}

func prettyGuard(name string) string {
	return strings.TrimSuffix(name, "_DepthLimit")
}

func guardWhy(name string) string {
	switch name {
	case "checkCrossProductUnion_DepthLimit":
		return "Union cross-product too large — discriminate or narrow the unions."
	case "recursiveTypeRelatedTo_DepthLimit", "checkTypeRelatedTo_DepthLimit":
		return "Recursive type comparison too deep — flatten or break the recursion."
	case "instantiateType_DepthLimit":
		return "Type instantiation too deep / too many — simplify the generic."
	case "removeSubtypes_DepthLimit":
		return "Union subtype reduction too large — fewer union members."
	case "getTypeAtFlowNode_DepthLimit":
		return "Control-flow narrowing too deep — simplify the function."
	case "typeRelatedToDiscriminatedType_DepthLimit":
		return "Discriminated-union match too large — reduce variants."
	case "traceUnionsOrIntersectionsTooLarge_DepthLimit":
		return "Union/intersection too large in a relation."
	}
	return "Type complexity ceiling hit."
}
